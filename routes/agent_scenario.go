package routes

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"qa-extension-backend/agent"
	"qa-extension-backend/services"
	"qa-extension-backend/tracker"

	"github.com/gin-gonic/gin"
	"golang.org/x/oauth2"
)

// ListScenarioSessions handles GET /projects/:id/scenario-agent/sessions
func ListScenarioSessions(c *gin.Context) {
	ctx := c.Request.Context()
	sessionService := agent.GetScenarioSessionService()
	userID := "user"

	sessionsData, err := sessionService.List(ctx, userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list sessions: " + err.Error()})
		return
	}

	type sessionInfo struct {
		SessionID      string    `json:"session_id"`
		LastUpdateTime time.Time `json:"last_update_time"`
		Preview        string    `json:"preview"`
	}

	sessions := make([]sessionInfo, 0)
	for _, sess := range sessionsData {
		preview := ""
		if len(sess.Messages) > 0 {
			last := sess.Messages[len(sess.Messages)-1]
			if last.Role == "assistant" {
				preview = last.Content
			} else if last.Role == "user" {
				preview = last.Content
			}
			if len(preview) > 200 {
				preview = preview[:200] + "..."
			}
		}
		sessions = append(sessions, sessionInfo{
			SessionID:      sess.ID,
			LastUpdateTime: sess.LastUpdateTime,
			Preview:        preview,
		})
	}

	c.JSON(http.StatusOK, gin.H{"sessions": sessions})
}

// GetScenarioSession handles GET /projects/:id/scenario-agent/sessions/:session_id
func GetScenarioSession(c *gin.Context) {
	ctx := c.Request.Context()
	sessionService := agent.GetScenarioSessionService()
	sessionID := c.Param("session_id")

	session, err := sessionService.Get(ctx, sessionID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Session not found"})
		return
	}
	if session == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Session not found"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"session_id":       session.ID,
		"user_id":          session.UserID,
		"last_update_time": session.LastUpdateTime,
		"messages":         session.Messages,
	})
}

// DeleteScenarioSession handles DELETE /projects/:id/scenario-agent/sessions/:session_id
func DeleteScenarioSession(c *gin.Context) {
	ctx := c.Request.Context()
	sessionService := agent.GetScenarioSessionService()
	sessionID := c.Param("session_id")

	if err := sessionService.Delete(ctx, sessionID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete session: " + err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Session deleted"})
}

// ChatWithScenarioAgent handles POST /projects/:id/scenario-agent/chat
// SSE streaming endpoint for discussing test scenarios with the scenario agent.
func ChatWithScenarioAgent(c *gin.Context) {
	var req struct {
		SessionID   string `json:"session_id"`
		Input       string `json:"input"`
		Attachments []struct {
			Name     string `json:"name"`
			MimeType string `json:"mimeType"`
			Data     string `json:"data"`
		} `json:"attachments"`
	}
	if err := c.BindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if req.SessionID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "session_id is required"})
		return
	}

	// Resolve project from route parameter
	appProjectID := c.Param("id")
	project, err := services.GetAppProject(c.Request.Context(), appProjectID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "project not found"})
		return
	}

	token := c.MustGet("token").(*oauth2.Token)

	ctx := context.WithValue(c.Request.Context(), "token", token)
	ctx = context.WithValue(ctx, "session_id", req.SessionID)

	r, err := agent.GetScenarioRunner(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to initialize agent runner: " + err.Error()})
		return
	}

	userID := "user"
	sessionService := agent.GetScenarioSessionService()

	if _, err = sessionService.GetOrCreate(ctx, req.SessionID, userID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create session: " + err.Error()})
		return
	}

	// Prefix the input with project context so the agent knows which project we're discussing
	input := fmt.Sprintf(`[Project Context]
Project Name: %s
Project ID: %s
Issue Repo ID: %d
Specs Repo ID: %d

User Message:
%s`, project.Name, project.ID, project.IssueRepoID, project.SpecsRepoID, req.Input)

	attachments := make([]agent.AgentAttachment, 0, len(req.Attachments))
	for _, att := range req.Attachments {
		decoded, err := base64.StdEncoding.DecodeString(att.Data)
		if err != nil {
			log.Printf("[ChatWithScenarioAgent] Failed to decode attachment %s: %v", att.Name, err)
			continue
		}
		mimeType := att.MimeType
		if mimeType == "" {
			mimeType = "image/png"
		}
		attachments = append(attachments, agent.AgentAttachment{Name: att.Name, MimeType: mimeType, Data: decoded})
	}

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")

	c.Writer.WriteHeaderNow()

	agentCtx := context.WithoutCancel(ctx)
	agentCtx = context.WithValue(agentCtx, "token", token)
	agentCtx = context.WithValue(agentCtx, "session_id", req.SessionID)
	agentCtx = context.WithValue(agentCtx, "agent_session_id", req.SessionID)

	sendEvent := func(eventType string, data interface{}) error {
		payload, err := json.Marshal(gin.H{
			"event": eventType,
			"data":  data,
		})
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(c.Writer, "data: %s\n\n", payload)
		if err != nil {
			return err
		}
		if flusher, ok := c.Writer.(http.Flusher); ok {
			flusher.Flush()
		}
		return nil
	}

	heartbeatDone := make(chan struct{})
	go func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-heartbeatDone:
				return
			case <-ticker.C:
				sendEvent("heartbeat", gin.H{"status": "alive"})
			}
		}
	}()
	defer close(heartbeatDone)

	agentStart := time.Now()
	eventCh := r.Run(agentCtx, agent.AgentRunRequest{
		SessionID:   req.SessionID,
		UserID:      userID,
		Input:       input,
		Attachments: attachments,
	})

	for event := range eventCh {
		if event.Err != nil {
			if strings.Contains(event.Err.Error(), "context canceled") {
				log.Printf("[ChatWithScenarioAgent] Request aborted by client: %v", event.Err)
				return
			}
			log.Printf("[ChatWithScenarioAgent] Agent error: %v", event.Err)
			sendEvent("error", gin.H{"message": event.Err.Error()})
			return
		}

		if event.Final {
			finalResponse := event.Content

			if event.Usage != nil {
				tracker.Log(agentCtx, tracker.TokenUsage{
					Feature:      "scenario_chat",
					Model:        r.Model(),
					InputTokens:  int32(event.Usage.InputTokens),
					OutputTokens: int32(event.Usage.OutputTokens),
					TotalTokens:  int32(event.Usage.TotalTokens),
					SessionID:    req.SessionID,
					Duration:     time.Since(agentStart),
				})
			}

			sendEvent("final", gin.H{
				"content":    finalResponse,
				"session_id": req.SessionID,
			})
			agent.NewAgentEmitter(agentCtx, req.SessionID).Done("Scenario agent completed")
			return
		}

		progressText := event.Message
		if progressText == "" {
			progressText = "Agent is processing..."
		}
		sendEvent("progress", gin.H{"status": "processing", "message": progressText})
		agent.NewAgentEmitter(agentCtx, req.SessionID).Progress(progressText)
	}
}
