package services

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"qa-extension-backend/internal/models"

	"github.com/google/uuid"
	"google.golang.org/genai"
)

// GenerateTestsForScenario uses Gemini to generate TestRecordings from parsed test cases and codebase context.
// It automatically detects and uses the knowledge graph if available in the codebase.
func GenerateTestsForScenario(
	ctx context.Context,
	testCases []models.ParsedTestCase,
	codebaseCtx *CodebaseContext,
	graph *models.KnowledgeGraph,
	authConfig models.AuthConfig,
) ([]models.TestRecording, []string, error) {

	// We batch test cases (e.g. 5 at a time) to prevent context limits/timeouts
	var allRecordings []models.TestRecording
	var allMissingRoutes []string

	batchSize := 5
	for i := 0; i < len(testCases); i += batchSize {
		end := i + batchSize
		if end > len(testCases) {
			end = len(testCases)
		}
		batch := testCases[i:end]

		var recordings []models.TestRecording
		var missingRoutes []string
		var err error

		if graph != nil {
			recordings, missingRoutes, err = processBatchGraphAware(ctx, batch, codebaseCtx, graph, authConfig)
		} else {
			recordings, err = processBatch(ctx, batch, codebaseCtx, authConfig)
		}

		if err != nil {
			return allRecordings, allMissingRoutes, fmt.Errorf("failed processing batch %d: %w", i/batchSize, err)
		}

		allMissingRoutes = append(allMissingRoutes, missingRoutes...)
		allRecordings = append(allRecordings, recordings...)
	}

	return allRecordings, allMissingRoutes, nil
}

func processBatch(
	ctx context.Context,
	batch []models.ParsedTestCase,
	codebaseCtx *CodebaseContext,
	authConfig models.AuthConfig,
) ([]models.TestRecording, error) {

	prompt := buildPrompt(batch, codebaseCtx, authConfig)
	return generateRecordings(ctx, batch, prompt)
}

func processBatchGraphAware(
	ctx context.Context,
	batch []models.ParsedTestCase,
	codebaseCtx *CodebaseContext,
	graph *models.KnowledgeGraph,
	authConfig models.AuthConfig,
) ([]models.TestRecording, []string, error) {

	prompt := buildGraphAwarePrompt(batch, graph, codebaseCtx, authConfig)

	recordings, err := generateRecordings(ctx, batch, prompt)
	if err != nil {
		return nil, nil, err
	}

	// Collect missing routes
	var missingRoutes []string
	for _, tc := range batch {
		if tc.Route != "" && !graph.HasRoute(tc.Route) {
			missingRoutes = append(missingRoutes, tc.Route)
		}
	}

	return recordings, missingRoutes, nil
}

func generateRecordings(
	ctx context.Context,
	batch []models.ParsedTestCase,
	prompt string,
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

	recordingSchema := &genai.Schema{
		Type: genai.TypeArray,
		Items: &genai.Schema{
			Type: genai.TypeObject,
			Properties: map[string]*genai.Schema{
				"id":          {Type: genai.TypeString},
				"name":        {Type: genai.TypeString},
				"description": {Type: genai.TypeString},
				"steps": {
					Type: genai.TypeArray,
					Items: &genai.Schema{
						Type: genai.TypeObject,
						Properties: map[string]*genai.Schema{
							"action": {
								Type:     genai.TypeString,
								Enum:     []string{"navigate", "click", "type", "press", "assert", "wait"},
							},
							"description": {Type: genai.TypeString},
							"selector":    {Type: genai.TypeString},
							"selectorCandidates": {
								Type:  genai.TypeArray,
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
			
			desc := fmt.Sprintf("[%s] %s", tCase.ID, tCase.Name)
			if tCase.Route != "" {
				desc = fmt.Sprintf("[%s] Route: %s — %s", tCase.ID, tCase.Route, tCase.Name)
			}
			if generatedRecordings[i].Description != "" {
				desc += "\n" + generatedRecordings[i].Description
			}
			generatedRecordings[i].Description = desc
			generatedRecordings[i].Status = "generated"
		}
	}

	return generatedRecordings, nil
}

// buildGraphAwarePrompt builds a prompt using the knowledge graph.
func buildGraphAwarePrompt(
	testCases []models.ParsedTestCase,
	graph *models.KnowledgeGraph,
	codebaseCtx *CodebaseContext,
	authConfig models.AuthConfig,
) string {

	routeContexts := []string{}
	usedRoutes := make(map[string]bool)

	for _, tc := range testCases {
		route := tc.Route
		if route == "" || usedRoutes[route] {
			continue
		}
		usedRoutes[route] = true

		ri, ok := graph.RouteSummary[route]
		if !ok {
			routeContexts = append(routeContexts, fmt.Sprintf(
				"\nRoute: %s\n[WARNING: Route not found in knowledge graph - will use source code context only]\n",
				route,
			))
			continue
		}

		// Build testid list
		testidList := []string{}
		for _, td := range ri.Testids {
			fieldName := "<none>"
			if td.FieldName != nil {
				fieldName = *td.FieldName
			}
			boundAction := "<none>"
			if td.BoundAction != nil {
				boundAction = *td.BoundAction
			}

			testidList = append(testidList, fmt.Sprintf(
				"  - %s (%s, field=%s, action=%s, handler=%s)",
				td.Testid,
				td.ElementType,
				fieldName,
				td.Action,
				boundAction,
			))

			for _, sel := range td.SuggestedSelectors {
				if sel.Confidence >= 0.7 {
					testidList = append(testidList, fmt.Sprintf(
						"    → %s: %s (confidence=%.0f%%)",
						sel.Type, sel.Value, sel.Confidence*100,
					))
				}
			}
		}

		// Form fields
		formFields := []string{}
		for _, form := range ri.Forms {
			formFields = append(formFields, fmt.Sprintf("  Form %s:", form.SchemaName))
			for _, f := range form.Fields {
				req := ""
				if f.Required {
					req = " (required)"
				}
				formFields = append(formFields, fmt.Sprintf("    - %s: %s%s", f.Name, f.Label, req))
			}
		}

		apis := []string{}
		for _, api := range ri.APIs {
			apis = append(apis, fmt.Sprintf("%s %s", api.HTTPMethod, api.Endpoint))
		}

		hooks := []string{}
		for _, hook := range ri.Hooks {
			hooks = append(hooks, fmt.Sprintf("%s (%s)", hook.HookName, hook.HookType))
		}

		formsStr := "None"
		if len(formFields) > 0 {
			formsStr = strings.Join(formFields, "\n")
		}

		apisStr := "None"
		if len(apis) > 0 {
			apisStr = strings.Join(apis, ", ")
		}

		hooksStr := "None"
		if len(hooks) > 0 {
			hooksStr = strings.Join(hooks, ", ")
		}

		ctx := fmt.Sprintf(`Route: %s
Module: %s
Testids (USE THESE ONLY - DO NOT GUESS SELECTORS):
%s
Form Fields:
%s
API Endpoints:
  %s
React Hooks:
  %s`,
			route,
			ri.Module,
			strings.Join(testidList, "\n"),
			formsStr,
			apisStr,
			hooksStr,
		)
		routeContexts = append(routeContexts, ctx)
	}

	tcJSON, _ := json.MarshalIndent(testCases, "", "  ")

	return fmt.Sprintf(`You are a QA automation expert writing Playwright test recordings for a Next.js application.

### INSTRUCTIONS:
1. Generate one TestRecording per Test Case, matching array order.
2. The first steps MUST navigate to Login URL and authenticate using the provided credentials.
   - Base URL: %s
   - Login URL: %s
   - Username: %s
   - Password: %s
3. SELECTOR RULES (CRITICAL):
   - Use ONLY the testids listed in the route context below from the knowledge graph.
   - For each step, pick the selector with HIGHEST confidence from suggestedSelectors.
   - Format: Use Playwright selector format, e.g. [data-testid='foo'], text('Label'), button[type='submit']
   - NEVER guess a selector not listed in the knowledge graph.
   - If a selector is not provided for a testid, use: [data-testid='testid-value']
4. ACTION RULES:
   - For "fill" actions: use the fieldName from the form schema, not the testid.
   - For "submit" buttons: use the testid that corresponds to a Form.Submit or htmlType="submit" button.
   - For "assert" steps: use the expectedValue from the test case's expectedResult.
5. ROUTE CONTEXT (from knowledge graph — USE THIS, NOT the source files):
%s

### SOURCE FILES (for reference only - use knowledge graph above for selectors):
%s

### TEST SCENARIOS:
%s

### RESPONSE FORMAT:
Return ONLY a JSON array of TestRecording objects.`, 
		authConfig.BaseURL, authConfig.LoginURL, authConfig.Username, authConfig.Password,
		strings.Join(routeContexts, "\n\n"),
		formatCodebaseContext(codebaseCtx),
		string(tcJSON))
}

func buildPrompt(batch []models.ParsedTestCase, codebaseCtx *CodebaseContext, authConfig models.AuthConfig) string {
	tcJSON, _ := json.MarshalIndent(batch, "", "  ")

	return fmt.Sprintf(`You are a QA automation expert writing Playwright-ready test blueprints based on test scenarios and the actual application source code.

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
%s

### TEST SCENARIOS:
%s

RETURN ONLY THE JSON ARRAY OF RECORDINGS.`, 
		len(batch), authConfig.BaseURL, authConfig.LoginURL, authConfig.Username, authConfig.Password,
		codebaseCtx.ProjectName,
		formatCodebaseContext(codebaseCtx),
		string(tcJSON))
}

func formatCodebaseContext(codebaseCtx *CodebaseContext) string {
	if codebaseCtx == nil || len(codebaseCtx.Files) == 0 {
		return "No source files available."
	}

	result := fmt.Sprintf("Project: %s (%d files, ~%d tokens)\n\n",
		codebaseCtx.ProjectName, len(codebaseCtx.Files), codebaseCtx.TotalTokens)

	for _, f := range codebaseCtx.Files {
		lines := strings.Split(f.Content, "\n")
		if len(lines) > 200 {
			f.Content = strings.Join(lines[:200], "\n") + "\n... (truncated)"
		}
		result += fmt.Sprintf("============= File: %s =============\n%s\n\n", f.Path, f.Content)
	}

	return result
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
	res := strings.TrimSpace(b.String())
	res = strings.TrimPrefix(res, "'''json")
	res = strings.TrimPrefix(res, "```json")
	res = strings.TrimSuffix(res, "'''")
	res = strings.TrimSuffix(res, "```")
	return res
}


