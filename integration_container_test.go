package main

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestBitbucketContainerE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping Bitbucket container image smoke test in short mode")
	}
	if os.Getenv("BR_E2E") != "1" {
		t.Skip("set BR_E2E=1 to run the Bitbucket container e2e test")
	}

	containerRuntime := "docker"
	if env := os.Getenv("BR_CONTAINER_RUNTIME"); env != "" {
		fmt.Printf("Will run container with custom runtime: '%s'\n", env)
		containerRuntime = env
	}
	if _, err := exec.LookPath(containerRuntime); err != nil {
		t.Skipf("%s not installed; skipping Bitbucket container e2e test", containerRuntime)
	}

	var delay bool
	if os.Getenv("BR_E2E_DELAY") == "1" {
		fmt.Printf("Will run rest api requests with 1min delay...\n")
		delay = true
	}

	adminUsername := "admin"
	adminPassword := generatePassword(20)
	alicePassword := generatePassword(20)   // owner1
	bobPassword := generatePassword(20)     // owner2
	charliePassword := generatePassword(20) // pr creator
	davePassword := generatePassword(20)    // default reviewer
	webhookPassword := generatePassword(20)

	t.Logf("Starting up SUT")
	fmt.Printf("Starting up SUT with dave/%s, webhook/%s...\n", davePassword, webhookPassword)
	sutPort, cleanupSut, err := startSut(t, "dave", davePassword, "webhook", webhookPassword)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupSut()

	t.Logf("Starting up Bitbucket container")
	fmt.Printf("Starting up Bitbucket container with admin/%s, alice/%s, bob/%s, charlie/%s, dave/%s...\n",
		adminPassword, alicePassword, bobPassword, charliePassword, davePassword)
	baseUrl, cleanupBitbucket, err := startBitbucketContainer(t, containerRuntime, adminUsername, adminPassword)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupBitbucket()

	adminClient := bitbucketClient{baseUrl: baseUrl, username: adminUsername, password: adminPassword}

	projectKey := "TEST"
	repoSlug := "demo"

	t.Logf("Setting up Bitbucket repo...")

	if err := adminClient.setupBitbucketRepo(projectKey, repoSlug,
		alicePassword, bobPassword, charliePassword, davePassword,
		fmt.Sprintf("http://host.docker.internal:%d/", sutPort), webhookPassword); err != nil {
		t.Fatalf("setup bitbucket repo: %v", err)
	}

	cloneUrl := (&url.URL{
		Scheme: "http",
		Host:   "localhost:7990",
		Path:   fmt.Sprintf("scm/%s/%s.git", projectKey, repoSlug),
		User:   url.UserPassword("charlie", charliePassword),
	}).String()
	workDir := filepath.Join(t.TempDir(), "repo-work")
	if err := setupFeatureBranchWithOwnerChange(t, cloneUrl, workDir, "alice", "bob"); err != nil {
		t.Fatalf("setup feature branch: %v", err)
	}

	charlieClient := bitbucketClient{baseUrl: baseUrl, username: "charlie", password: charliePassword}
	prID, err := charlieClient.createPullRequest(projectKey, repoSlug, "feature", "master", "owners workflow validation", []string{"dave"})
	if err != nil {
		t.Fatalf("create pull request: %v", err)
	}

	fmt.Printf("PR created!\n")

	// Wait for a while to allow the webhook to be processed and reviewers to be added
	time.Sleep(5 * time.Second)

	pr, err := charlieClient.getPullRequest(projectKey, repoSlug, prID)
	if err != nil {
		t.Fatalf("get pull request: %v", err)
	}

	actualReviwers := make([]string, 0, len(pr.Reviewers))
	for _, reviewer := range pr.Reviewers {
		actualReviwers = append(actualReviwers, reviewer.User.Name)
	}

	expectedReviewers := []string{"alice", "bob", "dave"}

	if len(actualReviwers) != len(expectedReviewers) {
		t.Fatalf("expected reviewers %v, got %v", expectedReviewers, actualReviwers)
	}

	for _, expected := range expectedReviewers {
		if !slices.Contains(actualReviwers, expected) {
			t.Fatalf("expected reviewer %q to be present after 10s, got reviewers %#v", expected, actualReviwers)
		}
	}

	var mergable bool
	if mergable, err = charlieClient.checkIfPRIsMergeable(projectKey, repoSlug, prID); err != nil {
		t.Fatalf("check if PR is mergeable: %v", err)
	}
	if mergable {
		t.Fatalf("PR should not be mergeable yet, as it requires 2 approvals from owners")
	}
	if delay {
		time.Sleep(60 * time.Second)
	}

	aliceClient := bitbucketClient{baseUrl: baseUrl, username: "alice", password: alicePassword}
	err = aliceClient.approvePullRequest(projectKey, repoSlug, prID)
	if err != nil {
		t.Fatalf("alice approve pull request: %v", err)
	}

	fmt.Printf("Alice approved PR!\n")
	if delay {
		time.Sleep(60 * time.Second)
	}

	bobClient := bitbucketClient{baseUrl: baseUrl, username: "bob", password: bobPassword}
	err = bobClient.approvePullRequest(projectKey, repoSlug, prID)
	if err != nil {
		t.Fatalf("bob approve pull request: %v", err)
	}

	fmt.Printf("Bob approved PR!\n")
	if delay {
		time.Sleep(60 * time.Second)
	}

	// Wait for a while to allow the webhook to be approve the pr
	time.Sleep(5 * time.Second)

	pr, err = charlieClient.getPullRequest(projectKey, repoSlug, prID)
	if err != nil {
		t.Fatalf("get pull request: %v", err)
	}

	if mergable, err = charlieClient.checkIfPRIsMergeable(projectKey, repoSlug, prID); err != nil {
		t.Fatalf("check if PR is mergeable: %v", err)
	}
	if !mergable {
		t.Fatalf("PR should be mergeable now, as it has 2 approvals from owners")
	}

	fmt.Printf("Great Success!\n")

	if delay {
		fmt.Printf("You can access bitbucket at http://localhost:7990, for 5 more minutes...\n")
		time.Sleep(300 * time.Second)
	}
}

func generatePassword(length int) string {
	if length <= 0 {
		return ""
	}

	const chars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	password := make([]byte, length)
	for i := range password {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(chars))))
		if err != nil {
			panic(err)
		}
		password[i] = chars[n.Int64()]
	}
	return string(password)
}

func startSut(t *testing.T, bitbucketUsername, bitbucketPassword, webhookUsername, webhookPassword string) (int, func(), error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, nil, fmt.Errorf("find free port for SUT: %w", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()

	binPath := filepath.Join(t.TempDir(), "bitbucketreviewer")
	build := exec.Command("go", "build", "-o", binPath)
	if out, err := build.CombinedOutput(); err != nil {
		return 0, nil, fmt.Errorf("failed to build SUT: %w: %s", err, strings.TrimSpace(string(out)))
	}

	var logs bytes.Buffer
	sut := exec.Command(binPath)
	sut.Stdout = io.MultiWriter(os.Stdout, &logs)
	sut.Stderr = io.MultiWriter(os.Stderr, &logs)
	sut.Env = append(os.Environ(), "BR_BITBUCKET_USERNAME="+bitbucketUsername)
	sut.Env = append(sut.Env, "BR_BITBUCKET_PASSWORD="+bitbucketPassword)
	sut.Env = append(sut.Env, "BR_WEBHOOK_USERNAME="+webhookUsername)
	sut.Env = append(sut.Env, "BR_WEBHOOK_PASSWORD="+webhookPassword)
	sut.Env = append(sut.Env, "BR_PORT="+strconv.Itoa(port))
	sut.Env = append(sut.Env, "BR_ALLOWED_PR_ORIGIN=http://")

	if err := sut.Start(); err != nil {
		return 0, nil, fmt.Errorf("failed to start SUT: %w", err)
	}

	cleanup := func() {
		if sut.Process != nil {
			_ = sut.Process.Kill()
			_ = sut.Wait()
		}
		if t.Failed() && logs.Len() > 0 {
			fmt.Fprintf(os.Stderr, "\n--- bitbucketreviewer logs ---\n%s\n--- end bitbucketreviewer logs ---\n", logs.String())
		}
	}

	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(10 * time.Second)
	url := fmt.Sprintf("http://localhost:%d/", port)
	for time.Now().Before(deadline) {
		if sut.ProcessState != nil && sut.ProcessState.Exited() {
			cleanup()
			return 0, nil, fmt.Errorf("SUT exited before it became ready on %s", url)
		}

		req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader([]byte("{\"test\": true}")))
		if err != nil {
			cleanup()
			return 0, nil, fmt.Errorf("create SUT readiness request: %w", err)
		}
		req.SetBasicAuth(webhookUsername, webhookPassword)
		req.Header.Set("Content-Type", "application/json")

		resp, err := client.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			if (resp.StatusCode >= 200 && resp.StatusCode < 300) || resp.StatusCode == http.StatusBadRequest {
				return port, cleanup, nil
			}
			t.Logf("waiting for SUT on %s: %d", url, resp.StatusCode)
		}

		time.Sleep(200 * time.Millisecond)
	}

	if sut.ProcessState != nil && sut.ProcessState.Exited() {
		cleanup()
		return 0, nil, fmt.Errorf("SUT exited before it became ready on %s", url)
	}
	cleanup()
	return 0, nil, fmt.Errorf("SUT did not become ready on %s within 10s", url)
}

func startBitbucketContainer(t *testing.T, containerRuntime, adminUsername, adminPassword string) (string, func(), error) {
	licenseFile := "license.txt"
	licenseBytes, err := os.ReadFile(licenseFile)
	if err != nil {
		return "", nil, fmt.Errorf("read Bitbucket license file from %s: %w", licenseFile, err)
	}
	license := strings.TrimSpace(string(licenseBytes))
	if license == "" {
		return "", nil, fmt.Errorf("Bitbucket license file %s is empty", licenseFile)
	}

	containerName := "bitbucketreviewer-test-e2e"
	_ = exec.Command(containerRuntime, "rm", "-f", containerName).Run()

	baseUrl := "http://localhost:7990"
	start := exec.Command(
		containerRuntime, "run", "-d",
		"--rm",
		"--name", containerName,
		"-p", "7990:7990",
		"-p", "7999:7999",
		"-e", "SETUP_DISPLAYNAME=Bitbucket",
		"-e", "SETUP_BASEURL="+baseUrl,
		"-e", "SETUP_LICENSE",
		"-e", "SETUP_SYSADMIN_USERNAME",
		"-e", "SETUP_SYSADMIN_PASSWORD",
		"-e", "SETUP_SYSADMIN_DISPLAYNAME=Administrator",
		"-e", "SETUP_SYSADMIN_EMAILADDRESS=admin@example.com",
		"-e", "JVM_SUPPORT_RECOMMENDED_ARGS=-Dcom.atlassian.plugins.authentication.basic.auth.filter.force.allow=true",
		"atlassian/bitbucket",
	)
	start.Env = append(start.Environ(),
		"SETUP_LICENSE="+license,
		"SETUP_SYSADMIN_USERNAME="+adminUsername,
		"SETUP_SYSADMIN_PASSWORD="+adminPassword,
	)

	if out, err := start.CombinedOutput(); err != nil {
		return "", nil, fmt.Errorf("Bitbucket container image could not be started: %w: %s", err, strings.TrimSpace(string(out)))
	}

	cleanup := func() {
		_ = exec.Command(containerRuntime, "rm", "-f", containerName).Run()
	}

	deadline := time.Now().Add(5 * time.Minute)
	client := &http.Client{
		Timeout: 5 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	defer func() {
		fmt.Println("\nDone waiting for Bitbucket")
	}()

	for time.Now().Before(deadline) {
		fmt.Printf(".")
		resp, err := client.Get(baseUrl)
		if err == nil {
			location := resp.Header.Get("Location")
			finalUrl := resp.Request.URL.String()
			_ = resp.Body.Close()

			if strings.Contains(location, "/unavailable") || strings.Contains(finalUrl, "/unavailable") {
				t.Logf("waiting for Bitbucket root page to leave /unavailable: %s (location=%q)", finalUrl, location)
				time.Sleep(1 * time.Second)
				continue
			}

			if (resp.StatusCode >= 200 && resp.StatusCode < 300) || resp.StatusCode == http.StatusFound {
				return baseUrl, cleanup, nil
			}
		}
		if resp != nil {
			if err != nil {
				t.Logf("waiting for Bitbucket root page to finish startup at %s: %v (status code: %d)", baseUrl, err, resp.StatusCode)
			} else {
				t.Logf("waiting for Bitbucket root page to finish startup at %s: %d", baseUrl, resp.StatusCode)
			}
		} else {
			if err != nil {
				t.Logf("waiting for Bitbucket root page to finish startup at %s: %v", baseUrl, err)
			} else {
				t.Logf("waiting for Bitbucket root page to finish startup at %s", baseUrl)
			}
		}
		time.Sleep(1 * time.Second)
	}

	return "", cleanup, fmt.Errorf("Bitbucket root page did not leave /unavailable within 5 minutes")
}

type bitbucketClient struct {
	baseUrl  string
	username string
	password string
	client   *http.Client
}

func (c *bitbucketClient) httpClient() *http.Client {
	if c.client != nil {
		return c.client
	}
	c.client = &http.Client{Timeout: 30 * time.Second}
	return c.client
}

func (c *bitbucketClient) do(req *http.Request) (*http.Response, error) {
	req.SetBasicAuth(c.username, c.password)
	return c.httpClient().Do(req)
}

func (c *bitbucketClient) setupBitbucketRepo(projectKey, repoSlug, alicePassword, bobPassword, charliePassword, davePassword, webhookUrl, webhookPassword string) error {
	if err := c.createProject(projectKey, "Test project"); err != nil {
		return fmt.Errorf("create project: %v", err)
	}
	if err := c.createRepo(projectKey, repoSlug); err != nil {
		return fmt.Errorf("create repo: %v", err)
	}

	for _, user := range []struct{ name, displayName, email, password string }{
		{name: "alice", displayName: "Alice", email: "alice@example.com", password: alicePassword},
		{name: "bob", displayName: "Bob", email: "bob@example.com", password: bobPassword},
		{name: "charlie", displayName: "Charlie", email: "charlie@example.com", password: charliePassword},
	} {
		if err := c.createUser(user.name, user.displayName, user.email, user.password); err != nil {
			return fmt.Errorf("create user %s: %w", user.name, err)
		}
		if err := c.grantRepoPermission(projectKey, repoSlug, user.name, "REPO_WRITE"); err != nil {
			return fmt.Errorf("grant repo write to %s: %w", user.name, err)
		}
	}
	for _, user := range []struct{ name, displayName, email, password string }{
		{name: "dave", displayName: "Dave", email: "dave@example.com", password: davePassword},
	} {
		if err := c.createUser(user.name, user.displayName, user.email, user.password); err != nil {
			return fmt.Errorf("create user %s: %w", user.name, err)
		}
		if err := c.grantRepoPermission(projectKey, repoSlug, user.name, "REPO_ADMIN"); err != nil {
			return fmt.Errorf("grant repo admin to %s: %w", user.name, err)
		}
	}
	if err := c.setDefaultReviewers(projectKey, repoSlug, []string{"dave"}, 1); err != nil {
		return fmt.Errorf("set default reviewers: %w", err)
	}

	if err := c.createRepoWebhook(projectKey, repoSlug, "bitbucketreviewer", webhookUrl, "webhook", webhookPassword,
		[]string{"pr:opened", "pr:modified", "pr:reviewer:updated", "pr:reviewer:approved", "pr:reviewer:unapproved"}); err != nil {
		return fmt.Errorf("create repo webhook: %w", err)
	}
	return nil
}

func (c *bitbucketClient) createProject(projectKey, projectName string) error {
	payload := map[string]any{"key": projectKey, "name": projectName, "public": false}
	return c.doJson(http.MethodPost, fmt.Sprintf("%s/rest/api/latest/projects", c.baseUrl), payload)
}

func (c *bitbucketClient) createRepo(projectKey, repoSlug string) error {
	payload := map[string]any{"name": repoSlug, "scmId": "git", "forkable": true}
	return c.doJson(http.MethodPost, fmt.Sprintf("%s/rest/api/latest/projects/%s/repos", c.baseUrl, projectKey), payload)
}

func (c *bitbucketClient) createUser(name, displayName, email, password string) error {
	endpoint := fmt.Sprintf("%s/rest/api/latest/admin/users", c.baseUrl)
	params := url.Values{}
	params.Set("name", name)
	params.Set("displayName", displayName)
	params.Set("emailAddress", email)
	params.Set("password", password)
	params.Set("addToDefaultGroup", "true")

	req, err := http.NewRequest(http.MethodPost, endpoint+"?"+params.Encode(), nil)
	if err != nil {
		return fmt.Errorf("new request: %w", err)
	}
	resp, err := c.do(req)
	if err != nil {
		return fmt.Errorf("send %s %s: %w", http.MethodPost, endpoint, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("unexpected status %d for %s %s: %s", resp.StatusCode, http.MethodPost, endpoint, strings.TrimSpace(string(body)))
	}
	return nil
}

func (c *bitbucketClient) grantRepoPermission(projectKey, repoSlug, username, permission string) error {
	endpoint := fmt.Sprintf("%s/rest/api/latest/projects/%s/repos/%s/permissions/users", c.baseUrl, projectKey, repoSlug)
	params := url.Values{}
	params.Set("name", username)
	params.Set("permission", permission)

	req, err := http.NewRequest(http.MethodPut, endpoint+"?"+params.Encode(), nil)
	if err != nil {
		return fmt.Errorf("new request: %w", err)
	}
	resp, err := c.do(req)
	if err != nil {
		return fmt.Errorf("send %s %s: %w", http.MethodPut, endpoint, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("unexpected status %d for %s %s: %s", resp.StatusCode, http.MethodPut, endpoint, strings.TrimSpace(string(body)))
	}
	return nil
}

func (c *bitbucketClient) setDefaultReviewers(projectKey, repoSlug string, reviewers []string, requiredApprovals int) error {
	reviewerStructs := make([]map[string]any, 0, len(reviewers))
	for _, name := range reviewers {
		userID, err := c.getUserIDByName(name)
		if err != nil {
			return err
		}
		reviewerStructs = append(reviewerStructs, map[string]any{
			"id": userID,
		})
	}

	payload := map[string]any{
		"requiredApprovals": requiredApprovals,
		"reviewers":         reviewerStructs,
		"sourceMatcher": map[string]any{
			"displayId": "main",
			"id":        "refs/heads/main",
			"type": map[string]any{
				"id":   "ANY_REF",
				"name": "Branch",
			},
		},
		"targetMatcher": map[string]any{
			"displayId": "master",
			"id":        "refs/heads/master",
			"type": map[string]any{
				"id":   "BRANCH",
				"name": "Branch",
			},
		},
	}
	return c.doJson(http.MethodPost, fmt.Sprintf("%s/rest/default-reviewers/latest/projects/%s/repos/%s/condition", c.baseUrl, projectKey, repoSlug), payload)
}

func (c *bitbucketClient) getUserIDByName(username string) (int64, error) {
	endpoint := fmt.Sprintf("%s/rest/api/latest/users?filter=%s", c.baseUrl, url.QueryEscape(username))
	var response struct {
		Values []struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
		} `json:"values"`
	}
	if err := c.doJsonRequest(http.MethodGet, endpoint, nil, &response); err != nil {
		return 0, fmt.Errorf("lookup user %q: %w", username, err)
	}
	for _, user := range response.Values {
		if user.Name == username {
			return user.ID, nil
		}
	}
	return 0, fmt.Errorf("user %q not found in Bitbucket users list", username)
}

func (c *bitbucketClient) createRepoWebhook(projectKey, repoSlug, name, webhookUrl, username, password string, events []string) error {
	payload := map[string]any{
		"name":                    name,
		"url":                     webhookUrl,
		"active":                  true,
		"events":                  events,
		"configuration":           map[string]any{},
		"credentials":             map[string]any{"username": username, "password": password},
		"sslVerificationRequired": false,
		"scopeType":               "REPOSITORY",
	}
	var resp bitbucketContainerWebhookResponse
	if err := c.doJsonRequest(http.MethodPost, fmt.Sprintf("%s/rest/api/latest/projects/%s/repos/%s/webhooks", c.baseUrl, projectKey, repoSlug), payload, &resp); err != nil {
		return fmt.Errorf("create repo webhook: %w", err)
	}

	endpoint := fmt.Sprintf("%s/rest/api/latest/projects/%s/repos/%s/webhooks/test", c.baseUrl, projectKey, repoSlug)
	params := url.Values{}
	params.Set("webhookId", strconv.FormatInt(resp.ID, 10))
	params.Set("sslVerificationRequired", strconv.FormatBool(resp.SslVerificationRequired))
	params.Set("url", resp.Url)

	if err := c.doJson(http.MethodPost, endpoint+"?"+params.Encode(), payload["credentials"]); err != nil {
		return fmt.Errorf("test repo webhook: %w", err)
	}
	return nil
}

type bitbucketContainerWebhookResponse struct {
	ID            int64    `json:"id"`
	Name          string   `json:"name"`
	CreatedDate   int64    `json:"createdDate"`
	UpdatedDate   int64    `json:"updatedDate"`
	Events        []string `json:"events"`
	Configuration struct {
	} `json:"configuration"`
	Credentials struct {
		Username string `json:"username"`
	} `json:"credentials"`
	Url                     string `json:"url"`
	Active                  bool   `json:"active"`
	ScopeType               string `json:"scopeType"`
	SslVerificationRequired bool   `json:"sslVerificationRequired"`
}

func setupFeatureBranchWithOwnerChange(t *testing.T, cloneUrl, workDir, initialOwner, newOwner string) error {
	t.Helper()
	runGit(t, "clone", cloneUrl, workDir)
	runGit(t, "-C", workDir, "config", "user.name", "Charlie")
	runGit(t, "-C", workDir, "config", "user.email", "charlie@example.com")

	if err := os.WriteFile(filepath.Join(workDir, "repo.yaml"), []byte("owner: "+initialOwner+"\n"), 0o600); err != nil {
		return fmt.Errorf("write repo.yaml: %w", err)
	}
	runGit(t, "-C", workDir, "add", "repo.yaml")
	runGit(t, "-C", workDir, "commit", "-m", "add repo")
	runGit(t, "-C", workDir, "push")

	runGit(t, "-C", workDir, "checkout", "-b", "feature")
	if err := os.WriteFile(filepath.Join(workDir, "repo.yaml"), []byte("owner: "+newOwner+"\n"), 0o600); err != nil {
		return fmt.Errorf("update repo.yaml: %w", err)
	}
	runGit(t, "-C", workDir, "add", "repo.yaml")
	runGit(t, "-C", workDir, "commit", "-m", "update repo")
	runGit(t, "-C", workDir, "push", "--set-upstream", "origin", "feature")
	return nil
}

func (c *bitbucketClient) createPullRequest(projectKey, repoSlug, headBranch, baseBranch, title string, defaultReviewers []string) (int64, error) {
	payload := map[string]any{
		"title":       title,
		"description": "Automated PR created by bitbucketreviewer container e2e test",
		"state":       "OPEN",
		"open":        true,
		"closed":      false,
		"fromRef": map[string]any{
			"id":         "refs/heads/" + headBranch,
			"repository": map[string]any{"slug": repoSlug, "project": map[string]any{"key": projectKey}},
		},
		"toRef": map[string]any{
			"id":         "refs/heads/" + baseBranch,
			"repository": map[string]any{"slug": repoSlug, "project": map[string]any{"key": projectKey}},
		},
	}
	if len(defaultReviewers) > 0 {
		payload["reviewers"] = make([]map[string]any, 0, len(defaultReviewers))
		for _, reviewer := range defaultReviewers {
			payload["reviewers"] = append(payload["reviewers"].([]map[string]any), map[string]any{
				"user": map[string]any{"name": reviewer},
			})
		}
	}
	var resp struct {
		ID int64 `json:"id"`
	}
	if err := c.doJsonRequest(http.MethodPost, fmt.Sprintf("%s/rest/api/latest/projects/%s/repos/%s/pull-requests", c.baseUrl, projectKey, repoSlug), payload, &resp); err != nil {
		return 0, err
	}
	return resp.ID, nil
}

func (c *bitbucketClient) getPullRequest(projectKey, repoSlug string, prID int64) (bitbucketContainerPR, error) {
	var pr bitbucketContainerPR
	if err := c.doJsonRequest(http.MethodGet, fmt.Sprintf("%s/rest/api/latest/projects/%s/repos/%s/pull-requests/%d", c.baseUrl, projectKey, repoSlug, prID), nil, &pr); err != nil {
		return bitbucketContainerPR{}, err
	}
	return pr, nil
}

func (c *bitbucketClient) approvePullRequest(projectKey, repoSlug string, prID int64) error {
	var pr bitbucketContainerPR
	if err := c.doJsonRequest(http.MethodPut, fmt.Sprintf("%s/rest/api/latest/projects/%s/repos/%s/pull-requests/%d/participants/%s",
		c.baseUrl, projectKey, repoSlug, prID, c.username), bitbucketApprovePR{Status: "APPROVED"}, &pr); err != nil {
		return err
	}
	return nil
}

type bitbucketContainerPR struct {
	ID        int64  `json:"id"`
	Open      bool   `json:"open"`
	Title     string `json:"title"`
	State     string `json:"state"`
	Reviewers []struct {
		User struct {
			Name string `json:"name"`
		} `json:"user"`
	} `json:"reviewers"`
}

type bitbucketApprovePR struct {
	Status string `json:"status"`
}

func (c *bitbucketClient) checkIfPRIsMergeable(projectKey, repoSlug string, prID int64) (bool, error) {
	endpoint := fmt.Sprintf("%s/rest/api/latest/projects/%s/repos/%s/pull-requests/%d/merge", c.baseUrl, projectKey, repoSlug, prID)
	var resp bitbucketMergePRResponse
	if err := c.doJsonRequest(http.MethodGet, endpoint, nil, &resp); err != nil {
		return false, err
	}
	return resp.CanMerge, nil
}

type bitbucketMergePRResponse struct {
	CanMerge   bool                           `json:"canMerge"`
	Conflicted bool                           `json:"conflicted"`
	Outcome    string                         `json:"outcome"`
	Vetoes     []bitbucketMergePRResponseVeto `json:"vetoes"`
}

type bitbucketMergePRResponseVeto struct {
	SummaryMessage  string `json:"summaryMessage"`
	DetailedMessage string `json:"detailedMessage"`
}

func (c *bitbucketClient) doJsonRequest(method, url string, payload any, result any) error {
	var body io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("marshal request: %w", err)
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		return fmt.Errorf("new request: %w", err)
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.do(req)
	if err != nil {
		return fmt.Errorf("send %s %s: %w", method, url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		responseBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("unexpected status %d for %s %s: %s", resp.StatusCode, method, url, strings.TrimSpace(string(responseBody)))
	}
	if result == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(result); err != nil {
		return fmt.Errorf("decode %s %s: %w", method, url, err)
	}
	return nil
}

func (c *bitbucketClient) doJson(method, url string, payload any) error {
	return c.doJsonRequest(method, url, payload, nil)
}
