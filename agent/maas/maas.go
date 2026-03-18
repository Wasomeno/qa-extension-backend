package maas

import (
	"context"
	"fmt"
	"io"
	"iter"

	openai "github.com/sashabaranov/go-openai"
	"golang.org/x/oauth2/google"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

// MaaSAdapter adapts an OpenAI-compatible Vertex AI MaaS endpoint (like GLM, Kimi, Llama) to the ADK LLM interface.
type MaaSAdapter struct {
	client *openai.Client
	model  string
}

// NewMaaSModel initializes a new generic adapter using Google Default Credentials
// for Vertex AI Model as a Service endpoints.
func NewMaaSModel(ctx context.Context, projectID, location, modelName string) (*MaaSAdapter, error) {
	// 1. Get Google OAuth 2.0 access token (required for Vertex MaaS endpoints)
	creds, err := google.FindDefaultCredentials(ctx, "https://www.googleapis.com/auth/cloud-platform")
	if err != nil {
		return nil, fmt.Errorf("failed to get google credentials: %w", err)
	}
	token, err := creds.TokenSource.Token()
	if err != nil {
		return nil, fmt.Errorf("failed to get token: %w", err)
	}

	// 2. Configure OpenAI client to point to Vertex AI
	baseURL := fmt.Sprintf("https://%s-aiplatform.googleapis.com/v1beta1/projects/%s/locations/%s/endpoints/openapi", location, projectID, location)
	config := openai.DefaultConfig(token.AccessToken)
	config.BaseURL = baseURL

	return &MaaSAdapter{
		client: openai.NewClientWithConfig(config),
		model:  modelName,	// The Vertex API expects `<publisher>/<model>`
	// Example: "meta/llama-3.1-70b-instruct-maas", "zai-org/glm-5", "moonshotai/kimi-k2-5"
	}, nil
}

func (m *MaaSAdapter) Name() string {
	return m.model
}

func (m *MaaSAdapter) GenerateContent(ctx context.Context, req *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		// Convert ADK request to OpenAI format
		var messages []openai.ChatCompletionMessage
		for _, content := range req.Contents {
			role := openai.ChatMessageRoleUser
			if content.Role == "model" {
				role = openai.ChatMessageRoleAssistant
			} else if content.Role == "system" {
				role = openai.ChatMessageRoleSystem
			}

			text := ""
			for _, part := range content.Parts {
				if part.Text != "" {
					text += part.Text
				}
			}
			messages = append(messages, openai.ChatCompletionMessage{
				Role:    role,
				Content: text,
			})
		}

		if !stream {
			// Non-streaming call
			resp, err := m.client.CreateChatCompletion(ctx, openai.ChatCompletionRequest{
				Model:    m.model,
				Messages: messages,
			})
			if err != nil {
				yield(nil, err)
				return
			}

			adkResp := &model.LLMResponse{
				Content: &genai.Content{
					Role: "model",
					Parts: []*genai.Part{
						{Text: resp.Choices[0].Message.Content},
					},
				},
				TurnComplete: true,
			}
			yield(adkResp, nil)
			return
		}

		// Streaming call
		streamReq := openai.ChatCompletionRequest{
			Model:    m.model,
			Messages: messages,
			Stream:   true,
		}
		
		streamResp, err := m.client.CreateChatCompletionStream(ctx, streamReq)
		if err != nil {
			yield(nil, err)
			return
		}
		defer streamResp.Close()

		for {
			chunk, err := streamResp.Recv()
			if err == io.EOF {
				break
			}
			if err != nil {
				yield(nil, err)
				return
			}

			if len(chunk.Choices) > 0 {
				adkResp := &model.LLMResponse{
					Content: &genai.Content{
						Role: "model",
						Parts: []*genai.Part{
							{Text: chunk.Choices[0].Delta.Content},
						},
					},
					Partial:      true,
					TurnComplete: chunk.Choices[0].FinishReason != "",
				}
				if !yield(adkResp, nil) {
					break
				}
			}
		}
	}
}