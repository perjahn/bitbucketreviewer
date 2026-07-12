package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestHandleWebhookE2E(t *testing.T) {
	repoBase := filepath.Join(t.TempDir())
	setupGitRepo1(t, repoBase, "scm/PRJ", "repo")

	var reviewerRequests int
	var approvalRequests int
	var reviewerBodies []string
	var approvalBodies []string
	var server *httptest.Server

	makeEventBody := func(reviewerStatuses map[string]bool) string {
		cloneUrl := server.URL + "/scm/PRJ/repo"
		prUrl := server.URL + "/projects/PRJ/repos/repo/pull-requests/42"
		parts := make([]string, 0, len(reviewerStatuses))
		for name, approved := range reviewerStatuses {
			parts = append(parts, `{"approved":`+map[bool]string{true: "true", false: "false"}[approved]+`,"user":{"name":"`+name+`"}}`)
		}
		if len(parts) == 0 {
			return `{"pullRequest":{"links":{"self":[{"href":"` + prUrl + `"}]},"reviewers":[],"fromRef":{"id":"refs/heads/feature","latestCommit":"feature","repository":{"links":{"clone":[{"href":"` + cloneUrl + `","name":"http"}]}}},"toRef":{"id":"refs/heads/master","latestCommit":"master"}}}`
		}
		return `{"pullRequest":{"links":{"self":[{"href":"` + prUrl + `"}]},"reviewers":[` + strings.Join(parts, ",") + `],"fromRef":{"id":"refs/heads/feature","latestCommit":"feature","repository":{"links":{"clone":[{"href":"` + cloneUrl + `","name":"http"}]}}},"toRef":{"id":"refs/heads/master","latestCommit":"master"}}}`
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/scm/PRJ/repo/", func(w http.ResponseWriter, r *http.Request) {
		t.Logf("Proxying git http backend for %s %s '%s'", r.Method, r.URL.Path, repoBase)
		proxyGitHttpBackend(w, r, repoBase)
	})
	mux.HandleFunc("/rest/api/latest/projects/PRJ/repos/repo/pull-requests/42.diff", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("diff --git src://owners.yaml dst://owners.yaml\n--- dst://owners.yaml\n+++ src://owners.yaml\n@@\n-owner: alice\n+owner: bob\n"))
	})
	mux.HandleFunc("/rest/api/latest/projects/PRJ/repos/repo/raw/owners.yaml", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		switch r.URL.Query().Get("at") {
		case "refs/heads/master", "master":
			_, _ = w.Write([]byte("owner: alice\n"))
		case "refs/heads/feature", "feature":
			_, _ = w.Write([]byte("owner: bob\n"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	mux.HandleFunc("/rest/api/latest/projects/PRJ/repos/repo/pull-requests/42", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		reviewerRequests++
		bodyBytes, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read reviewer request body: %v", err)
		}
		reviewerBodies = append(reviewerBodies, string(bodyBytes))

		var payload map[string]any
		if err := json.Unmarshal(bodyBytes, &payload); err != nil {
			t.Fatalf("unmarshal reviewer payload: %v", err)
		}
		reviewers, ok := payload["reviewers"].([]any)
		if !ok || len(reviewers) != 2 {
			t.Fatalf("expected two reviewers to be added, got %#v", payload["reviewers"])
		}
		reviewerNames := map[string]bool{}
		for _, rev := range reviewers {
			reviewerMap, ok := rev.(map[string]any)
			if !ok {
				t.Fatalf("expected reviewer object, got %T", rev)
			}
			user, ok := reviewerMap["user"].(map[string]any)
			if !ok {
				t.Fatalf("expected reviewer user object, got %T", reviewerMap["user"])
			}
			reviewerNames[user["name"].(string)] = true
		}
		if !reviewerNames["alice"] || !reviewerNames["bob"] {
			t.Fatalf("expected reviewers alice and bob, got %#v", reviewerNames)
		}
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/rest/api/latest/projects/PRJ/repos/repo/pull-requests/42/participants/user", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		approvalRequests++
		bodyBytes, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read approval request body: %v", err)
		}
		approvalBodies = append(approvalBodies, string(bodyBytes))
		var payload map[string]string
		if err := json.Unmarshal(bodyBytes, &payload); err != nil {
			t.Fatalf("unmarshal approval payload: %v", err)
		}
		if payload["status"] != "APPROVED" {
			t.Fatalf("expected approval status APPROVED, got %q", payload["status"])
		}
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})

	server = httptest.NewServer(mux)
	defer server.Close()

	t.Setenv("BR_BITBUCKET_USERNAME", "user")
	t.Setenv("BR_BITBUCKET_PASSWORD", "pass")
	t.Setenv("BR_BITBUCKET_TOKEN", "")
	t.Setenv("BR_ALLOWED_PR_ORIGIN", server.URL)
	t.Setenv("BR_WEBHOOK_USERNAME", "user")
	t.Setenv("BR_WEBHOOK_PASSWORD", "pass")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}

	call := func(reviewers map[string]bool) {
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(makeEventBody(reviewers)))
		rec := httptest.NewRecorder()
		handleWebhook(rec, req, cfg)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected status 200 for reviewers=%v, got %d: %s", reviewers, rec.Code, rec.Body.String())
		}
	}

	call(nil)
	if reviewerRequests != 1 {
		t.Fatalf("expected first reviewer add request after initial webhook, got %d", reviewerRequests)
	}
	if approvalRequests != 0 {
		t.Fatalf("expected no default-reviewer approval before both owners approved, got %d", approvalRequests)
	}

	call(map[string]bool{"alice": true})
	if reviewerRequests != 2 {
		t.Fatalf("expected second reviewer sync after first owner approval, got %d", reviewerRequests)
	}
	if approvalRequests != 0 {
		t.Fatalf("expected no approval yet when only the first owner approved, got %d", approvalRequests)
	}

	call(map[string]bool{"alice": true, "bob": true})
	if approvalRequests != 1 {
		t.Fatalf("expected final default-reviewer approval after both owners approved, got %d", approvalRequests)
	}
	if len(approvalBodies) != 1 {
		t.Fatalf("expected one approval request payload, got %#v", approvalBodies)
	}

	var approvalPayload map[string]string
	if err := json.Unmarshal([]byte(approvalBodies[0]), &approvalPayload); err != nil {
		t.Fatalf("unmarshal approval payload: %v", err)
	}
	if approvalPayload["status"] != "APPROVED" {
		t.Fatalf("expected final approval status APPROVED, got %q", approvalPayload["status"])
	}
}

func setupGitRepo1(t *testing.T, repoRoot, subPath, repoName string) {
	repoBase := filepath.Join(repoRoot, subPath)
	repoPath := filepath.Join(repoBase, repoName)

	if err := os.MkdirAll(repoBase, 0o700); err != nil {
		t.Fatalf("mkdir repo parent: %v", err)
	}

	runGit(t, "init", "--bare", repoPath)

	workDir := filepath.Join(t.TempDir(), "work")
	runGit(t, "clone", repoPath, workDir)
	runGit(t, "-C", workDir, "config", "user.name", "Test User")
	runGit(t, "-C", workDir, "config", "user.email", "test@example.com")
	runGit(t, "-C", workDir, "checkout", "-b", "master")

	if err := os.WriteFile(filepath.Join(workDir, "owners.yaml"), []byte("owner: alice\n"), 0o600); err != nil {
		t.Fatalf("write owners.yaml: %v", err)
	}

	runGit(t, "-C", workDir, "add", "owners.yaml")
	runGit(t, "-C", workDir, "commit", "-m", "add owners")
	runGit(t, "-C", workDir, "push", "origin", "master")

	runGit(t, "-C", workDir, "checkout", "-b", "feature")
	if err := os.WriteFile(filepath.Join(workDir, "changed.yaml"), []byte("owner: alice\n"), 0o600); err != nil {
		t.Fatalf("write changed.yaml: %v", err)
	}
	runGit(t, "-C", workDir, "add", "changed.yaml")
	runGit(t, "-C", workDir, "commit", "-m", "add changed owners")
	runGit(t, "-C", workDir, "push", "origin", "feature")
}

func runGit(t *testing.T, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
}
