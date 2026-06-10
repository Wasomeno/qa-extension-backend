package routes

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"qa-extension-backend/agent"
	"qa-extension-backend/database"
	"qa-extension-backend/services"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"golang.org/x/oauth2"
)

// FixSession represents the state of a fix-agent run, stored in Redis.
type FixSession struct {
	// Core identifiers
	SessionID string `json:"sessionId"` // Unique session identifier

	// Session description
	Runner string `json:"runner"` // "claude" or "pi"

	// Public QA project information
	AppProjectID   string `json:"appProjectId,omitempty"`
	AppProjectName string `json:"appProjectName,omitempty"`

	// GitLab issue project information
	ProjectID   int    `json:"projectId"`
	ProjectName string `json:"projectName,omitempty"`

	// Repository information (can be different from issue project)
	RepoProjectID   int    `json:"repoProjectId"`
	RepoProjectName string `json:"repoProjectName,omitempty"`

	// Issue information
	IssueIID   int    `json:"issueIid"`
	IssueTitle string `json:"issueTitle,omitempty"`
	IssueURL   string `json:"issueUrl,omitempty"`
	IssueDesc  string `json:"issueDescription,omitempty"`

	// Target branch
	TargetBranch string `json:"targetBranch"`

	// Additional context provided
	AdditionalContext string `json:"additionalContext,omitempty"`

	// Current status
	Status  string `json:"status"`  // "initialized", "running", "done", "error"
	Message string `json:"message"` // Current status message

	// Steps tracking
	Steps       []agent.FixStep `json:"steps"`
	CurrentStep int             `json:"currentStep"` // Index of current step (0-indexed), -1 if not started

	// Result
	MRURL string `json:"mrUrl,omitempty"` // Merge request URL (when done)
	Error string `json:"error,omitempty"` // Error message (when failed)

	// Timestamps
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
}

// FixIssueWithAgent handles POST /agent/fix-issue
// Starts a background fix agent and returns immediately with a session ID.
// Frontend tracks progress via SSE stream at GET /api/stream and can poll status at GET /agent/fix-status/:session_id.
func FixIssueWithAgent(c *gin.Context) {
	var req struct {
		AppProjectID      string `json:"app_project_id"`
		ProjectID         int    `json:"project_id"`
		IssueIID          int    `json:"issue_iid" binding:"required"`
		RepoProjectID     *int   `json:"repo_project_id"`
		TargetBranch      string `json:"target_branch"`
		AdditionalContext string `json:"additional_context"`
		Runner            string `json:"runner"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	appProjectID := req.AppProjectID
	appProjectName := ""
	if c.FullPath() != "" && strings.Contains(c.FullPath(), "/projects/:id/") {
		appProjectID = c.Param("id")
	}
	if appProjectID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "app_project_id is required"})
		return
	}
	project, err := services.GetAppProject(c.Request.Context(), appProjectID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "project not found"})
		return
	}
	appProjectName = project.Name
	req.ProjectID = int(project.IssueRepoID)
	if req.RepoProjectID == nil {
		repoID := int(project.SpecsRepoID)
		req.RepoProjectID = &repoID
	}

	repoProjectID := req.ProjectID
	if req.RepoProjectID != nil {
		repoProjectID = *req.RepoProjectID
	}

	targetBranch := req.TargetBranch
	if targetBranch == "" {
		targetBranch = "main"
	}

	runner := req.Runner
	if runner == "" {
		runner = "pi"
	}

	token, ok := c.Get("token")
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "authentication required"})
		return
	}
	oauthToken, ok := token.(*oauth2.Token)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid token"})
		return
	}

	sessionID := fmt.Sprintf("fix_%d_%d_%s", req.ProjectID, req.IssueIID, uuid.New().String()[:8])
	session := newFixSession(sessionID, runner, appProjectID, appProjectName, req.ProjectID, repoProjectID, req.IssueIID, targetBranch, req.AdditionalContext)
	saveFixSession(session)

	c.JSON(http.StatusAccepted, gin.H{
		"message":   fmt.Sprintf("%s fix agent started", runner),
		"sessionId": sessionID,
		"runner":    runner,
		"session":   session,
	})

	go launchFixAgent(oauthToken, session)
}

// RetryFixSession handles POST /agent/fix-sessions/:session_id/retry
// Creates a new fix session with the same params as the original and re-runs the agent.
func RetryFixSession(c *gin.Context) {
	sessionID := c.Param("session_id")
	if sessionID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "session_id is required"})
		return
	}

	orig, err := getFixSession(sessionID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "session not found"})
		return
	}
	if c.FullPath() != "" && strings.Contains(c.FullPath(), "/projects/:id/") && orig.AppProjectID != c.Param("id") {
		c.JSON(http.StatusNotFound, gin.H{"error": "session not found in project"})
		return
	}
	if orig.Status != "error" {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("session status is '%s', only failed sessions can be retried", orig.Status)})
		return
	}

	token, ok := c.Get("token")
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "authentication required"})
		return
	}
	oauthToken, ok := token.(*oauth2.Token)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid token"})
		return
	}

	newSessionID := fmt.Sprintf("fix_%d_%d_%s", orig.ProjectID, orig.IssueIID, uuid.New().String()[:8])
	session := newFixSession(newSessionID, orig.Runner, orig.AppProjectID, orig.AppProjectName, orig.ProjectID, orig.RepoProjectID, orig.IssueIID, orig.TargetBranch, orig.AdditionalContext)
	saveFixSession(session)

	c.JSON(http.StatusAccepted, gin.H{
		"message":          fmt.Sprintf("retrying fix for issue #%d", orig.IssueIID),
		"sessionId":        newSessionID,
		"previousSessionId": sessionID,
		"runner":           session.Runner,
		"session":          session,
	})

	go launchFixAgent(oauthToken, session)
}

// newFixSession constructs a fresh FixSession with all steps in pending state.
func newFixSession(sessionID, runner, appProjectID, appProjectName string, projectID, repoProjectID, issueIID int, targetBranch, additionalContext string) FixSession {
	steps := make([]agent.FixStep, len(agent.DefaultFixSteps))
	for i, step := range agent.DefaultFixSteps {
		steps[i] = agent.FixStep{
			ID:          step.ID,
			Title:       step.Title,
			Description: step.Description,
			Status:      agent.FixStepStatusPending,
		}
	}
	return FixSession{
		SessionID:         sessionID,
		Runner:            runner,
		AppProjectID:      appProjectID,
		AppProjectName:    appProjectName,
		ProjectID:         projectID,
		RepoProjectID:     repoProjectID,
		IssueIID:          issueIID,
		TargetBranch:      targetBranch,
		AdditionalContext: additionalContext,
		Status:            "initialized",
		Message:           fmt.Sprintf("Starting %s fix agent...", runner),
		Steps:             steps,
		CurrentStep:       -1,
		CreatedAt:         time.Now().Format(time.RFC3339),
		UpdatedAt:         time.Now().Format(time.RFC3339),
	}
}

// launchFixAgent runs the fix agent in a background goroutine and streams events back to Redis.
func launchFixAgent(oauthToken *oauth2.Token, session FixSession) {
	bgCtx := context.WithValue(context.Background(), "token", oauthToken)
	events := agent.NewAgentEmitter(bgCtx, session.SessionID)

	eventCh := make(chan agent.FixEvent, 64)
	go func() {
		for fixEvent := range eventCh {
			session.Status = fixEvent.Stage
			session.Message = fixEvent.Message
			session.UpdatedAt = time.Now().Format(time.RFC3339)

			if len(fixEvent.Steps) > 0 {
				session.Steps = fixEvent.Steps
			}
			if fixEvent.CurrentStep >= 0 {
				session.CurrentStep = fixEvent.CurrentStep
			}
			if fixEvent.SessionInfo != nil {
				session.ProjectName = fixEvent.SessionInfo.ProjectName
				session.IssueTitle = fixEvent.SessionInfo.IssueTitle
				session.IssueURL = fixEvent.SessionInfo.IssueURL
			}
			if fixEvent.Stage == "done" {
				session.Status = "done"
				session.MRURL = fixEvent.MRURL
			}
			if fixEvent.Stage == "error" {
				session.Status = "error"
				session.Error = fixEvent.Error
			}

			saveFixSession(session)

			switch fixEvent.Stage {
			case "done":
				events.Done("%s | MR: %s", fixEvent.Message, fixEvent.MRURL)
			case "error":
				events.Error(fixEvent.Error)
			default:
				events.Progress(fixEvent.Message)
			}
		}
	}()

	log.Printf("[FixRoute] Starting %s fix agent: session=%s issue project=%d, issue_iid=%d, repo project=%d, target_branch=%s",
		session.Runner, session.SessionID, session.ProjectID, session.IssueIID, session.RepoProjectID, session.TargetBranch)

	events.Start("Starting %s fix for issue #%d in project %d...", session.Runner, session.IssueIID, session.ProjectID)

	agent.RunFixAgent(bgCtx, session.Runner, session.ProjectID, session.IssueIID, session.RepoProjectID, session.TargetBranch, session.AdditionalContext, eventCh)

	log.Printf("[FixRoute] Fix agent completed: session=%s", session.SessionID)
}

// GetFixStatus handles GET /agent/fix-status/:session_id
// Returns the current state of a fix session.
func GetFixStatus(c *gin.Context) {
	sessionID := c.Param("session_id")
	if sessionID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "session_id is required"})
		return
	}

	session, err := getFixSession(sessionID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "session not found"})
		return
	}
	if c.FullPath() != "" && strings.Contains(c.FullPath(), "/projects/:id/") && session.AppProjectID != c.Param("id") {
		c.JSON(http.StatusNotFound, gin.H{"error": "session not found in project"})
		return
	}

	c.JSON(http.StatusOK, session)
}

// ListFixSessions handles GET /agent/fix-sessions
// Returns a list of all fix sessions, optionally filtered by status.
func ListFixSessions(c *gin.Context) {
	statusFilter := c.Query("status") // ?status=running, ?status=done, ?status=error
	appProjectID := ""
	if c.FullPath() != "" && strings.Contains(c.FullPath(), "/projects/:id/") {
		appProjectID = c.Param("id")
		if _, err := services.GetAppProject(c.Request.Context(), appProjectID); err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "project not found"})
			return
		}
	}

	ctx := context.Background()
	var sessions []FixSession

	// Scan all fix_session:* keys
	iter := database.RedisClient.Scan(ctx, 0, "fix_session:*", 100).Iterator()
	for iter.Next(ctx) {
		key := iter.Val()
		val, err := database.RedisClient.Get(ctx, key).Result()
		if err != nil {
			continue
		}
		var session FixSession
		if err := json.Unmarshal([]byte(val), &session); err != nil {
			continue
		}
		if statusFilter != "" && session.Status != statusFilter {
			continue
		}
		if appProjectID != "" && session.AppProjectID != appProjectID {
			continue
		}
		sessions = append(sessions, session)
	}
	if err := iter.Err(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	if sessions == nil {
		sessions = []FixSession{}
	}

	c.JSON(http.StatusOK, gin.H{"sessions": sessions})
}

// DeleteFixSession handles DELETE /agent/fix-sessions/:session_id
// Removes a fix session from Redis.
func DeleteFixSession(c *gin.Context) {
	sessionID := c.Param("session_id")
	if sessionID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "session_id is required"})
		return
	}

	session, err := getFixSession(sessionID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "session not found"})
		return
	}
	if c.FullPath() != "" && strings.Contains(c.FullPath(), "/projects/:id/") && session.AppProjectID != c.Param("id") {
		c.JSON(http.StatusNotFound, gin.H{"error": "session not found in project"})
		return
	}

	key := fmt.Sprintf("fix_session:%s", sessionID)
	result, err := database.RedisClient.Del(context.Background(), key).Result()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if result == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "session not found"})
		return
	}
	if session.AppProjectID != "" {
		database.RedisClient.SRem(context.Background(), fmt.Sprintf("fix_sessions:project:%s", session.AppProjectID), session.SessionID)
	}

	c.JSON(http.StatusOK, gin.H{"message": "session deleted"})
}
func saveFixSession(session FixSession) {
	ctx := context.Background()
	key := fmt.Sprintf("fix_session:%s", session.SessionID)
	val, err := json.Marshal(session)
	if err != nil {
		log.Printf("[FixRoute] Failed to marshal fix session: %v", err)
		return
	}
	// Store with 24 hour TTL
	database.RedisClient.Set(ctx, key, val, 24*time.Hour)
	if session.AppProjectID != "" {
		database.RedisClient.SAdd(ctx, fmt.Sprintf("fix_sessions:project:%s", session.AppProjectID), session.SessionID)
	}
}

// getFixSession retrieves a FixSession from Redis
func getFixSession(sessionID string) (*FixSession, error) {
	ctx := context.Background()
	key := fmt.Sprintf("fix_session:%s", sessionID)
	val, err := database.RedisClient.Get(ctx, key).Result()
	if err != nil {
		return nil, err
	}
	var session FixSession
	if err := json.Unmarshal([]byte(val), &session); err != nil {
		return nil, err
	}
	return &session, nil
}
