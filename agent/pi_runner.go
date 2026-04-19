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
	defaultPiTimeout   = 10 * time.Minute
	piBranchPrefix     = "fix/issue-"
	piDefaultBranch    = "main"
	piRPCTimeout       = 30 * time.Second
	piBinaryName       = "pi"
)

// PiRunnerConfig holds configuration for the Pi runner
type PiRunnerConfig struct {
	BinaryPath    string        // Path to pi binary (default: "pi")
	WorkingDir    string        // Working directory for the session
	Model         string        // Model to use (default: from PI_MODEL env or first available)
	MaxTurns      int           // Maximum turns (default: 50)
	Timeout       time.Duration // Overall timeout (default: 10 minutes)
	SystemPrompt  string        // Custom system prompt (optional)
}

// PiRPCCommand represents a command sent to Pi in RPC mode
type PiRPCCommand struct {
	ID    string          `json:"id,omitempty"`
	Type  string          `json:"type"`
	Data  json.RawMessage `json:"data,omitempty"`
}

// PiRPCResponse represents a response from Pi in RPC mode
type PiRPCResponse struct {
	ID      string          `json:"id,omitempty"`
	Type    string          `json:"type"`
	Success bool            `json:"success"`
	Data    json.RawMessage `json:"data,omitempty"`
	Error   string          `json:"error,omitempty"`
}

// PiRPCEvent represents an event streamed from Pi
type PiRPCEvent struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data,omitempty"`
}

// PiSessionState represents the state of a Pi session
type PiSessionState struct {
	Model           string `json:"model,omitempty"`
	ThinkingLevel   string `json:"thinkingLevel"`
	IsStreaming     bool   `json:"isStreaming"`
	IsCompacting    bool   `json:"isCompacting"`
	SessionFile     string `json:"sessionFile,omitempty"`
	SessionID       string `json:"sessionId"`
	SessionName     string `json:"sessionName,omitempty"`
	MessageCount    int    `json:"messageCount"`
}

// Default Pi system prompt for fixing issues
const PiFixSystemPrompt = `You are a software engineer fixing a GitLab issue. Follow this process EXACTLY:

## Step 1: Understand the Issue
- Read the issue description carefully
- Identify the EXACT problem being reported
- Note any error messages, affected components, or file paths mentioned
- If the issue is unclear, look for related files that might contain the bug

## Step 2: Explore the Codebase FIRST
- Use grep and find tools to search for relevant files
- Read files to understand the context
- DO NOT make changes until you've explored enough

## Step 3: Make Targeted Changes
- Change ONLY the files directly related to the issue
- Make the MINIMAL changes needed to fix the problem
- DO NOT refactor unrelated code
- DO NOT change formatting, imports, or style in unrelated areas
- DO NOT create new files unless absolutely necessary for the fix

## Step 4: Verify Your Fix
- Run tests if they exist: npm test, npm run test, go test, pytest, etc.
- Check that the build compiles: npm run build, go build, etc.
- Verify your changes actually address the issue described

## Important Rules
- NEVER run git push or git commit — the wrapper handles that
- If you cannot fix the issue, explain what you tried and why it failed
- Focus ONLY on the reported issue — ignore other improvements
- When done, simply stop. The system will create the MR automatically.`

// RunFixWithPi orchestrates the fix using Pi coding agent in RPC mode.
// This function mirrors the structure of RunFixAgent but uses Pi instead of Claude Code CLI.
func RunFixWithPi(ctx context.Context, issueProjectID int, issueIID int, repoProjectID int, targetBranch string, additionalContext string, eventCh chan<- FixEvent) {
	defer close(eventCh)

	// Setup event publishing helpers
	publishEvent := func(stage, message string, extra ...map[string]string) {
		event := FixEvent{Stage: stage, Message: message, Timestamp: time.Now().Format(time.RFC3339)}
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
		log.Printf("[PiRunner] ERROR at stage %s: %v", stage, err)
		publishEvent(stage, err.Error(), map[string]string{"error": err.Error()})
	}

	// Get auth token
	token, ok := ctx.Value("token").(*oauth2.Token)
	if !ok {
		publishError("auth", fmt.Errorf("no GitLab token in context"))
		return
	}
	tokenCtx := context.WithValue(ctx, "token", token)

	// Create session
	sessionID := uuid.New().String()
	workDir := filepath.Join(os.TempDir(), "qa-fix-pi-"+sessionID)

	// Check for remote mode
	remoteMode := isRemote()
	log.Printf("[PiRunner] Running in %s mode", map[bool]string{true: "remote (SSH)", false: "local"}[remoteMode])

	// Set timeout
	timeout := defaultPiTimeout
	timeoutCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Step 1: Fetch issue
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

	// Step 2: Get project info
	publishEvent("cloning_repo", fmt.Sprintf("Cloning repository from project %d...", repoProjectID))
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

	// Step 3: Get current user
	currentUser, _ := GetCurrentUser(tokenCtx)
	gitUserName := "Pi Fix Agent"
	gitUserEmail := "pi-agent@fix.local"
	if currentUser != nil {
		if currentUser.Name != "" {
			gitUserName = currentUser.Name
		}
		if currentUser.Email != "" {
			gitUserEmail = currentUser.Email
		}
	}

	// Set default target branch
	if targetBranch == "" {
		targetBranch = piDefaultBranch
	}

	branchName := fmt.Sprintf("%s%d", piBranchPrefix, issueIID)

	// Execute based on mode
	if remoteMode {
		runPiFixRemote(timeoutCtx, eventCh, publishEvent, publishError, workDir, cloneURL, branchName,
			issueTitle, issueDesc, additionalContext, gitUserName, gitUserEmail, targetBranch,
			tokenCtx, repoProjectID, issueIID, issueProjectID)
	} else {
		runPiFixLocal(timeoutCtx, eventCh, publishEvent, publishError, workDir, cloneURL, branchName,
			issueTitle, issueDesc, additionalContext, gitUserName, gitUserEmail, targetBranch,
			tokenCtx, repoProjectID, issueIID, issueProjectID)
	}
}

// runPiFixLocal executes the fix workflow locally using Pi in RPC mode
func runPiFixLocal(ctx context.Context, eventCh chan<- FixEvent, publishEvent func(string, string, ...map[string]string), publishError func(string, error), workDir, cloneURL, branchName, issueTitle, issueDesc, additionalContext, gitUserName, gitUserEmail, targetBranch string, tokenCtx context.Context, repoProjectID, issueIID, issueProjectID int) {
	// Cleanup on exit
	defer func() {
		log.Printf("[PiRunner] Cleaning up work directory: %s", workDir)
		os.RemoveAll(workDir)
	}()

	// Step 1: Clone repository
	cmd := exec.CommandContext(ctx, "git", "clone", "--depth", "1", cloneURL, workDir)
	if output, err := cmd.CombinedOutput(); err != nil {
		publishError("cloning_repo", fmt.Errorf("git clone failed: %w, %s", err, string(output)))
		return
	}

	// Step 2: Configure git user
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

	// Step 3: Create branch
	publishEvent("creating_branch", fmt.Sprintf("Creating branch %s...", branchName))
	cmd2 := exec.Command("git", "checkout", "-b", branchName)
	cmd2.Dir = workDir
	if output, err := cmd2.CombinedOutput(); err != nil {
		publishError("creating_branch", fmt.Errorf("git checkout failed: %w, %s", err, string(output)))
		return
	}

	// Step 4: Run Pi in RPC mode
	publishEvent("agent_running", "Starting Pi coding agent...")

	piResult, err := runPiRPC(ctx, workDir, issueTitle, issueDesc, additionalContext, eventCh)
	if err != nil {
		publishError("agent_running", fmt.Errorf("Pi agent failed: %w", err))
		return
	}

	log.Printf("[PiRunner] Pi completed: %s", piResult)

	// Step 5: Check for changes
	hasChanges, _ := localHasChanges(workDir)
	if !hasChanges {
		publishError("pushing_changes", fmt.Errorf("Pi did not make any changes"))
		return
	}
	hasSource, _ := localHasSourceChanges(workDir)
	if !hasSource {
		publishError("pushing_changes", fmt.Errorf("Pi only made config changes, no source code fixes"))
		return
	}

	// Log changed files
	logChangedFiles(workDir)

	// Step 6: Commit and push
	publishEvent("pushing_changes", "Committing and pushing changes...")
	commitMsg := fmt.Sprintf("fix: resolve issue #%d - %s", issueIID, issueTitle)
	for _, args := range [][]string{
		{"git", "add", "-A"},
		{"git", "commit", "-m", commitMsg},
		{"git", "push", "-u", "origin", branchName, "--force"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = workDir
		if output, err := cmd.CombinedOutput(); err != nil {
			publishError("pushing_changes", fmt.Errorf("%s failed: %w, %s", args[0], err, string(output)))
			return
		}
	}

	// Step 7: Create MR
	publishEvent("creating_mr", fmt.Sprintf("Creating merge request in project %d...", repoProjectID))
	mrTitle := fmt.Sprintf("Fix: %s (Issue #%d)", issueTitle, issueIID)
	mrDesc := fmt.Sprintf("Closes #%d.\n\nFixed by AI agent (Pi).\n\n**Source:** Issue #%d in project %d\n\n**Changes:** See commits on branch `%s`.",
		issueIID, issueIID, issueProjectID, branchName)

	mr, err := CreateMergeRequest(tokenCtx, repoProjectID, branchName, targetBranch, mrTitle, mrDesc)
	if err != nil {
		publishError("creating_mr", fmt.Errorf("failed to create MR: %w", err))
		return
	}

	publishEvent("done", "Fix complete! Merge request created.", map[string]string{"mr_url": mr.WebURL})
}

// runPiRPC starts Pi in RPC mode and communicates via JSON protocol
func runPiRPC(ctx context.Context, workDir, issueTitle, issueDesc, additionalContext string, eventCh chan<- FixEvent) (string, error) {
	// Find Pi binary
	piPath := piBinaryName
	if customPath := os.Getenv("PI_BINARY_PATH"); customPath != "" {
		piPath = customPath
	}

	// Build arguments for RPC mode
	args := []string{
		"--mode", "rpc",
		"--cwd", workDir,
	}

	// Add model if specified
	if model := os.Getenv("PI_MODEL"); model != "" {
		args = append(args, "--model", model)
	}

	log.Printf("[PiRunner] Starting Pi RPC: %s %v", piPath, args)

	// Create command
	cmd := exec.CommandContext(ctx, piPath, args...)
	cmd.Dir = workDir

	// Setup stdin/stdout pipes
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return "", fmt.Errorf("failed to create stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("failed to create stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return "", fmt.Errorf("failed to create stderr pipe: %w", err)
	}

	// Setup environment
	cmd.Env = append(os.Environ(),
		"PI_DISABLE_UPDATE_CHECK=1",
	)
	if apiKey := os.Getenv("ANTHROPIC_API_KEY"); apiKey != "" {
		cmd.Env = append(cmd.Env, "ANTHROPIC_API_KEY="+apiKey)
	}
	if apiKey := os.Getenv("OPENAI_API_KEY"); apiKey != "" {
		cmd.Env = append(cmd.Env, "OPENAI_API_KEY="+apiKey)
	}

	// Start the process
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("failed to start Pi: %w", err)
	}
	log.Printf("[PiRunner] Pi started (PID: %d)", cmd.Process.Pid)

	// Ensure cleanup on exit
	defer func() {
		stdin.Close()
		cmd.Wait()
	}()

	// Start stderr reader in background
	go func() {
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			log.Printf("[PiRunner stderr] %s", scanner.Text())
		}
	}()

	// Create JSON-RPC client
	client := newPiRPCClient(stdin, stdout, eventCh)

	// Step 1: Build the fix prompt
	fixPrompt := buildPiFixPrompt(issueTitle, issueDesc, additionalContext)

	// Step 2: Send prompt command
	log.Printf("[PiRunner] Sending fix prompt to Pi...")
	
	promptCmd := map[string]interface{}{
		"type":    "prompt",
		"message": fixPrompt,
	}
	
	response, err := client.sendCommand(ctx, promptCmd)
	if err != nil {
		return "", fmt.Errorf("failed to send prompt: %w", err)
	}

	log.Printf("[PiRunner] Prompt response: %+v", response)

	// Step 3: Wait for agent to complete
	log.Printf("[PiRunner] Waiting for agent to complete...")
	agentEndErr := client.waitForAgentEnd(ctx, 10*time.Minute)
	if agentEndErr != nil {
		log.Printf("[PiRunner] Wait for agent_end failed: %v", agentEndErr)
	}
	log.Printf("[PiRunner] Agent completed processing")

	// Step 4: Get last assistant text for logging
	lastText, _ := client.getLastAssistantText(ctx)
	log.Printf("[PiRunner] Pi response: %s", lastText)

	return lastText, nil
}

// piRPCClient handles JSON-RPC communication with Pi
type piRPCClient struct {
	stdin  io.Writer
	stdout *bufio.Reader
	events chan<- FixEvent

	// Track pending requests
	pendingRequests map[string]chan *PiRPCResponse
	responseCh      chan *PiRPCResponse
	
	// Optional custom event handler
	handleEventFunc func(*PiRPCResponse)
}

func newPiRPCClient(stdin io.Writer, stdout io.Reader, events chan<- FixEvent) *piRPCClient {
	client := &piRPCClient{
		stdin:           stdin,
		stdout:          bufio.NewReader(stdout),
		events:          events,
		pendingRequests: make(map[string]chan *PiRPCResponse),
		responseCh:      make(chan *PiRPCResponse, 100),
	}

	// Start response reader
	go client.readResponses()

	return client
}

// sendCommand sends a command to Pi and waits for the response
func (c *piRPCClient) sendCommand(ctx context.Context, cmd map[string]interface{}) (*PiRPCResponse, error) {
	// Generate request ID
	id := fmt.Sprintf("req-%d", time.Now().UnixNano())
	cmd["id"] = id

	// Marshal command
	data, err := json.Marshal(cmd)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal command: %w", err)
	}

	// Create response channel
	responseCh := make(chan *PiRPCResponse, 1)
	c.pendingRequests[id] = responseCh
	defer delete(c.pendingRequests, id)

	// Send command
	log.Printf("[PiRPC] Sending: %s", string(data))
	if _, err := fmt.Fprintf(c.stdin, "%s\n", string(data)); err != nil {
		return nil, fmt.Errorf("failed to send command: %w", err)
	}

	// Wait for response or timeout
	select {
	case response := <-responseCh:
		if !response.Success {
			return nil, fmt.Errorf("command failed: %s", response.Error)
		}
		return response, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(10 * time.Minute):
		return nil, fmt.Errorf("timeout waiting for response")
	}
}

// readResponses continuously reads responses from Pi
func (c *piRPCClient) readResponses() {
	for {
		line, err := c.stdout.ReadString('\n')
		if err != nil {
			if err != io.EOF {
				log.Printf("[PiRPC] Error reading: %v", err)
			}
			return
		}

		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		log.Printf("[PiRPC] Received: %s", line)

		// Parse the response
		var response PiRPCResponse
		if err := json.Unmarshal([]byte(line), &response); err != nil {
			log.Printf("[PiRPC] Failed to parse response: %v", err)
			continue
		}

		// Check if this is an event or a response
		if response.ID != "" {
			// This is a response to a request
			if ch, ok := c.pendingRequests[response.ID]; ok {
				ch <- &response
			}
		} else if response.Type != "" {
			// This is an event - handle it
			c.handleEvent(&response)
		}
	}
}

// handleEvent processes events from Pi
func (c *piRPCClient) handleEvent(response *PiRPCResponse) {
	// Call custom handler if set (for waiting for agent_end)
	if c.handleEventFunc != nil {
		c.handleEventFunc(response)
	}

	// Extract event data and publish to FixEvent channel
	var eventData map[string]interface{}
	if len(response.Data) > 0 {
		if err := json.Unmarshal(response.Data, &eventData); err != nil {
			log.Printf("[PiRPC] Failed to parse event data: %v", err)
			return
		}
	}

	// Map Pi events to FixEvents
	message := ""
	if msg, ok := eventData["message"].(string); ok {
		message = msg
	} else if text, ok := eventData["text"].(string); ok {
		message = text
	}

	if message != "" && c.events != nil {
		select {
		case c.events <- FixEvent{
			Stage:     "agent_running",
			Message:   message,
			Timestamp: time.Now().Format(time.RFC3339),
		}:
		default:
		}
	}
}

// getLastAssistantText retrieves the last assistant message text
func (c *piRPCClient) getLastAssistantText(ctx context.Context) (string, error) {
	cmd := map[string]interface{}{
		"type": "get_last_assistant_text",
	}
	
	response, err := c.sendCommand(ctx, cmd)
	if err != nil {
		return "", err
	}

	var result struct {
		Text string `json:"text"`
	}
	if len(response.Data) > 0 {
		if err := json.Unmarshal(response.Data, &result); err != nil {
			return "", err
		}
	}
	return result.Text, nil
}

// waitForAgentEnd blocks until the agent finishes processing (agent_end event)
func (c *piRPCClient) waitForAgentEnd(ctx context.Context, timeout time.Duration) error {
	// Create a channel to receive agent_end event
	agentEndCh := make(chan struct{}, 1)
	
	// Store the original handler
	originalHandler := c.handleEvent
	
	// Replace handler with one that detects agent_end
	c.handleEventFunc = func(response *PiRPCResponse) {
		if response.Type == "agent_end" {
			select {
			case agentEndCh <- struct{}{}:
			default:
			}
		}
		// Also call original handler
		if originalHandler != nil {
			originalHandler(response)
		}
	}
	
	// Wait for agent_end or timeout
	select {
	case <-agentEndCh:
		log.Printf("[PiRPC] Received agent_end event")
		return nil
	case <-time.After(timeout):
		return fmt.Errorf("timeout waiting for agent_end after %v", timeout)
	case <-ctx.Done():
		return ctx.Err()
	}
}

// buildPiFixPrompt constructs the prompt for Pi
func buildPiFixPrompt(title, desc, ctx string) string {
	prompt := fmt.Sprintf("Fix issue: %s\n\nIssue description:\n%s", title, desc)
	if ctx != "" {
		prompt += fmt.Sprintf("\n\nAdditional context:\n%s", ctx)
	}
	return prompt + "\n\nPlease fix this issue now. Remember to explore the codebase first before making changes."
}

// runPiFixRemote executes the fix workflow on a remote server via SSH using RPC mode
func runPiFixRemote(ctx context.Context, eventCh chan<- FixEvent, publishEvent func(string, string, ...map[string]string), publishError func(string, error), workDir, cloneURL, branchName, issueTitle, issueDesc, additionalContext, gitUserName, gitUserEmail, targetBranch string, tokenCtx context.Context, repoProjectID, issueIID, issueProjectID int) {
	// Create SSH client
	sshClient, err := NewSSHClient()
	if err != nil {
		publishError("cloning_repo", fmt.Errorf("failed to create SSH client: %w", err))
		return
	}
	defer sshClient.Close()

	log.Printf("[PiRunner] Connected to SSH server: %s", sshClient.Host())

	// Cleanup remote work directory on exit
	defer func() {
		sshClient.RunCommand("rm -rf " + workDir)
	}()

	// Step 1: Create work directory on remote
	publishEvent("cloning_repo", "Setting up remote work directory...")
	if _, err := sshClient.RunCommand("mkdir -p " + workDir); err != nil {
		publishError("cloning_repo", fmt.Errorf("failed to create remote directory: %w", err))
		return
	}

	// Step 2: Clone on remote
	publishEvent("cloning_repo", "Cloning repository on remote server...")
	if output, err := sshClient.RunCommand(fmt.Sprintf("git clone --depth 1 %s %s", cloneURL, workDir)); err != nil {
		publishError("cloning_repo", fmt.Errorf("git clone failed: %w, %s", err, string(output)))
		return
	}

	// Step 3: Configure git user on remote
	if _, err := sshClient.RunCommandInDir(fmt.Sprintf("git config user.email '%s' && git config user.name '%s'", gitUserEmail, gitUserName), workDir); err != nil {
		publishError("creating_branch", fmt.Errorf("git config failed: %w", err))
		return
	}

	// Step 4: Create branch on remote
	publishEvent("creating_branch", fmt.Sprintf("Creating branch %s...", branchName))
	if _, err := sshClient.RunCommandInDir("git checkout -b "+branchName, workDir); err != nil {
		publishError("creating_branch", fmt.Errorf("git checkout failed: %w", err))
		return
	}

	// Step 5: Run Pi in RPC mode over SSH
	publishEvent("agent_running", "Starting Pi coding agent on remote server...")

	piResult, err := runPiRPCOverSSH(ctx, sshClient, workDir, issueTitle, issueDesc, additionalContext, eventCh)
	if err != nil {
		publishError("agent_running", fmt.Errorf("Pi agent failed: %w", err))
		return
	}

	log.Printf("[PiRunner] Pi remote completed: %s", piResult)

	// Step 6: Check for changes on remote
	output, err := sshClient.RunCommandInDir("git status --porcelain", workDir)
	if err != nil {
		publishError("pushing_changes", fmt.Errorf("failed to check remote changes: %w", err))
		return
	}
	if len(strings.TrimSpace(string(output))) == 0 {
		publishError("pushing_changes", fmt.Errorf("Pi did not make any changes to fix the issue"))
		return
	}

	log.Printf("[PiRunner] Remote changed files:\n%s", string(output))

	// Step 7: Commit and push on remote
	publishEvent("pushing_changes", "Committing and pushing changes...")
	commitMsg := fmt.Sprintf("fix: resolve issue #%d - %s", issueIID, issueTitle)
	if _, err := sshClient.RunCommandInDir(fmt.Sprintf("git add -A && git commit -m '%s' && git push -u origin %s --force", commitMsg, branchName), workDir); err != nil {
		publishError("pushing_changes", fmt.Errorf("git push failed: %w", err))
		return
	}

	// Step 8: Create MR (local - GitLab API)
	publishEvent("creating_mr", fmt.Sprintf("Creating merge request in project %d...", repoProjectID))
	mrTitle := fmt.Sprintf("Fix: %s (Issue #%d)", issueTitle, issueIID)
	mrDesc := fmt.Sprintf("Closes #%d.\n\nFixed by AI agent (Pi).\n\n**Source:** Issue #%d in project %d\n\n**Changes:** See commits on branch `%s`.",
		issueIID, issueIID, issueProjectID, branchName)

	mr, err := CreateMergeRequest(tokenCtx, repoProjectID, branchName, targetBranch, mrTitle, mrDesc)
	if err != nil {
		publishError("creating_mr", fmt.Errorf("failed to create MR: %w", err))
		return
	}

	publishEvent("done", "Fix complete! Merge request created.", map[string]string{"mr_url": mr.WebURL})
}

// runPiRPCOverSSH runs Pi in RPC mode over an SSH connection
func runPiRPCOverSSH(ctx context.Context, sshClient *SSHClient, workDir, issueTitle, issueDesc, additionalContext string, eventCh chan<- FixEvent) (string, error) {
	// Create SSH session for Pi RPC
	session, err := sshClient.NewSession()
	if err != nil {
		return "", fmt.Errorf("failed to create SSH session: %w", err)
	}
	defer session.Close()

	// Request a pseudo-terminal (some programs need it for proper I/O)
	// Use a dumb terminal type to avoid any terminal-specific behavior
	if err := session.RequestPty("dumb", 80, 40, nil); err != nil {
		log.Printf("[PiRunner] Warning: could not request PTY: %v (continuing anyway)", err)
	}

	// Setup stdin/stdout pipes for RPC communication
	stdin, err := session.StdinPipe()
	if err != nil {
		return "", fmt.Errorf("failed to create stdin pipe: %w", err)
	}
	stdout, err := session.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("failed to create stdout pipe: %w", err)
	}
	stderr, err := session.StderrPipe()
	if err != nil {
		return "", fmt.Errorf("failed to create stderr pipe: %w", err)
	}

	// Get model from env or use default
	model := os.Getenv("PI_MODEL")
	if model == "" {
		model = "claude-sonnet-4-20250514"
	}

	// Build Pi command for RPC mode
	piCmd := fmt.Sprintf("cd %s && pi --mode rpc --model %s", workDir, model)
	log.Printf("[PiRunner] Starting Pi RPC over SSH: %s", piCmd)

	// Start Pi on remote server
	if err := session.Start(piCmd); err != nil {
		return "", fmt.Errorf("failed to start Pi on remote: %w", err)
	}

	// Start stderr reader in background
	go func() {
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			log.Printf("[PiRunner SSH stderr] %s", scanner.Text())
		}
	}()

	// Create JSON-RPC client (same as local mode)
	client := newPiRPCClient(stdin, stdout, eventCh)

	// Build the fix prompt
	fixPrompt := buildPiFixPrompt(issueTitle, issueDesc, additionalContext)

	// Send prompt command
	log.Printf("[PiRunner] Sending fix prompt to Pi over SSH...")
	
	promptCmd := map[string]interface{}{
		"type":    "prompt",
		"message": fixPrompt,
	}
	
	_, err = client.sendCommand(ctx, promptCmd)
	if err != nil {
		return "", fmt.Errorf("failed to send prompt: %w", err)
	}

	log.Printf("[PiRunner] Prompt sent, waiting for agent to complete...")

	// Wait for agent_end event (signals the agent has finished processing)
	agentEndErr := client.waitForAgentEnd(ctx, 10*time.Minute)
	if agentEndErr != nil {
		log.Printf("[PiRunner] Wait for agent_end failed: %v", agentEndErr)
	}

	log.Printf("[PiRunner] Agent completed processing")

	// Get last assistant text for logging
	lastText, _ := client.getLastAssistantText(ctx)
	log.Printf("[PiRunner] Pi response: %s", lastText)

	// Close stdin to signal we're done
	stdin.Close()

	// Wait for Pi to exit
	if err := session.Wait(); err != nil {
		log.Printf("[PiRunner] Pi session exited with error: %v", err)
	}

	return lastText, nil
}

