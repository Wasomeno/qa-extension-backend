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
	SessionID    string `json:"session_id"`
	ProjectID    int    `json:"project_id"`
	IssueIID     int    `json:"issue_iid"`
	RepoProjectID int   `json:"repo_project_id"`
	TargetBranch string `json:"target_branch"`
	Status       string `json:"status"`    // "running", "done", "error"
	Message      string `json:"message"`    // Current status message
	MRURL        string `json:"mr_url"`     // Merge request URL (when done)
	Error        string `json:"error"`      // Error message (when failed)
	CreatedAt    string `json:"created_at"`
	UpdatedAt    string `json:"updated_at"`
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
		AdditionalContext  string `json:"additional_context"`
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

	// Save initial session state to Redis
	session := FixSession{
		SessionID:     sessionID,
		ProjectID:     req.ProjectID,
		IssueIID:      req.IssueIID,
		RepoProjectID: repoProjectID,
		TargetBranch:  targetBranch,
		Status:        "running",
		Message:       "Starting fix agent...",
		CreatedAt:     time.Now().Format(time.RFC3339),
		UpdatedAt:     time.Now().Format(time.RFC3339),
	}
	saveFixSession(session)

	// Return immediately with session ID
	c.JSON(http.StatusAccepted, gin.H{
		"message":    "fix agent started",
		"session_id": sessionID,
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

		log.Printf("[FixRoute] Starting fix agent: session=%s issue project=%d, issue_iid=%d, repo project=%d, target_branch=%s",
			sessionID, req.ProjectID, req.IssueIID, repoProjectID, targetBranch)

		events.Start("Starting fix for issue #%d in project %d...", req.IssueIID, req.ProjectID)

		agent.RunFixAgent(bgCtx, req.ProjectID, req.IssueIID, repoProjectID, targetBranch, req.AdditionalContext, eventCh)

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

// saveFixSession persists a FixSession to Redis
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