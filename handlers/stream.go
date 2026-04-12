package handlers

import (
	"encoding/json"
	"fmt"

	"qa-extension-backend/database"

	"github.com/gin-gonic/gin"
)

// StreamEvents SSE endpoint — single unified stream for all long-running operations.
// Frontend subscribes here and filters events client-side by type/resourceId.
//
// Query params (all optional):
//   - resourceId: only receive events for a specific resource (e.g. scenario-123, rec-456)
//   - type: only receive events of a specific type (e.g. "generation", "execution")
func StreamEvents(c *gin.Context) {
	// Handle preflight CORS requests
	if c.Request.Method == "OPTIONS" {
		c.Header("Vary", "Origin")
		c.Header("Access-Control-Allow-Origin", "*")
		c.Header("Access-Control-Allow-Headers", "Content-Type")
		c.Header("Access-Control-Allow-Methods", "GET, OPTIONS")
		c.Header("Access-Control-Max-Age", "86400")
		c.AbortWithStatus(204)
		return
	}

	// Set SSE headers
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no") // Disable nginx buffering if behind proxy
	c.Header("Vary", "Origin")
	c.Header("Access-Control-Allow-Origin", "*")
	c.Header("Access-Control-Expose-Headers", "*")

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
		"type":    "system",
		"stage":   "connected",
		"message": "Connected to unified event stream",
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