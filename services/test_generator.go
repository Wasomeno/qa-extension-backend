package services

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"qa-extension-backend/models"
	"strings"

	"github.com/google/uuid"
	"google.golang.org/genai"
)

// GenerateTestsForScenario uses Gemini to generate TestRecordings from parsed test cases and codebase context
func GenerateTestsForScenario(
	ctx context.Context,
	testCases []models.ParsedTestCase,
	codebaseCtx *CodebaseContext,
	authConfig models.AuthConfig,
) ([]models.TestRecording, error) {

	projectID := os.Getenv("GOOGLE_CLOUD_PROJECT")
	location := os.Getenv("VERTEX_LOCATION")
	if location == "" {
		location = "us-central1"
	}

	client, err := genai.NewClient(ctx, &genai.ClientConfig{
		Backend:  genai.BackendVertexAI,
		Project:  projectID,
		Location: location,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create genai client: %w", err)
	}

	// We'll batch test cases (e.g. 5 at a time) to prevent context limits/timeouts
	var allRecordings []models.TestRecording

	batchSize := 5
	for i := 0; i < len(testCases); i += batchSize {
		end := i + batchSize
		if end > len(testCases) {
			end = len(testCases)
		}
		batch := testCases[i:end]

		recordings, err := processBatch(ctx, client, batch, codebaseCtx, authConfig)
		if err != nil {
			// Alternatively we could skip and continue, but let's just abort for now or wrap error
			return allRecordings, fmt.Errorf("failed processing batch %d: %w", i/batchSize, err)
		}
		allRecordings = append(allRecordings, recordings...)
	}

	return allRecordings, nil
}

func processBatch(
	ctx context.Context,
	client *genai.Client,
	batch []models.ParsedTestCase,
	codebaseCtx *CodebaseContext,
	authConfig models.AuthConfig,
) ([]models.TestRecording, error) {

	// Prepare the prompt
	prompt := buildPrompt(batch, codebaseCtx, authConfig)

	// We want JSON output directly. Configure Structured Output
	recordingSchema := &genai.Schema{
		Type: genai.TypeArray,
		Items: &genai.Schema{
			Type: genai.TypeObject,
			Properties: map[string]*genai.Schema{
				"id": {
					Type: genai.TypeString,
				},
				"name": {
					Type: genai.TypeString,
				},
				"description": {
					Type: genai.TypeString,
				},
				"steps": {
					Type: genai.TypeArray,
					Items: &genai.Schema{
						Type: genai.TypeObject,
						Properties: map[string]*genai.Schema{
							"action": {
								Type: genai.TypeString,
								Enum: []string{"navigate", "click", "type", "press", "assert", "wait"},
							},
							"description": {Type: genai.TypeString},
							"selector":    {Type: genai.TypeString},
							"selectorCandidates": {
								Type: genai.TypeArray,
								Items: &genai.Schema{Type: genai.TypeString},
							},
							"value":         {Type: genai.TypeString},
							"assertionType": {Type: genai.TypeString},
							"expectedValue": {Type: genai.TypeString},
						},
					},
				},
			},
		},
	}

	config := &genai.GenerateContentConfig{
		Temperature:      genai.Ptr(float32(0.2)),
		ResponseMIMEType: "application/json",
		ResponseSchema:   recordingSchema,
	}

	resp, err := client.Models.GenerateContent(
		ctx,
		"gemini-3.1-flash-lite-preview",
		genai.Text(prompt),
		config,
	)

	if err != nil {
		return nil, fmt.Errorf("gemini API call failed: %w", err)
	}

	resStr := getResponseString(resp)
	if resStr == "" {
		return nil, fmt.Errorf("empty response from gemini")
	}

	var generatedRecordings []models.TestRecording
	err = json.Unmarshal([]byte(resStr), &generatedRecordings)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal gemini response: %w\nResponse was: %s", err, resStr)
	}

	// Link them back to the original parsed cases and add UUIDs/Defaults
	for i, tCase := range batch {
		if i < len(generatedRecordings) {
			generatedRecordings[i].ID = uuid.NewString()
			
			// Always prefix description with the test case ID
			generatedRecordings[i].Description = fmt.Sprintf("[%s] %s\n%s", tCase.ID, tCase.Name, generatedRecordings[i].Description)
			generatedRecordings[i].Status = "generated"
			
			// Optional: we can add `authConfig` elements to the first steps if we want
			// But the prompt instructs the AI to do it. Let's make sure it's valid.
		}
	}

	return generatedRecordings, nil
}

func buildPrompt(batch []models.ParsedTestCase, codebaseCtx *CodebaseContext, authConfig models.AuthConfig) string {
	
	tcJSON, _ := json.MarshalIndent(batch, "", "  ")

	prompt := fmt.Sprintf(`You are a QA automation expert writing Playwright-ready test blueprints based on test scenarios and the actual application source code.

Your task is to convert the following %d Test Scenarios into an array of TestRecording JSON objects.

### INSTRUCTIONS:
1. Generate one TestRecording per Test Scenario, keeping the array order identical.
2. Ensure the first steps in each test navigate to the Login URL and authenticate using the provided credentials.
   - Base URL: %s
   - Login URL: %s
   - Username: %s
   - Password: %s
3. Derive your Selectors from the provided SOURCE CODE context. DO NOT GUESS. Look for "data-testid", "id", or semantic elements (like "button:has-text('Submit')").
4. Include at least 2 "selectorCandidates" (e.g. xpath, css fallback) per interactive step.
5. Use proper "expectedValue" and "assertionType" (e.g., "isVisible", "toHaveText") for steps that are checking results.
6. Allowed actions: "navigate", "click", "type", "press", "assert", "wait". Use "assert" for verification steps.
7. CRITICAL: Respect the "userStory" field for overarching business logic context.
8. CRITICAL: Pay attention to the "testType" field. If it's a "Negative case" or similar, strictly ensure assertions check for expected error messages or validation states.

### SOURCE CODE CONTEXT (%s):
`, len(batch), authConfig.BaseURL, authConfig.LoginURL, authConfig.Username, authConfig.Password, codebaseCtx.ProjectName)

	// Append limited source files
	for _, f := range codebaseCtx.Files {
		prompt += fmt.Sprintf("============= File: %s =============\n%s\n\n", f.Path, f.Content)
	}

	prompt += fmt.Sprintf(`### TEST SCENARIOS TO AUTOMATE:
%s

RETURN ONLY THE JSON ARRAY OF RECORDINGS. DO NOT WRAP WITH MARKDOWN BLOCKS EXCEPT WHAT IS ALLOWED BY THE SCHEMA.`, string(tcJSON))

	return prompt
}

func getResponseString(resp *genai.GenerateContentResponse) string {
	if resp == nil || len(resp.Candidates) == 0 || resp.Candidates[0].Content == nil {
		return ""
	}
	var b strings.Builder
	for _, part := range resp.Candidates[0].Content.Parts {
		if part.Text != "" {
			b.WriteString(part.Text)
		}
	}
	// clean up any potential markdown wraps if structured output acts weirdly across models
	res := strings.TrimSpace(b.String())
	res = strings.TrimPrefix(res, "'''json")
	res = strings.TrimPrefix(res, "```json")
	res = strings.TrimSuffix(res, "'''")
	res = strings.TrimSuffix(res, "```")
	return res
}
