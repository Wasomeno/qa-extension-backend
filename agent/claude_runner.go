package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"golang.org/x/oauth2"
)

const (
	// Agent timeout for the entire fix operation
	defaultAgentTimeout = 10 * time.Minute

	// Git branch prefix for fix branches
	fixBranchPrefix = "fix/issue-"

	// Default target branch for merge requests
	defaultTargetBranch = "main"
)

// FixResult contains the outcome of a fix agent run
type FixResult struct {
	Success   bool   `json:"success"`
	MRURL     string `json:"mr_url,omitempty"`
	MRIID     int    `json:"mr_iid,omitempty"`
	CommitSHA string `json:"commit_sha,omitempty"`
	Error     string `json:"error,omitempty"`
}

// FixEvent represents a single event published during the fix process
type FixEvent struct {
	Stage     string `json:"stage"`
	Message   string `json:"message"`
	MRURL     string `json:"mr_url,omitempty"`
	Error     string `json:"error,omitempty"`
	LogLine   string `json:"log_line,omitempty"`
	Timestamp string `json:"timestamp"`
}

// FixWorkerRequest is the request body sent to the fix worker
type FixWorkerRequest struct {
	ProjectID      int    `json:"project_id"`
	IssueIID       int    `json:"issue_iid"`
	IssueTitle     string `json:"issue_title"`
	IssueDesc      string `json:"issue_desc"`
	RepoURL        string `json:"repo_url"`
	SystemPrompt   string `json:"system_prompt"`
	GitUserName    string `json:"git_user_name"`
	GitUserEmail   string `json:"git_user_email"`
	TargetBranch   string `json:"target_branch"`
	AdditionalCtx  string `json:"additional_context"`
	Model          string `json:"model"`
}

// FixWorkerStatus is the response from the fix worker
type FixWorkerStatus struct {
	SessionID string `json:"session_id"`
	Status    string `json:"status"`
	Message   string `json:"message"`
	MRURL     string `json:"mr_url,omitempty"`
	Error     string `json:"error,omitempty"`
	Duration  string `json:"duration,omitempty"`
}

// getFixWorkerURL returns the fix worker URL from environment
func getFixWorkerURL() string {
	url := os.Getenv("FIX_WORKER_URL")
	if url == "" {
		// Default: Claude Code runs locally (same server)
		return ""
	}
	return strings.TrimRight(url, "/")
}

// RunFixAgent orchestrates the fix flow.
// If FIX_WORKER_URL is set, it delegates to the remote fix worker.
// Otherwise, it runs Claude Code locally (for backwards compatibility).
func RunFixAgent(ctx context.Context, issueProjectID int, issueIID int, repoProjectID int, targetBranch string, additionalContext string, eventCh chan<- FixEvent) {
	workerURL := getFixWorkerURL()

	if workerURL != "" {
		runFixAgentRemote(ctx, issueProjectID, issueIID, repoProjectID, targetBranch, additionalContext, eventCh, workerURL)
	} else {
		runFixAgentLocal(ctx, issueProjectID, issueIID, repoProjectID, targetBranch, additionalContext, eventCh)
	}
}

// runFixAgentRemote delegates the fix to a remote worker and polls for status.
// After the remote fix completes, it creates the MR locally (needs GitLab token).
func runFixAgentRemote(ctx context.Context, issueProjectID int, issueIID int, repoProjectID int, targetBranch string, additionalContext string, eventCh chan<- FixEvent, workerURL string) {
	defer close(eventCh)

	publishEvent := func(stage, message string, extra ...map[string]string) {
		event := FixEvent{
			Stage:     stage,
			Message:   message,
			Timestamp: time.Now().Format(time.RFC3339),
		}
		if len(extra) > 0 {
			if url, ok := extra[0]["mr_url"]; ok {
				event.MRURL = url
			}
			if err, ok := extra[0]["error"]; ok {
				event.Error = err
			}
		}
		select {
		case eventCh <- event:
		case <-ctx.Done():
		}
	}

	publishError := func(stage string, err error) {
		log.Printf("[FixAgent] ERROR at stage %s: %v", stage, err)
		publishEvent(stage, err.Error(), map[string]string{"error": err.Error()})
	}

	token, ok := ctx.Value("token").(*oauth2.Token)
	if !ok {
		publishError("fetching_issue", fmt.Errorf("no GitLab token in context"))
		return
	}
	tokenCtx := context.WithValue(ctx, "token", token)

	// Step 1: Fetch issue from GitLab
	publishEvent("fetching_issue", fmt.Sprintf("Fetching issue #%d from project %d...", issueIID, issueProjectID))
	issue, err := GetIssue(tokenCtx, issueProjectID, int64(issueIID))
	if err != nil {
		publishError("fetching_issue", fmt.Errorf("failed to fetch issue: %w", err))
		return
	}

	// Step 2: Get project info for clone URL and MR creation
	publishEvent("cloning_repo", fmt.Sprintf("Getting project info for %d...", repoProjectID))
	project, err := GetProject(tokenCtx, repoProjectID)
	if err != nil {
		publishError("cloning_repo", fmt.Errorf("failed to get project: %w", err))
		return
	}

	// Build clone URL with token
	cloneURL := project.HTTPURLToRepo
	if token.AccessToken != "" {
		baseURL := strings.TrimPrefix(cloneURL, "https://")
		baseURL = strings.TrimPrefix(baseURL, "http://")
		cloneURL = "https://oauth2:" + token.AccessToken + "@" + baseURL
	}

	// Step 3: Get current user info for git commits
	currentUser, _ := GetCurrentUser(tokenCtx)
	gitUserName := ""
	gitUserEmail := ""
	if currentUser != nil {
		gitUserName = currentUser.Name
		gitUserEmail = currentUser.Email
	}

	// Step 4: Send fix request to remote worker
	publishEvent("agent_running", "Sending fix request to remote worker...")

	reqBody := FixWorkerRequest{
		ProjectID:     issueProjectID,
		IssueIID:      issueIID,
		IssueTitle:    issue.Title,
		IssueDesc:     issue.Description,
		RepoURL:       cloneURL,
		SystemPrompt:  FixSystemPrompt,
		GitUserName:   gitUserName,
		GitUserEmail:  gitUserEmail,
		TargetBranch:  targetBranch,
		AdditionalCtx: additionalContext,
		Model:         os.Getenv("CLAUDE_MODEL"),
	}
	if reqBody.Model == "" {
		reqBody.Model = "minimax-m2.7"
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		publishError("agent_running", fmt.Errorf("failed to marshal request: %w", err))
		return
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", workerURL+"/fix", bytes.NewReader(body))
	if err != nil {
		publishError("agent_running", fmt.Errorf("failed to create request: %w", err))
		return
	}
	httpReq.Header.Set("Content-Type", "application/json")
	// Forward auth token to worker
	httpReq.Header.Set("X-Fix-Worker-Token", os.Getenv("FIX_WORKER_SECRET"))

	httpClient := &http.Client{Timeout: 30 * time.Second}
	resp, err := httpClient.Do(httpReq)
	if err != nil {
		publishError("agent_running", fmt.Errorf("failed to call fix worker: %w", err))
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		respBody, _ := io.ReadAll(resp.Body)
		publishError("agent_running", fmt.Errorf("fix worker returned %d: %s", resp.StatusCode, string(respBody)))
		return
	}

	var startResp struct {
		SessionID string `json:"session_id"`
		Status    string `json:"status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&startResp); err != nil {
		publishError("agent_running", fmt.Errorf("failed to decode worker response: %w", err))
		return
	}

	sessionID := startResp.SessionID
	log.Printf("[FixAgent] Remote worker session: %s", sessionID)

	// Step 5: Poll for status
	publishEvent("agent_running", "Fix worker is processing...")

	pollCtx, pollCancel := context.WithTimeout(ctx, defaultAgentTimeout)
	defer pollCancel()

	for {
		select {
		case <-pollCtx.Done():
			publishError("agent_running", fmt.Errorf("timeout waiting for fix worker"))
			return
		case <-time.After(5 * time.Second):
		}

		status, err := pollFixWorkerStatus(workerURL, sessionID)
		if err != nil {
			log.Printf("[FixAgent] Warning: failed to poll status: %v", err)
			continue
		}

		log.Printf("[FixAgent] Worker status: %s - %s", status.Status, status.Message)

		switch status.Status {
		case "running":
			publishEvent("agent_running", status.Message)
		case "done":
			publishEvent("pushing_changes", "Remote fix completed, creating merge request...")

			// Create MR
			branchName := fmt.Sprintf("%s%d", fixBranchPrefix, issueIID)
			mrTitle := fmt.Sprintf("Fix: %s (Issue #%d)", issue.Title, issueIID)
			mrDesc := fmt.Sprintf("Closes #%d.\n\nFixed by AI agent (Claude Code).\n\n**Source:** Issue #%d in project %d\n\n**Changes:** See commits on branch `%s`.",
				issueIID, issueIID, issueProjectID, branchName)

			mr, err := CreateMergeRequest(tokenCtx, repoProjectID, branchName, targetBranch, mrTitle, mrDesc)
			if err != nil {
				publishError("creating_mr", fmt.Errorf("failed to create MR: %w", err))
				return
			}

			publishEvent("done", "Fix complete! Merge request created.", map[string]string{"mr_url": mr.WebURL})
			return
		case "error":
			publishError("agent_running", fmt.Errorf("fix worker error: %s", status.Error))
			return
		}
	}
}

// pollFixWorkerStatus polls the fix worker for the current status
func pollFixWorkerStatus(workerURL, sessionID string) (*FixWorkerStatus, error) {
	url := fmt.Sprintf("%s/fix/%s", workerURL, sessionID)
	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("worker returned status %d", resp.StatusCode)
	}

	var status FixWorkerStatus
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		return nil, err
	}
	return &status, nil
}

// runFixAgentLocal runs the fix agent locally (legacy mode, when no FIX_WORKER_URL is set)
func runFixAgentLocal(ctx context.Context, issueProjectID int, issueIID int, repoProjectID int, targetBranch string, additionalContext string, eventCh chan<- FixEvent) {
	defer close(eventCh)

	publishEvent := func(stage, message string, extra ...map[string]string) {
		event := FixEvent{
			Stage:     stage,
			Message:   message,
			Timestamp: time.Now().Format(time.RFC3339),
		}
		if len(extra) > 0 {
			if url, ok := extra[0]["mr_url"]; ok {
				event.MRURL = url
			}
			if err, ok := extra[0]["error"]; ok {
				event.Error = err
			}
		}
		select {
		case eventCh <- event:
		case <-ctx.Done():
		}
	}

	publishError := func(stage string, err error) {
		log.Printf("[FixAgent] ERROR at stage %s: %v", stage, err)
		publishEvent(stage, err.Error(), map[string]string{"error": err.Error()})
	}

	token, ok := ctx.Value("token").(*oauth2.Token)
	if !ok {
		publishError("fetching_issue", fmt.Errorf("no GitLab token in context"))
		return
	}
	tokenCtx := context.WithValue(ctx, "token", token)

	sessionID := uuid.New().String()
	workDir := filepath.Join(os.TempDir(), "qa-fix-"+sessionID)
	defer func() {
		log.Printf("[FixAgent] Cleaning up work directory: %s", workDir)
		os.RemoveAll(workDir)
	}()

	timeoutCtx, cancel := context.WithTimeout(ctx, defaultAgentTimeout)
	defer cancel()

	// Step 1: Fetch issue
	publishEvent("fetching_issue", fmt.Sprintf("Fetching issue #%d from project %d...", issueIID, issueProjectID))
	issue, err := GetIssue(tokenCtx, issueProjectID, int64(issueIID))
	if err != nil {
		publishError("fetching_issue", fmt.Errorf("failed to fetch issue #%d from project %d: %w", issueIID, issueProjectID, err))
		return
	}

	issueTitle := issue.Title
	issueDescription := issue.Description
	if issueDescription == "" {
		issueDescription = "No description provided."
	}

	// Step 2: Get project info and clone repo
	publishEvent("cloning_repo", fmt.Sprintf("Cloning repository from project %d...", repoProjectID))
	project, err := GetProject(tokenCtx, repoProjectID)
	if err != nil {
		publishError("cloning_repo", fmt.Errorf("failed to get repo project %d info: %w", repoProjectID, err))
		return
	}

	cloneURL := project.HTTPURLToRepo
	if token.AccessToken != "" {
		baseURL := strings.TrimPrefix(cloneURL, "https://")
		baseURL = strings.TrimPrefix(baseURL, "http://")
		if strings.HasPrefix(cloneURL, "https://") {
			cloneURL = "https://oauth2:" + token.AccessToken + "@" + baseURL
		} else {
			cloneURL = "https://oauth2:" + token.AccessToken + "@" + baseURL
		}
	}

	if err := localCloneRepo(timeoutCtx, cloneURL, workDir); err != nil {
		publishError("cloning_repo", fmt.Errorf("failed to clone repository: %w", err))
		return
	}

	// Step 3: Get current user info and configure git
	currentUser, err := GetCurrentUser(tokenCtx)
	if err != nil {
		log.Printf("[FixAgent] Warning: failed to get current user, using default: %v", err)
	}
	if err := localConfigureGitUser(workDir, currentUser); err != nil {
		publishError("creating_branch", fmt.Errorf("failed to configure git user: %w", err))
		return
	}

	branchName := fmt.Sprintf("%s%d", fixBranchPrefix, issueIID)
	publishEvent("creating_branch", fmt.Sprintf("Creating branch %s...", branchName))
	if err := localCreateBranch(workDir, branchName); err != nil {
		publishError("creating_branch", fmt.Errorf("failed to create branch %s: %w", branchName, err))
		return
	}

	// Step 4: Prepare hooks
	if err := PrepareSessionHook(workDir); err != nil {
		publishError("agent_running", fmt.Errorf("failed to prepare session hooks: %w", err))
		return
	}

	// Step 5: Spawn Claude Code
	publishEvent("agent_running", "Claude Code is analyzing and fixing the issue...")

	fixPrompt := localBuildFixPrompt(issueTitle, issueDescription, additionalContext)
	systemPromptFile := filepath.Join(workDir, ".fix-system-prompt.txt")
	if err := os.WriteFile(systemPromptFile, []byte(FixSystemPrompt), 0644); err != nil {
		publishError("agent_running", fmt.Errorf("failed to write system prompt file: %w", err))
		return
	}

	claudePath := "/usr/local/bin/claude"
	if _, err := os.Stat(claudePath); err != nil {
		claudePath = "claude"
	}

	claudeArgs := []string{
		"--print",
		"--output-format", "stream-json",
		"--verbose",
		"--dangerously-skip-permissions",
		"--no-session-persistence",
		"--max-turns", "50",
		"--model", "minimax-m2.7",
		"--system-prompt-file", systemPromptFile,
		"--add-dir", workDir,
	}

	log.Printf("[FixAgent] Spawning Claude Code: %s %v", claudePath, claudeArgs)
	log.Printf("[FixAgent] Working directory: %s", workDir)
	log.Printf("[FixAgent] Prompt length: %d chars", len(fixPrompt))

	claudeCmd := exec.CommandContext(timeoutCtx, claudePath, claudeArgs...)
	claudeCmd.Dir = workDir
	claudeCmd.Stdin = strings.NewReader(fixPrompt)

	claudeEnv := append(os.Environ(),
		"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1",
	)
	if apiKey := os.Getenv("ANTHROPIC_API_KEY"); apiKey != "" {
		claudeEnv = append(claudeEnv, "ANTHROPIC_API_KEY="+apiKey)
	}
	if baseURL := os.Getenv("ANTHROPIC_BASE_URL"); baseURL != "" {
		claudeEnv = append(claudeEnv, "ANTHROPIC_BASE_URL="+baseURL)
	}
	claudeCmd.Env = claudeEnv

	stdout, err := claudeCmd.StdoutPipe()
	if err != nil {
		publishError("agent_running", fmt.Errorf("failed to create stdout pipe: %w", err))
		return
	}
	stderr, err := claudeCmd.StderrPipe()
	if err != nil {
		publishError("agent_running", fmt.Errorf("failed to create stderr pipe: %w", err))
		return
	}

	if err := claudeCmd.Start(); err != nil {
		publishError("agent_running", fmt.Errorf("failed to start Claude Code: %w", err))
		return
	}

	log.Printf("[FixAgent] Claude Code started (PID: %d)", claudeCmd.Process.Pid)
	publishEvent("agent_running", fmt.Sprintf("Claude Code started (PID: %d). Analyzing issue...", claudeCmd.Process.Pid))

	go localStreamClaudeOutput(stdout, eventCh)
	go localStreamClaudeStderr(stderr)

	claudeDone := make(chan error, 1)
	go func() {
		claudeDone <- claudeCmd.Wait()
	}()

	var claudeErr error
	select {
	case claudeErr = <-claudeDone:
		log.Printf("[FixAgent] Claude Code process exited")
	case <-time.After(defaultAgentTimeout):
		publishError("agent_running", fmt.Errorf("Claude Code timed out after %v", defaultAgentTimeout))
		claudeCmd.Process.Kill()
		return
	case <-timeoutCtx.Done():
		publishError("agent_running", fmt.Errorf("operation cancelled: %w", timeoutCtx.Err()))
		claudeCmd.Process.Kill()
		return
	}

	if claudeErr != nil {
		log.Printf("[FixAgent] Claude Code exited with error: %v", claudeErr)
	}

	// Cleanup temp and hook files before checking changes
	for _, path := range []string{".claude", ".fix-system-prompt.txt", ".fix-user-prompt.txt"} {
		os.RemoveAll(filepath.Join(workDir, path))
	}

	logChangedFiles(workDir)

	hasChanges, err := localHasUncommittedChanges(workDir)
	if err != nil || !hasChanges {
		publishError("pushing_changes", fmt.Errorf("Claude Code did not make any changes to fix the issue"))
		return
	}

	hasSource, _ := localHasSourceCodeChanges(workDir)
	if !hasSource {
		publishError("pushing_changes", fmt.Errorf("Claude Code only made config/metadata changes, no source code fixes"))
		return
	}

	// Step 6: Commit and push
	publishEvent("pushing_changes", "Committing and pushing changes...")
	commitMsg := fmt.Sprintf("fix: resolve issue #%d - %s", issueIID, issueTitle)
	if err := localCommitAndPush(workDir, branchName, commitMsg); err != nil {
		publishError("pushing_changes", fmt.Errorf("failed to commit and push: %w", err))
		return
	}

	// Step 7: Create MR
	publishEvent("creating_mr", fmt.Sprintf("Creating merge request in project %d...", repoProjectID))
	mrTitle := fmt.Sprintf("Fix: %s (Issue #%d)", issueTitle, issueIID)
	mrDesc := fmt.Sprintf("Closes #%d.\n\nFixed by AI agent (Claude Code).\n\n**Source:** Issue #%d in project %d\n\n**Changes:** See commits on branch `%s`.",
		issueIID, issueIID, issueProjectID, branchName)

	mr, err := CreateMergeRequest(tokenCtx, repoProjectID, branchName, targetBranch, mrTitle, mrDesc)
	if err != nil {
		publishError("creating_mr", fmt.Errorf("failed to create MR: %w. Branch %s was pushed successfully.", err, branchName))
		return
	}

	publishEvent("done", "Fix complete! Merge request created.", map[string]string{"mr_url": mr.WebURL})
}

// --- Local helper functions (prefixed with "local" to avoid conflicts) ---

func localCloneRepo(ctx context.Context, url, dir string) error {
	cmd := exec.CommandContext(ctx, "git", "clone", "--depth", "1", url, dir)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git clone failed: %w, output: %s", err, string(output))
	}
	return nil
}

func localConfigureGitUser(dir string, user *GitUser) error {
	name := "QA Fix Agent"
	email := "qa-agent@fix.local"
	if user != nil {
		if user.Name != "" {
			name = user.Name
		}
		if user.Email != "" {
			email = user.Email
		}
	}
	for _, args := range [][]string{
		{"git", "config", "user.email", email},
		{"git", "config", "user.name", name},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if output, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("git config %s failed: %w, output: %s", args[2], err, string(output))
		}
	}
	return nil
}

func localCreateBranch(dir, branchName string) error {
	cmd := exec.Command("git", "checkout", "-b", branchName)
	cmd.Dir = dir
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git checkout -b %s failed: %w, output: %s", branchName, err, string(output))
	}
	return nil
}

func localHasUncommittedChanges(dir string) (bool, error) {
	cmd := exec.Command("git", "status", "--porcelain")
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		return false, fmt.Errorf("git status failed: %w", err)
	}
	return len(strings.TrimSpace(string(output))) > 0, nil
}

func localHasSourceCodeChanges(dir string) (bool, error) {
	cmd := exec.Command("git", "status", "--porcelain")
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		return false, fmt.Errorf("git status failed: %w", err)
	}
	sourceExts := map[string]bool{
		".js": true, ".jsx": true, ".ts": true, ".tsx": true, ".vue": true,
		".py": true, ".go": true, ".java": true, ".rb": true, ".php": true,
		".css": true, ".scss": true, ".html": true, ".json": true, ".yaml": true,
		".yml": true, ".sh": true, ".sql": true, ".svelte": true,
	}
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}
		filename := parts[len(parts)-1]
		if strings.Contains(filename, ".claude/") {
			continue
		}
		ext := strings.ToLower(filepath.Ext(filename))
		if sourceExts[ext] {
			return true, nil
		}
	}
	return false, nil
}

func localCommitAndPush(dir, branchName, commitMsg string) error {
	for _, args := range [][]string{
		{"git", "add", "-A"},
		{"git", "commit", "-m", commitMsg},
		{"git", "push", "-u", "origin", branchName},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if output, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("%s failed: %w, output: %s", args[0], err, string(output))
		}
	}
	return nil
}

func localStreamClaudeOutput(stdout io.Reader, eventCh chan<- FixEvent) {
	scanner := bufio.NewScanner(stdout)
	buf := make([]byte, 0, 1024*1024)
	scanner.Buffer(buf, 10*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		var jsonData map[string]interface{}
		if err := json.Unmarshal([]byte(line), &jsonData); err == nil {
			if msg, ok := jsonData["message"].(string); ok && msg != "" {
				event := FixEvent{
					Stage:     "agent_running",
					Message:   msg,
					Timestamp: time.Now().Format(time.RFC3339),
				}
				select {
				case eventCh <- event:
				default:
				}
			}
		}
	}
}

func localStreamClaudeStderr(stderr io.Reader) {
	scanner := bufio.NewScanner(stderr)
	for scanner.Scan() {
		line := scanner.Text()
		if line != "" {
			log.Printf("[ClaudeCode stderr] %s", line)
		}
	}
}

func localBuildFixPrompt(issueTitle, issueDescription, additionalContext string) string {
	prompt := fmt.Sprintf("Fix issue: %s\n\nIssue description:\n%s", issueTitle, issueDescription)
	if additionalContext != "" {
		prompt += fmt.Sprintf("\n\nAdditional context/instructions:\n%s", additionalContext)
	}
	prompt += "\n\nPlease fix this issue now. Remember to explore the codebase first before making changes."
	return prompt
}

func logChangedFiles(dir string) {
	cmd := exec.Command("git", "status", "--porcelain")
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("[FixAgent] Failed to list changed files: %v", err)
		return
	}
	lines := strings.Split(string(output), "\n")
	log.Printf("[FixAgent] Changed files (%d):", len(lines))
	for _, line := range lines {
		if line != "" {
			log.Printf("[FixAgent]   %s", line)
		}
	}
}

// GetGitLabBaseURL returns the GitLab base URL from environment
func GetGitLabBaseURL() string {
	val := os.Getenv("GITLAB_BASE_URL")
	if val == "" {
		return "https://gitlab.com"
	}
	return val
}