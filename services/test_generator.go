package services

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"regexp"
	"strings"
	"qa-extension-backend/internal/models"

	"github.com/google/uuid"
	"google.golang.org/genai"
)

// GenerateTestsForScenario uses Gemini to generate automation steps from parsed test cases and module catalog.
// It uses the pre-extracted selectors to ensure all selectors exist in the codebase.
func GenerateTestsForScenario(
	ctx context.Context,
	testCases []models.ParsedTestCase,
	codebaseCtx *CodebaseContext,
	catalog *ModuleCatalog,
	authConfig models.AuthConfig,
) ([]models.GeneratedAutomation, []string, error) {

	// We batch test cases (e.g. 5 at a time) to prevent context limits/timeouts
	var allAutomations []models.GeneratedAutomation
	var allMissingRoutes []string

	batchSize := 5
	for i := 0; i < len(testCases); i += batchSize {
		end := i + batchSize
		if end > len(testCases) {
			end = len(testCases)
		}
		batch := testCases[i:end]

		automations, missingRoutes, err := processBatchWithCatalog(ctx, batch, codebaseCtx, catalog, authConfig)
		if err != nil {
			return allAutomations, allMissingRoutes, fmt.Errorf("failed processing batch %d: %w", i/batchSize, err)
		}

		allMissingRoutes = append(allMissingRoutes, missingRoutes...)
		allAutomations = append(allAutomations, automations...)
	}

	return allAutomations, allMissingRoutes, nil
}

// processBatchWithCatalog generates automation steps using the module catalog with pre-extracted selectors
func processBatchWithCatalog(
	ctx context.Context,
	batch []models.ParsedTestCase,
	codebaseCtx *CodebaseContext,
	catalog *ModuleCatalog,
	authConfig models.AuthConfig,
) ([]models.GeneratedAutomation, []string, error) {

	prompt := buildCatalogAwarePrompt(batch, codebaseCtx, catalog, authConfig)

	automations, err := generateAutomations(ctx, batch, prompt)
	if err != nil {
		return nil, nil, err
	}

	// Validate generated selectors against actual selectors in catalog
	var missingRoutes []string
	for i, rec := range automations {
		automations[i].Steps = validateAndFixSelectors(rec.Steps, catalog)
	}

	return automations, missingRoutes, nil
}

// validateAndFixSelectors checks that each step's selector exists in the catalog
// If a selector doesn't exist, it tries to find a matching selector or marks as warning
func validateAndFixSelectors(steps []models.RecordingStep, catalog *ModuleCatalog) []models.RecordingStep {
	// Build a comprehensive selector map including both raw values and formatted
	validSelectors := buildValidSelectorMap(catalog)
	
	// Track validation stats
	validCount := 0
	fixedCount := 0
	warningCount := 0

	for i, step := range steps {
		if step.Selector == "" {
			continue
		}

		// Extract the selector value from various formats
		selectorValue := extractSelectorValue(step.Selector)
		
		if _, exists := validSelectors[selectorValue]; !exists {
			// Try to find an alternative selector by text matching
			if alternative := findAlternativeSelector(step.Description, validSelectors); alternative != "" {
				log.Printf("[TestGenerator] Step %d: Selector '%s' not found, using alternative '%s'", 
					i+1, step.Selector, alternative)
				steps[i].Selector = alternative
				steps[i].SelectorCandidates = append(steps[i].SelectorCandidates, step.Selector) // Keep original as fallback
				fixedCount++
			} else {
				log.Printf("[TestGenerator] Warning: Step %d selector '%s' not validated against codebase", 
					i+1, step.Selector)
				warningCount++
			}
		} else {
			validCount++
		}
	}

	if warningCount > 0 || fixedCount > 0 {
		log.Printf("[TestGenerator] Selector validation: %d valid, %d fixed, %d warnings", 
			validCount, fixedCount, warningCount)
	}

	return steps
}

// buildValidSelectorMap builds a comprehensive map of all valid selectors
func buildValidSelectorMap(catalog *ModuleCatalog) map[string]string {
	validSelectors := make(map[string]string)
	
	for filePath, selectors := range catalog.Selectors {
		for _, sel := range selectors {
			// Add formatted selectors
			if sel.Testid != "" {
				validSelectors[sel.Testid] = fmt.Sprintf("[data-testid='%s']", sel.Testid)
			}
			if sel.ID != "" {
				validSelectors[sel.ID] = fmt.Sprintf("#%s", sel.ID)
			}
			if sel.Placeholder != "" {
				validSelectors[sel.Placeholder] = fmt.Sprintf("[placeholder='%s']", sel.Placeholder)
			}
			if sel.AriaLabel != "" {
				validSelectors[sel.AriaLabel] = fmt.Sprintf("[aria-label='%s']", sel.AriaLabel)
			}
			if sel.Name != "" {
				validSelectors[sel.Name] = fmt.Sprintf("[name='%s']", sel.Name)
			}
			if sel.Text != "" {
				validSelectors[sel.Text] = fmt.Sprintf("text('%s')", sel.Text)
				validSelectors[strings.ToLower(sel.Text)] = fmt.Sprintf("text('%s')", sel.Text)
			}
			if sel.Title != "" {
				validSelectors[sel.Title] = fmt.Sprintf("[title='%s']", sel.Title)
			}
			if sel.Role != "" {
				validSelectors[sel.Role] = fmt.Sprintf("[role='%s']", sel.Role)
			}
			
			// Also add formatted versions directly
			if sel.Testid != "" {
				validSelectors[fmt.Sprintf("[data-testid='%s']", sel.Testid)] = fmt.Sprintf("[data-testid='%s']", sel.Testid)
			}
			if sel.ID != "" {
				validSelectors[fmt.Sprintf("#%s", sel.ID)] = fmt.Sprintf("#%s", sel.ID)
			}
			if sel.Placeholder != "" {
				validSelectors[fmt.Sprintf("[placeholder='%s']", sel.Placeholder)] = fmt.Sprintf("[placeholder='%s']", sel.Placeholder)
			}
			if sel.Name != "" {
				validSelectors[fmt.Sprintf("[name='%s']", sel.Name)] = fmt.Sprintf("[name='%s']", sel.Name)
			}
			if sel.Text != "" {
				validSelectors[fmt.Sprintf("text('%s')", sel.Text)] = fmt.Sprintf("text('%s')", sel.Text)
			}
		}
		log.Printf("[TestGenerator] Loaded %d selectors from %s", len(selectors), filePath)
	}
	
	return validSelectors
}

// extractSelectorValue extracts the actual value from various Playwright selector formats
func extractSelectorValue(selector string) string {
	// [data-testid='value'] → value
	re := regexp.MustCompile(`\[data-testid=['"]([^'"]+)['"]\]`)
	if matches := re.FindStringSubmatch(selector); len(matches) > 1 {
		return matches[1]
	}
	// #id → id
	re = regexp.MustCompile(`#([a-zA-Z0-9_-]+)`)
	if matches := re.FindStringSubmatch(selector); len(matches) > 1 {
		return matches[1]
	}
	// [placeholder='value'] → value
	re = regexp.MustCompile(`\[placeholder=['"]([^'"]+)['"]\]`)
	if matches := re.FindStringSubmatch(selector); len(matches) > 1 {
		return matches[1]
	}
	// [aria-label='value'] → value
	re = regexp.MustCompile(`\[aria-label=['"]([^'"]+)['"]\]`)
	if matches := re.FindStringSubmatch(selector); len(matches) > 1 {
		return matches[1]
	}
	// [name='value'] → value
	re = regexp.MustCompile(`\[name=['"]([^'"]+)['"]\]`)
	if matches := re.FindStringSubmatch(selector); len(matches) > 1 {
		return matches[1]
	}
	// text('value') or text="value" → value
	re = regexp.MustCompile(`text\(['"]([^'"]+)['"]\)`)
	if matches := re.FindStringSubmatch(selector); len(matches) > 1 {
		return matches[1]
	}
	// button:has-text('value') → value
	re = regexp.MustCompile(`button:has-text\(['"]([^'"]+)['"]\)`)
	if matches := re.FindStringSubmatch(selector); len(matches) > 1 {
		return matches[1]
	}
	// h1:has-text('value') → value (and other element selectors with has-text)
	re = regexp.MustCompile(`\w+:has-text\(['"]([^'"]+)['"]\)`)
	if matches := re.FindStringSubmatch(selector); len(matches) > 1 {
		return matches[1]
	}
	// span:has-text('value') → value
	re = regexp.MustCompile(`span:has-text\(['"]([^'"]+)['"]\)`)
	if matches := re.FindStringSubmatch(selector); len(matches) > 1 {
		return matches[1]
	}
	// th:has-text('value') → value
	re = regexp.MustCompile(`th:has-text\(['"]([^'"]+)['"]\)`)
	if matches := re.FindStringSubmatch(selector); len(matches) > 1 {
		return matches[1]
	}
	// button[aria-label='value'] → value
	re = regexp.MustCompile(`button\[aria-label=['"]([^'"]+)['"]\]`)
	if matches := re.FindStringSubmatch(selector); len(matches) > 1 {
		return matches[1]
	}
	// XPath patterns - (//input[@type='checkbox'])[2] → extract the type
	re = regexp.MustCompile(`\[\d+\]$`) // Trailing [n] in XPath
	if re.FindString(selector) != "" {
		// For XPath selectors, try to extract meaningful content
		if strings.Contains(selector, "checkbox") {
			return "checkbox"
		}
		if strings.Contains(selector, "button") {
			return "button"
		}
	}
	// Just return the original if no pattern matches
	return selector
}

// findAlternativeSelector tries to find a matching selector based on step description and available selectors
func findAlternativeSelector(description string, validSelectors map[string]string) string {
	descLower := strings.ToLower(description)

	// Keywords to match
	keywords := []string{
		"submit", "save", "click", "button", "login", "sign in",
		"search", "input", "field", "email", "password", "name",
		"close", "cancel", "delete", "remove", "add", "create", "new",
		"edit", "update", "confirm", "proceed", "next", "continue",
	}

	for _, keyword := range keywords {
		if strings.Contains(descLower, keyword) {
			// Find selector with matching text
			for selValue, formatted := range validSelectors {
				selLower := strings.ToLower(selValue)
				if strings.Contains(selLower, keyword) {
					return formatted
				}
			}
			// Try partial match
			for selValue, formatted := range validSelectors {
				selLower := strings.ToLower(selValue)
				if len(keyword) >= 4 && strings.Contains(selLower, keyword[:min(len(keyword), 6)]) {
					return formatted
				}
			}
		}
	}

	return ""
}

func generateAutomations(
	ctx context.Context,
	batch []models.ParsedTestCase,
	prompt string,
) ([]models.GeneratedAutomation, error) {

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

	automationSchema := &genai.Schema{
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
								Enum:     []string{"navigate", "click", "type", "press", "assert", "wait", "api_request"},
							},
							"description": {Type: genai.TypeString},
							"selector":    {Type: genai.TypeString},
							"selectorCandidates": {
								Type:  genai.TypeArray,
								Items: &genai.Schema{Type: genai.TypeString},
							},
							"apiMethod":     {Type: genai.TypeString, Enum: []string{"GET", "POST", "PUT", "DELETE", "PATCH"}},
							"apiEndpoint":   {Type: genai.TypeString},
							"apiPayload":    {Type: genai.TypeString},
							"apiHeaders":    {Type: genai.TypeString},
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
		ResponseSchema:   automationSchema,
	}

	resp, err := client.Models.GenerateContent(
		ctx,
		LLMModel,
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

	var generatedAutomations []models.GeneratedAutomation
	err = json.Unmarshal([]byte(resStr), &generatedAutomations)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal gemini response: %w\nResponse was: %s", err, resStr)
	}

	// Link them back to the original parsed cases and add UUIDs/Defaults
	for i, tCase := range batch {
		if i < len(generatedAutomations) {
			generatedAutomations[i].ID = uuid.NewString()
			generatedAutomations[i].TestCaseID = tCase.ID

			desc := fmt.Sprintf("[%s] %s", tCase.ID, tCase.Name)
			if tCase.Route != "" {
				desc = fmt.Sprintf("[%s] Route: %s — %s", tCase.ID, tCase.Route, tCase.Name)
			}
			if generatedAutomations[i].Description != "" {
				desc += "\n" + generatedAutomations[i].Description
			}
			generatedAutomations[i].Description = desc
		}
	}

	return generatedAutomations, nil
}

// buildCatalogAwarePrompt builds a prompt using the module catalog with pre-extracted selectors
// This ensures the LLM only uses selectors that actually exist in the codebase
func buildCatalogAwarePrompt(
	testCases []models.ParsedTestCase,
	codebaseCtx *CodebaseContext,
	catalog *ModuleCatalog,
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

		// Get route info from catalog
		routeEntry := catalog.GetRouteEntry(route)
		if routeEntry == nil {
			routeContexts = append(routeContexts, fmt.Sprintf(
				"\nRoute: %s\n[WARNING: Route not found in module catalog - using source code context only]\n",
				route,
			))
			continue
		}

		// Build selector list from catalog's keyElements
		var selectorList []string
		for semanticName, selectorValue := range routeEntry.KeyElements {
			selectorList = append(selectorList, fmt.Sprintf("  - %s: %s", semanticName, selectorValue))
		}

		// Get all selectors for this file from the selector index
		fileSelectors, ok := catalog.Selectors[routeEntry.FilePath]
		var allSelectors []string
		if ok {
			for _, sel := range fileSelectors {
				allSelectors = append(allSelectors, sel.FormatSelectorForPrompt())
			}
		}

		ctx := fmt.Sprintf(`Route: %s
File: %s
Description: %s
Available Actions: %s

Key Elements (mapped from catalog):
%s

All Available Selectors in this file:
%s`,
			route,
			routeEntry.FilePath,
			routeEntry.Description,
			strings.Join(routeEntry.AvailableActions, ", "),
			strings.Join(selectorList, "\n"),
			strings.Join(allSelectors, "\n"),
		)
		routeContexts = append(routeContexts, ctx)
	}

	tcJSON, _ := json.MarshalIndent(testCases, "", "  ")

	// Build selector summary for reference
	selectorSummary := buildFullSelectorSummary(catalog)

	return fmt.Sprintf(`You are a QA automation expert writing Playwright automation tests for a Next.js application.

### INSTRUCTIONS:
1. Generate one automation test per Test Case, matching array order.
2. The first steps MUST navigate to Login URL and authenticate using the provided credentials.
   - Base URL: %s
   - Login URL: %s
   - Username: %s
   - Password: %s
3. SELECTOR RULES (CRITICAL):
   - Use ONLY selectors from the "Available Selectors" or "All Available Selectors" sections
   - Pick the BEST selector based on:
     * testid > id > aria-label > placeholder > name > text (stability priority)
     * Match the semantic meaning (e.g., "search input" → data-testid='search-box')
   - Format: Use Playwright selector format:
     * [data-testid='value'] - preferred if available
     * #id - for elements with id attribute
     * [aria-label='value'] - for accessible labels
     * [placeholder='value'] - for input placeholders
     * [name='value'] - for form field names
     * text('Label') - for visible text elements
     * button:has-text('Submit') - compound selectors
   - Include "selectorCandidates" as fallback options (e.g., text alternative if using testid)
   - NEVER guess or invent selectors not in the lists below
4. ACTION RULES:
   - For form inputs: use [name='fieldName'] or [placeholder='label']
   - For buttons: use testid, text, or aria-label
   - For assertions: use the selector that best identifies the element to check
5. ROUTE CONTEXT (from module catalog):
%s

### ALL AVAILABLE SELECTORS (use these ONLY):
%s

### SOURCE FILES (for reference):
%s

### TEST SCENARIOS:
%s

### RESPONSE FORMAT:
Return ONLY a JSON array of automation test objects.`, 
		authConfig.BaseURL, authConfig.LoginURL, authConfig.Username, authConfig.Password,
		strings.Join(routeContexts, "\n\n"),
		selectorSummary,
		formatCodebaseContext(codebaseCtx),
		string(tcJSON))
}

// buildFullSelectorSummary creates a comprehensive list of all available selectors
func buildFullSelectorSummary(catalog *ModuleCatalog) string {
	var lines []string
	lines = append(lines, "=== ALL AVAILABLE SELECTORS IN CODEBASE ===")
	lines = append(lines, "")
	
	// Group by file for better organization
	for filePath, selectors := range catalog.Selectors {
		lines = append(lines, fmt.Sprintf("[%s]", filePath))
		
		// Group by selector type
		typeGroups := map[string][]string{
			"testid":     {},
			"id":         {},
			"aria-label": {},
			"placeholder": {},
			"name":      {},
			"text":      {},
		}
		
		for _, sel := range selectors {
			if sel.Testid != "" {
				typeGroups["testid"] = append(typeGroups["testid"], fmt.Sprintf("[data-testid='%s']", sel.Testid))
			}
			if sel.ID != "" {
				typeGroups["id"] = append(typeGroups["id"], fmt.Sprintf("#%s", sel.ID))
			}
			if sel.AriaLabel != "" {
				typeGroups["aria-label"] = append(typeGroups["aria-label"], fmt.Sprintf("[aria-label='%s']", sel.AriaLabel))
			}
			if sel.Placeholder != "" {
				typeGroups["placeholder"] = append(typeGroups["placeholder"], fmt.Sprintf("[placeholder='%s']", sel.Placeholder))
			}
			if sel.Name != "" {
				typeGroups["name"] = append(typeGroups["name"], fmt.Sprintf("[name='%s']", sel.Name))
			}
			if sel.Text != "" {
				typeGroups["text"] = append(typeGroups["text"], fmt.Sprintf("text('%s')", sel.Text))
			}
		}
		
		for groupName, items := range typeGroups {
			if len(items) > 0 {
				lines = append(lines, fmt.Sprintf("  %s: %s", groupName, strings.Join(items, ", ")))
			}
		}
		lines = append(lines, "")
	}
	
	return strings.Join(lines, "\n")
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


