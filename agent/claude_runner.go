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
	defaultAgentTimeout = 10 * time.Minute
	fixBranchPrefix     = "fix/issue-"
	defaultTargetBranch  = "main"
)

type FixResult struct {
	Success   bool   `json:"success"`
	MRURL     string `json:"mr_url,omitempty"`
	MRIID     int    `json:"mr_iid,omitempty"`
	CommitSHA string `json:"commit_sha,omitempty"`
	Error     string `json:"error,omitempty"`
}

type FixEvent struct {
	Stage     string `json:"stage"`
	Message   string `json:"message"`
	MRURL     string `json:"mr_url,omitempty"`
	Error     string `json:"error,omitempty"`
	LogLine   string `json:"log_line,omitempty"`
	Timestamp string `json:"timestamp"`
}

// GitUser represents the current GitLab user for git commits
type GitUser struct {
	Name  string
	Email string
}

// remoteSSHHost returns "user@host" if FIX_SSH_HOST is set, empty string otherwise
func remoteSSHHost() string {
	host := os.Getenv("FIX_SSH_HOST")
	if host == "" {
		return ""
	}
	user := os.Getenv("FIX_SSH_USER")
	if user == "" {
		user = "root"
	}
	return user + "@" + host
}

// isRemote returns true if commands should run via SSH on a remote server
func isRemote() bool {
	return remoteSSHHost() != ""
}

// runCommand runs a command either locally or via SSH
func runCommand(ctx context.Context, dir string, args ...string) ([]byte, error) {
	sshHost := remoteSSHHost()
	if sshHost != "" {
		// Build remote command with directory change
		cmdStr := fmt.Sprintf("cd %s && %s", dir, strings.Join(args, " "))
		// Escape the command for SSH
		cmd := exec.CommandContext(ctx, "ssh", sshHost, cmdStr)
		return cmd.CombinedOutput()
	}
	// Run locally
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Dir = dir
	return cmd.CombinedOutput()
}

func RunFixAgent(ctx context.Context, issueProjectID int, issueIID int, repoProjectID int, targetBranch string, additionalContext string, eventCh chan<- FixEvent) {
	defer close(eventCh)

	publishEvent := func(stage, message string, extra ...map[string]string) {
		event := FixEvent{Stage: stage, Message: message, Timestamp: time.Now().Format(time.RFC3339)}
		if len(extra) > 0 {
			if url, ok := extra[0]["mr_url"]; ok { event.MRURL = url }
			if err, ok := extra[0]["error"]; ok { event.Error = err }
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

	remoteMode := isRemote()
	log.Printf("[FixAgent] Running in %s mode", map[bool]string{true: "remote (SSH)", false: "local"}[remoteMode])

	// For remote mode, cleanup happens on remote server via defer

	timeoutCtx, cancel := context.WithTimeout(ctx, defaultAgentTimeout)
	defer cancel()

	// Step 1: Fetch issue (always local - GitLab API)
	publishEvent("fetching_issue", fmt.Sprintf("Fetching issue #%d from project %d...", issueIID, issueProjectID))
	issue, err := GetIssue(tokenCtx, issueProjectID, int64(issueIID))
	if err != nil {
		publishError("fetching_issue", fmt.Errorf("failed to fetch issue: %w", err))
		return
	}
	issueTitle := issue.Title
	issueDesc := issue.Description
	if issueDesc == "" {
		issueDesc = "No description provided."
	}

	// Step 2: Get project info (always local - GitLab API)
	publishEvent("cloning_repo", fmt.Sprintf("Cloning repository from project %d...", repoProjectID))
	project, err := GetProject(tokenCtx, repoProjectID)
	if err != nil {
		publishError("cloning_repo", fmt.Errorf("failed to get project: %w", err))
		return
	}

	cloneURL := project.HTTPURLToRepo
	if token.AccessToken != "" {
		baseURL := strings.TrimPrefix(cloneURL, "https://")
		baseURL = strings.TrimPrefix(baseURL, "http://")
		cloneURL = "https://oauth2:" + token.AccessToken + "@" + baseURL
	}

	// Step 3: Get current user (always local - GitLab API)
	currentUser, _ := GetCurrentUser(tokenCtx)
	gitUserName := "QA Fix Agent"
	gitUserEmail := "qa-agent@fix.local"
	if currentUser != nil {
		if currentUser.Name != "" { gitUserName = currentUser.Name }
		if currentUser.Email != "" { gitUserEmail = currentUser.Email }
	}

	branchName := fmt.Sprintf("%s%d", fixBranchPrefix, issueIID)

	if remoteMode {
		// REMOTE MODE: All git/claude operations via SSH
		runFixRemote(timeoutCtx, eventCh, publishEvent, publishError, workDir, cloneURL, branchName, issueTitle, issueDesc, additionalContext, gitUserName, gitUserEmail, targetBranch, tokenCtx, repoProjectID, issueIID, issueProjectID)
	} else {
		// LOCAL MODE: Everything on this server
		runFixLocal(timeoutCtx, eventCh, publishEvent, publishError, workDir, cloneURL, branchName, issueTitle, issueDesc, additionalContext, gitUserName, gitUserEmail, targetBranch, tokenCtx, repoProjectID, issueIID, issueProjectID)
	}
}

func runFixRemote(ctx context.Context, eventCh chan<- FixEvent, publishEvent func(string, string, ...map[string]string), publishError func(string, error), workDir, cloneURL, branchName, issueTitle, issueDesc, additionalContext, gitUserName, gitUserEmail, targetBranch string, tokenCtx context.Context, repoProjectID, issueIID, issueProjectID int) {
	sshHost := remoteSSHHost()
	defer func() {
		// Cleanup remote work directory
		cmd := exec.Command("ssh", sshHost, "rm", "-rf", workDir)
		cmd.Run()
	}()

	// Create work directory on remote
	publishEvent("cloning_repo", "Setting up remote work directory...")
	if output, err := exec.Command("ssh", sshHost, "mkdir", "-p", workDir).CombinedOutput(); err != nil {
		publishError("cloning_repo", fmt.Errorf("failed to create remote directory: %w, %s", err, string(output)))
		return
	}

	// Clone on remote
	publishEvent("cloning_repo", "Cloning repository on remote server...")
	if output, err := exec.CommandContext(ctx, "ssh", sshHost, "git", "clone", "--depth", "1", cloneURL, workDir).CombinedOutput(); err != nil {
		publishError("cloning_repo", fmt.Errorf("git clone failed: %w, %s", err, string(output)))
		return
	}

	// Configure git user on remote
	remoteCmd := fmt.Sprintf("cd %s && git config user.email '%s' && git config user.name '%s'", workDir, gitUserEmail, gitUserName)
	if output, err := exec.CommandContext(ctx, "ssh", sshHost, remoteCmd).CombinedOutput(); err != nil {
		publishError("creating_branch", fmt.Errorf("git config failed: %w, %s", err, string(output)))
		return
	}

	// Create branch on remote
	publishEvent("creating_branch", fmt.Sprintf("Creating branch %s...", branchName))
	remoteCmd = fmt.Sprintf("cd %s && git checkout -b %s", workDir, branchName)
	if output, err := exec.CommandContext(ctx, "ssh", sshHost, remoteCmd).CombinedOutput(); err != nil {
		publishError("creating_branch", fmt.Errorf("git checkout failed: %w, %s", err, string(output)))
		return
	}

	// Write system prompt file on remote
	publishEvent("agent_running", "Writing prompt files on remote server...")
	systemPrompt := FixSystemPrompt
	remoteCmd = fmt.Sprintf("cd %s && cat > .fix-system-prompt.txt << 'PROMPT_EOF'\n%s\nPROMPT_EOF", workDir, systemPrompt)
	if output, err := exec.CommandContext(ctx, "ssh", sshHost, remoteCmd).CombinedOutput(); err != nil {
		publishError("agent_running", fmt.Errorf("failed to write system prompt: %w, %s", err, string(output)))
		return
	}

	// Build user prompt
	prompt := fmt.Sprintf("Fix issue: %s\n\nIssue description:\n%s", issueTitle, issueDesc)
	if additionalContext != "" {
		prompt += fmt.Sprintf("\n\nAdditional context/instructions:\n%s", additionalContext)
	}
	prompt += "\n\nPlease fix this issue now. Remember to explore the codebase first before making changes."

	remoteCmd = fmt.Sprintf("cd %s && cat > .fix-user-prompt.txt << 'PROMPT_EOF'\n%s\nPROMPT_EOF", workDir, prompt)
	if output, err := exec.CommandContext(ctx, "ssh", sshHost, remoteCmd).CombinedOutput(); err != nil {
		publishError("agent_running", fmt.Errorf("failed to write prompt: %w, %s", err, string(output)))
		return
	}

	// Run Claude Code on remote server
	publishEvent("agent_running", "Running Claude Code on remote server...")
	model := os.Getenv("CLAUDE_MODEL")
	if model == "" {
		model = "minimax-m2.7"
	}

	claudeCmd := fmt.Sprintf(
		"cd %s && cat .fix-user-prompt.txt | claude --print --output-format stream-json --verbose --dangerously-skip-permissions --no-session-persistence --max-turns 50 --model %s --system-prompt-file .fix-system-prompt.txt --add-dir %s 2>&1",
		workDir, model, workDir,
	)

	log.Printf("[FixAgent] Running Claude on remote: %s", sshHost)
	publishEvent("agent_running", "Claude Code is analyzing and fixing the issue...")

	// Run Claude via SSH with a timeout
	cmd := exec.CommandContext(ctx, "ssh", sshHost, claudeCmd)
	output, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("[FixAgent] Claude remote output: %s", string(output))
		// Check if there are changes anyway
	}

	log.Printf("[FixAgent] Claude remote completed, output length: %d", len(output))

	// Remove prompt files on remote
	exec.Command("ssh", sshHost, "rm", "-f", workDir+"/.fix-system-prompt.txt", workDir+"/.fix-user-prompt.txt").Run()
	exec.Command("ssh", sshHost, "rm", "-rf", workDir+"/.claude").Run()

	// Check for changes on remote
	remoteCmd = fmt.Sprintf("cd %s && git status --porcelain", workDir)
	output, err = exec.CommandContext(ctx, "ssh", sshHost, remoteCmd).CombinedOutput()
	if err != nil {
		publishError("pushing_changes", fmt.Errorf("failed to check remote changes: %w", err))
		return
	}
	if len(strings.TrimSpace(string(output))) == 0 {
		publishError("pushing_changes", fmt.Errorf("Claude Code did not make any changes to fix the issue"))
		return
	}

	log.Printf("[FixAgent] Remote changed files:\n%s", string(output))

	// Commit and push on remote
	publishEvent("pushing_changes", "Committing and pushing changes...")
	commitMsg := fmt.Sprintf("fix: resolve issue #%d - %s", issueIID, issueTitle)
	remoteCmd = fmt.Sprintf("cd %s && git add -A && git commit -m '%s' && git push -u origin %s", workDir, commitMsg, branchName)
	if output, err = exec.CommandContext(ctx, "ssh", sshHost, remoteCmd).CombinedOutput(); err != nil {
		publishError("pushing_changes", fmt.Errorf("git push failed: %w, %s", err, string(output)))
		return
	}

	// Step 7: Create MR (local - GitLab API)
	publishEvent("creating_mr", fmt.Sprintf("Creating merge request in project %d...", repoProjectID))
	mrTitle := fmt.Sprintf("Fix: %s (Issue #%d)", issueTitle, issueIID)
	mrDesc := fmt.Sprintf("Closes #%d.\n\nFixed by AI agent (Claude Code).\n\n**Source:** Issue #%d in project %d\n\n**Changes:** See commits on branch `%s`.",
		issueIID, issueIID, issueProjectID, branchName)

	mr, err := CreateMergeRequest(tokenCtx, repoProjectID, branchName, targetBranch, mrTitle, mrDesc)
	if err != nil {
		publishError("creating_mr", fmt.Errorf("failed to create MR: %w", err))
		return
	}

	publishEvent("done", "Fix complete! Merge request created.", map[string]string{"mr_url": mr.WebURL})
}

func runFixLocal(ctx context.Context, eventCh chan<- FixEvent, publishEvent func(string, string, ...map[string]string), publishError func(string, error), workDir, cloneURL, branchName, issueTitle, issueDesc, additionalContext, gitUserName, gitUserEmail, targetBranch string, tokenCtx context.Context, repoProjectID, issueIID, issueProjectID int) {
	defer func() {
		log.Printf("[FixAgent] Cleaning up work directory: %s", workDir)
		os.RemoveAll(workDir)
	}()

	// Clone
	cmd := exec.CommandContext(ctx, "git", "clone", "--depth", "1", cloneURL, workDir)
	if output, err := cmd.CombinedOutput(); err != nil {
		publishError("cloning_repo", fmt.Errorf("git clone failed: %w, %s", err, string(output)))
		return
	}

	// Configure git user
	for _, args := range [][]string{
		{"git", "config", "user.email", gitUserEmail},
		{"git", "config", "user.name", gitUserName},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = workDir
		if output, err := cmd.CombinedOutput(); err != nil {
			publishError("creating_branch", fmt.Errorf("git config failed: %w, %s", err, string(output)))
			return
		}
	}

	// Create branch
	publishEvent("creating_branch", fmt.Sprintf("Creating branch %s...", branchName))
	cmd2 := exec.Command("git", "checkout", "-b", branchName)
	cmd2.Dir = workDir
	if output, err := cmd2.CombinedOutput(); err != nil {
		publishError("creating_branch", fmt.Errorf("git checkout failed: %w, %s", err, string(output)))
		return
	}

	// Prepare hooks
	if err := PrepareSessionHook(workDir); err != nil {
		publishError("agent_running", fmt.Errorf("failed to prepare session hooks: %w", err))
		return
	}

	// Build prompt
	fixPrompt := buildFixPrompt(issueTitle, issueDesc, additionalContext)
	systemPromptFile := filepath.Join(workDir, ".fix-system-prompt.txt")
	if err := os.WriteFile(systemPromptFile, []byte(FixSystemPrompt), 0644); err != nil {
		publishError("agent_running", fmt.Errorf("failed to write system prompt file: %w", err))
		return
	}

	// Spawn Claude Code
	publishEvent("agent_running", "Claude Code is analyzing and fixing the issue...")

	claudePath := "/usr/local/bin/claude"
	if _, err := os.Stat(claudePath); err != nil { claudePath = "claude" }

	model := os.Getenv("CLAUDE_MODEL")
	if model == "" { model = "minimax-m2.7" }

	claudeArgs := []string{
		"--print", "--output-format", "stream-json", "--verbose",
		"--dangerously-skip-permissions", "--no-session-persistence",
		"--max-turns", "50", "--model", model,
		"--system-prompt-file", systemPromptFile,
		"--add-dir", workDir,
	}

	log.Printf("[FixAgent] Spawning Claude Code: %s %v", claudePath, claudeArgs)
	claudeCmd := exec.CommandContext(ctx, claudePath, claudeArgs...)
	claudeCmd.Dir = workDir
	claudeCmd.Stdin = strings.NewReader(fixPrompt)

	claudeEnv := append(os.Environ(), "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1")
	if apiKey := os.Getenv("ANTHROPIC_API_KEY"); apiKey != "" { claudeEnv = append(claudeEnv, "ANTHROPIC_API_KEY="+apiKey) }
	if baseURL := os.Getenv("ANTHROPIC_BASE_URL"); baseURL != "" { claudeEnv = append(claudeEnv, "ANTHROPIC_BASE_URL="+baseURL) }
	claudeCmd.Env = claudeEnv

	stdout, _ := claudeCmd.StdoutPipe()
	stderr, _ := claudeCmd.StderrPipe()

	if err := claudeCmd.Start(); err != nil {
		publishError("agent_running", fmt.Errorf("failed to start Claude Code: %w", err))
		return
	}
	log.Printf("[FixAgent] Claude Code started (PID: %d)", claudeCmd.Process.Pid)

	go streamOutput(stdout, eventCh)
	go streamStderr(stderr)

	if err := claudeCmd.Wait(); err != nil {
		log.Printf("[FixAgent] Claude Code exited with error: %v", err)
	}

	// Cleanup temp files
	for _, p := range []string{".claude", ".fix-system-prompt.txt", ".fix-user-prompt.txt"} {
		os.RemoveAll(filepath.Join(workDir, p))
	}

	// Check changes
	hasChanges, _ := localHasChanges(workDir)
	if !hasChanges {
		publishError("pushing_changes", fmt.Errorf("Claude Code did not make any changes"))
		return
	}
	hasSource, _ := localHasSourceChanges(workDir)
	if !hasSource {
		publishError("pushing_changes", fmt.Errorf("Claude Code only made config changes, no source code fixes"))
		return
	}

	// Commit and push
	publishEvent("pushing_changes", "Committing and pushing changes...")
	commitMsg := fmt.Sprintf("fix: resolve issue #%d - %s", issueIID, issueTitle)
	for _, args := range [][]string{
		{"git", "add", "-A"},
		{"git", "commit", "-m", commitMsg},
		{"git", "push", "-u", "origin", branchName},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = workDir
		if output, err := cmd.CombinedOutput(); err != nil {
			publishError("pushing_changes", fmt.Errorf("%s failed: %w, %s", args[0], err, string(output)))
			return
		}
	}

	// Create MR
	publishEvent("creating_mr", fmt.Sprintf("Creating merge request..."))
	mrTitle := fmt.Sprintf("Fix: %s (Issue #%d)", issueTitle, issueIID)
	mrDesc := fmt.Sprintf("Closes #%d.\n\nFixed by AI agent (Claude Code).\n\n**Source:** Issue #%d in project %d\n\n**Changes:** See commits on branch `%s`.",
		issueIID, issueIID, issueProjectID, branchName)

	mr, err := CreateMergeRequest(tokenCtx, repoProjectID, branchName, targetBranch, mrTitle, mrDesc)
	if err != nil {
		publishError("creating_mr", fmt.Errorf("failed to create MR: %w", err))
		return
	}

	publishEvent("done", "Fix complete! Merge request created.", map[string]string{"mr_url": mr.WebURL})
}

func localHasChanges(dir string) (bool, error) {
	cmd := exec.Command("git", "status", "--porcelain")
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil { return false, err }
	return len(strings.TrimSpace(string(output))) > 0, nil
}

func localHasSourceChanges(dir string) (bool, error) {
	cmd := exec.Command("git", "status", "--porcelain")
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil { return false, err }
	exts := map[string]bool{".js":true,".jsx":true,".ts":true,".tsx":true,".vue":true,".py":true,".go":true,".java":true,".css":true,".scss":true,".html":true,".json":true,".yaml":true,".yml":true,".sh":true,".sql":true}
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		if line == "" { continue }
		parts := strings.Fields(line)
		if len(parts) < 2 { continue }
		f := parts[len(parts)-1]
		if strings.Contains(f, ".claude/") { continue }
		if exts[strings.ToLower(filepath.Ext(f))] { return true, nil }
	}
	return false, nil
}

func streamOutput(stdout io.Reader, eventCh chan<- FixEvent) {
	scanner := bufio.NewScanner(stdout)
	buf := make([]byte, 0, 1024*1024)
	scanner.Buffer(buf, 10*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" { continue }
		var jd map[string]interface{}
		if json.Unmarshal([]byte(line), &jd) == nil {
			if msg, ok := jd["message"].(string); ok && msg != "" {
				select {
				case eventCh <- FixEvent{Stage: "agent_running", Message: msg, Timestamp: time.Now().Format(time.RFC3339)}:
				default:
				}
			}
		}
	}
}

func streamStderr(stderr io.Reader) {
	scanner := bufio.NewScanner(stderr)
	for scanner.Scan() {
		if line := scanner.Text(); line != "" {
			log.Printf("[ClaudeCode stderr] %s", line)
		}
	}
}

func buildFixPrompt(title, desc, ctx string) string {
	p := fmt.Sprintf("Fix issue: %s\n\nIssue description:\n%s", title, desc)
	if ctx != "" { p += fmt.Sprintf("\n\nAdditional context:\n%s", ctx) }
	return p + "\n\nPlease fix this issue now. Remember to explore the codebase first before making changes."
}

func logChangedFiles(dir string) {
	cmd := exec.Command("git", "status", "--porcelain")
	cmd.Dir = dir
	if output, err := cmd.CombinedOutput(); err == nil {
		for _, line := range strings.Split(string(output), "\n") {
			if line != "" { log.Printf("[FixAgent]   %s", line) }
		}
	}
}

func GetGitLabBaseURL() string {
	if val := os.Getenv("GITLAB_BASE_URL"); val != "" { return val }
	return "https://gitlab.com"
}