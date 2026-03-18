package routes

import (
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"qa-extension-backend/agent"
	"strings"
	"time"

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

	// Create a progress channel and add it to the context
	progressCh := make(chan string, 10)
	ctx := context.WithValue(c.Request.Context(), "token", token)
	ctx = context.WithValue(ctx, "session_id", req.SessionID)
	ctx = context.WithValue(ctx, "progressCh", progressCh)

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

	content := &genai.Content{
		Role: genai.RoleUser,
		Parts: []*genai.Part{{Text: req.Input}},
	}

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("Transfer-Encoding", "chunked")

	// Create a background-ish context that inherits values but isn't canceled when the request ends.
	// This ensures the agent finishes its work (like uploading video) even if the client disconnects.
	agentCtx := context.WithoutCancel(c.Request.Context())

	// Create a wrapper to consume the iterator and send to a channel
	type resultEvent struct {
		event *session.Event
		err   error
	}
	resCh := make(chan resultEvent)
	go func() {
		defer close(resCh)
		eventCh := r.Run(agentCtx, userID, req.SessionID, content, adkagent.RunConfig{})
		for event, err := range eventCh {
			resCh <- resultEvent{event, err}
		}
	}()

	heartbeatTicker := time.NewTicker(15 * time.Second)
	defer heartbeatTicker.Stop()

	c.Stream(func(w io.Writer) bool {
		for {
			select {
			case <-c.Request.Context().Done():
				log.Printf("[ChatWithAgent] Client disconnected")
				return false
			case <-heartbeatTicker.C:
				// Send a heartbeat to keep the connection alive
				c.SSEvent("heartbeat", gin.H{
					"status": "alive",
				})
				if flusher, ok := w.(http.Flusher); ok {
					flusher.Flush()
				}
			case progressMsg, ok := <-progressCh:
				if !ok {
					progressCh = nil
					continue
				}
				c.SSEvent("progress", gin.H{
					"status":  "processing",
					"message": progressMsg,
				})
				if flusher, ok := w.(http.Flusher); ok {
					flusher.Flush()
				}
			case res, ok := <-resCh:
				if !ok {
					return false
				}
				if res.err != nil {
					if errors.Is(res.err, context.Canceled) || strings.Contains(res.err.Error(), "context canceled") {
						log.Printf("[ChatWithAgent] Request aborted by client, exiting gracefully: %v", res.err)
						return false
					}
					log.Printf("[ChatWithAgent] Agent execution error: %v", res.err)
					c.SSEvent("error", res.err.Error())
					if flusher, ok := w.(http.Flusher); ok {
						flusher.Flush()
					}
					return false
				}

				if res.event.IsFinalResponse() {
					var finalResponse string
					for _, part := range res.event.Content.Parts {
						if part.Text != "" {
							finalResponse += part.Text
						}
					}
					c.SSEvent("final", gin.H{
						"content":    finalResponse,
						"session_id": req.SessionID,
					})
				} else {
					c.SSEvent("progress", gin.H{
						"status": "processing",
					})
				}
				if flusher, ok := w.(http.Flusher); ok {
					flusher.Flush()
				}
			}
		}
	})
}
