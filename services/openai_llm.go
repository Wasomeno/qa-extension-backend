package services

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	openai "github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
)

// OpenAILLMRequest describes a simple OpenAI-compatible chat completion call.
type OpenAILLMRequest struct {
	Feature      string
	Model        string
	SystemPrompt string
	Prompt       string
	Temperature  float64
	JSONMode     bool
}

// OpenAILLMResponse contains the text and token usage from an OpenAI-compatible response.
type OpenAILLMResponse struct {
	Text         string
	Model        string
	InputTokens  int32
	OutputTokens int32
	TotalTokens  int32
}

type openAIChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openAIChatRequest struct {
	Model           string              `json:"model"`
	Messages        []openAIChatMessage `json:"messages"`
	Temperature     *float64            `json:"temperature,omitempty"`
	ResponseFormat  any                 `json:"response_format,omitempty"`
	ReasoningEffort string              `json:"reasoning_effort,omitempty"`
}

type openAIChatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int32 `json:"prompt_tokens"`
		CompletionTokens int32 `json:"completion_tokens"`
		TotalTokens      int32 `json:"total_tokens"`
	} `json:"usage"`
}

// GenerateOpenAIText calls the configured OpenAI-compatible chat completions API.
func GenerateOpenAIText(ctx context.Context, req OpenAILLMRequest) (*OpenAILLMResponse, error) {
	start := time.Now()
	model := req.Model
	if model == "" {
		model = os.Getenv("OPENAI_MODEL")
	}
	if model == "" {
		model = "gpt-4.1-mini"
	}

	opts := []option.RequestOption{}
	if apiKey := os.Getenv("OPENAI_API_KEY"); apiKey != "" {
		opts = append(opts, option.WithAPIKey(apiKey))
	}
	baseURL := os.Getenv("OPENAI_BASE_URL")
	if baseURL != "" {
		opts = append(opts, option.WithBaseURL(baseURL))
	}
	log.Printf("[OpenAILLM] request start feature=%s model=%s baseURLSet=%t jsonMode=%t promptBytes=%d systemPromptBytes=%d", req.Feature, model, baseURL != "", req.JSONMode, len(req.Prompt), len(req.SystemPrompt))
	client := openai.NewClient(opts...)

	messages := make([]openAIChatMessage, 0, 2)
	if strings.TrimSpace(req.SystemPrompt) != "" {
		role := os.Getenv("OPENAI_INSTRUCTION_ROLE")
		if role == "" {
			role = "system"
			if strings.Contains(baseURL, "crof.ai") {
				role = "user"
			}
		}
		content := req.SystemPrompt
		if role == "user" {
			content = "System instructions:\n" + req.SystemPrompt + "\n\nFollow these instructions for all subsequent messages."
		}
		messages = append(messages, openAIChatMessage{Role: role, Content: content})
	}
	prompt := req.Prompt
	if req.JSONMode && strings.Contains(baseURL, "crof.ai") {
		prompt += "\n\nReturn ONLY valid JSON. Do not include markdown code fences or explanatory text."
	}
	messages = append(messages, openAIChatMessage{Role: "user", Content: prompt})

	body := openAIChatRequest{
		Model:           model,
		Messages:        messages,
		Temperature:     &req.Temperature,
		ReasoningEffort: openAIReasoningEffort(baseURL),
	}
	if req.JSONMode && !strings.Contains(baseURL, "crof.ai") {
		body.ResponseFormat = map[string]string{"type": "json_object"}
	}

	var resp openAIChatResponse
	if err := client.Post(ctx, "chat/completions", body, &resp); err != nil {
		log.Printf("[OpenAILLM] request failed feature=%s model=%s duration=%s error=%v", req.Feature, model, time.Since(start), err)
		return nil, fmt.Errorf("OpenAI chat completion failed: %w", err)
	}
	if len(resp.Choices) == 0 {
		log.Printf("[OpenAILLM] request empty choices feature=%s model=%s duration=%s", req.Feature, model, time.Since(start))
		return nil, fmt.Errorf("OpenAI returned no choices")
	}
	log.Printf("[OpenAILLM] request success feature=%s model=%s duration=%s inputTokens=%d outputTokens=%d totalTokens=%d responseBytes=%d", req.Feature, model, time.Since(start), resp.Usage.PromptTokens, resp.Usage.CompletionTokens, resp.Usage.TotalTokens, len(resp.Choices[0].Message.Content))

	return &OpenAILLMResponse{
		Text:         cleanLLMText(resp.Choices[0].Message.Content),
		Model:        model,
		InputTokens:  resp.Usage.PromptTokens,
		OutputTokens: resp.Usage.CompletionTokens,
		TotalTokens:  resp.Usage.TotalTokens,
	}, nil
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

func cleanLLMText(s string) string {
	res := strings.TrimSpace(s)
	res = strings.TrimPrefix(res, "'''json")
	res = strings.TrimPrefix(res, "```json")
	res = strings.TrimPrefix(res, "```")
	res = strings.TrimSuffix(res, "'''")
	res = strings.TrimSuffix(res, "```")
	return strings.TrimSpace(res)
}
