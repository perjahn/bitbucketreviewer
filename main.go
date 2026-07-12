package main

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/goccy/go-yaml"
)

var ErrNoYamlFilesChanged = errors.New("no yaml files changed in pr")

type config struct {
	port              string
	bitbucketUsername string
	bitbucketPassword string
	bitbucketToken    string
	webhookUsername   string
	webhookPassword   string
	allowedPROrigin   string
	ignoreOwners      []string
	dryRunRepos       []string
}

var logger *slog.Logger

func init() {
	if strings.HasSuffix(os.Args[0], ".test") {
		initLogger(io.Discard, slog.LevelError)
	} else {
		initLogger(os.Stderr, slog.LevelInfo)
	}
	log.SetFlags(0)
	log.SetOutput(os.Stderr)
}

func initLogger(output io.Writer, level slog.Level) {
	logger = slog.New(slog.NewJSONHandler(output, &slog.HandlerOptions{Level: level}))
}

func main() {
	cfg, err := loadConfig()
	if err != nil {
		os.WriteFile("/tmp/bitbucketreviewer_error.txt", []byte(err.Error()), 0600)
		log.Fatalf("configuration error: %v", err)
	}

	os.WriteFile("/tmp/bitbucketreviewer_start.txt", fmt.Appendf(nil, "%+v", cfg), 0600)

	http.Handle("/", basicAuth(cfg.webhookUsername, cfg.webhookPassword, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handleWebhook(w, r, cfg)
	})))

	logger.InfoContext(context.Background(), "listening on :"+cfg.port, "port", cfg.port)
	if err := http.ListenAndServe(":"+cfg.port, nil); err != nil {
		logger.ErrorContext(context.Background(), "listen failed", "error", err)
		os.Exit(1)
	}
}

func loadConfig() (config, error) {
	port := strings.TrimSpace(os.Getenv("BR_PORT"))
	if port == "" {
		port = "8080"
	}
	portNum, err := strconv.Atoi(port)
	if err != nil || portNum < 0 || portNum > 65535 {
		return config{}, fmt.Errorf("BR_PORT must be a valid port number between 0 and 65535: '%s'", port)
	}

	bitbucketUsername := strings.TrimSpace(os.Getenv("BR_BITBUCKET_USERNAME"))
	bitbucketPassword := strings.TrimSpace(os.Getenv("BR_BITBUCKET_PASSWORD"))
	bitbucketToken := strings.TrimSpace(os.Getenv("BR_BITBUCKET_TOKEN"))
	webhookUsername := strings.TrimSpace(os.Getenv("BR_WEBHOOK_USERNAME"))
	webhookPassword := strings.TrimSpace(os.Getenv("BR_WEBHOOK_PASSWORD"))
	if bitbucketUsername == "" {
		return config{}, fmt.Errorf("BR_BITBUCKET_USERNAME is not set/empty")
	}
	if bitbucketPassword == "" && bitbucketToken == "" {
		return config{}, fmt.Errorf("either BR_BITBUCKET_PASSWORD or BR_BITBUCKET_TOKEN is required")
	}
	if webhookUsername == "" {
		return config{}, fmt.Errorf("BR_WEBHOOK_USERNAME is not set/empty")
	}
	if webhookPassword == "" {
		return config{}, fmt.Errorf("BR_WEBHOOK_PASSWORD is not set/empty")
	}

	allowedPROrigin := strings.TrimSpace(os.Getenv("BR_ALLOWED_PR_ORIGIN"))
	if allowedPROrigin == "" {
		return config{}, fmt.Errorf("BR_ALLOWED_PR_ORIGIN is not set/empty")
	}
	if _, err := url.ParseRequestURI(allowedPROrigin); err != nil {
		return config{}, fmt.Errorf("BR_ALLOWED_PR_ORIGIN is not a valid url: '%v'", err)
	}

	ignoreOwners := splitOwnerValues(strings.TrimSpace(os.Getenv("BR_IGNORE_OWNERS")))
	dryRunRepos := splitOwnerValues(strings.TrimSpace(os.Getenv("BR_DRY_RUN_REPOS")))

	return config{
		port:              port,
		bitbucketUsername: bitbucketUsername,
		bitbucketPassword: bitbucketPassword,
		bitbucketToken:    bitbucketToken,
		webhookUsername:   webhookUsername,
		webhookPassword:   webhookPassword,
		allowedPROrigin:   allowedPROrigin,
		ignoreOwners:      ignoreOwners,
		dryRunRepos:       dryRunRepos,
	}, nil
}

func basicAuth(username, password string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()

		userOK := subtle.ConstantTimeCompare([]byte(user), []byte(username)) == 1
		passOK := subtle.ConstantTimeCompare([]byte(pass), []byte(password)) == 1

		if !ok || !userOK || !passOK {
			logger.ErrorContext(r.Context(), "unauthorized request: invalid credentials", "remote", r.RemoteAddr, "user", user)
			w.Header().Set("WWW-Authenticate", `Basic realm="Restricted"`)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func splitOwnerValues(value string) []string {
	parts := strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == ';'
	})
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		username := strings.TrimSpace(part)
		if username != "" {
			result = append(result, username)
		}
	}
	return result
}

var requestID atomic.Uint64

func handleWebhook(w http.ResponseWriter, r *http.Request, cfg config) {
	requestLogger := logger.With("request_id", requestID.Add(1))
	requestLogger.InfoContext(r.Context(), "received webhook request", "remote", r.RemoteAddr, "method", r.Method, "url", r.URL.String())

	defer r.Body.Close()

	ctx := r.Context()

	body, err := io.ReadAll(r.Body)
	if err != nil {
		requestLogger.ErrorContext(ctx, "failed to read webhook payload", "error", err)
		http.Error(w, "failed to read webhook payload", http.StatusBadRequest)
		return
	}

	if string(body) == "{\"test\": true}" {
		requestLogger.InfoContext(ctx, "got test request")
		w.Write([]byte("Ready 2 Rumble!"))
		return
	}

	var event BitbucketEvent
	if err := json.Unmarshal(body, &event); err != nil {
		requestLogger.ErrorContext(ctx, "failed to parse webhook payload", "payload", string(body), "error", err)
		http.Error(w, "failed to parse webhook payload", http.StatusBadRequest)
		return
	}

	projectKey := event.PullRequest.ToRef.Repository.Project.Key
	repoName := event.PullRequest.ToRef.Repository.Name
	prID := event.PullRequest.ID
	repoLogger := requestLogger.With("project", projectKey, "repo", repoName, "pr_id", prID)

	repoUrl, err := getRepoUrl(event, cfg)
	if err != nil {
		repoLogger.ErrorContext(ctx, "failed to get clone url", "error", err)
		http.Error(w, fmt.Sprintf("failed to get clone url: %v", err), http.StatusBadRequest)
		return
	}

	prUrl, err := getPRUrl(event, cfg)
	if err != nil {
		repoLogger.ErrorContext(ctx, "failed to get pull request url", "error", err)
		http.Error(w, fmt.Sprintf("failed to get pull request url: %v", err), http.StatusBadRequest)
		return
	}

	fromRef, err := getPullRequestFromRef(event.PullRequest)
	if err != nil {
		repoLogger.ErrorContext(ctx, "failed to retrieve pull request ref (from)", "repoUrl", repoUrl, "event", event, "error", err)
		http.Error(w, fmt.Sprintf("failed to retrieve pull request ref: %s: %v: %v", repoUrl, event, err), http.StatusBadGateway)
		return
	}

	toRef, err := getPullRequestToRef(event.PullRequest)
	if err != nil {
		repoLogger.ErrorContext(ctx, "failed to retrieve pull request ref (to)", "repoUrl", repoUrl, "event", event, "error", err)
		http.Error(w, fmt.Sprintf("failed to retrieve pull request ref: %s: %v: %v", repoUrl, event, err), http.StatusBadGateway)
		return
	}

	baseCommitID := strings.TrimSpace(event.PullRequest.ToRef.LatestCommit)
	if baseCommitID == "" {
		baseCommitID = strings.TrimSpace(event.PullRequest.ToRef.ID)
	}
	headCommitID := strings.TrimSpace(event.PullRequest.FromRef.LatestCommit)
	if headCommitID == "" {
		headCommitID = strings.TrimSpace(event.PullRequest.FromRef.ID)
	}
	if baseCommitID == "" {
		repoLogger.ErrorContext(ctx, "missing pull request base commit in webhook payload", "to_ref", event.PullRequest.ToRef)
		http.Error(w, fmt.Sprintf("missing pull request base commit in webhook payload: %v", event.PullRequest.ToRef), http.StatusBadGateway)
		return
	}
	if headCommitID == "" {
		repoLogger.ErrorContext(ctx, "missing pull request head commit in webhook payload", "from_ref", event.PullRequest.FromRef)
		http.Error(w, fmt.Sprintf("missing pull request head commit in webhook payload: %v", event.PullRequest.FromRef), http.StatusBadGateway)
		return
	}

	owners, err := collectOwnersFromRepository(prUrl, baseCommitID, headCommitID, cfg.ignoreOwners, cfg, ctx, repoLogger)
	if errors.Is(err, ErrNoYamlFilesChanged) {
		repoLogger.InfoContext(ctx, ErrNoYamlFilesChanged.Error(), "prUrl", prUrl, "toRef", toRef, "fromRef", fromRef, "pr_id", event.PullRequest.ID)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err != nil {
		repoLogger.ErrorContext(ctx, "failed to inspect repository yaml files", "repoUrl", repoUrl, "fromRef", fromRef, "toRef", toRef, "pr_id", event.PullRequest.ID, "error", err)
		http.Error(w, fmt.Sprintf("failed to inspect repository yaml files: %s: %s: %s: %v", repoUrl, toRef, fromRef, err), http.StatusBadGateway)
		return
	}
	repoLogger.InfoContext(ctx, "collected owners", "owners", owners)

	if err := updatePullRequest(prUrl, event.PullRequest, owners, cfg, repoName, ctx, repoLogger); err != nil {
		repoLogger.ErrorContext(ctx, "failed to update pull request", "prUrl", prUrl, "pr_id", event.PullRequest.ID, "error", err)
		http.Error(w, fmt.Sprintf("failed to update pull request: %s: %v", prUrl, err), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("reviewers updated"))
}

func getPullRequestFromRef(eventPR BitbucketEventPullRequest) (string, error) {
	id := strings.TrimSpace(eventPR.FromRef.ID)
	if id == "" {
		return "", fmt.Errorf("missing pull request fromRef id in webhook payload")
	}
	return id, nil
}

func getPullRequestToRef(eventPR BitbucketEventPullRequest) (string, error) {
	id := strings.TrimSpace(eventPR.ToRef.ID)
	if id == "" {
		return "", fmt.Errorf("missing pull request toRef id in webhook payload")
	}
	return id, nil
}

func getRepoUrl(event BitbucketEvent, cfg config) (string, error) {
	var repoUrl string
	for _, cloneUrl := range event.PullRequest.FromRef.Repository.Links.Clone {
		if cloneUrl.Name == "http" {
			if _, err := url.ParseRequestURI(cloneUrl.Href); err == nil {
				repoUrl = cloneUrl.Href
				break
			}
		}
	}
	if repoUrl == "" {
		return "", fmt.Errorf("repository clone url not found in webhook payload: %v", event.PullRequest.FromRef.Repository.Links.Clone)
	}
	if !strings.HasPrefix(repoUrl, cfg.allowedPROrigin) {
		return "", fmt.Errorf("invalid repository clone url (invalid origin): '%s'", repoUrl)
	}
	parsedUrl, err := url.ParseRequestURI(repoUrl)
	if err != nil {
		return "", fmt.Errorf("invalid repository clone url (invalid format): '%s': %v", repoUrl, err)
	}
	if parsedUrl.Scheme != "http" && parsedUrl.Scheme != "https" {
		return "", fmt.Errorf("invalid repository clone url (invalid scheme): '%s'", parsedUrl)
	}
	segments := strings.Split(strings.Trim(parsedUrl.Path, "/"), "/")
	if len(segments) != 3 || segments[0] != "scm" {
		return "", fmt.Errorf("invalid repository clone url (invalid path): '%s'", parsedUrl)
	}

	return repoUrl, nil
}

func getPRUrl(event BitbucketEvent, cfg config) (string, error) {
	var prUrl string
	if len(event.PullRequest.Links.Self) > 0 {
		prUrl = event.PullRequest.Links.Self[0].Href
	}
	if prUrl == "" {
		return "", fmt.Errorf("pull request url not found in webhook payload: %v", event.PullRequest.Links.Self)
	}
	if !strings.HasPrefix(prUrl, cfg.allowedPROrigin) {
		return "", fmt.Errorf("invalid pull request url (invalid origin): '%s'", prUrl)
	}
	parsedUrl, err := url.ParseRequestURI(prUrl)
	if err != nil {
		return "", fmt.Errorf("invalid pull request url (invalid format): '%s': %v", prUrl, err)
	}
	if parsedUrl.Scheme != "http" && parsedUrl.Scheme != "https" {
		return "", fmt.Errorf("invalid pull request url (invalid scheme): '%s'", prUrl)
	}
	segments := strings.Split(strings.Trim(parsedUrl.Path, "/"), "/")
	if len(segments) != 6 || segments[0] != "projects" || segments[2] != "repos" || segments[4] != "pull-requests" {
		return "", fmt.Errorf("invalid pull request url (invalid path): '%s'", prUrl)
	}

	return prUrl, nil
}

func collectOwnersFromRepository(prUrl, baseCommitID, headCommitID string, ignoreOwners []string, cfg config, ctx context.Context, repoLogger *slog.Logger) ([]string, error) {
	parsedUrl, err := url.ParseRequestURI(prUrl)
	if err != nil {
		return nil, fmt.Errorf("invalid pull request url (invalid format): '%s': %v", prUrl, err)
	}

	diffUrl, err := url.JoinPath(parsedUrl.Scheme+"://"+parsedUrl.Host, "/rest/api/latest/"+parsedUrl.Path+".diff")
	if err != nil {
		return nil, fmt.Errorf("failed to construct pull request diff url: %w", err)
	}

	req, err := http.NewRequest(http.MethodGet, diffUrl, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create pull request diff request: %w", err)
	}
	req.Header.Set("Accept", "text/plain")
	applyBitbucketAuth(req, cfg)

	repoLogger.InfoContext(ctx, "fetching pull request diff", "url", diffUrl, "username", cfg.bitbucketUsername, "method", req.Method)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch pull request diff: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read pull request diff: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	yamlFiles := extractYamlFilesFromDiff(string(body))
	repoLogger.InfoContext(ctx, "found yaml files in diff", "yaml_files", yamlFiles)
	if len(yamlFiles) == 0 {
		return nil, ErrNoYamlFilesChanged
	}

	allOwners := make([]string, 0)
	for _, path := range yamlFiles {
		for _, commitID := range []string{baseCommitID, headCommitID} {
			yamlOwners, err := ownersFromYamlAtRef(prUrl, commitID, path, cfg, ctx, repoLogger)
			if err != nil {
				return nil, fmt.Errorf("couldn't read yaml file %s at %s: %w", path, commitID, err)
			}
			for _, owner := range yamlOwners {
				if slices.Contains(ignoreOwners, owner) {
					repoLogger.InfoContext(ctx, "ignoring owner", "owner", owner, "path", path, "commit_id", commitID)
					continue
				}
				if !slices.Contains(allOwners, owner) {
					allOwners = append(allOwners, owner)
				}
			}
		}
	}

	if len(allOwners) == 0 {
		return nil, fmt.Errorf("no owners found in yaml files: %v", yamlFiles)
	}

	return allOwners, nil
}

func extractYamlFilesFromDiff(diff string) []string {
	yamlFiles := make([]string, 0)
	for line := range strings.SplitSeq(diff, "\n") {
		yamlFileSrc := ""
		yamlFileDst := ""
		switch {
		case strings.HasPrefix(line, "diff --git "):
			parts := strings.Fields(line)
			if len(parts) == 4 {
				yamlFileSrc = strings.TrimPrefix(parts[2], "src://")
				yamlFileDst = strings.TrimPrefix(parts[3], "dst://")
			}
		case strings.HasPrefix(line, "+++ src://"):
			yamlFileSrc = strings.TrimPrefix(line, "+++ src://")
		case strings.HasPrefix(line, "--- dst://"):
			yamlFileDst = strings.TrimPrefix(line, "--- dst://")
		}
		yamlFileSrc = strings.TrimSpace(yamlFileSrc)
		if yamlFileSrc != "" {
			lowerPath := strings.ToLower(yamlFileSrc)
			if strings.HasSuffix(lowerPath, ".yaml") || strings.HasSuffix(lowerPath, ".yml") {
				if !slices.Contains(yamlFiles, yamlFileSrc) {
					yamlFiles = append(yamlFiles, yamlFileSrc)
				}
			}
		}
		if yamlFileDst != "" {
			lowerPath := strings.ToLower(yamlFileDst)
			if strings.HasSuffix(lowerPath, ".yaml") || strings.HasSuffix(lowerPath, ".yml") {
				if !slices.Contains(yamlFiles, yamlFileDst) {
					yamlFiles = append(yamlFiles, yamlFileDst)
				}
			}
		}
	}
	return yamlFiles
}

func ownersFromYamlAtRef(prUrl, commitID, path string, cfg config, ctx context.Context, repoLogger *slog.Logger) ([]string, error) {
	if strings.TrimSpace(commitID) == "" {
		return nil, fmt.Errorf("empty commit id for %s", path)
	}
	parsedUrl, err := url.ParseRequestURI(prUrl)
	if err != nil {
		return nil, fmt.Errorf("invalid pull request url (invalid format): '%s': %v", prUrl, err)
	}
	segments := strings.Split(strings.Trim(parsedUrl.Path, "/"), "/")
	projectKey := segments[1]
	repoName := segments[3]

	rawUrl, err := url.JoinPath(parsedUrl.Scheme+"://"+parsedUrl.Host, "/rest/api/latest/projects/"+projectKey+"/repos/"+repoName+"/raw/"+path)
	if err != nil {
		return nil, fmt.Errorf("failed to construct raw file url for %s: %w", path, err)
	}
	q := url.Values{}
	q.Set("at", commitID)
	rawUrl += "?" + q.Encode()

	req, err := http.NewRequest(http.MethodGet, rawUrl, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create raw file request: %w", err)
	}
	applyBitbucketAuth(req, cfg)

	repoLogger.InfoContext(ctx, "fetching yaml file at commit", "path", path, "commit_id", commitID, "url", rawUrl, "username", cfg.bitbucketUsername, "method", req.Method)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch yaml file %s at %s: %w", path, commitID, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}

	contents, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read yaml file %s at %s: %w", path, commitID, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("unexpected status %d for %s at %s: %s", resp.StatusCode, path, commitID, strings.TrimSpace(string(contents)))
	}

	var repo Repo
	if err := yaml.Unmarshal(contents, &repo); err != nil {
		return nil, fmt.Errorf("couldn't parse yaml file %s: %w", path, err)
	}
	if strings.TrimSpace(repo.Owner) == "" {
		return nil, fmt.Errorf("couldn't parse yaml file %s: missing owner", path)
	}

	return splitOwnerValues(repo.Owner), nil
}

func updatePullRequest(prUrl string, eventPR BitbucketEventPullRequest, owners []string, cfg config, repoName string, ctx context.Context, repoLogger *slog.Logger) error {

	// If all owners have approved the PR, call
	// PUT /rest/api/latest/projects/MYPROJ/repos/my-repo/pull-requests/123/approve
	// to approve the PR automatically as a default reviewer.
	// else check if any owner isn't a reviewer yet, and if so, add them as a reviewer.

	ownerApprovals := getOwnerApprovalCount(eventPR, owners)
	if len(owners) > 0 && ownerApprovals == len(owners) {
		repoLogger.InfoContext(ctx, "all owners approved PR, approving automatically")
		err := approvePullRequest(prUrl, repoName, cfg, ctx, repoLogger)
		if err != nil {
			return fmt.Errorf("PR id: %d. Failed to approve pr: %w\n", eventPR.ID, err)
		} else {
			repoLogger.InfoContext(ctx, "successfully approved PR")
			return nil
		}
	}

	repoLogger.InfoContext(ctx, "owner approval status",
		"approved_count", ownerApprovals,
		"total_owners", len(owners),
	)

	if ownersNeedToBeAddedToPRAsReviewers(eventPR, owners) {
		repoLogger.InfoContext(ctx, "adding owners to PR as reviewers", "owners", owners)

		err := addOwnersToPRAsReviewers(prUrl, eventPR, owners, cfg, repoName, ctx, repoLogger)
		if err != nil {
			return fmt.Errorf("PR id: %d. Failed to add owners to pr as reviewers: %w\n", eventPR.ID, err)
		} else {
			repoLogger.InfoContext(ctx, "successfully added owners to PR as reviewers")
			return nil
		}
	}

	return nil
}

func getOwnerApprovalCount(eventPR BitbucketEventPullRequest, owners []string) int {
	var count int
	for _, reviewer := range eventPR.Reviewers {
		if reviewer.Approved && slices.ContainsFunc(owners, func(owner string) bool {
			return strings.EqualFold(owner, reviewer.User.Name)
		}) {
			count++
		}
	}
	return count
}

func approvePullRequest(prUrl, repoName string, cfg config, ctx context.Context, repoLogger *slog.Logger) error {
	parsedUrl, err := url.ParseRequestURI(prUrl)
	if err != nil {
		return fmt.Errorf("invalid pull request url (invalid format): '%s': %v", prUrl, err)
	}

	approveUrl, err := url.JoinPath(parsedUrl.Scheme+"://"+parsedUrl.Host, "/rest/api/latest", parsedUrl.Path, "participants", cfg.bitbucketUsername)
	if err != nil {
		return fmt.Errorf("failed to construct rest api url: %w", err)
	}

	payload, err := json.Marshal(map[string]string{"status": "APPROVED"})
	if err != nil {
		return fmt.Errorf("failed to encode approval payload: %w", err)
	}

	req, err := http.NewRequest(http.MethodPut, approveUrl, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("failed to create approve request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	applyBitbucketAuth(req, cfg)

	repoLogger.InfoContext(ctx, "approving pull request", "url", approveUrl, "username", cfg.bitbucketUsername, "method", req.Method, "status", "APPROVED")

	if slices.Contains(cfg.dryRunRepos, repoName) {
		repoLogger.InfoContext(ctx, "dry-run: skipping approve request")
		return nil
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to make approve request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		responseBody, err := io.ReadAll(resp.Body)
		if err != nil {
			return fmt.Errorf("failed to read approve response body: %w", err)
		}

		return fmt.Errorf("unexpected status %d: %s", resp.StatusCode, strings.TrimSpace(string(responseBody)))
	}

	return nil
}

func ownersNeedToBeAddedToPRAsReviewers(eventPR BitbucketEventPullRequest, owners []string) bool {
	if len(owners) == 0 {
		return false
	}

	for _, owner := range owners {
		if !slices.ContainsFunc(eventPR.Reviewers, func(reviewer BitbucketEventPullRequestReviewer) bool {
			return strings.EqualFold(reviewer.User.Name, owner)
		}) {
			return true
		}
	}

	return false
}

func addOwnersToPRAsReviewers(prUrl string, eventPR BitbucketEventPullRequest, owners []string, cfg config, repoName string, ctx context.Context, repoLogger *slog.Logger) error {
	parsedUrl, err := url.ParseRequestURI(prUrl)
	if err != nil {
		return fmt.Errorf("invalid pull request url (invalid format): '%s': %v", prUrl, err)
	}

	restapiUrl, err := url.JoinPath(parsedUrl.Scheme+"://"+parsedUrl.Host, "/rest/api/latest", parsedUrl.Path)
	if err != nil {
		return fmt.Errorf("failed to construct rest api url: %w", err)
	}

	resolvedOwners := make([]string, 0, len(owners))
	for _, owner := range owners {
		resolvedOwner, err := resolveBitbucketUsername(prUrl, owner, cfg, ctx, repoLogger)
		if err != nil {
			repoLogger.WarnContext(ctx, "could not resolve bitbucket username; falling back to original value", "owner", owner, "error", err)
			resolvedOwners = append(resolvedOwners, owner)
			continue
		}
		resolvedOwners = append(resolvedOwners, resolvedOwner)
	}

	pr := buildPRPayload(eventPR, resolvedOwners)

	statusCode, responseBody, err := makePRRequest(pr, restapiUrl, repoName, cfg, ctx, repoLogger)
	if err != nil {
		return fmt.Errorf("failed to make request: %w", err)
	}

	if statusCode == http.StatusConflict {
		errorReviewers, err := getErrorReviewers(statusCode, responseBody)
		if err != nil {
			return fmt.Errorf("failed to get error reviewers: %w", err)
		}

		var deletedReviewers []string
		var finalReviewers []string
		for _, reviewer := range pr.Reviewers {
			if slices.Contains(errorReviewers, reviewer.User.Name) {
				deletedReviewers = append(deletedReviewers, reviewer.User.Name)
			} else {
				finalReviewers = append(finalReviewers, reviewer.User.Name)
			}
		}

		pr.Reviewers = make([]BitbucketPullRequestReviewer, 0, len(finalReviewers))
		for _, reviewerName := range finalReviewers {
			pr.Reviewers = append(pr.Reviewers, BitbucketPullRequestReviewer{User: struct {
				Name string `json:"name"`
			}{Name: reviewerName}})
		}

		repoLogger.InfoContext(ctx, "some owners were unavailable as reviewers, retrying with final reviewer list",
			"deleted_reviewers", deletedReviewers,
			"final_reviewers", finalReviewers,
		)

		statusCode, responseBody, err = makePRRequest(pr, restapiUrl, repoName, cfg, ctx, repoLogger)
		if err != nil {
			return fmt.Errorf("failed to make request: %w", err)
		}
	}

	if statusCode < 200 || statusCode >= 300 {
		return fmt.Errorf("unexpected status %d: %s", statusCode, strings.TrimSpace(string(responseBody)))
	}

	return nil
}

func resolveBitbucketUsername(prUrl, username string, cfg config, ctx context.Context, repoLogger *slog.Logger) (string, error) {
	if strings.TrimSpace(username) == "" {
		return "", nil
	}

	parsedUrl, err := url.ParseRequestURI(prUrl)
	if err != nil {
		return "", fmt.Errorf("invalid pull request url (invalid format): '%s': %v", prUrl, err)
	}

	adminUsersUrl, err := url.JoinPath(parsedUrl.Scheme+"://"+parsedUrl.Host, "/rest/api/latest/admin/users")
	if err != nil {
		return "", fmt.Errorf("failed to construct admin users lookup url: %w", err)
	}

	queryUrl, err := url.Parse(adminUsersUrl)
	if err != nil {
		return "", fmt.Errorf("failed to parse admin users lookup url: %w", err)
	}
	q := queryUrl.Query()
	q.Set("filter", username)
	queryUrl.RawQuery = q.Encode()

	req, err := http.NewRequest(http.MethodGet, queryUrl.String(), nil)
	if err != nil {
		return "", fmt.Errorf("failed to create user lookup request: %w", err)
	}
	applyBitbucketAuth(req, cfg)

	repoLogger.InfoContext(ctx, "resolving bitbucket username", "username", username, "url", queryUrl.String())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to lookup bitbucket user %q: %w", username, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read bitbucket user lookup response for %q: %w", username, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("unexpected status %d while resolving %q: %s", resp.StatusCode, username, strings.TrimSpace(string(body)))
	}

	var result struct {
		Values []struct {
			Name string `json:"name"`
		} `json:"values"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("failed to parse bitbucket user lookup response for %q: %w", username, err)
	}
	if len(result.Values) == 0 {
		return username, nil
	}
	if strings.TrimSpace(result.Values[0].Name) == "" {
		return username, nil
	}

	return result.Values[0].Name, nil
}

func buildPRPayload(eventPR BitbucketEventPullRequest, owners []string) BitbucketPullRequest {
	reviewersList := make([]BitbucketPullRequestReviewer, 0, len(eventPR.Reviewers)+len(owners))

	for _, eventprreviewer := range eventPR.Reviewers {
		var reviewer BitbucketPullRequestReviewer
		reviewer.User.Name = eventprreviewer.User.Name
		reviewersList = append(reviewersList, reviewer)
	}

	for _, username := range owners {
		if !slices.ContainsFunc(reviewersList, func(r BitbucketPullRequestReviewer) bool {
			return strings.EqualFold(r.User.Name, username)
		}) {
			var reviewer BitbucketPullRequestReviewer
			reviewer.User.Name = username
			reviewersList = append(reviewersList, reviewer)
		}
	}

	pr := BitbucketPullRequest{
		ID:          eventPR.ID,
		Version:     eventPR.Version,
		Title:       eventPR.Title,
		Description: eventPR.Description,
		FromRef: struct {
			ID string `json:"id"`
		}{ID: eventPR.FromRef.ID},
		ToRef: struct {
			ID string `json:"id"`
		}{ID: eventPR.ToRef.ID},
		Reviewers: reviewersList,
	}

	return pr
}

func makePRRequest(pr BitbucketPullRequest, restapiUrl, repoName string, cfg config, ctx context.Context, repoLogger *slog.Logger) (int, []byte, error) {
	body, err := json.Marshal(pr)
	if err != nil {
		return 0, nil, err
	}

	req, err := http.NewRequest(http.MethodPut, restapiUrl, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	applyBitbucketAuth(req, cfg)

	repoLogger.InfoContext(ctx, "updating pull request", "url", restapiUrl, "username", cfg.bitbucketUsername, "method", req.Method)

	if slices.Contains(cfg.dryRunRepos, repoName) {
		repoLogger.InfoContext(ctx, "dry-run: skipping PR request")
		return http.StatusOK, []byte(""), nil
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()

	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, nil, err
	}

	return resp.StatusCode, responseBody, nil
}

func getErrorReviewers(statusCode int, responseBody []byte) ([]string, error) {
	var pullRequestResponse BitbucketPullRequestResponse
	err := json.Unmarshal(responseBody, &pullRequestResponse)
	if err != nil {
		return nil, fmt.Errorf("failed to parse pr payload %d: '%s': %w", statusCode, strings.TrimSpace(string(responseBody)), err)
	}

	var errorReviewers []string
	for _, e := range pullRequestResponse.Errors {
		for _, re := range e.ReviewerErrors {
			errorReviewers = append(errorReviewers, re.Context)
		}
	}
	return errorReviewers, nil
}

func applyBitbucketAuth(req *http.Request, cfg config) {
	if cfg.bitbucketUsername != "" {
		if cfg.bitbucketPassword != "" {
			req.SetBasicAuth(cfg.bitbucketUsername, cfg.bitbucketPassword)
		} else if cfg.bitbucketToken != "" {
			req.SetBasicAuth(cfg.bitbucketUsername, cfg.bitbucketToken)
		}
	}
}
