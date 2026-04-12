package routes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

	content := &genai.Content{
		Role: genai.RoleUser,
		Parts: []*genai.Part{{Text: input}},
	}

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("Transfer-Encoding", "chunked")

	// Create a background-ish context that inherits values but isn't canceled when the request ends.
	// This ensures the agent finishes its work (like uploading video) even if the client disconnects.
	agentCtx := context.WithoutCancel(c.Request.Context())

	// Preserve context values from the original context (including GitLab token)
	// Note: context.WithoutCancel doesn't inherit context values, so we need to copy them
	if val := ctx.Value("token"); val != nil {
		agentCtx = context.WithValue(agentCtx, "token", val)
	}
	if val := ctx.Value("session_id"); val != nil {
		agentCtx = context.WithValue(agentCtx, "session_id", val)
	}
	if val := ctx.Value("progressCh"); val != nil {
		agentCtx = context.WithValue(agentCtx, "progressCh", val)
	}

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
