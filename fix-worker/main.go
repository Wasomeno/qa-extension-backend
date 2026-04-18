package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/mux"
)

// FixRequest is the request body for starting a fix
type FixRequest struct {
	ProjectID      int    `json:"project_id"`
	IssueIID       int    `json:"issue_iid"`
	IssueTitle     string `json:"issue_title"`
	IssueDesc      string `json:"issue_desc"`
	RepoURL        string `json:"repo_url"`         // git clone URL with token embedded
	SystemPrompt   string `json:"system_prompt"`     // system prompt for Claude
	GitUserName    string `json:"git_user_name"`      // user's name for git commits
	GitUserEmail   string `json:"git_user_email"`     // user's email for git commits
	TargetBranch   string `json:"target_branch"`      // defaults to "main"
	AdditionalCtx  string `json:"additional_context"` // optional extra instructions
	Model          string `json:"model"`              // model to use, defaults to "minimax-m2.7"
}

// FixResponse is returned after a fix completes
type FixResponse struct {
	SessionID string `json:"session_id"`
	Status     string `json:"status"` // "running", "done", "error"
	Message    string `json:"message"`
	MRURL      string `json:"mr_url,omitempty"`
	CommitSHA  string `json:"commit_sha,omitempty"`
	Error      string `json:"error,omitempty"`
	Duration   string `json:"duration,omitempty"`
}

// StatusResponse is returned for status checks
type StatusResponse struct {
	SessionID string `json:"session_id"`
	Status    string `json:"status"`
	Message   string `json:"message"`
	MRURL     string `json:"mr_url,omitempty"`
	Error     string `json:"error,omitempty"`
}

var (
	// In-memory store of fix sessions
	sessions = make(map[string]*FixResponse)
)

func main() {
	port := os.Getenv("FIX_WORKER_PORT")
	if port == "" {
		port = "8080"
	}

	r := mux.NewRouter()
	r.HandleFunc("/fix", startFix).Methods("POST")
	r.HandleFunc("/fix/{session_id}", getStatus).Methods("GET")
	r.HandleFunc("/health", healthCheck).Methods("GET")

	log.Printf("[FixWorker] Starting on port %s", port)
	log.Fatal(http.ListenAndServe(":"+port, r))
}

func healthCheck(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func startFix(w http.ResponseWriter, r *http.Request) {
	var req FixRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if req.RepoURL == "" {
		http.Error(w, "repo_url is required", http.StatusBadRequest)
		return
	}

	if req.IssueTitle == "" {
		http.Error(w, "issue_title is required", http.StatusBadRequest)
		return
	}

	if req.TargetBranch == "" {
		req.TargetBranch = "main"
	}

	if req.Model == "" {
		req.Model = "minimax-m2.7"
	}

	sessionID := fmt.Sprintf("fix_%d_%d_%s", req.ProjectID, req.IssueIID, uuid.New().String()[:8])

	// Initialize session
	sessions[sessionID] = &FixResponse{
		SessionID: sessionID,
		Status:    "running",
		Message:   "Starting fix...",
	}

	// Return immediately with session ID
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]string{
		"session_id": sessionID,
		"status":     "running",
	})

	// Run fix in background
	go runFix(sessionID, req)
}

func getStatus(w http.ResponseWriter, r *http.Request) {
	sessionID := mux.Vars(r)["session_id"]
	session, ok := sessions[sessionID]
	if !ok {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}
	json.NewEncoder(w).Encode(session)
}

func runFix(sessionID string, req FixRequest) {
	startTime := time.Now()
	log.Printf("[%s] Starting fix for issue #%d: %s", sessionID, req.IssueIID, req.IssueTitle)

	workDir := filepath.Join(os.TempDir(), "qa-fix-"+sessionID)
	defer func() {
		log.Printf("[%s] Cleaning up %s", sessionID, workDir)
		os.RemoveAll(workDir)
	}()

	updateSession := func(status, message string) {
		sessions[sessionID].Status = status
		sessions[sessionID].Message = message
		log.Printf("[%s] %s: %s", sessionID, status, message)
	}

	// Step 1: Clone repo
	updateSession("running", fmt.Sprintf("Cloning repository..."))
	if err := gitClone(req.RepoURL, workDir); err != nil {
		updateSession("error", fmt.Sprintf("Failed to clone repo: %v", err))
		return
	}

	// Step 2: Configure git user
	if err := gitConfig(workDir, req.GitUserName, req.GitUserEmail); err != nil {
		updateSession("error", fmt.Sprintf("Failed to configure git: %v", err))
		return
	}

	// Step 3: Create branch
	branchName := fmt.Sprintf("fix/issue-%d", req.IssueIID)
	updateSession("running", fmt.Sprintf("Creating branch %s...", branchName))
	if err := gitCheckoutBranch(workDir, branchName); err != nil {
		updateSession("error", fmt.Sprintf("Failed to create branch: %v", err))
		return
	}

	// Step 4: Write system prompt file
	systemPromptFile := filepath.Join(workDir, ".fix-system-prompt.txt")
	if err := os.WriteFile(systemPromptFile, []byte(req.SystemPrompt), 0644); err != nil {
		updateSession("error", fmt.Sprintf("Failed to write system prompt: %v", err))
		return
	}

	// Build user prompt
	prompt := fmt.Sprintf("Fix issue: %s\n\nIssue description:\n%s", req.IssueTitle, req.IssueDesc)
	if req.AdditionalCtx != "" {
		prompt += fmt.Sprintf("\n\nAdditional context/instructions:\n%s", req.AdditionalCtx)
	}
	prompt += "\n\nPlease fix this issue now. Remember to explore the codebase first before making changes."

	// Step 5: Run Claude Code
	updateSession("running", "Running Claude Code...")
	result, err := runClaudeCode(workDir, prompt, systemPromptFile, req.Model)
	
	// Cleanup temp files
	os.Remove(systemPromptFile)
	os.RemoveAll(filepath.Join(workDir, ".claude"))

	if err != nil {
		// Check if there are changes despite the error
		hasChanges, _ := hasUncommittedChanges(workDir)
		if !hasChanges {
			updateSession("error", fmt.Sprintf("Claude Code failed: %v", err))
			return
		}
		log.Printf("[%s] Claude had errors but changes exist, proceeding", sessionID)
	}

	// Step 6: Check for changes
	hasChanges, err := hasUncommittedChanges(workDir)
	if err != nil || !hasChanges {
		updateSession("error", "Claude Code did not make any source code changes")
		return
	}

	hasSource, _ := hasSourceCodeChanges(workDir)
	if !hasSource {
		updateSession("error", "Claude Code only made config/metadata changes, no source code fixes")
		return
	}

	// Step 7: Commit and push
	commitMsg := fmt.Sprintf("fix: resolve issue #%d - %s", req.IssueIID, req.IssueTitle)
	updateSession("running", "Committing and pushing changes...")
	if err := gitCommitAndPush(workDir, branchName, commitMsg); err != nil {
		updateSession("error", fmt.Sprintf("Failed to commit and push: %v", err))
		return
	}

	duration := time.Since(startTime)
	log.Printf("[%s] Fix completed in %v", sessionID, duration)

	sessions[sessionID].Status = "done"
	sessions[sessionID].Message = "Fix complete! MR URL will be created by the backend."
	sessions[sessionID].Duration = duration.Round(time.Second).String()
}

// --- Git helpers ---

func gitClone(url, dir string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "clone", "--depth", "1", url, dir)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git clone failed: %w, output: %s", err, string(output))
	}
	return nil
}

func gitConfig(dir, name, email string) error {
	defaultName := "QA Fix Agent"
	defaultEmail := "qa-agent@fix.local"
	if name == "" {
		name = defaultName
	}
	if email == "" {
		email = defaultEmail
	}
	for _, args := range [][]string{
		{"git", "config", "user.email", email},
		{"git", "config", "user.name", name},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if output, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("git config failed: %w, output: %s", err, string(output))
		}
	}
	return nil
}

func gitCheckoutBranch(dir, branch string) error {
	cmd := exec.Command("git", "checkout", "-b", branch)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git checkout -b failed: %w, output: %s", err, string(output))
	}
	return nil
}

func hasUncommittedChanges(dir string) (bool, error) {
	cmd := exec.Command("git", "status", "--porcelain")
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		return false, err
	}
	return len(strings.TrimSpace(string(output))) > 0, nil
}

func hasSourceCodeChanges(dir string) (bool, error) {
	cmd := exec.Command("git", "status", "--porcelain")
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		return false, err
	}
	sourceExts := map[string]bool{
		".js": true, ".jsx": true, ".ts": true, ".tsx": true, ".vue": true,
		".py": true, ".go": true, ".java": true, ".rb": true, ".php": true,
		".css": true, ".scss": true, ".html": true, ".json": true, ".yaml": true,
		".yml": true, ".sh": true, ".sql": true,
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
		ext := strings.ToLower(filepath.Ext(filename))
		if strings.Contains(filename, ".claude/") {
			continue
		}
		if sourceExts[ext] {
			return true, nil
		}
	}
	return false, nil
}

func gitCommitAndPush(dir, branch, msg string) error {
	for _, args := range [][]string{
		{"git", "add", "-A"},
		{"git", "commit", "-m", msg},
		{"git", "push", "-u", "origin", branch},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if output, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("%s failed: %w, output: %s", args[0], err, string(output))
		}
	}
	return nil
}

// --- Claude Code runner ---

func runClaudeCode(workDir, prompt, systemPromptFile, model string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	claudePath := "/usr/local/bin/claude"
	if _, err := os.Stat(claudePath); err != nil {
		claudePath = "claude"
	}

	args := []string{
		"--print",
		"--output-format", "stream-json",
		"--verbose",
		"--dangerously-skip-permissions",
		"--no-session-persistence",
		"--max-turns", "50",
		"--model", model,
		"--system-prompt-file", systemPromptFile,
		"--add-dir", workDir,
	}

	log.Printf("[Claude] Spawning: %s %v", claudePath, args)
	log.Printf("[Claude] Prompt length: %d chars", len(prompt))

	cmd := exec.CommandContext(ctx, claudePath, args...)
	cmd.Dir = workDir
	cmd.Stdin = strings.NewReader(prompt)

	// Build environment
	cmd.Env = append(os.Environ(),
		"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1",
	)
	if apiKey := os.Getenv("ANTHROPIC_API_KEY"); apiKey != "" {
		cmd.Env = append(cmd.Env, "ANTHROPIC_API_KEY="+apiKey)
	}
	if baseURL := os.Getenv("ANTHROPIC_BASE_URL"); baseURL != "" {
		cmd.Env = append(cmd.Env, "ANTHROPIC_BASE_URL="+baseURL)
	}

	output, err := cmd.CombinedOutput()
	if err != nil {
		return string(output), fmt.Errorf("claude exited with error: %w, output: %s", err, string(output))
	}

	log.Printf("[Claude] Completed successfully, output length: %d", len(output))
	return string(output), nil
}