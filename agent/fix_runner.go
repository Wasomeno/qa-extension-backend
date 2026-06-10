package agent

import (
	"context"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
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

// FixStepStatus represents the status of a fix step
type FixStepStatus string

const (
	FixStepStatusPending    FixStepStatus = "pending"
	FixStepStatusInProgress FixStepStatus = "in_progress"
	FixStepStatusDone       FixStepStatus = "done"
	FixStepStatusError      FixStepStatus = "error"
	FixStepStatusSkipped    FixStepStatus = "skipped"
)

// FixStep represents a single step in the fix process
type FixStep struct {
	ID          string        `json:"id"`
	Title       string        `json:"title"`
	Description string        `json:"description"`
	Status      FixStepStatus `json:"status"`
	StartedAt   string        `json:"startedAt,omitempty"`
	CompletedAt string        `json:"completedAt,omitempty"`
	Message     string        `json:"message,omitempty"`
}

// FixSessionInfo contains metadata about the fix session
type FixSessionInfo struct {
	SessionID       string `json:"sessionId"`
	Runner          string `json:"runner"`
	ProjectID       int    `json:"projectId"`
	ProjectName     string `json:"projectName,omitempty"`
	RepoProjectID   int    `json:"repoProjectId"`
	IssueIID        int    `json:"issueIid"`
	IssueTitle      string `json:"issueTitle,omitempty"`
	IssueURL        string `json:"issueUrl,omitempty"`
	TargetBranch    string `json:"targetBranch"`
	AdditionalCtx   string `json:"additionalCtx,omitempty"`
}

// FixEvent represents an event emitted during the fix process
type FixEvent struct {
	Stage     string `json:"stage"`
	Message   string `json:"message"`
	Timestamp string `json:"timestamp"`

	MRURL string `json:"mr_url,omitempty"`
	Error string `json:"error,omitempty"`

	LogLine string `json:"log_line,omitempty"`

	SessionInfo *FixSessionInfo `json:"sessionInfo,omitempty"`

	Steps       []FixStep `json:"steps,omitempty"`
	CurrentStep int       `json:"currentStep,omitempty"`

	StepUpdate *FixStep `json:"stepUpdate,omitempty"`
}

// Default fix steps template
var DefaultFixSteps = []FixStep{
	{
		ID:          "fetch_issue",
		Title:       "Fetch Issue",
		Description: "Retrieving the issue details from GitLab, including title, description, and metadata",
		Status:      FixStepStatusPending,
	},
	{
		ID:          "get_project",
		Title:       "Get Project Info",
		Description: "Fetching project details including repository URL and default branch configuration",
		Status:      FixStepStatusPending,
	},
	{
		ID:          "clone_repo",
		Title:       "Clone Repository",
		Description: "Cloning the repository to a temporary working directory for making changes",
		Status:      FixStepStatusPending,
	},
	{
		ID:          "create_branch",
		Title:       "Create Branch",
		Description: "Creating a new feature branch for the fix changes",
		Status:      FixStepStatusPending,
	},
	{
		ID:          "analyze_issue",
		Title:       "Analyze Issue",
		Description: "Understanding the issue and exploring the codebase to identify the root cause",
		Status:      FixStepStatusPending,
	},
	{
		ID:          "implement_fix",
		Title:       "Implement Fix",
		Description: "Making targeted code changes to resolve the issue",
		Status:      FixStepStatusPending,
	},
	{
		ID:          "verify_fix",
		Title:       "Verify Fix",
		Description: "Running tests and build checks to ensure the fix works correctly",
		Status:      FixStepStatusPending,
	},
	{
		ID:          "commit_changes",
		Title:       "Commit Changes",
		Description: "Committing the fix changes with a descriptive commit message",
		Status:      FixStepStatusPending,
	},
	{
		ID:          "push_branch",
		Title:       "Push Branch",
		Description: "Pushing the branch to the remote repository",
		Status:      FixStepStatusPending,
	},
	{
		ID:          "create_mr",
		Title:       "Create Merge Request",
		Description: "Creating a merge request with the fix changes for review",
		Status:      FixStepStatusPending,
	},
}

// GitUser represents the current GitLab user for git commits
type GitUser struct {
	Name  string
	Email string
}

// RunFixAgent runs the fix using Pi runner.
func RunFixAgent(ctx context.Context, _ string, issueProjectID int, issueIID int, repoProjectID int, targetBranch string, additionalContext string, eventCh chan<- FixEvent) {
	RunFixWithPi(ctx, issueProjectID, issueIID, repoProjectID, targetBranch, additionalContext, eventCh)
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
