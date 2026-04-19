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
	defaultPiTimeout = 10 * time.Minute
	piDefaultBranch  = "main"
	piRPCTimeout     = 30 * time.Second
	piBinaryName     = "pi"
)

// slugify converts a string to a lowercase, dash-separated format suitable for branch names
func slugify(s string) string {
	// Convert to lowercase
	s = strings.ToLower(s)
	
	// Replace spaces and underscores with dashes
	s = strings.ReplaceAll(s, " ", "-")
	s = strings.ReplaceAll(s, "_", "-")
	
	// Remove special characters, keep only alphanumeric and dashes
	var result strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			result.WriteRune(r)
		}
	}
	s = result.String()
	
	// Replace multiple consecutive dashes with single dash
	for strings.Contains(s, "--") {
		s = strings.ReplaceAll(s, "--", "-")
	}
	
	// Trim dashes from start and end
	s = strings.Trim(s, "-")
	
	// Limit length to 50 characters
	if len(s) > 50 {
		s = s[:50]
		// Trim trailing dash if we cut in the middle
		s = strings.TrimRight(s, "-")
	}
	
	return s
}

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
// stepManager manages the steps for a fix session
type stepManager struct {
	steps       []FixStep
	currentStep int
	eventCh     chan<- FixEvent
	sessionInfo *FixSessionInfo
}

// newStepManager creates a new step manager with default steps
func newStepManager(eventCh chan<- FixEvent, sessionInfo *FixSessionInfo) *stepManager {
	// Make a copy of default steps with fresh timestamps
	steps := make([]FixStep, len(DefaultFixSteps))
	for i, step := range DefaultFixSteps {
		steps[i] = FixStep{
			ID:          step.ID,
			Title:       step.Title,
			Description: step.Description,
			Status:      FixStepStatusPending,
		}
	}
	return &stepManager{
		steps:       steps,
		currentStep: -1,
		eventCh:     eventCh,
		sessionInfo: sessionInfo,
	}
}

// emitEvent emits a FixEvent to the event channel
func (sm *stepManager) emitEvent(event FixEvent) {
	event.Timestamp = time.Now().Format(time.RFC3339)
	event.SessionInfo = sm.sessionInfo
	event.Steps = sm.steps
	select {
	case sm.eventCh <- event:
	case <-time.After(5 * time.Second):
		log.Printf("[StepManager] WARNING: event channel blocked, skipping event")
	}
}

// emitInitialEvent emits the initial session event with all steps in pending state
func (sm *stepManager) emitInitialEvent() {
	sm.emitEvent(FixEvent{
		Stage:       "initialized",
		Message:     "Fix session initialized",
		Steps:       sm.steps,
		CurrentStep: -1,
	})
}

// startStep marks a step as in progress and emits an event
func (sm *stepManager) startStep(stepID string, message string) {
	for i := range sm.steps {
		if sm.steps[i].ID == stepID {
			sm.steps[i].Status = FixStepStatusInProgress
			sm.steps[i].StartedAt = time.Now().Format(time.RFC3339)
			sm.steps[i].Message = message
			sm.currentStep = i
			sm.emitEvent(FixEvent{
				Stage:       stepID,
				Message:     message,
				Steps:       sm.steps,
				CurrentStep: i,
				StepUpdate:  &sm.steps[i],
			})
			return
		}
	}
	log.Printf("[StepManager] WARNING: step %s not found", stepID)
}

// completeStep marks a step as done and emits an event
func (sm *stepManager) completeStep(stepID string, message string) {
	for i := range sm.steps {
		if sm.steps[i].ID == stepID {
			sm.steps[i].Status = FixStepStatusDone
			sm.steps[i].CompletedAt = time.Now().Format(time.RFC3339)
			if message != "" {
				sm.steps[i].Message = message
			}
			sm.emitEvent(FixEvent{
				Stage:       stepID,
				Message:     message,
				Steps:       sm.steps,
				CurrentStep: i,
				StepUpdate:  &sm.steps[i],
			})
			return
		}
	}
}

// failStep marks a step as failed and emits an error event
func (sm *stepManager) failStep(stepID string, errMsg string) {
	for i := range sm.steps {
		if sm.steps[i].ID == stepID {
			sm.steps[i].Status = FixStepStatusError
			sm.steps[i].CompletedAt = time.Now().Format(time.RFC3339)
			sm.steps[i].Message = errMsg
			sm.emitEvent(FixEvent{
				Stage:       "error",
				Message:     errMsg,
				Error:       errMsg,
				Steps:       sm.steps,
				CurrentStep: i,
				StepUpdate:  &sm.steps[i],
			})
			return
		}
	}
}

// emitDone emits the final done event with MR URL
func (sm *stepManager) emitDone(mrURL string) {
	sm.emitEvent(FixEvent{
		Stage:       "done",
		Message:     "Fix complete! Merge request created.",
		MRURL:       mrURL,
		Steps:       sm.steps,
		CurrentStep: len(sm.steps) - 1,
	})
}

// emitError emits an error event
func (sm *stepManager) emitError(stage string, errMsg string) {
	sm.emitEvent(FixEvent{
		Stage:   "error",
		Message: errMsg,
		Error:   errMsg,
		Steps:   sm.steps,
	})
}

// This function mirrors the structure of RunFixAgent but uses Pi instead of Claude Code CLI.
func RunFixWithPi(ctx context.Context, issueProjectID int, issueIID int, repoProjectID int, targetBranch string, additionalContext string, eventCh chan<- FixEvent) {
	defer close(eventCh)

	// Create session info
	sessionID := uuid.New().String()
	sessionInfo := &FixSessionInfo{
		SessionID:     sessionID,
		Runner:        "pi",
		ProjectID:     issueProjectID,
		RepoProjectID: repoProjectID,
		IssueIID:      issueIID,
		TargetBranch:  targetBranch,
		AdditionalCtx: additionalContext,
	}

	// Create step manager
	sm := newStepManager(eventCh, sessionInfo)

	// Get auth token
	token, ok := ctx.Value("token").(*oauth2.Token)
	if !ok {
		sm.emitError("auth", "no GitLab token in context")
		return
	}
	tokenCtx := context.WithValue(ctx, "token", token)

	workDir := filepath.Join(os.TempDir(), "qa-fix-pi-"+sessionID)

	// Check for remote mode
	remoteMode := isRemote()
	log.Printf("[PiRunner] Running in %s mode", map[bool]string{true: "remote (SSH)", false: "local"}[remoteMode])

	// Set timeout
	timeout := defaultPiTimeout
	timeoutCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Emit initial event with all steps
	sm.emitInitialEvent()

	// Step 1: Fetch issue
	sm.startStep("fetch_issue", fmt.Sprintf("Fetching issue #%d from project %d...", issueIID, issueProjectID))
	issue, err := GetIssue(tokenCtx, issueProjectID, int64(issueIID))
	if err != nil {
		sm.failStep("fetch_issue", fmt.Sprintf("Failed to fetch issue: %v", err))
		return
	}
	issueTitle := issue.Title
	issueDesc := issue.Description
	if issueDesc == "" {
		issueDesc = "No description provided."
	}
	// Update session info with issue details
	sessionInfo.IssueTitle = issueTitle
	sm.completeStep("fetch_issue", fmt.Sprintf("Fetched issue: %s", issueTitle))

	// Step 2: Get project info
	sm.startStep("get_project", fmt.Sprintf("Getting project info for project %d...", repoProjectID))
	project, err := GetProject(tokenCtx, repoProjectID)
	if err != nil {
		sm.failStep("get_project", fmt.Sprintf("Failed to get project: %v", err))
		return
	}
	// Update session info with project details
	sessionInfo.ProjectName = project.Name
	sm.completeStep("get_project", fmt.Sprintf("Project: %s", project.Name))

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

	// Create branch name from issue title: fix/issue-title-parsed-to-dash-format
	branchName := fmt.Sprintf("fix/%s", slugify(issueTitle))

	// Execute based on mode
	if remoteMode {
		runPiFixRemote(timeoutCtx, sm, workDir, cloneURL, branchName,
			issueTitle, issueDesc, additionalContext, gitUserName, gitUserEmail, targetBranch,
			tokenCtx, repoProjectID, issueIID, issueProjectID)
	} else {
		runPiFixLocal(timeoutCtx, sm, workDir, cloneURL, branchName,
			issueTitle, issueDesc, additionalContext, gitUserName, gitUserEmail, targetBranch,
			tokenCtx, repoProjectID, issueIID, issueProjectID)
	}
}

// runPiFixLocal executes the fix workflow locally using Pi in RPC mode
func runPiFixLocal(ctx context.Context, sm *stepManager, workDir, cloneURL, branchName, issueTitle, issueDesc, additionalContext, gitUserName, gitUserEmail, targetBranch string, tokenCtx context.Context, repoProjectID, issueIID, issueProjectID int) {
	// Cleanup on exit
	defer func() {
		log.Printf("[PiRunner] Cleaning up work directory: %s", workDir)
		os.RemoveAll(workDir)
	}()

	// Step 3: Clone repository
	sm.startStep("clone_repo", fmt.Sprintf("Cloning repository to %s...", workDir))
	cmd := exec.CommandContext(ctx, "git", "clone", "--depth", "1", cloneURL, workDir)
	if output, err := cmd.CombinedOutput(); err != nil {
		sm.failStep("clone_repo", fmt.Sprintf("Git clone failed: %s", string(output)))
		return
	}
	sm.completeStep("clone_repo", "Repository cloned successfully")

	// Step 4: Configure git user
	for _, args := range [][]string{
		{"git", "config", "user.email", gitUserEmail},
		{"git", "config", "user.name", gitUserName},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = workDir
		if output, err := cmd.CombinedOutput(); err != nil {
			sm.failStep("create_branch", fmt.Sprintf("Git config failed: %s", string(output)))
			return
		}
	}

	// Step 4: Create branch
	sm.startStep("create_branch", fmt.Sprintf("Creating branch %s...", branchName))
	cmd2 := exec.Command("git", "checkout", "-b", branchName)
	cmd2.Dir = workDir
	if output, err := cmd2.CombinedOutput(); err != nil {
		sm.failStep("create_branch", fmt.Sprintf("Git checkout failed: %s", string(output)))
		return
	}
	sm.completeStep("create_branch", fmt.Sprintf("Branch %s created", branchName))

	// Step 5: Analyze issue and implement fix (combined for Pi)
	sm.startStep("analyze_issue", "Analyzing issue and exploring codebase...")
	sm.startStep("implement_fix", "Implementing fix with Pi coding agent...")

	piResult, summaryText, err := runPiRPCWithSummary(ctx, workDir, issueTitle, issueDesc, additionalContext, sm.eventCh)
	if err != nil {
		sm.failStep("implement_fix", fmt.Sprintf("Pi agent failed: %v", err))
		return
	}

	log.Printf("[PiRunner] Pi completed: %s", piResult)
	sm.completeStep("analyze_issue", "Issue analysis complete")
	sm.completeStep("implement_fix", fmt.Sprintf("Fix implemented: %s", piResult))

	// Step 6: Verify fix
	sm.startStep("verify_fix", "Verifying the fix...")
	// Check for changes
	hasChanges, _ := localHasChanges(workDir)
	if !hasChanges {
		sm.failStep("verify_fix", "Pi did not make any changes")
		return
	}
	hasSource, _ := localHasSourceChanges(workDir)
	if !hasSource {
		sm.failStep("verify_fix", "Pi only made config changes, no source code fixes")
		return
	}

	// Log changed files
	logChangedFiles(workDir)
	sm.completeStep("verify_fix", "Changes verified successfully")

	// Step 7: Commit changes
	sm.startStep("commit_changes", "Committing changes...")
	commitMsg := fmt.Sprintf("fix: resolve issue #%d - %s", issueIID, issueTitle)
	for _, args := range [][]string{
		{"git", "add", "-A"},
		{"git", "commit", "-m", commitMsg},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = workDir
		if output, err := cmd.CombinedOutput(); err != nil {
			sm.failStep("commit_changes", fmt.Sprintf("%s failed: %s", args[0], string(output)))
			return
		}
	}
	sm.completeStep("commit_changes", fmt.Sprintf("Committed: %s", commitMsg))

	// Step 8: Push branch
	sm.startStep("push_branch", fmt.Sprintf("Pushing branch %s...", branchName))
	cmd = exec.Command("git", "push", "-u", "origin", branchName, "--force")
	cmd.Dir = workDir
	if output, err := cmd.CombinedOutput(); err != nil {
		sm.failStep("push_branch", fmt.Sprintf("Git push failed: %s", string(output)))
		return
	}
	sm.completeStep("push_branch", fmt.Sprintf("Branch pushed to origin/%s", branchName))

	// Step 9: Create MR
	sm.startStep("create_mr", fmt.Sprintf("Creating merge request in project %d...", repoProjectID))
	mrTitle := fmt.Sprintf("Fix: %s", issueTitle)

	// Build MR description with agent-generated summary
	changesSection := "See commits for details."
	if summaryText != "" {
		changesSection = summaryText
	}

	mrDesc := fmt.Sprintf(`## Summary

Fixes issue #%d: %s

## Changes

%s

## Issue Link

Issue: #%d (Project %d)

---
*This merge request was created by AI agent (Pi).*`,
		issueIID, issueTitle, changesSection, issueIID, issueProjectID)

	mr, err := CreateMergeRequest(tokenCtx, repoProjectID, branchName, targetBranch, mrTitle, mrDesc)
	if err != nil {
		sm.failStep("create_mr", fmt.Sprintf("Failed to create MR: %v", err))
		return
	}

	sm.completeStep("create_mr", fmt.Sprintf("MR created: %s", mr.WebURL))

	// Emit final done event
	sm.emitDone(mr.WebURL)
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

// runPiRPCWithSummary runs Pi in RPC mode and returns both the response and a summary of changes
func runPiRPCWithSummary(ctx context.Context, workDir, issueTitle, issueDesc, additionalContext string, eventCh chan<- FixEvent) (string, string, error) {
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
		return "", "", fmt.Errorf("failed to create stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", "", fmt.Errorf("failed to create stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return "", "", fmt.Errorf("failed to create stderr pipe: %w", err)
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
		return "", "", fmt.Errorf("failed to start Pi: %w", err)
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
		return "", "", fmt.Errorf("failed to send prompt: %w", err)
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

	// Step 5: Ask for summary of changes
	summaryPrompt := "Please provide a brief summary of the changes you made to fix this issue. " +
		"Include: 1) A list of files that were modified, created, or deleted. " +
		"2) A brief description of the key changes in each file. " +
		"3) How these changes address the issue. " +
		"Be concise and factual. Format your response as markdown."

	summaryCmd := map[string]interface{}{
		"type":    "prompt",
		"message": summaryPrompt,
	}

	summaryResponse, err := client.sendCommand(ctx, summaryCmd)
	if err != nil {
		log.Printf("[PiRunner] Failed to get summary: %v", err)
		return lastText, "", nil // Return without summary but don't fail
	}

	log.Printf("[PiRunner] Summary response: %+v", summaryResponse)

	// Wait for summary to complete
	agentEndErr = client.waitForAgentEnd(ctx, 2*time.Minute)
	if agentEndErr != nil {
		log.Printf("[PiRunner] Wait for summary agent_end failed: %v", agentEndErr)
	}

	// Get the summary text
	summaryText, _ := client.getLastAssistantText(ctx)
	log.Printf("[PiRunner] Summary: %s", summaryText)

	return lastText, summaryText, nil
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
	
	// Set a custom handler that detects agent_end
	oldHandler := c.handleEventFunc
	c.handleEventFunc = func(response *PiRPCResponse) {
		if response.Type == "agent_end" {
			select {
			case agentEndCh <- struct{}{}:
			default:
			}
		}
		// Call the old handler if it was set
		if oldHandler != nil {
			oldHandler(response)
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
func runPiFixRemote(ctx context.Context, sm *stepManager, workDir, cloneURL, branchName, issueTitle, issueDesc, additionalContext, gitUserName, gitUserEmail, targetBranch string, tokenCtx context.Context, repoProjectID, issueIID, issueProjectID int) {
	// Create SSH client
	sshClient, err := NewSSHClient()
	if err != nil {
		sm.failStep("clone_repo", fmt.Sprintf("Failed to create SSH client: %v", err))
		return
	}
	defer sshClient.Close()

	log.Printf("[PiRunner] Connected to SSH server: %s", sshClient.Host())

	// Cleanup remote work directory on exit
	defer func() {
		sshClient.RunCommand("rm -rf " + workDir)
	}()

	// Step 3: Clone repository on remote
	sm.startStep("clone_repo", "Setting up remote work directory...")
	if _, err := sshClient.RunCommand("mkdir -p " + workDir); err != nil {
		sm.failStep("clone_repo", fmt.Sprintf("Failed to create remote directory: %v", err))
		return
	}

	sm.startStep("clone_repo", "Cloning repository on remote server...")
	if output, err := sshClient.RunCommand(fmt.Sprintf("git clone --depth 1 %s %s", cloneURL, workDir)); err != nil {
		sm.failStep("clone_repo", fmt.Sprintf("Git clone failed: %s", string(output)))
		return
	}
	sm.completeStep("clone_repo", "Repository cloned on remote server")

	// Step 4: Configure git user on remote
	if _, err := sshClient.RunCommandInDir(fmt.Sprintf("git config user.email '%s' && git config user.name '%s'", gitUserEmail, gitUserName), workDir); err != nil {
		sm.failStep("create_branch", fmt.Sprintf("Git config failed: %v", err))
		return
	}

	// Step 4: Create branch on remote
	sm.startStep("create_branch", fmt.Sprintf("Creating branch %s...", branchName))
	if _, err := sshClient.RunCommandInDir("git checkout -b "+branchName, workDir); err != nil {
		sm.failStep("create_branch", fmt.Sprintf("Git checkout failed: %v", err))
		return
	}
	sm.completeStep("create_branch", fmt.Sprintf("Branch %s created", branchName))

	// Step 5: Analyze issue and implement fix (combined for Pi)
	sm.startStep("analyze_issue", "Analyzing issue and exploring codebase...")
	sm.startStep("implement_fix", "Implementing fix with Pi coding agent on remote server...")

	piResult, summaryText, err := runPiRPCOverSSH(ctx, sshClient, workDir, issueTitle, issueDesc, additionalContext, sm.eventCh)
	if err != nil {
		sm.failStep("implement_fix", fmt.Sprintf("Pi agent failed: %v", err))
		return
	}

	log.Printf("[PiRunner] Pi remote completed: %s", piResult)
	sm.completeStep("analyze_issue", "Issue analysis complete")
	sm.completeStep("implement_fix", fmt.Sprintf("Fix implemented: %s", piResult))

	// Step 6: Verify fix
	sm.startStep("verify_fix", "Verifying the fix...")
	output, err := sshClient.RunCommandInDir("git status --porcelain", workDir)
	if err != nil {
		sm.failStep("verify_fix", fmt.Sprintf("Failed to check remote changes: %v", err))
		return
	}
	if len(strings.TrimSpace(string(output))) == 0 {
		sm.failStep("verify_fix", "Pi did not make any changes to fix the issue")
		return
	}

	log.Printf("[PiRunner] Remote changed files:\n%s", string(output))
	sm.completeStep("verify_fix", "Changes verified successfully")

	// Step 7: Commit changes
	sm.startStep("commit_changes", "Committing changes...")
	commitMsg := fmt.Sprintf("fix: resolve issue #%d - %s", issueIID, issueTitle)
	if _, err := sshClient.RunCommandInDir(fmt.Sprintf("git add -A && git commit -m '%s'", commitMsg), workDir); err != nil {
		sm.failStep("commit_changes", fmt.Sprintf("Git commit failed: %v", err))
		return
	}
	sm.completeStep("commit_changes", fmt.Sprintf("Committed: %s", commitMsg))

	// Step 8: Push branch
	sm.startStep("push_branch", fmt.Sprintf("Pushing branch %s...", branchName))
	if _, err := sshClient.RunCommandInDir(fmt.Sprintf("git push -u origin %s --force", branchName), workDir); err != nil {
		sm.failStep("push_branch", fmt.Sprintf("Git push failed: %v", err))
		return
	}
	sm.completeStep("push_branch", fmt.Sprintf("Branch pushed to origin/%s", branchName))

	// Step 9: Create MR
	sm.startStep("create_mr", fmt.Sprintf("Creating merge request in project %d...", repoProjectID))
	mrTitle := fmt.Sprintf("Fix: %s", issueTitle)

	// Build MR description with agent-generated summary
	changesSection := "See commits for details."
	if summaryText != "" {
		changesSection = summaryText
	}

	mrDesc := fmt.Sprintf(`## Summary

Fixes issue #%d: %s

## Changes

%s

## Issue Link

Issue: #%d (Project %d)

---
*This merge request was created by AI agent (Pi).*`,
		issueIID, issueTitle, changesSection, issueIID, issueProjectID)

	mr, err := CreateMergeRequest(tokenCtx, repoProjectID, branchName, targetBranch, mrTitle, mrDesc)
	if err != nil {
		sm.failStep("create_mr", fmt.Sprintf("Failed to create MR: %v", err))
		return
	}

	sm.completeStep("create_mr", fmt.Sprintf("MR created: %s", mr.WebURL))

	// Emit final done event
	sm.emitDone(mr.WebURL)
}

// runPiRPCOverSSH runs Pi in RPC mode over an SSH connection and returns response + summary
func runPiRPCOverSSH(ctx context.Context, sshClient *SSHClient, workDir, issueTitle, issueDesc, additionalContext string, eventCh chan<- FixEvent) (string, string, error) {
	// Create SSH session for Pi RPC
	session, err := sshClient.NewSession()
	if err != nil {
		return "", "", fmt.Errorf("failed to create SSH session: %w", err)
	}
	defer session.Close()

	// Setup stdin/stdout pipes for RPC communication
	// Note: Do NOT request PTY - it causes echo which breaks the JSON-RPC protocol
	stdin, err := session.StdinPipe()
	if err != nil {
		return "", "", fmt.Errorf("failed to create stdin pipe: %w", err)
	}
	stdout, err := session.StdoutPipe()
	if err != nil {
		return "", "", fmt.Errorf("failed to create stdout pipe: %w", err)
	}
	stderr, err := session.StderrPipe()
	if err != nil {
		return "", "", fmt.Errorf("failed to create stderr pipe: %w", err)
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
		return "", "", fmt.Errorf("failed to start Pi on remote: %w", err)
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
		return "", "", fmt.Errorf("failed to send prompt: %w", err)
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

	// Ask for summary of changes
	summaryPrompt := "Please provide a brief summary of the changes you made to fix this issue. " +
		"Include: 1) A list of files that were modified, created, or deleted. " +
		"2) A brief description of the key changes in each file. " +
		"3) How these changes address the issue. " +
		"Be concise and factual. Format your response as markdown."

	summaryCmd := map[string]interface{}{
		"type":    "prompt",
		"message": summaryPrompt,
	}

	_, err = client.sendCommand(ctx, summaryCmd)
	if err != nil {
		log.Printf("[PiRunner] Failed to get summary: %v", err)
		return lastText, "", nil // Return without summary but don't fail
	}

	log.Printf("[PiRunner] Waiting for summary...")

	// Wait for summary to complete
	agentEndErr = client.waitForAgentEnd(ctx, 2*time.Minute)
	if agentEndErr != nil {
		log.Printf("[PiRunner] Wait for summary agent_end failed: %v", agentEndErr)
	}

	// Get the summary text
	summaryText, _ := client.getLastAssistantText(ctx)
	log.Printf("[PiRunner] Summary: %s", summaryText)

	// Close stdin to signal we're done
	stdin.Close()

	// Wait for Pi to exit
	if err := session.Wait(); err != nil {
		log.Printf("[PiRunner] Pi session exited with error: %v", err)
	}

	return lastText, summaryText, nil
}

