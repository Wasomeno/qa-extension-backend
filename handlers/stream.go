package handlers

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"qa-extension-backend/database"

	"github.com/gin-gonic/gin"
)

// StreamEvents SSE endpoint — single unified stream for all long-running operations.
// The connection stays open until the client disconnects so project pages can
// observe multiple jobs (import, generation, …) without reconnecting.
//
// Query params (all optional):
//   - projectId: only events for this app project UUID (ProjectID match,
//     ResourceID equality, or ResourceID containing the id)
//   - resourceId: only receive events for a specific resource
//   - type: only receive events of a specific type (e.g. "generation")
func StreamEvents(c *gin.Context) {
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")

	filterProjectID := c.Query("projectId")
	filterResourceID := c.Query("resourceId")
	filterType := c.Query("type")

	ctx := c.Request.Context()

	sub := database.SubscribeAllStreamEvents(ctx)
	defer sub.Close()

	ch := sub.Channel()

	connectedEvent := map[string]string{
		"type":    "system",
		"stage":   "connected",
		"message": "Connected to unified event stream",
	}
	if filterProjectID != "" {
		connectedEvent["filteredProjectId"] = filterProjectID
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

	heartbeat := time.NewTicker(25 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case msg, ok := <-ch:
			if !ok {
				return
			}
			eventJSON := msg.Payload

			var ev database.StreamEvent
			if err := json.Unmarshal([]byte(eventJSON), &ev); err != nil {
				continue
			}

			if filterType != "" && ev.Type != filterType {
				continue
			}
			if filterResourceID != "" && ev.ResourceID != filterResourceID {
				continue
			}
			if filterProjectID != "" && !eventMatchesProject(ev, filterProjectID) {
				continue
			}

			fmt.Fprintf(c.Writer, "data: %s\n\n", eventJSON)
			c.Writer.Flush()
			// Do not close on done/error — keep the socket for subsequent jobs.

		case <-heartbeat.C:
			// Comment-line ping keeps proxies from idle-closing the connection.
			fmt.Fprintf(c.Writer, ": ping\n\n")
			c.Writer.Flush()

		case <-ctx.Done():
			return
		}
	}
}

func eventMatchesProject(ev database.StreamEvent, projectID string) bool {
	if projectID == "" {
		return true
	}
	if ev.ProjectID != "" {
		return ev.ProjectID == projectID
	}
	if ev.ResourceID == "" {
		// Unscoped system events are not project-specific.
		return ev.Type == "system"
	}
	if ev.ResourceID == projectID {
		return true
	}
	return strings.Contains(ev.ResourceID, projectID)
}
