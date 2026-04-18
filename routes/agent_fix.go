package routes

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"

	"qa-extension-backend/agent"

	"github.com/gin-gonic/gin"
	"golang.org/x/oauth2"
)

// FixIssueWithAgent handles POST /agent/fix-issue
// Starts a fix agent session that clones the repo, runs Claude Code, and creates an MR
func FixIssueWithAgent(c *gin.Context) {
	var req struct {
		ProjectID      int  `json:"project_id" binding:"required"`       // Project where the issue exists
		IssueIID       int  `json:"issue_iid" binding:"required"`        // Issue IID in the issue project
		RepoProjectID  *int `json:"repo_project_id"`                     // Optional: Project containing the code to fix (defaults to project_id)
		TargetBranch   string `json:"target_branch"`                      // Optional: Target branch for MR (defaults to "main")
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

	ctx := context.WithValue(c.Request.Context(), "token", oauthToken)

	// Set SSE headers
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no") // Disable nginx buffering

	// Create event channel
	eventCh := make(chan agent.FixEvent, 64)

	// Start the fix agent in a goroutine
	go func() {
		log.Printf("[FixRoute] Starting fix agent: issue project=%d, issue_iid=%d, repo project=%d, target_branch=%s", 
			req.ProjectID, req.IssueIID, repoProjectID, targetBranch)
		agent.RunFixAgent(ctx, req.ProjectID, req.IssueIID, repoProjectID, targetBranch, eventCh)
	}()

	// Get the response writer's flusher
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "streaming not supported"})
		return
	}

	// Stream events to SSE
	for event := range eventCh {
		data, err := json.Marshal(event)
		if err != nil {
			log.Printf("[FixRoute] Failed to marshal event: %v", err)
			continue
		}

		// Write SSE event
		fmt.Fprintf(c.Writer, "event: fix_event\ndata: %s\n\n", data)
		flusher.Flush()

		// If this is a terminal event (done or error), we can stop after a brief delay
		if event.Stage == "done" || event.Stage == "error" {
			// Give a moment for the client to receive the final event
			break
		}
	}

	log.Printf("[FixRoute] SSE stream completed for project %d, issue %d", req.ProjectID, req.IssueIID)
}
