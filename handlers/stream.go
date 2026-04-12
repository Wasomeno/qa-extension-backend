package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"

	"qa-extension-backend/auth"
	"qa-extension-backend/database"

	"github.com/gin-gonic/gin"
)

// StreamEvents SSE endpoint — single unified stream for all long-running operations.
// Frontend subscribes here and filters events client-side by type/resourceId.
//
// Query params (all optional):
//   - resourceId: only receive events for a specific resource (e.g. scenario-123, rec-456)
//   - type: only receive events of a specific type (e.g. "generation", "execution")
//   - session_id: for auth (SSE can't use custom headers or cookies reliably)
func StreamEvents(c *gin.Context) {
	// Try to get token from query param first (for SSE from browser extensions)
	// Then fall back to cookie-based auth
	var sessionID string

	if querySessionID := c.Query("session_id"); querySessionID != "" {
		// Authenticate via session_id query param (for SSE connections that can't pass cookies)
		t, err := auth.GetSession(c, querySessionID)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized: Invalid session"})
			return
		}
		_ = t // token validated
		sessionID = querySessionID
	} else {
		// Try cookie-based auth
		cookieSessionID, err := c.Cookie("session_id")
		if err != nil || cookieSessionID == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized: No session found"})
			return
		}

		t, err := auth.GetSession(c, cookieSessionID)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized: Invalid session"})
			return
		}
		_ = t // token validated
		sessionID = cookieSessionID
	}

	// Set SSE headers
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no") // Disable nginx buffering if behind proxy

	// Optional filters from query params
	filterResourceID := c.Query("resourceId")
	filterType := c.Query("type")

	ctx := c.Request.Context()

	// Subscribe to the unified Redis channel
	sub := database.SubscribeAllStreamEvents(ctx)
	defer sub.Close()

	ch := sub.Channel()

	// Send initial connected event so the frontend knows the stream is alive
	connectedEvent := map[string]string{
		"type":       "system",
		"stage":      "connected",
		"message":    "Connected to unified event stream",
		"session_id": sessionID,
	}
	if filterResourceID != "" {
		connectedEvent["filteredResourceId"] = filterResourceID
	}
	if filterType != "" {
		connectedEvent["filteredType"] = filterType
	}
	connectedJSON, _ := json.Marshal(connectedEvent)
	fmt.Fprintf(c.Writer, "data: %s\n\n", string(connectedJSON))
	c.Writer.Flush()

	// Stream events as they arrive
	for {
		select {
		case msg := <-ch:
			eventJSON := msg.Payload

			// Parse event to check filters
			var ev database.StreamEvent
			if err := json.Unmarshal([]byte(eventJSON), &ev); err != nil {
				// Invalid JSON, skip
				continue
			}

			// Apply filters
			if filterResourceID != "" && ev.ResourceID != filterResourceID {
				continue
			}
			if filterType != "" && ev.Type != filterType {
				continue
			}

			// Forward event to client
			fmt.Fprintf(c.Writer, "data: %s\n\n", eventJSON)
			c.Writer.Flush()

			// If it's a terminal event, close the stream
			if ev.Stage == "done" || ev.Stage == "error" {
				closedJSON, _ := json.Marshal(map[string]string{
					"type":    "system",
					"stage":   "closed",
					"message": "Stream ended for " + ev.ResourceID,
				})
				fmt.Fprintf(c.Writer, "data: %s\n\n", closedJSON)
				c.Writer.Flush()
				return
			}

		case <-ctx.Done():
			// Client disconnected
			return
		}
	}
}
