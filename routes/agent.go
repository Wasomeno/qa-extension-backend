package routes

import (
	"context"
	"net/http"
	"qa-extension-backend/agent"

	"github.com/gin-gonic/gin"
	"golang.org/x/oauth2"
	adkagent "google.golang.org/adk/agent"
	"google.golang.org/adk/session"
	"google.golang.org/genai"
)

func ChatWithAgent(c *gin.Context) {
	var req struct {
		SessionID string `json:"session_id"`
		Input     string `json:"input"`
	}
	if err := c.BindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if req.SessionID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "session_id is required"})
		return
	}

	token := c.MustGet("token").(*oauth2.Token)

	// Create a context with the token so tools can access it
	ctx := context.WithValue(c.Request.Context(), "token", token)

	r, err := agent.GetQARunner(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to initialize agent runner: " + err.Error()})
		return
	}

	// We use a fixed userID for now
	userID := "user"

	sessionService := agent.GetSessionService()

	// Check if session exists, if not create it
	_, err = sessionService.Get(ctx, &session.GetRequest{
		AppName:   "qa_extension",
		UserID:    userID,
		SessionID: req.SessionID,
	})

	if err != nil {
		// Attempt to create
		_, err = sessionService.Create(ctx, &session.CreateRequest{
			AppName:   "qa_extension",
			UserID:    userID,
			SessionID: req.SessionID,
		})
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create session: " + err.Error()})
			return
		}
	}

	content := genai.NewContentFromText(req.Input, genai.RoleUser)

	eventCh := r.Run(ctx, userID, req.SessionID, content, adkagent.RunConfig{})

	var finalResponse string
	for event, err := range eventCh {
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Agent execution error: " + err.Error()})
			return
		}
		if event.IsFinalResponse() {
			for _, part := range event.Content.Parts {
				if part.Text != "" {
					finalResponse += part.Text
				}
			}
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"content":    finalResponse,
		"session_id": req.SessionID,
		"status":     "done",
	})
}
