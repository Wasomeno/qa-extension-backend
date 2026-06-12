package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	openai "github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
)

// AgentRunRequest is the input for an OpenAI-backed agent invocation.
type AgentRunRequest struct {
	SessionID   string
	UserID      string
	Input       string
	Attachments []AgentAttachment
}

// AgentUsage captures token usage returned by OpenAI.
type AgentUsage struct {
	InputTokens  int
	OutputTokens int
	TotalTokens  int
}

// AgentRunEvent is emitted by the OpenAI agent runner.
type AgentRunEvent struct {
	Type     string
	Message  string
	Content  string
	ToolName string
	Usage    *AgentUsage
	Final    bool
	Err      error
}

// AgentRunner is the ADK-free runner contract used by routes and automation generation.
type AgentRunner interface {
	Run(ctx context.Context, req AgentRunRequest) <-chan AgentRunEvent
	Model() string
}

// OpenAIAgentRunner executes an agent loop with OpenAI chat completions and function tools.
type OpenAIAgentRunner struct {
	client            openai.Client
	model             string
	instructionRole   string
	store             *RedisSessionService
	tools             *ToolRegistry
	systemInstruction string
}

func GetQARunner(ctx context.Context) (AgentRunner, error) {
	model := os.Getenv("OPENAI_MODEL")
	if model == "" {
		model = "gpt-4.1-mini"
	}

	opts := []option.RequestOption{}
	if apiKey := os.Getenv("OPENAI_API_KEY"); apiKey != "" {
		opts = append(opts, option.WithAPIKey(apiKey))
	}
	if baseURL := os.Getenv("OPENAI_BASE_URL"); baseURL != "" {
		opts = append(opts, option.WithBaseURL(baseURL))
	}
	instructionRole := os.Getenv("OPENAI_INSTRUCTION_ROLE")
	if instructionRole == "" {
		instructionRole = "system"
		if strings.Contains(os.Getenv("OPENAI_BASE_URL"), "crof.ai") {
			// Crof/Kimi currently returns 500 when some system prompts are combined
			// with tool-result messages. A user-role instruction preamble avoids that
			// provider bug while keeping the OpenAI default as system.
			instructionRole = "user"
		}
	}
	return &OpenAIAgentRunner{
		client:            openai.NewClient(opts...),
		model:             model,
		instructionRole:   instructionRole,
		store:             GetSessionService(),
		tools:             NewQAToolRegistry(),
		systemInstruction: SYSTEM_INSTRUCTION,
	}, nil
}

func (r *OpenAIAgentRunner) Model() string { return r.model }

func (r *OpenAIAgentRunner) Run(ctx context.Context, req AgentRunRequest) <-chan AgentRunEvent {
	ch := make(chan AgentRunEvent)
	go func() {
		defer close(ch)
		if err := r.run(ctx, req, ch); err != nil {
			ch <- AgentRunEvent{Type: "error", Message: err.Error(), Err: err}
		}
	}()
	return ch
}

func (r *OpenAIAgentRunner) run(ctx context.Context, req AgentRunRequest, ch chan<- AgentRunEvent) error {
	if req.SessionID == "" {
		return fmt.Errorf("session_id is required")
	}
	if req.UserID == "" {
		req.UserID = "user"
	}

	sess, err := r.store.GetOrCreate(ctx, req.SessionID, req.UserID)
	if err != nil {
		return fmt.Errorf("failed to load session: %w", err)
	}

	userMsg := AgentMessage{
		Role:        "user",
		Content:     req.Input,
		Attachments: req.Attachments,
		Timestamp:   time.Now(),
	}
	sess.Messages = append(sess.Messages, userMsg)
	if err := r.store.ReplaceMessages(ctx, req.SessionID, sess.Messages); err != nil {
		return fmt.Errorf("failed to save user message: %w", err)
	}

	var lastUsage *AgentUsage
	for {
		messages := buildOpenAIChatMessages(r.instructionRole, r.systemInstruction, sess.Messages)
		resp, err := r.createChatCompletion(ctx, messages)
		if err != nil {
			return err
		}
		if resp.Usage.TotalTokens > 0 || resp.Usage.PromptTokens > 0 || resp.Usage.CompletionTokens > 0 {
			lastUsage = &AgentUsage{
				InputTokens:  resp.Usage.PromptTokens,
				OutputTokens: resp.Usage.CompletionTokens,
				TotalTokens:  resp.Usage.TotalTokens,
			}
		}
		if len(resp.Choices) == 0 {
			return fmt.Errorf("OpenAI returned no choices")
		}

		assistant := resp.Choices[0].Message
		if len(assistant.ToolCalls) == 0 {
			finalMsg := AgentMessage{
				Role:      "assistant",
				Content:   assistant.Content,
				Timestamp: time.Now(),
			}
			sess.Messages = append(sess.Messages, finalMsg)
			if err := r.store.ReplaceMessages(ctx, req.SessionID, sess.Messages); err != nil {
				return fmt.Errorf("failed to save assistant message: %w", err)
			}
			ch <- AgentRunEvent{Type: "final", Content: assistant.Content, Usage: lastUsage, Final: true}
			return nil
		}

		toolCalls := make([]AgentToolCall, 0, len(assistant.ToolCalls))
		for _, tc := range assistant.ToolCalls {
			toolCalls = append(toolCalls, AgentToolCall{ID: tc.ID, Name: tc.Function.Name, Arguments: tc.Function.Arguments})
		}
		sess.Messages = append(sess.Messages, AgentMessage{
			Role:      "assistant",
			Content:   assistant.Content,
			ToolCalls: toolCalls,
			Timestamp: time.Now(),
		})

		for _, tc := range assistant.ToolCalls {
			ch <- AgentRunEvent{Type: "tool_start", Message: fmt.Sprintf("Calling %s", tc.Function.Name), ToolName: tc.Function.Name}
			result, execErr := r.tools.Execute(ctx, tc.Function.Name, jsonRawMessageFromString(tc.Function.Arguments))
			toolContent := marshalToolResult(result, execErr)
			if execErr != nil {
				log.Printf("[OpenAIAgentRunner] tool %s failed: %v", tc.Function.Name, execErr)
				ch <- AgentRunEvent{Type: "tool_error", Message: execErr.Error(), ToolName: tc.Function.Name}
			} else {
				ch <- AgentRunEvent{Type: "tool_done", Message: fmt.Sprintf("%s completed", tc.Function.Name), ToolName: tc.Function.Name}
			}
			sess.Messages = append(sess.Messages, AgentMessage{
				Role:       "tool",
				Content:    toolContent,
				ToolCallID: tc.ID,
				ToolName:   tc.Function.Name,
				Timestamp:  time.Now(),
			})
		}

		if err := r.store.ReplaceMessages(ctx, req.SessionID, sess.Messages); err != nil {
			return fmt.Errorf("failed to save tool messages: %w", err)
		}
	}
}

type openAIChatRequest struct {
	Model       string                           `json:"model"`
	Messages    []openAIChatMessage              `json:"messages"`
	Tools       []openai.ChatCompletionToolParam `json:"tools,omitempty"`
	ToolChoice  string                           `json:"tool_choice,omitempty"`
	Temperature *float64                         `json:"temperature,omitempty"`
	// reasoning_effort is supported by reasoning-capable OpenAI-compatible providers.
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
}

type openAIChatMessage struct {
	Role       string                   `json:"role"`
	Content    any                      `json:"content,omitempty"`
	ToolCalls  []openAIResponseToolCall `json:"tool_calls,omitempty"`
	ToolCallID string                   `json:"tool_call_id,omitempty"`
}

type openAIChatResponse struct {
	Choices []struct {
		Message      openAIResponseMessage `json:"message"`
		FinishReason string                `json:"finish_reason"`
	} `json:"choices"`
	Usage openAIUsage `json:"usage"`
}

type openAIResponseMessage struct {
	Role      string                   `json:"role"`
	Content   string                   `json:"content"`
	ToolCalls []openAIResponseToolCall `json:"tool_calls"`
}

type openAIResponseToolCall struct {
	ID       string                     `json:"id"`
	Type     string                     `json:"type"`
	Function openAIResponseToolFunction `json:"function"`
}

type openAIResponseToolFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type openAIUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

func (r *OpenAIAgentRunner) createChatCompletion(ctx context.Context, messages []openAIChatMessage) (*openAIChatResponse, error) {
	temperature := 0.2
	body := openAIChatRequest{
		Model:           r.model,
		Messages:        messages,
		Tools:           r.tools.OpenAITools(),
		ToolChoice:      "auto",
		Temperature:     &temperature,
		ReasoningEffort: openAIReasoningEffort(os.Getenv("OPENAI_BASE_URL")),
	}
	var resp openAIChatResponse
	if err := r.client.Post(ctx, "chat/completions", body, &resp); err != nil {
		return nil, fmt.Errorf("OpenAI chat completion failed: %w", err)
	}
	return &resp, nil
}

func openAIReasoningEffort(baseURL string) string {
	if effort := os.Getenv("OPENAI_REASONING_EFFORT"); effort != "" {
		return effort
	}
	if strings.Contains(baseURL, "crof.ai") {
		return "high"
	}
	return ""
}

func buildOpenAIChatMessages(instructionRole, systemInstruction string, messages []AgentMessage) []openAIChatMessage {
	if instructionRole == "" {
		instructionRole = "system"
	}
	if systemInstruction == "" {
		systemInstruction = SYSTEM_INSTRUCTION
	}
	instructionContent := systemInstruction
	if instructionRole == "user" {
		instructionContent = "System instructions:\n" + systemInstruction + "\n\nFollow these instructions for all subsequent messages."
	}
	out := []openAIChatMessage{{Role: instructionRole, Content: instructionContent}}
	for _, msg := range messages {
		switch msg.Role {
		case "user":
			out = append(out, openAIChatMessage{Role: "user", Content: userMessageContent(msg)})
		case "assistant":
			toolCalls := make([]openAIResponseToolCall, 0, len(msg.ToolCalls))
			for _, tc := range msg.ToolCalls {
				toolCalls = append(toolCalls, openAIResponseToolCall{
					ID:   tc.ID,
					Type: "function",
					Function: openAIResponseToolFunction{
						Name:      tc.Name,
						Arguments: tc.Arguments,
					},
				})
			}
			out = append(out, openAIChatMessage{Role: "assistant", Content: msg.Content, ToolCalls: toolCalls})
		case "tool":
			out = append(out, openAIChatMessage{Role: "tool", Content: msg.Content, ToolCallID: msg.ToolCallID})
		}
	}
	return out
}

func userMessageContent(msg AgentMessage) any {
	if len(msg.Attachments) == 0 {
		return msg.Content
	}
	parts := make([]map[string]any, 0, len(msg.Attachments)+1)
	if msg.Content != "" {
		parts = append(parts, map[string]any{"type": "text", "text": msg.Content})
	}
	for _, att := range msg.Attachments {
		mimeType := att.MimeType
		if mimeType == "" {
			mimeType = "image/png"
		}
		if !strings.HasPrefix(mimeType, "image/") {
			continue
		}
		dataURL := fmt.Sprintf("data:%s;base64,%s", mimeType, base64.StdEncoding.EncodeToString(att.Data))
		parts = append(parts, map[string]any{
			"type": "image_url",
			"image_url": map[string]any{
				"url":    dataURL,
				"detail": "auto",
			},
		})
	}
	return parts
}

func marshalToolResult(result any, err error) string {
	payload := map[string]any{"ok": err == nil}
	if err != nil {
		payload["error"] = err.Error()
	} else {
		payload["result"] = result
	}
	b, marshalErr := json.Marshal(payload)
	if marshalErr != nil {
		return fmt.Sprintf(`{"ok":false,"error":"failed to marshal tool result: %s"}`, marshalErr.Error())
	}
	return string(b)
}
