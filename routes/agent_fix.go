package routes

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"qa-extension-backend/agent"
	"qa-extension-backend/database"

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

	// Project information
	ProjectID   int    `json:"projectId"`
	ProjectName string `json:"projectName,omitempty"`

	// Repository information (can be different from issue project)
	RepoProjectID   int    `json:"repoProjectId"`
	RepoProjectName string `json:"repoProjectName,omitempty"`

	// Issue information
	IssueIID    int    `json:"issueIid"`
	IssueTitle  string `json:"issueTitle,omitempty"`
	IssueURL    string `json:"issueUrl,omitempty"`
	IssueDesc   string `json:"issueDescription,omitempty"`

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
		ProjectID         int    `json:"project_id" binding:"required"`
		IssueIID          int    `json:"issue_iid" binding:"required"`
		RepoProjectID     *int   `json:"repo_project_id"`
		TargetBranch      string `json:"target_branch"`
		AdditionalContext string `json:"additional_context"`
		Runner            string `json:"runner"` // "claude" (default) or "pi"
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	repoProjectID := req.ProjectID
	if req.RepoProjectID != nil {
		repoProjectID = *req.RepoProjectID
	}

	targetBranch := req.TargetBranch
	if targetBranch == "" {
		targetBranch = "main"
	}

	// Default to "claude" if runner not specified
	runner := req.Runner
	if runner == "" {
		runner = "claude"
	}
	// Validate runner value
	if runner != "claude" && runner != "pi" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid runner value, must be 'claude' or 'pi'"})
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

	// Generate unique session ID
	sessionID := fmt.Sprintf("fix_%d_%d_%s", req.ProjectID, req.IssueIID, uuid.New().String()[:8])

	// Initialize steps with pending status
	initialSteps := make([]agent.FixStep, len(agent.DefaultFixSteps))
	for i, step := range agent.DefaultFixSteps {
		initialSteps[i] = agent.FixStep{
			ID:          step.ID,
			Title:       step.Title,
			Description: step.Description,
			Status:      agent.FixStepStatusPending,
		}
	}

	// Save initial session state to Redis
	session := FixSession{
		SessionID:         sessionID,
		Runner:            runner,
		ProjectID:         req.ProjectID,
		RepoProjectID:     repoProjectID,
		IssueIID:          req.IssueIID,
		TargetBranch:      targetBranch,
		AdditionalContext: req.AdditionalContext,
		Status:            "initialized",
		Message:           fmt.Sprintf("Starting %s fix agent...", runner),
		Steps:             initialSteps,
		CurrentStep:       -1,
		CreatedAt:         time.Now().Format(time.RFC3339),
		UpdatedAt:         time.Now().Format(time.RFC3339),
	}
	saveFixSession(session)

	// Return immediately with session ID
	c.JSON(http.StatusAccepted, gin.H{
		"message":   fmt.Sprintf("%s fix agent started", runner),
		"sessionId": sessionID,
		"runner":    runner,
		"session":   session,
	})

	// Run fix agent in background
	go func() {
		bgCtx := context.WithValue(context.Background(), "token", oauthToken)
		events := agent.NewAgentEmitter(bgCtx, sessionID)

		eventCh := make(chan agent.FixEvent, 64)
		go func() {
			for fixEvent := range eventCh {
				// Update session state in Redis
				session.Status = fixEvent.Stage
				session.Message = fixEvent.Message
				session.UpdatedAt = time.Now().Format(time.RFC3339)

				// Update steps if provided
				if len(fixEvent.Steps) > 0 {
					session.Steps = fixEvent.Steps
				}
				if fixEvent.CurrentStep >= 0 {
					session.CurrentStep = fixEvent.CurrentStep
				}

				// Update session info if provided
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

				// Publish to Redis pub/sub for SSE
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
			runner, sessionID, req.ProjectID, req.IssueIID, repoProjectID, targetBranch)

		events.Start("Starting %s fix for issue #%d in project %d...", runner, req.IssueIID, req.ProjectID)

		agent.RunFixAgent(bgCtx, runner, req.ProjectID, req.IssueIID, repoProjectID, targetBranch, req.AdditionalContext, eventCh)

		log.Printf("[FixRoute] Fix agent completed: session=%s", sessionID)
	}()
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

	c.JSON(http.StatusOK, session)
}

// ListFixSessions handles GET /agent/fix-sessions
// Returns a list of all fix sessions, optionally filtered by status.
func ListFixSessions(c *gin.Context) {
	statusFilter := c.Query("status") // ?status=running, ?status=done, ?status=error

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