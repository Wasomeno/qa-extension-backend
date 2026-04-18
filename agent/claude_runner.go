package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
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

// Event channel for streaming fix events
type FixEventChannel <-chan FixEvent

// RunFixAgent orchestrates the full fix flow:
// 1. Fetch issue from GitLab (from issueProjectID)
// 2. Clone repo to temp directory (from repoProjectID)
// 3. Create fix branch
// 4. Spawn Claude Code to fix the issue
// 5. Wait for Claude to finish
// 6. Git commit and push
// 7. Create merge request (in repoProjectID, targeting targetBranch)
// 8. Cleanup
func RunFixAgent(ctx context.Context, issueProjectID int, issueIID int, repoProjectID int, targetBranch string, additionalContext string, eventCh chan<- FixEvent) {
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

	// Create a timeout context for the entire operation
	timeoutCtx, cancel := context.WithTimeout(ctx, defaultAgentTimeout)
	defer cancel()

	// Get GitLab token from context
	token, ok := ctx.Value("token").(*oauth2.Token)
	if !ok {
		publishError("fetching_issue", fmt.Errorf("no GitLab token in context"))
		return
	}

	tokenCtx := context.WithValue(ctx, "token", token)

	// Generate unique session ID
	sessionID := uuid.New().String()
	workDir := filepath.Join(os.TempDir(), "qa-fix-"+sessionID)

	// Always cleanup temp directory on exit
	defer func() {
		log.Printf("[FixAgent] Cleaning up work directory: %s", workDir)
		if err := os.RemoveAll(workDir); err != nil {
			log.Printf("[FixAgent] Warning: failed to cleanup %s: %v", workDir, err)
		}
	}()

	// Step 1: Fetch issue from GitLab (issue project may differ from repo project)
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

	log.Printf("[FixAgent] Fetched issue #%d: %s", issueIID, issueTitle)

	// Step 2: Get repo project info and clone repo (may be different from issue project)
	publishEvent("cloning_repo", fmt.Sprintf("Cloning repository from project %d...", repoProjectID))
	project, err := GetProject(tokenCtx, repoProjectID)
	if err != nil {
		publishError("cloning_repo", fmt.Errorf("failed to get repo project %d info: %w", repoProjectID, err))
		return
	}

	// Use HTTPS URL with oauth token for authentication
	cloneURL := project.HTTPURLToRepo
	// Embed the token in the URL for authentication
	if token.AccessToken != "" {
		// For GitLab, we can use oauth2:PRIVATE-TOKEN@ format
		baseURL := cloneURL
		// Insert token into URL: https://oauth2:TOKEN@gitlab.com/...
		baseURL = strings.TrimPrefix(baseURL, "https://")
		baseURL = strings.TrimPrefix(baseURL, "http://")
		if strings.HasPrefix(cloneURL, "https://") {
			cloneURL = "https://oauth2:" + token.AccessToken + "@" + baseURL
		} else {
			cloneURL = "https://oauth2:" + token.AccessToken + "@" + baseURL
		}
	}

	if err := cloneRepo(timeoutCtx, cloneURL, workDir); err != nil {
		publishError("cloning_repo", fmt.Errorf("failed to clone repository: %w", err))
		return
	}

	log.Printf("[FixAgent] Repository cloned to %s", workDir)

	// Step 3: Configure git user and create fix branch
	publishEvent("creating_branch", fmt.Sprintf("Creating branch %s...", fixBranchPrefix+fmt.Sprintf("%d", issueIID)))

	if err := configureGitUser(workDir); err != nil {
		publishError("creating_branch", fmt.Errorf("failed to configure git user: %w", err))
		return
	}

	branchName := fixBranchPrefix + fmt.Sprintf("%d", issueIID)
	if err := createBranch(workDir, branchName); err != nil {
		publishError("creating_branch", fmt.Errorf("failed to create branch %s: %w", branchName, err))
		return
	}

	log.Printf("[FixAgent] Created branch %s", branchName)

	// Step 4: Prepare hooks
	if err := PrepareSessionHook(workDir); err != nil {
		publishError("agent_running", fmt.Errorf("failed to prepare session hooks: %w", err))
		return
	}

	// Step 5: Spawn Claude Code
	publishEvent("agent_running", "Claude Code is analyzing and fixing the issue...")

	// Use full path to claude if available, otherwise rely on PATH
	claudePath := "/usr/local/bin/claude"
	if _, err := os.Stat(claudePath); err != nil {
		// Fallback to PATH lookup
		claudePath = "claude"
	}

	claudeCmd := exec.CommandContext(timeoutCtx, claudePath,
		"--print",
		"--session-id", sessionID,
		"--output-format", "stream-json",
		"--include-hook-events",
		"--permission-mode", "bypassPermissions",
		"--no-session-persistence",
		"--allowedTools", "Bash,Read,Edit,Write,NotebookRead,MultiEdit,TodoWrite,Glob,Grep",
		"--disallowedTools", "WebFetch,WebSearch,Bash(git push*),Bash(git commit*)",
		"--max-turns", "50",
		"--system-prompt", FixSystemPrompt,
		"--add-dir", workDir,
		buildFixPrompt(issueTitle, issueDescription, additionalContext),
	)

	claudeCmd.Dir = workDir
	
	// Build environment for Claude Code
	claudeEnv := append(os.Environ(),
		"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1",
	)
	
	// Add Anthropic API configuration
	if apiKey := os.Getenv("ANTHROPIC_API_KEY"); apiKey != "" {
		claudeEnv = append(claudeEnv, "ANTHROPIC_API_KEY="+apiKey)
	}
	if baseURL := os.Getenv("ANTHROPIC_BASE_URL"); baseURL != "" {
		// Custom API endpoint (e.g., OpenCode)
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

	// Stream stdout (Claude's JSON output) and stderr
	go streamClaudeOutput(stdout, eventCh)
	go streamClaudeStderr(stderr)

	// Wait for Claude to finish
	claudeDone := make(chan error, 1)
	go func() {
		claudeDone <- claudeCmd.Wait()
	}()

	// Wait for either Claude to finish or the stop signal
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
		// Check if there are changes to push even on error
		hasChanges, err := hasUncommittedChanges(workDir)
		if err != nil || !hasChanges {
			publishError("agent_running", fmt.Errorf("Claude Code failed: %w", claudeErr))
			return
		}
		log.Printf("[FixAgent] Claude Code had errors but there are changes to push, proceeding...")
	}

	// Check if there are any changes to commit
	hasChanges, err := hasUncommittedChanges(workDir)
	if err != nil {
		publishError("pushing_changes", fmt.Errorf("failed to check for changes: %w", err))
		return
	}

	if !hasChanges {
		publishError("pushing_changes", fmt.Errorf("Claude Code did not make any changes to fix the issue"))
		return
	}

	// Step 6: Commit and push changes
	publishEvent("pushing_changes", "Committing and pushing changes...")

	commitMsg := fmt.Sprintf("fix: resolve issue #%d - %s", issueIID, issueTitle)
	if err := commitAndPush(workDir, branchName, commitMsg); err != nil {
		publishError("pushing_changes", fmt.Errorf("failed to commit and push: %w", err))
		return
	}

	log.Printf("[FixAgent] Changes pushed to branch %s", branchName)

	// Step 7: Create merge request (in repo project, targeting specified branch)
	publishEvent("creating_mr", fmt.Sprintf("Creating merge request in project %d...", repoProjectID))

	mrTitle := fmt.Sprintf("Fix: %s (Issue #%d)", issueTitle, issueIID)
	mrDescription := fmt.Sprintf("Closes #%d.\n\nFixed by AI agent (Claude Code).\n\n**Issue:** %s\n\n**Source:** Issue #%d in project %d\n\n**Changes:** See commits on branch `%s`.",
		issueIID, issueTitle, issueIID, issueProjectID, branchName)

	mr, err := CreateMergeRequest(tokenCtx, repoProjectID, branchName, targetBranch, mrTitle, mrDescription)
	if err != nil {
		// If MR creation fails but changes were pushed, still return success with partial info
		log.Printf("[FixAgent] Failed to create MR but changes were pushed: %v", err)
		publishError("creating_mr", fmt.Errorf("failed to create MR: %w. Branch %s was pushed successfully.", err, branchName))
		return
	}

	// Step 8: Done!
	log.Printf("[FixAgent] Fix complete! MR created: %s", mr.WebURL)
	publishEvent("done", "Fix complete! Merge request created.", map[string]string{"mr_url": mr.WebURL})
}

// cloneRepo clones a Git repository to the specified directory
func cloneRepo(ctx context.Context, url, dir string) error {
	cmd := exec.CommandContext(ctx, "git", "clone", "--depth", "1", url, dir)
	cmd.Stdout = nil
	cmd.Stderr = nil
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git clone failed: %w, output: %s", err, string(output))
	}
	return nil
}

// configureGitUser sets up git user name and email for commits
func configureGitUser(dir string) error {
	commands := [][]string{
		{"git", "config", "user.email", "qa-agent@fix.local"},
		{"git", "config", "user.name", "QA Fix Agent"},
	}

	for _, args := range commands {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if output, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("git config %s failed: %w, output: %s", args[2], err, string(output))
		}
	}
	return nil
}

// createBranch creates a new git branch and checks it out
func createBranch(dir, branchName string) error {
	cmd := exec.Command("git", "checkout", "-b", branchName)
	cmd.Dir = dir
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git checkout -b %s failed: %w, output: %s", branchName, err, string(output))
	}
	return nil
}

// hasUncommittedChanges checks if there are any uncommitted changes in the working directory
func hasUncommittedChanges(dir string) (bool, error) {
	cmd := exec.Command("git", "status", "--porcelain")
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		return false, fmt.Errorf("git status failed: %w", err)
	}
	return len(strings.TrimSpace(string(output))) > 0, nil
}

// commitAndPush stages all changes, commits them, and pushes to the remote
func commitAndPush(dir, branchName, commitMsg string) error {
	// Stage all changes
	cmd := exec.Command("git", "add", "-A")
	cmd.Dir = dir
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git add failed: %w, output: %s", err, string(output))
	}

	// Commit
	cmd = exec.Command("git", "commit", "-m", commitMsg)
	cmd.Dir = dir
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git commit failed: %w, output: %s", err, string(output))
	}

	// Push
	cmd = exec.Command("git", "push", "-u", "origin", branchName)
	cmd.Dir = dir
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git push failed: %w, output: %s", err, string(output))
	}

	return nil
}

// streamClaudeOutput reads Claude's JSON stdout output and publishes events
func streamClaudeOutput(stdout io.Reader, eventCh chan<- FixEvent) {
	scanner := bufio.NewScanner(stdout)
	// Increase buffer size for potentially long JSON lines
	buf := make([]byte, 0, 1024*1024)
	scanner.Buffer(buf, 10*1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		// Try to parse as JSON to extract useful information
		var jsonData map[string]interface{}
		if err := json.Unmarshal([]byte(line), &jsonData); err == nil {
			// Extract message or type if available
			if msg, ok := jsonData["message"].(string); ok && msg != "" {
				event := FixEvent{
					Stage:     "agent_running",
					Message:   msg,
					Timestamp: time.Now().Format(time.RFC3339),
				}
				select {
				case eventCh <- event:
				default:
					// Channel might be closed or full, skip
				}
			}
		}
	}

	if err := scanner.Err(); err != nil {
		log.Printf("[FixAgent] Error reading Claude stdout: %v", err)
	}
}

// streamClaudeStderr reads Claude's stderr output for logging
func streamClaudeStderr(stderr io.Reader) {
	scanner := bufio.NewScanner(stderr)
	for scanner.Scan() {
		line := scanner.Text()
		if line != "" {
			log.Printf("[ClaudeCode stderr] %s", line)
		}
	}
}

// GetGitLabTokenFromContext extracts the GitLab OAuth token from context
func GetGitLabTokenFromContext(ctx context.Context) (*oauth2.Token, error) {
	token, ok := ctx.Value("token").(*oauth2.Token)
	if !ok {
		return nil, fmt.Errorf("no GitLab token in context")
	}
	return token, nil
}

// GetGitLabBaseURL returns the GitLab base URL from environment
func GetGitLabBaseURL() string {
	val := os.Getenv("GITLAB_BASE_URL")
	if val == "" {
		return "https://gitlab.com"
	}
	return val
}

// buildFixPrompt constructs the full prompt for Claude Code
func buildFixPrompt(issueTitle, issueDescription, additionalContext string) string {
	prompt := fmt.Sprintf("Fix issue: %s\n\nIssue description:\n%s", issueTitle, issueDescription)
	
	if additionalContext != "" {
		prompt += fmt.Sprintf("\n\nAdditional context/instructions:\n%s", additionalContext)
	}
	
	prompt += "\n\nPlease fix this issue now. Remember to explore the codebase first before making changes."
	return prompt
}
