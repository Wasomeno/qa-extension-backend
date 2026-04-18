package routes

import (
	"context"
	"fmt"
	"log"
	"net/http"

	"qa-extension-backend/agent"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"golang.org/x/oauth2"
)

// FixIssueWithAgent handles POST /agent/fix-issue
// Starts a background fix agent and returns immediately with a session ID.
// Frontend tracks progress via the existing SSE stream at GET /api/stream.
func FixIssueWithAgent(c *gin.Context) {
	var req struct {
		ProjectID         int    `json:"project_id" binding:"required"` // Project where the issue exists
		IssueIID          int    `json:"issue_iid" binding:"required"`  // Issue IID in the issue project
		RepoProjectID     *int   `json:"repo_project_id"`              // Optional: Project containing the code to fix
		TargetBranch      string `json:"target_branch"`                // Optional: Target branch for MR
		AdditionalContext  string `json:"additional_context"`           // Optional: Extra instructions for the agent
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Default repo_project_id to project_id if not specified
	repoProjectID := req.ProjectID
	if req.RepoProjectID != nil {
		repoProjectID = *req.RepoProjectID
	}

	// Default target branch to "main" if not specified
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

	// Generate a unique fix session ID
	sessionID := fmt.Sprintf("fix_%d_%d_%s", req.ProjectID, req.IssueIID, uuid.New().String()[:8])

	// Return immediately with the session ID — frontend tracks progress via SSE
	c.JSON(http.StatusAccepted, gin.H{
		"message":    "fix agent started",
		"session_id": sessionID,
	})

	// Run the fix agent in the background
	go func() {
		// Use a background context — not tied to the HTTP request
		bgCtx := context.WithValue(context.Background(), "token", oauthToken)

		// Create an event emitter that publishes to Redis (same pattern as test generation)
		events := agent.NewAgentEmitter(bgCtx, sessionID)

		// Map fix stages to emitter methods
		eventCh := make(chan agent.FixEvent, 64)
		go func() {
			for fixEvent := range eventCh {
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

		// Run the fix agent
		log.Printf("[FixRoute] Starting fix agent: session=%s issue project=%d, issue_iid=%d, repo project=%d, target_branch=%s",
			sessionID, req.ProjectID, req.IssueIID, repoProjectID, targetBranch)

		events.Start("Starting fix for issue #%d in project %d...", req.IssueIID, req.ProjectID)

		agent.RunFixAgent(bgCtx, req.ProjectID, req.IssueIID, repoProjectID, targetBranch, req.AdditionalContext, eventCh)

		log.Printf("[FixRoute] Fix agent completed: session=%s", sessionID)
	}()
}