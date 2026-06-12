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
	"qa-extension-backend/tracker"

	"github.com/gin-gonic/gin"
	"golang.org/x/oauth2"
)

func ListSessions(c *gin.Context) {
	ctx := c.Request.Context()
	sessionService := agent.GetSessionService()

	// For now, we use "user" as default userID or we can try to get it from token/context if available
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
		messages := sess.Messages
		if len(messages) > 0 {
			// Find the first user message for preview
			for _, msg := range messages {
				if msg.Role == "user" && msg.Content != "" {
					preview = msg.Content
					if len(preview) > 100 {
						preview = preview[:97] + "..."
					}
					break
				}
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

// ChatMessage represents a single message in the chat history
type ChatMessage struct {
	Role      string   `json:"role"`            // "user" or "assistant"
	Content   string   `json:"content"`         // The text content
	Timestamp string   `json:"timestamp"`       // ISO 8601 timestamp
	Parts     []string `json:"parts,omitempty"` // Additional parts (for multimodal messages)
}

// GetSession retrieves a specific chat session by ID with full message history
func GetSession(c *gin.Context) {
	sessionID := c.Param("session_id")
	if sessionID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "session_id is required"})
		return
	}

	ctx := c.Request.Context()
	sessionService := agent.GetSessionService()

	sess, err := sessionService.Get(ctx, sessionID)

	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Session not found"})
		return
	}

	// Convert events to chat messages
	messages := make([]ChatMessage, 0)
	for _, msg := range sess.Messages {
		if msg.Content == "" {
			continue
		}

		messages = append(messages, ChatMessage{
			Role:      msg.Role,
			Content:   msg.Content,
			Timestamp: msg.Timestamp.Format(time.RFC3339),
			Parts:     []string{msg.Content},
		})
	}

	// Build response
	response := gin.H{
		"session_id":       sess.ID,
		"last_update_time": sess.LastUpdateTime.Format(time.RFC3339),
		"messages":         messages,
		"message_count":    len(messages),
	}

	c.JSON(http.StatusOK, response)
}

// DeleteSession deletes a specific chat session
func DeleteSession(c *gin.Context) {
	sessionID := c.Param("session_id")
	if sessionID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "session_id is required"})
		return
	}

	ctx := c.Request.Context()
	sessionService := agent.GetSessionService()

	err := sessionService.Delete(ctx, sessionID)

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete session: " + err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Session deleted successfully"})
}

func ChatWithAgent(c *gin.Context) {
	var req struct {
		SessionID    string `json:"session_id"`
		Input        string `json:"input"`
		AppProjectID string `json:"app_project_id"`
		Attachments  []struct {
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

	token := c.MustGet("token").(*oauth2.Token)

	ctx := context.WithValue(c.Request.Context(), "token", token)
	ctx = context.WithValue(ctx, "session_id", req.SessionID)

	r, err := agent.GetQARunner(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to initialize agent runner: " + err.Error()})
		return
	}

	// We use a fixed userID for now
	userID := "user"

	sessionService := agent.GetSessionService()

	if _, err = sessionService.GetOrCreate(ctx, req.SessionID, userID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create session: " + err.Error()})
		return
	}

	// Process input - check for slash commands
	input := req.Input
	if agent.IsSlashCommand(input) {
		// Try built-in slash commands first
		if cmd, args, matched := agent.MatchSlashCommand(input); matched {
			log.Printf("[ChatWithAgent] Matched built-in slash command: %s", cmd.ToolName)

			// Execute the tool pre-emptively
			if cmd.ToolName != "" && agent.HasToolExecutor(cmd.ToolName) {
				toolResult, execErr := agent.ExecuteTool(cmd.ToolName, ctx, args)
				if execErr != nil {
					log.Printf("[ChatWithAgent] Tool execution error: %v", execErr)
					// Continue to LLM with error context
					input = fmt.Sprintf("%s\n\n[Tool execution error: %v]", input, execErr)
				} else {
					// Serialize tool result and inject into context
					resultJSON, _ := json.MarshalIndent(toolResult, "", "  ")
					log.Printf("[ChatWithAgent] Tool executed successfully, result length: %d", len(resultJSON))

					// Prepend tool result to user input for LLM to format
					input = fmt.Sprintf(`%s

[PRE-EXECUTED TOOL RESULT]
The slash command "%s" was pre-executed and returned:
%s

Please format this result nicely for the user, presenting the key information in a clear, readable format.`, input, input, string(resultJSON))
				}
			} else if cmd.ToolName == "" {
				// /help command - special handling
				commands := agent.GetAllSlashCommands(ctx)
				var helpText strings.Builder
				helpText.WriteString("Available slash commands:\n\n")
				for _, c := range commands {
					helpText.WriteString(fmt.Sprintf("- %s: %s\n", c.Pattern, c.Description))
				}
				helpText.WriteString("\nYou can also type naturally and I'll help you with GitLab issues and test automation.")
				input = fmt.Sprintf(`The user invoked /help. Display the available commands:

%s`, helpText.String())
			}
		} else if cmd, args, matched := agent.MatchCustomSlashCommand(ctx, input); matched {
			log.Printf("[ChatWithAgent] Matched custom slash command: %s", cmd.Name)

			// Execute the custom command's tool
			if cmd.ToolName != "" && agent.HasToolExecutor(cmd.ToolName) {
				toolResult, execErr := agent.ExecuteTool(cmd.ToolName, ctx, args)
				if execErr != nil {
					log.Printf("[ChatWithAgent] Custom tool execution error: %v", execErr)
					input = fmt.Sprintf("%s\n\n[Tool execution error: %v]", input, execErr)
				} else {
					resultJSON, _ := json.MarshalIndent(toolResult, "", "  ")
					log.Printf("[ChatWithAgent] Custom tool executed successfully, result length: %d", len(resultJSON))

					// Prepend tool result to user input for LLM to format
					input = fmt.Sprintf(`%s

[PRE-EXECUTED TOOL RESULT - Custom Command: %s]
The custom command "%s" was pre-executed and returned:
%s

Please format this result nicely for the user.`, input, cmd.Name, cmd.Name, string(resultJSON))
				}
			}
		}
	}

	attachments := make([]agent.AgentAttachment, 0, len(req.Attachments))
	for _, att := range req.Attachments {
		decoded, err := base64.StdEncoding.DecodeString(att.Data)
		if err != nil {
			log.Printf("[ChatWithAgent] Failed to decode attachment %s: %v", att.Name, err)
			continue
		}
		mimeType := att.MimeType
		if mimeType == "" {
			mimeType = "image/png" // default fallback
		}
		log.Printf("[ChatWithAgent] Adding attachment: %s (%s, %d bytes)", att.Name, mimeType, len(decoded))
		attachments = append(attachments, agent.AgentAttachment{Name: att.Name, MimeType: mimeType, Data: decoded})
	}

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no") // Disable nginx buffering

	// Write headers immediately
	c.Writer.WriteHeaderNow()

	// Create a background-ish context that inherits values but isn't canceled when the request ends.
	agentCtx := context.WithoutCancel(ctx)
	agentCtx = context.WithValue(agentCtx, "token", token)
	agentCtx = context.WithValue(agentCtx, "session_id", req.SessionID)
	agentCtx = context.WithValue(agentCtx, "agent_session_id", req.SessionID)

	// Helper to send SSE events directly with guaranteed flush
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

	// Start heartbeat in background
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

	// Run the agent
	agentStart := time.Now()
	eventCh := r.Run(agentCtx, agent.AgentRunRequest{
		SessionID:    req.SessionID,
		UserID:       userID,
		Input:        input,
		Attachments:  attachments,
		AppProjectID: req.AppProjectID,
	})

	for event := range eventCh {
		if event.Err != nil {
			if strings.Contains(event.Err.Error(), "context canceled") {
				log.Printf("[ChatWithAgent] Request aborted by client, exiting gracefully: %v", event.Err)
				return
			}
			log.Printf("[ChatWithAgent] Agent execution error: %v", event.Err)
			sendEvent("error", gin.H{"message": event.Err.Error()})
			return
		}

		if event.Final {
			finalResponse := event.Content
			log.Printf("[ChatWithAgent] Sending final response (total %d bytes)", len(finalResponse))

			if event.Usage != nil {
				tracker.Log(agentCtx, tracker.TokenUsage{
					Feature:      "qa_chat",
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
			agent.NewAgentEmitter(agentCtx, req.SessionID).Done("Agent completed")
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
