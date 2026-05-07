package routes

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"qa-extension-backend/agent"

	"github.com/gin-gonic/gin"
	"golang.org/x/oauth2"
	adkagent "google.golang.org/adk/agent"
	"google.golang.org/adk/session"
	"google.golang.org/genai"
)

func ListSessions(c *gin.Context) {
	ctx := c.Request.Context()
	sessionService := agent.GetSessionService()

	// For now, we use "user" as default userID or we can try to get it from token/context if available
	userID := "user"

	resp, err := sessionService.List(ctx, &session.ListRequest{
		AppName: "qa_extension",
		UserID:  userID,
	})

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
	for _, sess := range resp.Sessions {
		rs, ok := sess.(*agent.RedisSession)
		if !ok {
			continue
		}

		preview := ""
		events := rs.Data.Events
		if len(events) > 0 {
			// Find the first user message for preview
			for _, ev := range events {
				if ev.Type == session.EventTypeUserMessage {
					preview = ev.Content
					if len(preview) > 100 {
						preview = preview[:97] + "..."
					}
					break
				}
			}
		}

		sessions = append(sessions, sessionInfo{
			SessionID:      rs.Data.ID,
			LastUpdateTime: rs.Data.LastUpdateTime,
			Preview:        preview,
		})
	}

	c.JSON(http.StatusOK, gin.H{"sessions": sessions})
}

func ChatWithAgent(c *gin.Context) {
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

	// Build content parts: text first, then any image attachments
	parts := []*genai.Part{genai.NewPartFromText(input)}
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
		parts = append(parts, genai.NewPartFromBytes(decoded, mimeType))
	}

	content := &genai.Content{
		Role:  genai.RoleUser,
		Parts: parts,
	}

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no") // Disable nginx buffering

	// Write headers immediately
	c.Writer.WriteHeaderNow()

	// Create a background-ish context that inherits values but isn't canceled when the request ends.
	agentCtx := context.WithoutCancel(c.Request.Context())

	// Preserve context values from the original context (including GitLab token)
	if val := c.Value("token"); val != nil {
		agentCtx = context.WithValue(agentCtx, "token", val)
	}
	if val := c.Value("session_id"); val != nil {
		agentCtx = context.WithValue(agentCtx, "auth_session_id", val)
	}
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
	var accumulatedResponse strings.Builder
	eventCh := r.Run(agentCtx, userID, req.SessionID, content, adkagent.RunConfig{})
	
	for event, err := range eventCh {
		if err != nil {
			if errors.Is(err, context.Canceled) || strings.Contains(err.Error(), "context canceled") {
				log.Printf("[ChatWithAgent] Request aborted by client, exiting gracefully: %v", err)
				return
			}
			log.Printf("[ChatWithAgent] Agent execution error: %v", err)
			sendEvent("error", gin.H{"message": err.Error()})
			return
		}

		// Accumulate text from ALL events that contain content parts
		var chunkText string
		if event.Content != nil {
			for _, part := range event.Content.Parts {
				if part.Text != "" {
					chunkText += part.Text
				}
			}
		}
		if chunkText != "" {
			accumulatedResponse.WriteString(chunkText)
		}

		if event.IsFinalResponse() {
			finalResponse := accumulatedResponse.String()
			log.Printf("[ChatWithAgent] Sending final response (total %d bytes)", len(finalResponse))
			
			sendEvent("final", gin.H{
				"content":    finalResponse,
				"session_id": req.SessionID,
			})
			
			// Publish final event to Redis for unified stream consumers
			agent.NewAgentEmitter(agentCtx, req.SessionID).Done("Agent completed")
			return
		} else {
			// Extract text for progress update
			progressText := chunkText
			if progressText == "" {
				progressText = "Agent is processing..."
			}
			
			sendEvent("progress", gin.H{
				"status":  "processing",
				"message": progressText,
			})
			
			// Publish progress event to Redis
			agent.NewAgentEmitter(agentCtx, req.SessionID).Progress(progressText)
		}
	}
}