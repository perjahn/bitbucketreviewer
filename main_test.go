package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/goccy/go-yaml"
)

func TestValidateConfigRequiresBitbucketAuth(t *testing.T) {
	t.Setenv("BR_BITBUCKET_USERNAME", "")
	t.Setenv("BR_BITBUCKET_PASSWORD", "")
	t.Setenv("BR_BITBUCKET_TOKEN", "")
	t.Setenv("BR_ALLOWED_PR_ORIGIN", "https://bitbucket.org")
	t.Setenv("BR_WEBHOOK_USERNAME", "")
	t.Setenv("BR_WEBHOOK_PASSWORD", "")

	if _, err := loadConfig(); err == nil {
		t.Fatal("expected loadConfig to fail when config is missing")
	}
}

func TestValidateConfigRejectsInvalidPort(t *testing.T) {
	t.Setenv("BR_PORT", "65536")
	t.Setenv("BR_BITBUCKET_USERNAME", "user")
	t.Setenv("BR_BITBUCKET_PASSWORD", "pass")
	t.Setenv("BR_ALLOWED_PR_ORIGIN", "https://bitbucket.org")
	t.Setenv("BR_WEBHOOK_USERNAME", "webhook")
	t.Setenv("BR_WEBHOOK_PASSWORD", "secret")

	if _, err := loadConfig(); err == nil {
		t.Fatal("expected loadConfig to fail for invalid BR_PORT")
	}
}

func TestApprovePullRequestUsesParticipantApprovalEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Fatalf("expected PUT, got %s", r.Method)
		}
		if r.URL.Path != "/rest/api/latest/projects/PRJ/repos/repo/pull-requests/42/participants/user" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}

		var payload map[string]string
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("failed to decode request body: %v", err)
		}
		if payload["status"] != "APPROVED" {
			t.Fatalf("expected approved status, got %#v", payload)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	cfg := config{bitbucketUsername: "user", bitbucketPassword: "pass"}
	prUrl := server.URL + "/projects/PRJ/repos/repo/pull-requests/42"
	if err := approvePullRequest(prUrl, "repo", cfg, context.Background(), logger); err != nil {
		t.Fatalf("approvePullRequest() error = %v", err)
	}
}

func TestHandleWebhookRejectsMismatchedPROrigin(t *testing.T) {
	t.Setenv("BR_BITBUCKET_USERNAME", "user")
	t.Setenv("BR_BITBUCKET_PASSWORD", "pass")
	t.Setenv("BR_BITBUCKET_TOKEN", "")
	t.Setenv("BR_ALLOWED_PR_ORIGIN", "https://bitbucket.org")
	t.Setenv("BR_WEBHOOK_USERNAME", "user")
	t.Setenv("BR_WEBHOOK_PASSWORD", "pass")

	eventBody := `{"pullRequest":{"links":{"self":[{"href":"https://other.example/workspace/repo/pull-requests/1"}]},"fromRef":{"id":"refs/heads/feature","repository":{"links":{"clone":[{"href":"https://other.example.org/workspace/repo","name":"http"}]}}},"toRef":{"id":"refs/heads/master"}}}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(eventBody))
	rec := httptest.NewRecorder()

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}

	handleWebhook(rec, req, cfg)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "invalid repository clone url") {
		t.Fatalf("expected invalid repository clone url error, got %q", rec.Body.String())
	}
}

func TestParseOwnersFromYamlParsesOwnerField(t *testing.T) {
	var repo Repo
	if err := yaml.Unmarshal([]byte("owner: alice,bob;carol\n"), &repo); err != nil {
		t.Fatalf("yaml.Unmarshal() error = %v", err)
	}

	owners := splitOwnerValues(repo.Owner)
	want := []string{"alice", "bob", "carol"}
	if len(owners) != len(want) {
		t.Fatalf("expected %d owners, got %d", len(want), len(owners))
	}
	for i := range want {
		if owners[i] != want[i] {
			t.Fatalf("expected owner %q at index %d, got %q", want[i], i, owners[i])
		}
	}
}

func TestExtractYamlFilesFromDiff(t *testing.T) {
	diff := `diff --git src://.github/owners.yaml dst://.github/owners.yaml
index 1111111..2222222 100644
--- dst://.github/owners.yaml
+++ src://.github/owners.yaml
@@
-owner: alice
+owner: bob

diff --git src://config/team.yml dst://config/team.yml
index 3333333..4444444 100644
--- dst://config/team.yml
+++ src://config/team.yml
@@
-owner: carol
+owner: dan

diff --git src://config/team.yml dst://config/team.yml
index 5555555..6666666 100644
--- dst://config/team.yml
+++ src://config/team.yml
@@
-owner: dan
+owner: erin
`

	got := extractYamlFilesFromDiff(diff)
	want := []string{".github/owners.yaml", "config/team.yml"}
	if len(got) != len(want) {
		t.Fatalf("expected %d yaml files, got %d: %#v", len(want), len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("expected yaml file %q at index %d, got %q", want[i], i, got[i])
		}
	}
}

func TestOwnersFromYamlAtRefReadsRawFileAtCommit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/api/latest/projects/PRJ/repos/repo/raw/owners.yaml" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if r.URL.Query().Get("at") != "abc123" {
			t.Fatalf("unexpected at query: %q", r.URL.Query().Get("at"))
		}
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("owner: alice\n"))
	}))
	defer server.Close()

	cfg := config{bitbucketUsername: "user", bitbucketPassword: "pass"}
	prUrl := server.URL + "/projects/PRJ/repos/repo/pull-requests/42"
	owners, err := ownersFromYamlAtRef(prUrl, "abc123", "owners.yaml", cfg, context.Background(), logger)
	if err != nil {
		t.Fatalf("ownersFromYamlAtRef() error = %v", err)
	}
	if len(owners) != 1 || owners[0] != "alice" {
		t.Fatalf("expected [alice], got %#v", owners)
	}
}

func TestCollectOwnersFromRepositoryUsesPRDiffEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/rest/api/latest/projects/PRJ/repos/repo/pull-requests/42.diff":
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte("diff --git src://owners.yaml dst://owners.yaml\nindex 1111111..2222222 100644\n--- dst://owners.yaml\n+++ src://owners.yaml\n@@\n-owner: alice\n+owner: bob\n"))
		case "/rest/api/latest/projects/PRJ/repos/repo/raw/owners.yaml":
			if r.URL.Query().Get("at") == "base123" {
				_, _ = w.Write([]byte("owner: alice\n"))
				return
			}
			if r.URL.Query().Get("at") == "head123" {
				_, _ = w.Write([]byte("owner: bob\n"))
				return
			}
			w.WriteHeader(http.StatusBadRequest)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	cfg := config{bitbucketUsername: "user", bitbucketPassword: "pass"}
	prUrl := server.URL + "/projects/PRJ/repos/repo/pull-requests/42"
	owners, err := collectOwnersFromRepository(prUrl, "base123", "head123", nil, cfg, context.Background(), logger)
	if err != nil {
		t.Fatalf("collectOwnersFromRepository() error = %v", err)
	}
	want := []string{"alice", "bob"}
	if len(owners) != len(want) {
		t.Fatalf("expected %d owners, got %d: %#v", len(want), len(owners), owners)
	}
	for i := range want {
		if owners[i] != want[i] {
			t.Fatalf("expected owner %q at index %d, got %q", want[i], i, owners[i])
		}
	}
}

func TestCollectOwnersFromRepositoryIncludesBaseAndHeadOwners(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/rest/api/latest/projects/PRJ/repos/repo/pull-requests/42.diff":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("diff --git src://owners.yaml dst://owners.yaml\n--- dst://owners.yaml\n+++ src://owners.yaml\n@@\n-owner: alice\n+owner: bob\n"))
		case "/rest/api/latest/projects/PRJ/repos/repo/raw/owners.yaml":
			switch r.URL.Query().Get("at") {
			case "master":
				_, _ = w.Write([]byte("owner: alice\n"))
			case "feature":
				_, _ = w.Write([]byte("owner: bob\n"))
			default:
				http.Error(w, "unexpected at value", http.StatusBadRequest)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	prUrl := server.URL + "/projects/PRJ/repos/repo/pull-requests/42"
	cfg := config{bitbucketUsername: "user", bitbucketPassword: "pass"}
	owners, err := collectOwnersFromRepository(prUrl, "master", "feature", []string{}, cfg, context.Background(), logger)
	if err != nil {
		t.Fatalf("collectOwnersFromRepository() error = %v", err)
	}
	want := []string{"alice", "bob"}
	if len(owners) != len(want) {
		t.Fatalf("expected %d owners, got %d: %v", len(want), len(owners), owners)
	}
	for i := range want {
		if owners[i] != want[i] {
			t.Fatalf("expected owner %q at index %d, got %q", want[i], i, owners[i])
		}
	}
}

func TestCollectOwnersFromRepositoryFiltersIgnoredOwners(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/rest/api/latest/projects/PRJ/repos/repo/pull-requests/42.diff":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("diff --git src://owners.yaml dst://owners.yaml\n--- dst://owners.yaml\n+++ src://owners.yaml\n@@\n-owner: alice\n+owner: bob\n"))
		case "/rest/api/latest/projects/PRJ/repos/repo/raw/owners.yaml":
			switch r.URL.Query().Get("at") {
			case "master":
				_, _ = w.Write([]byte("owner: alice\n"))
			case "feature":
				_, _ = w.Write([]byte("owner: bob\n"))
			default:
				http.Error(w, "unexpected at value", http.StatusBadRequest)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	prUrl := server.URL + "/projects/PRJ/repos/repo/pull-requests/42"
	cfg := config{bitbucketUsername: "user", bitbucketPassword: "pass"}
	owners, err := collectOwnersFromRepository(prUrl, "master", "feature", []string{"alice"}, cfg, context.Background(), logger)
	if err != nil {
		t.Fatalf("collectOwnersFromRepository() error = %v", err)
	}
	want := []string{"bob"}
	if len(owners) != len(want) {
		t.Fatalf("expected %d owners, got %d: %v", len(want), len(owners), owners)
	}
	for i := range want {
		if owners[i] != want[i] {
			t.Fatalf("expected owner %q at index %d, got %q", want[i], i, owners[i])
		}
	}
}

func TestResolveBitbucketUsernameUsesAdminUsersFilter(t *testing.T) {
	originalClient := http.DefaultClient
	http.DefaultClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodGet {
			t.Fatalf("expected GET request, got %s", req.Method)
		}
		if req.URL.Path != "/rest/api/latest/admin/users" {
			t.Fatalf("unexpected path: %s", req.URL.Path)
		}
		if req.URL.Query().Get("filter") != "alice" {
			t.Fatalf("unexpected filter: %q", req.URL.Query().Get("filter"))
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"values":[{"name":"Alice"}]}`)),
		}, nil
	})}
	defer func() { http.DefaultClient = originalClient }()

	got, err := resolveBitbucketUsername("https://bitbucket.example/projects/myproj/repos/myrepo/pull-requests/42", "alice", config{bitbucketUsername: "user", bitbucketPassword: "pass"}, context.Background(), logger)
	if err != nil {
		t.Fatalf("resolveBitbucketUsername() error = %v", err)
	}
	if got != "Alice" {
		t.Fatalf("expected canonical username Alice, got %q", got)
	}
}

func TestBuildPRPayloadUsernames(t *testing.T) {
	pr := buildPRPayload(BitbucketEventPullRequest{}, []string{"alice", "bob"})

	reviewers := pr.Reviewers
	if len(reviewers) != 2 {
		t.Fatalf("expected 2 reviewers, got %d", len(reviewers))
	}

	if reviewers[0].User.Name != "alice" {
		t.Fatalf("expected first reviewer to be alice, got %#v", reviewers[0].User)
	}
	if reviewers[1].User.Name != "bob" {
		t.Fatalf("expected second reviewer to be bob, got %#v", reviewers[1].User)
	}
}

func TestHandleWebhookRejectsUnauthorizedMissingCredentials(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(""))
	rec := httptest.NewRecorder()

	wrapped := basicAuth("user", "pass", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	wrapped.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected status 401, got %d", rec.Code)
	}
}

func TestHandleWebhookRejectsUnauthorizedWrongCredentials(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(""))
	req.SetBasicAuth("user", "wrongpass")
	rec := httptest.NewRecorder()

	wrapped := basicAuth("user", "pass", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	wrapped.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected status 401, got %d", rec.Code)
	}
}

func TestLogInfoIncludesRepoField(t *testing.T) {
	oldLogger := logger
	var buf bytes.Buffer
	initLogger(&buf, slog.LevelInfo)
	defer func() {
		if oldLogger != nil {
			logger = oldLogger
		}
	}()

	logger.InfoContext(context.Background(), "testing repo metadata", "repo", "myrepo")

	line := strings.TrimSpace(buf.String())
	var payload map[string]string
	if err := json.Unmarshal([]byte(line), &payload); err != nil {
		t.Fatalf("unmarshal log output: %v", err)
	}

	if payload["repo"] != "myrepo" {
		t.Fatalf("expected repo field myrepo, got %q", payload["repo"])
	}
}

func TestHandleWebhookAcceptsAuthorized(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(""))
	req.SetBasicAuth("user", "pass")
	rec := httptest.NewRecorder()

	wrapped := basicAuth("user", "pass", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	wrapped.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}
}

func TestUpdatePullRequestWithConflict(t *testing.T) {
	var putCalls int
	var gotResolveCall bool

	originalClient := http.DefaultClient
	http.DefaultClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.Method {
		case http.MethodGet:
			if req.URL.Path == "/rest/api/latest/admin/users" {
				gotResolveCall = true
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader(`{"values":[{"name":"alice"}]}`)),
				}, nil
			}
			t.Fatalf("expected admin user lookup GET, got %s %s", req.Method, req.URL.String())
		case http.MethodPut:
			putCalls++
			if putCalls == 1 {
				return &http.Response{
					StatusCode: http.StatusConflict,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader(`{"errors":[{"reviewerErrors":[{"context":"alice"}]}]}`)),
				}, nil
			}
			if putCalls == 2 {
				bodyBytes, err := io.ReadAll(req.Body)
				if err != nil {
					t.Fatalf("read request body: %v", err)
				}

				var payload BitbucketPullRequest
				if err := json.Unmarshal(bodyBytes, &payload); err != nil {
					t.Fatalf("unmarshal second request body: %v", err)
				}
				if len(payload.Reviewers) != 0 {
					t.Fatalf("expected reviewers array to be empty after removing conflicted reviewer, got %d entries", len(payload.Reviewers))
				}
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader("")),
			}, nil
		default:
			t.Fatalf("unexpected method %s", req.Method)
		}
		return nil, nil
	})}
	defer func() { http.DefaultClient = originalClient }()

	err := updatePullRequest("https://bitbucket.example/workspace/repo/pull-requests/42", BitbucketEventPullRequest{}, []string{"alice"}, config{}, "repo", context.Background(), logger)
	if err != nil {
		t.Fatalf("expected conflict retry to succeed, got %v", err)
	}
	if !gotResolveCall {
		t.Fatalf("expected admin user lookup request before PUT")
	}
	if putCalls != 2 {
		t.Fatalf("expected 2 PUT calls, got %d", putCalls)
	}
}

func TestHandleWebhookUpdatesReviewers(t *testing.T) {
	var putCalls int
	var gotBody string

	originalClient := http.DefaultClient
	http.DefaultClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.Method {
		case http.MethodPut:
			putCalls++
			bodyBytes, err := io.ReadAll(req.Body)
			if err != nil {
				t.Fatalf("read body: %v", err)
			}
			gotBody = string(bodyBytes)
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(""))}, nil
		case http.MethodGet:
			switch req.URL.Path {
			case "/rest/api/latest/projects/myproj/repos/myrepo/pull-requests/42.diff":
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("diff --git src://changed.yaml dst://changed.yaml\n--- dst://changed.yaml\n+++ src://changed.yaml\n@@\n-owner: alice\n+owner: charlie\n"))}, nil
			case "/rest/api/latest/projects/myproj/repos/myrepo/raw/changed.yaml":
				switch req.URL.Query().Get("at") {
				case "master":
					return &http.Response{StatusCode: http.StatusNotFound, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("not found"))}, nil
				case "feature":
					return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("owner: charlie\n"))}, nil
				default:
					return &http.Response{StatusCode: http.StatusBadRequest, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("bad at value"))}, nil
				}
			default:
				return &http.Response{StatusCode: http.StatusNotFound, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("not found"))}, nil
			}
		default:
			return &http.Response{StatusCode: http.StatusMethodNotAllowed, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(""))}, nil
		}
	})}
	defer func() { http.DefaultClient = originalClient }()

	t.Setenv("BR_BITBUCKET_USERNAME", "user")
	t.Setenv("BR_BITBUCKET_PASSWORD", "pass")
	t.Setenv("BR_BITBUCKET_TOKEN", "")
	t.Setenv("BR_ALLOWED_PR_ORIGIN", "https://bitbucket.example")
	t.Setenv("BR_WEBHOOK_USERNAME", "user")
	t.Setenv("BR_WEBHOOK_PASSWORD", "pass")

	prUrl := "https://bitbucket.example/projects/myproj/repos/myrepo/pull-requests/42"
	cloneUrl := "https://bitbucket.example/scm/myproj/repo.git"
	eventBody := `{"pullRequest":{"links":{"self":[{"href":"` + prUrl + `"}]},"fromRef":{"id":"refs/heads/feature","latestCommit":"feature","repository":{"links":{"clone":[{"href":"` + cloneUrl + `","name":"http"}]}}},"toRef":{"id":"refs/heads/master","latestCommit":"master"}}}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(eventBody))
	rec := httptest.NewRecorder()

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}

	handleWebhook(rec, req, cfg)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}

	if putCalls != 1 {
		t.Fatalf("expected 1 PUT request, got %d", putCalls)
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(gotBody), &payload); err != nil {
		t.Fatalf("unmarshal updated payload: %v", err)
	}

	reviewers, ok := payload["reviewers"].([]any)
	if !ok {
		t.Fatalf("expected reviewers array, got %T", payload["reviewers"])
	}
	if len(reviewers) != 1 {
		t.Fatalf("expected 1 reviewer, got %d", len(reviewers))
	}

	reviewer, ok := reviewers[0].(map[string]any)
	if !ok {
		t.Fatalf("expected reviewer object, got %T", reviewers[0])
	}

	user, ok := reviewer["user"].(map[string]any)
	if !ok {
		t.Fatalf("expected user object, got %T", reviewer["user"])
	}

	if user["name"] != "charlie" {
		t.Fatalf("expected reviewer username charlie, got %v", user["name"])
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}
