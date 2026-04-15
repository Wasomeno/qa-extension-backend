package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"qa-extension-backend/client"
	"qa-extension-backend/database"
	"qa-extension-backend/internal/models"
	"qa-extension-backend/services"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	gitlab "gitlab.com/gitlab-org/api/client-go"
	"golang.org/x/oauth2"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
)

// AuthConfig stores auth credentials for test generation
type AuthConfig struct {
	BaseURL  string `json:"baseUrl"`
	LoginURL string `json:"loginUrl"`
	Username string `json:"username"`
	Password string `json:"password"`
}

// =============================================================================
// TOOL REGISTRATION
// =============================================================================

// GetTestRecordingTools returns all test recording generation tools for the agent
func GetTestRecordingTools() []tool.Tool {
	tools := []tool.Tool{}

	t1, _ := functiontool.New(functiontool.Config{
		Name:        "analyze_test_case",
		Description: "Analyze a test case to understand its requirements. Returns: target routes, action types, module name, and confidence score.",
	}, analyzeTestCase)
	tools = append(tools, t1)

	t2, _ := functiontool.New(functiontool.Config{
		Name:        "decide_files_to_fetch",
		Description: "Decide which source files need to be fetched based on the target routes.",
	}, decideFilesToFetch)
	tools = append(tools, t2)

	t3, _ := functiontool.New(functiontool.Config{
		Name:        "fetch_source_files",
		Description: "Fetch actual source file content from GitLab.",
	}, fetchSourceFiles)
	tools = append(tools, t3)

	t4, _ := functiontool.New(functiontool.Config{
		Name:        "extract_selectors_from_files",
		Description: "Extract UI selectors from source files.",
	}, extractSelectorsFromFiles)
	tools = append(tools, t4)

	t5, _ := functiontool.New(functiontool.Config{
		Name:        "build_recording_steps",
		Description: "Build recording steps by mapping test case actions to extracted selectors.",
	}, buildRecordingSteps)
	tools = append(tools, t5)

	t6, _ := functiontool.New(functiontool.Config{
		Name:        "generate_recording_for_test_case",
		Description: "Complete pipeline: analyze → decide files → fetch → extract → build. One-shot generation for a single test case.",
	}, generateRecordingForTestCase)
	tools = append(tools, t6)

	t7, _ := functiontool.New(functiontool.Config{
		Name:        "generate_recordings_for_scenario",
		Description: "Generate recordings for ALL test cases in a scenario. Handles batching.",
	}, generateRecordingsForScenario)
	tools = append(tools, t7)

	return tools
}

// =============================================================================
// TOOL 1: ANALYZE TEST CASE
// =============================================================================

type AnalyzeTestCaseInput struct {
	ScenarioID string `json:"scenarioID"`
	TestCaseID string `json:"testCaseID"`
}

type RouteCandidate struct {
	Route      string  `json:"route"`
	Confidence float64 `json:"confidence"`
	Reason     string  `json:"reason"`
}

type AnalyzeTestCaseOutput struct {
	TargetRoutes   []string          `json:"targetRoutes"`
	ModuleName     string            `json:"moduleName"`
	ActionTypes    []string          `json:"actionTypes"`
	Components     []string          `json:"components"`
	Reasoning      string            `json:"reasoning"`
	Confidence     float64           `json:"confidence"`
	Ambiguous      bool              `json:"ambiguous"`
	Candidates     []RouteCandidate  `json:"candidates,omitempty"`
	TestCaseName   string            `json:"testCaseName"`
	PreCondition   string            `json:"preCondition"`
	Steps          []TestStepSummary `json:"steps"`
}

type TestStepSummary struct {
	Index    int    `json:"index"`
	Action   string `json:"action"`
	Input    string `json:"inputData"`
	Expected string `json:"expectedResult"`
}

func analyzeTestCase(ctx tool.Context, input AnalyzeTestCaseInput) (*AnalyzeTestCaseOutput, error) {
	log.Printf("[TestRecordingAgent] analyzeTestCase: scenario=%s, testCase=%s", input.ScenarioID, input.TestCaseID)

	scenario, err := getScenarioFromRedis(input.ScenarioID)
	if err != nil {
		return nil, fmt.Errorf("scenario not found: %w", err)
	}

	var testCase *models.ParsedTestCase
	for _, sheet := range scenario.Sheets {
		for _, tc := range sheet.TestCases {
			if tc.ID == input.TestCaseID || tc.Name == input.TestCaseID {
				testCase = &tc
				break
			}
		}
		if testCase != nil {
			break
		}
	}

	if testCase == nil {
		return nil, fmt.Errorf("test case not found: %s", input.TestCaseID)
	}

	output := &AnalyzeTestCaseOutput{
		TestCaseName: testCase.Name,
		PreCondition: testCase.PreCondition,
		Steps:        make([]TestStepSummary, len(testCase.Steps)),
	}

	actionSet := make(map[string]bool)
	for i, step := range testCase.Steps {
		output.Steps[i] = TestStepSummary{
			Index:    i,
			Action:   step.Action,
			Input:    step.InputData,
			Expected: step.ExpectedResult,
		}

		action := extractActionType(step.Action)
		if action != "" {
			actionSet[action] = true
		}
	}
	for a := range actionSet {
		output.ActionTypes = append(output.ActionTypes, a)
	}

	route := testCase.Route
	if route == "" {
		route = inferRouteFromName(testCase.Name)
	}

	if route != "" {
		parts := strings.Split(strings.TrimPrefix(route, "/"), "/")
		if len(parts) > 0 {
			output.ModuleName = parts[0]
		}

		for _, part := range parts {
			if isRouteSegment(part) {
				continue
			}
			if len(part) > 2 {
				output.Components = append(output.Components, strings.Title(part))
			}
		}

		output.TargetRoutes = []string{route}
		output.Confidence = 0.7
		output.Reasoning = fmt.Sprintf("Route '%s' inferred from test case. Actions: %s",
			route, strings.Join(output.ActionTypes, ", "))
	} else {
		output.Confidence = 0.3
		output.Reasoning = "No route could be determined."
	}

	if scenario.ProjectID != "" {
		catalog, err := getModuleCatalogFromCache(scenario.ProjectID)
		if err == nil && catalog != nil {
			candidates := findRouteCandidatesFromCatalog(catalog, testCase, output)
			if len(candidates) > 1 {
				output.Ambiguous = true
				output.Candidates = candidates
				output.Confidence = candidates[0].Confidence
				output.TargetRoutes = []string{candidates[0].Route}
				output.Reasoning = fmt.Sprintf("Ambiguous route. Best: %s (%.0f%%)", candidates[0].Route, candidates[0].Confidence*100)
			} else if len(candidates) == 1 {
				output.TargetRoutes = []string{candidates[0].Route}
				output.Confidence = candidates[0].Confidence
				output.Reasoning = fmt.Sprintf("Matched route: %s", candidates[0].Route)
			}
		}
	}

	log.Printf("[TestRecordingAgent] Analysis: route=%v, confidence=%.0f%%", output.TargetRoutes, output.Confidence*100)
	return output, nil
}

// =============================================================================
// TOOL 2: DECIDE FILES TO FETCH
// =============================================================================

type DecideFilesInput struct {
	TargetRoutes []string `json:"targetRoutes"`
	ProjectID    string   `json:"projectID"`
	ScenarioID   string   `json:"scenarioID,omitempty"`
}

type DecideFilesOutput struct {
	FilesToFetch []string `json:"filesToFetch"`
	Reasoning    string   `json:"reasoning"`
}

func decideFilesToFetch(ctx tool.Context, input DecideFilesInput) (*DecideFilesOutput, error) {
	log.Printf("[TestRecordingAgent] decideFilesToFetch: routes=%v", input.TargetRoutes)

	output := &DecideFilesOutput{
		FilesToFetch: []string{},
	}

	for _, route := range input.TargetRoutes {
		filePath := routeToFilePath(route)
		output.FilesToFetch = append(output.FilesToFetch, filePath)

		entityName := extractEntityName(route)
		relatedComponents := []string{
			fmt.Sprintf("components/%sForm.tsx", entityName),
			fmt.Sprintf("components/%sTable.tsx", entityName),
			fmt.Sprintf("components/%s.tsx", entityName),
		}
		for _, comp := range relatedComponents {
			output.FilesToFetch = append(output.FilesToFetch, comp)
		}

		parts := strings.Split(strings.TrimPrefix(route, "/"), "/")
		if len(parts) >= 2 {
			for i := 1; i < len(parts); i++ {
				parentRoute := "/" + strings.Join(parts[:i], "/")
				output.FilesToFetch = append(output.FilesToFetch, routeToFilePath(parentRoute))
			}
		}
	}

	seen := make(map[string]bool)
	var unique []string
	for _, f := range output.FilesToFetch {
		if !seen[f] {
			seen[f] = true
			unique = append(unique, f)
		}
	}
	output.FilesToFetch = unique
	output.Reasoning = fmt.Sprintf("Selected %d files", len(unique))

	log.Printf("[TestRecordingAgent] Files to fetch: %v", output.FilesToFetch)
	return output, nil
}

func routeToFilePath(route string) string {
	route = strings.TrimPrefix(route, "/")
	return fmt.Sprintf("app/%s/page.tsx", route)
}

func extractEntityName(route string) string {
	parts := strings.Split(strings.TrimPrefix(route, "/"), "/")
	actionWords := map[string]bool{"create": true, "edit": true, "list": true, "view": true, "delete": true}
	for i := len(parts) - 1; i >= 0; i-- {
		if !actionWords[parts[i]] && parts[i] != "" && !strings.HasPrefix(parts[i], "[") {
			return strings.Title(parts[i])
		}
	}
	return ""
}

// =============================================================================
// TOOL 3: FETCH SOURCE FILES
// =============================================================================

type FetchFilesInput struct {
	ProjectID  string   `json:"projectID"`
	Branch     string   `json:"branch"`
	FilePaths  []string `json:"filePaths"`
	ScenarioID string   `json:"scenarioID,omitempty"`
}

type FetchedFile struct {
	Path      string `json:"path"`
	Content   string `json:"content"`
	FetchedAt int64  `json:"fetchedAt"`
}

type FetchFilesOutput struct {
	Files       []FetchedFile `json:"files"`
	FailedFiles []string      `json:"failedFiles"`
	TotalTokens int           `json:"totalTokens"`
}

func fetchSourceFiles(ctx tool.Context, input FetchFilesInput) (*FetchFilesOutput, error) {
	log.Printf("[TestRecordingAgent] fetchSourceFiles: %d files", len(input.FilePaths))

	output := &FetchFilesOutput{
		Files:       []FetchedFile{},
		FailedFiles: []string{},
	}

	glClient, err := getGitLabClientFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get GitLab client: %w", err)
	}

	branch := input.Branch
	if branch == "" && input.ScenarioID != "" {
		if scenario, err := getScenarioFromRedis(input.ScenarioID); err == nil && scenario.ProjectID != "" {
			if project, _, err := glClient.Projects.GetProject(scenario.ProjectID, nil); err == nil && project != nil {
				branch = project.DefaultBranch
			}
		}
	}
	if branch == "" {
		branch = "main"
	}

	totalTokens := 0
	for _, filePath := range input.FilePaths {
		content, err := fetchSingleFileFromGitLab(glClient, input.ProjectID, branch, filePath)
		if err != nil {
			log.Printf("[TestRecordingAgent] Failed to fetch %s: %v", filePath, err)
			output.FailedFiles = append(output.FailedFiles, filePath)
			continue
		}

		output.Files = append(output.Files, FetchedFile{
			Path:      filePath,
			Content:   content,
			FetchedAt: time.Now().Unix(),
		})
		totalTokens += len(content) / 4
	}

	output.TotalTokens = totalTokens
	log.Printf("[TestRecordingAgent] Fetched %d/%d files, %d tokens", len(output.Files), len(input.FilePaths), output.TotalTokens)
	return output, nil
}

func fetchSingleFileFromGitLab(glClient *gitlab.Client, projectID, branch, filePath string) (string, error) {
	fileOpt := &gitlab.GetFileOptions{Ref: gitlab.Ptr(branch)}
	file, _, err := glClient.RepositoryFiles.GetFile(projectID, filePath, fileOpt)
	if err != nil {
		return "", err
	}

	contentBytes, err := base64.StdEncoding.DecodeString(file.Content)
	if err != nil {
		return "", err
	}

	return string(contentBytes), nil
}

func getGitLabClientFromContext(ctx tool.Context) (*gitlab.Client, error) {
	token, ok := ctx.Value("token").(*oauth2.Token)
	if !ok {
		return nil, fmt.Errorf("unauthorized: missing GitLab token in context")
	}
	return client.GetClient(ctx, token, nil)
}

// =============================================================================
// TOOL 4: EXTRACT SELECTORS
// =============================================================================

type ExtractSelectorsInput struct {
	Files   []FetchedFile `json:"files"`
	Context string        `json:"context"`
}

type SelectorInfo struct {
	Type         string  `json:"type"`
	Value        string  `json:"value"`
	FilePath     string  `json:"filePath"`
	LineNumber   int     `json:"lineNumber"`
	SemanticName string  `json:"semanticName"`
	ElementType  string  `json:"elementType"`
	Confidence   float64 `json:"confidence"`
}

type ExtractSelectorsOutput struct {
	Selectors []SelectorInfo            `json:"selectors"`
	ByFile    map[string][]SelectorInfo `json:"byFile"`
	Warnings  []string                  `json:"warnings"`
}

func extractSelectorsFromFiles(ctx tool.Context, input ExtractSelectorsInput) (*ExtractSelectorsOutput, error) {
	log.Printf("[TestRecordingAgent] extractSelectorsFromFiles: %d files", len(input.Files))

	output := &ExtractSelectorsOutput{
		Selectors: []SelectorInfo{},
		ByFile:    make(map[string][]SelectorInfo),
	}

	for _, file := range input.Files {
		selectors := extractSelectorsFromFile(file.Path, file.Content)
		for i := range selectors {
			selectors[i].SemanticName = inferSemanticName(selectors[i])
			output.Selectors = append(output.Selectors, selectors[i])
		}
		output.ByFile[file.Path] = selectors
		if len(selectors) == 0 {
			output.Warnings = append(output.Warnings, fmt.Sprintf("No selectors in %s", file.Path))
		}
	}

	log.Printf("[TestRecordingAgent] Extracted %d selectors", len(output.Selectors))
	return output, nil
}

func extractSelectorsFromFile(filePath, content string) []SelectorInfo {
	var selectors []SelectorInfo
	lines := strings.Split(content, "\n")

	patterns := map[string]*regexp.Regexp{
		"testid":      regexp.MustCompile(`data-testid\s*=\s*["']([^"']+)["']`),
		"id":          regexp.MustCompile(`\bid\s*=\s*["']([^"']+)["']`),
		"placeholder": regexp.MustCompile(`placeholder\s*=\s*["']([^"']+)["']`),
		"aria-label":  regexp.MustCompile(`aria-label\s*=\s*["']([^"']+)["']`),
		"name":        regexp.MustCompile(`\bname\s*=\s*["']([^"']+)["']`),
		"role":        regexp.MustCompile(`\brole\s*=\s*["']([^"']+)["']`),
		"title":       regexp.MustCompile(`\btitle\s*=\s*["']([^"']+)["']`),
	}

	elementTypes := []string{"button", "input", "div", "span", "a", "form", "table", "tr", "td", "th", "ul", "li", "label", "select", "textarea"}

	for i, line := range lines {
		lineNum := i + 1
		if strings.Contains(line, "//") || strings.Contains(line, "/*") {
			continue
		}
		if !strings.Contains(line, "<") {
			continue
		}

		elementType := ""
		for _, et := range elementTypes {
			tagPattern := regexp.MustCompile(fmt.Sprintf(`<(Button|Input|%s)[^>]*[\s>]`, strings.Title(et)))
			if tagPattern.MatchString(line) {
				elementType = et
				break
			}
			simplePattern := regexp.MustCompile(fmt.Sprintf(`<%s[\s>]`, et))
			if simplePattern.MatchString(line) {
				elementType = et
				break
			}
		}

		for selType, re := range patterns {
			matches := re.FindAllStringSubmatch(line, -1)
			for _, match := range matches {
				if len(match) > 1 {
					selectors = append(selectors, SelectorInfo{
						Type:         selType,
						Value:        match[1],
						FilePath:     filePath,
						LineNumber:   lineNum,
						ElementType:  elementType,
						Confidence:   calculateSelectorConfidence(selType, match[1]),
					})
				}
			}
		}

		textMatch := regexp.MustCompile(`>([^<]+)<`).FindStringSubmatch(line)
		if len(textMatch) > 1 {
			text := strings.TrimSpace(textMatch[1])
			if len(text) > 1 && !strings.Contains(text, "{") {
				selectors = append(selectors, SelectorInfo{
					Type:        "text",
					Value:       text,
					FilePath:    filePath,
					LineNumber:  lineNum,
					ElementType: elementType,
					Confidence:  0.5,
				})
			}
		}
	}

	return selectors
}

func calculateSelectorConfidence(selType, value string) float64 {
	baseConfidence := map[string]float64{
		"testid": 0.95, "id": 0.85, "aria-label": 0.8,
		"name": 0.75, "placeholder": 0.7, "role": 0.65, "title": 0.6, "text": 0.5,
	}

	confidence := baseConfidence[selType]
	if confidence == 0 {
		confidence = 0.5
	}

	semanticWords := []string{"btn", "button", "submit", "cancel", "save", "input", "field", "select", "dropdown", "modal", "dialog", "form", "search", "filter"}
	valueLower := strings.ToLower(value)
	for _, word := range semanticWords {
		if strings.Contains(valueLower, word) {
			confidence += 0.05
		}
	}

	if confidence > 1.0 {
		confidence = 1.0
	}

	return confidence
}

func inferSemanticName(selector SelectorInfo) string {
	value := strings.ToLower(selector.Value)
	parts := strings.FieldsFunc(value, func(r rune) bool { return r == '-' || r == '_' || r == ' ' })

	var semanticParts []string
	for _, part := range parts {
		switch part {
		case "btn", "button":
			semanticParts = append(semanticParts, "button")
		case "input", "field", "txt":
			semanticParts = append(semanticParts, "input")
		case "submit", "save", "cancel", "delete", "edit", "create":
			semanticParts = append(semanticParts, part)
		case "form":
			semanticParts = append(semanticParts, "form")
		case "select", "dropdown":
			semanticParts = append(semanticParts, "dropdown")
		case "modal", "dialog":
			semanticParts = append(semanticParts, "modal")
		default:
			if len(part) > 2 {
				semanticParts = append(semanticParts, part)
			}
		}
	}

	if len(semanticParts) == 0 {
		return fmt.Sprintf("%s %s", selector.ElementType, selector.Type)
	}

	return strings.Join(semanticParts, " ")
}

// =============================================================================
// TOOL 5: BUILD RECORDING STEPS
// =============================================================================

type BuildRecordingInput struct {
	TestCaseID string         `json:"testCaseID"`
	ScenarioID string         `json:"scenarioID"`
	Selectors  []SelectorInfo `json:"selectors"`
	AuthConfig AuthConfig     `json:"authConfig"`
	ProjectID  string         `json:"projectID"`
}

type BuildRecordingOutput struct {
	Recording *models.TestRecording `json:"recording"`
	Warnings  []string              `json:"warnings"`
	Issues    []string              `json:"issues"`
}

func buildRecordingSteps(ctx tool.Context, input BuildRecordingInput) (*BuildRecordingOutput, error) {
	log.Printf("[TestRecordingAgent] buildRecordingSteps: testCase=%s, selectors=%d", input.TestCaseID, len(input.Selectors))

	output := &BuildRecordingOutput{
		Recording: &models.TestRecording{
			ID:     uuid.NewString(),
			Status: "generated",
			Steps:  []models.RecordingStep{},
		},
		Warnings: []string{},
		Issues:   []string{},
	}

	scenario, err := getScenarioFromRedis(input.ScenarioID)
	if err != nil {
		return nil, fmt.Errorf("scenario not found: %w", err)
	}

	var testCase *models.ParsedTestCase
	for _, sheet := range scenario.Sheets {
		for _, tc := range sheet.TestCases {
			if tc.ID == input.TestCaseID || tc.Name == input.TestCaseID {
				testCase = &tc
				break
			}
		}
		if testCase != nil {
			break
		}
	}

	if testCase == nil {
		return nil, fmt.Errorf("test case not found: %s", input.TestCaseID)
	}

	selectorMap := buildSelectorMap(input.Selectors)

	if input.AuthConfig.LoginURL != "" {
		output.Recording.Steps = append(output.Recording.Steps,
			models.RecordingStep{Action: "navigate", Description: "Navigate to login page", Value: input.AuthConfig.LoginURL},
			models.RecordingStep{Action: "type", Description: "Enter username", Selector: findSelectorByKeyword(selectorMap, "username", "email", "user"), Value: input.AuthConfig.Username},
			models.RecordingStep{Action: "type", Description: "Enter password", Selector: findSelectorByKeyword(selectorMap, "password"), Value: input.AuthConfig.Password},
			models.RecordingStep{Action: "click", Description: "Click login button", Selector: findSelectorByKeyword(selectorMap, "login", "submit", "signin")},
		)
	}

	if testCase.Route != "" {
		baseURL := input.AuthConfig.BaseURL
		if baseURL == "" {
			baseURL = "https://app.example.com"
		}
		output.Recording.Steps = append(output.Recording.Steps,
			models.RecordingStep{Action: "navigate", Description: fmt.Sprintf("Navigate to %s", testCase.Route), Value: baseURL + testCase.Route},
		)
	}

	for i, step := range testCase.Steps {
		action := extractActionType(step.Action)
		if action == "" {
			action = "click"
		}

		recordingStep := models.RecordingStep{
			Action:      action,
			Description: step.Action,
			Value:       step.InputData,
		}

		selector := findSelectorForAction(selectorMap, action, step.Action, step.InputData)
		if selector != "" {
			recordingStep.Selector = selector
			// Also populate elementHints, xpath, xpathCandidates from selector
			if sel := getBestSelectorFromMap(selectorMap, action, step.Action, step.InputData); sel != nil {
				recordingStep.ElementHints = sel.ToElementHints()
				recordingStep.XPath = sel.ToXPath()
				recordingStep.XPathCandidates = sel.ToXPathCandidates()
			}
		} else {
			output.Warnings = append(output.Warnings, fmt.Sprintf("Step %d: No selector for '%s'", i+1, step.Action))
		}

		if action == "assert" || strings.Contains(strings.ToLower(step.Action), "verify") || strings.Contains(strings.ToLower(step.Action), "check") {
			recordingStep.AssertionType = "visible"
			recordingStep.ExpectedValue = step.ExpectedResult
		}

		output.Recording.Steps = append(output.Recording.Steps, recordingStep)
	}

	output.Recording.Name = fmt.Sprintf("[%s] %s", testCase.ID, testCase.Name)
	output.Recording.Description = fmt.Sprintf("Auto-generated from %s. %s", input.ScenarioID, testCase.PreCondition)
	output.Recording.ProjectID = input.ProjectID

	log.Printf("[TestRecordingAgent] Built recording with %d steps", len(output.Recording.Steps))
	return output, nil
}

func getBestSelectorFromMap(m map[string][]SelectorInfo, action, description, value string) *SelectorInfo {
	descLower := strings.ToLower(description)
	valueLower := strings.ToLower(value)

	var best *SelectorInfo

	if action == "type" {
		for _, kw := range []string{"input", "field", "text"} {
			if selectors, ok := m[kw]; ok {
				for i := range selectors {
					if selectors[i].Type == "testid" || selectors[i].Type == "name" || selectors[i].Type == "placeholder" {
						if best == nil || selectors[i].Confidence > best.Confidence {
							best = &selectors[i]
						}
					}
				}
			}
		}
		if best == nil && valueLower != "" {
			parts := strings.FieldsFunc(valueLower, func(r rune) bool { return r == ' ' || r == '@' || r == '.' })
			if len(parts) > 0 {
				if sel := findBestSelectorByKeyword(m, parts[0]); sel != nil {
					best = sel
				}
			}
		}
	}

	if action == "click" {
		clickKeywords := []string{"button", "submit", "save", "cancel", "delete", "edit", "create", "add", "select", "click"}
		for _, kw := range clickKeywords {
			if selectors, ok := m[kw]; ok {
				for i := range selectors {
					if selectors[i].Type == "testid" {
						if best == nil || selectors[i].Confidence > best.Confidence {
							best = &selectors[i]
						}
					}
					if selectors[i].Type == "text" && strings.ToLower(selectors[i].Value) == descLower {
						if best == nil || selectors[i].Confidence > best.Confidence {
							best = &selectors[i]
						}
					}
				}
			}
		}
	}

	// Fallback to any high-confidence selector
	if best == nil {
		for _, selectors := range m {
			for i := range selectors {
				sel := &selectors[i]
				if sel.Confidence >= 0.7 {
					if best == nil || sel.Confidence > best.Confidence {
						best = sel
					}
				}
			}
		}
	}

	return best
}

func findBestSelectorByKeyword(m map[string][]SelectorInfo, keyword string) *SelectorInfo {
	kwLower := strings.ToLower(keyword)
	if selectors, ok := m[kwLower]; ok {
		var best *SelectorInfo
		for i := range selectors {
			if best == nil || selectors[i].Confidence > best.Confidence {
				best = &selectors[i]
			}
		}
		return best
	}
	return nil
}

func buildSelectorMap(selectors []SelectorInfo) map[string][]SelectorInfo {
	m := make(map[string][]SelectorInfo)
	for _, sel := range selectors {
		keywords := strings.FieldsFunc(strings.ToLower(sel.SemanticName), func(r rune) bool {
			return r == ' ' || r == '-' || r == '_'
		})
		for _, kw := range keywords {
			m[kw] = append(m[kw], sel)
		}
		m[sel.Type] = append(m[sel.Type], sel)
		valueKeywords := strings.FieldsFunc(strings.ToLower(sel.Value), func(r rune) bool {
			return r == '-' || r == '_'
		})
		for _, kw := range valueKeywords {
			m[kw] = append(m[kw], sel)
		}
	}
	return m
}

func findSelectorByKeyword(m map[string][]SelectorInfo, keywords ...string) string {
	for _, kw := range keywords {
		kwLower := strings.ToLower(kw)
		if selectors, ok := m[kwLower]; ok {
			var best *SelectorInfo
			for i := range selectors {
				if best == nil || selectors[i].Confidence > best.Confidence {
					best = &selectors[i]
				}
			}
			if best != nil {
				return best.ToPlaywrightSelector()
			}
		}
	}
	return ""
}

func findSelectorForAction(m map[string][]SelectorInfo, action, description, value string) string {
	descLower := strings.ToLower(description)
	valueLower := strings.ToLower(value)

	if action == "type" {
		for _, kw := range []string{"input", "field", "text"} {
			if selectors, ok := m[kw]; ok {
				for _, sel := range selectors {
					if sel.Type == "testid" || sel.Type == "name" || sel.Type == "placeholder" {
						return sel.ToPlaywrightSelector()
					}
				}
			}
		}
		if valueLower != "" {
			parts := strings.FieldsFunc(valueLower, func(r rune) bool { return r == ' ' || r == '@' || r == '.' })
			if len(parts) > 0 {
				return findSelectorByKeyword(m, parts[0])
			}
		}
	}

	if action == "click" {
		clickKeywords := []string{"button", "submit", "save", "cancel", "delete", "edit", "create", "add", "select", "click"}
		for _, kw := range clickKeywords {
			if selectors, ok := m[kw]; ok {
				for _, sel := range selectors {
					if sel.Type == "testid" {
						return sel.ToPlaywrightSelector()
					}
					if sel.Type == "text" && strings.ToLower(sel.Value) == descLower {
						return sel.ToPlaywrightSelector()
					}
				}
			}
		}
	}

	var best *SelectorInfo
	for _, selectors := range m {
		for i := range selectors {
			sel := &selectors[i]
			if sel.Confidence >= 0.7 {
				if best == nil || sel.Confidence > best.Confidence {
					best = sel
				}
			}
		}
	}

	if best != nil {
		return best.ToPlaywrightSelector()
	}

	return ""
}

func (s *SelectorInfo) ToPlaywrightSelector() string {
	return s.ToCSSSelector()
}

func (s *SelectorInfo) ToCSSSelector() string {
	switch s.Type {
	case "testid":
		return fmt.Sprintf("[data-testid='%s']", s.Value)
	case "id":
		return fmt.Sprintf("#%s", s.Value)
	case "placeholder":
		return fmt.Sprintf("[placeholder='%s']", s.Value)
	case "aria-label":
		return fmt.Sprintf("[aria-label='%s']", s.Value)
	case "name":
		return fmt.Sprintf("[name='%s']", s.Value)
	case "role":
		return fmt.Sprintf("[role='%s']", s.Value)
	case "title":
		return fmt.Sprintf("[title='%s']", s.Value)
	case "text":
		return fmt.Sprintf("text('%s')", s.Value)
	default:
		return fmt.Sprintf("[%s='%s']", s.Type, s.Value)
	}
}

// ToElementHints converts SelectorInfo to ElementHints for a RecordingStep
func (s *SelectorInfo) ToElementHints() models.ElementHints {
	attrs := make(map[string]string)
	switch s.Type {
	case "testid":
		attrs["data-testid"] = s.Value
	case "id":
		attrs["id"] = s.Value
	case "name":
		attrs["name"] = s.Value
	case "placeholder":
		attrs["placeholder"] = s.Value
	case "aria-label":
		attrs["aria-label"] = s.Value
	case "role":
		attrs["role"] = s.Value
	}
	return models.ElementHints{
		Attributes: attrs,
		TagName:    s.ElementType,
	}
}

// ToSelectorCandidates generates alternative CSS selectors for the same element
func (s *SelectorInfo) ToSelectorCandidates() []string {
	var candidates []string

	// Always include the primary selector
	candidates = append(candidates, s.ToCSSSelector())

	// Generate alternatives based on available info
	switch s.Type {
	case "testid":
		candidates = append(candidates, fmt.Sprintf("[data-testid='%s']", s.Value))
		if s.ElementType != "" {
			candidates = append(candidates, fmt.Sprintf("%s[data-testid='%s']", s.ElementType, s.Value))
		}
	case "id":
		candidates = append(candidates, fmt.Sprintf("#%s", s.Value))
		candidates = append(candidates, fmt.Sprintf("*[id='%s']", s.Value))
	case "name":
		candidates = append(candidates, fmt.Sprintf("[name='%s']", s.Value))
		candidates = append(candidates, fmt.Sprintf("input[name='%s']", s.Value))
		candidates = append(candidates, fmt.Sprintf("%s[name='%s']", s.ElementType, s.Value))
	case "placeholder":
		candidates = append(candidates, fmt.Sprintf("[placeholder='%s']", s.Value))
		candidates = append(candidates, fmt.Sprintf("input[placeholder='%s']", s.Value))
		candidates = append(candidates, fmt.Sprintf("textarea[placeholder='%s']", s.Value))
	case "aria-label":
		candidates = append(candidates, fmt.Sprintf("[aria-label='%s']", s.Value))
		candidates = append(candidates, fmt.Sprintf("%s[aria-label='%s']", s.ElementType, s.Value))
	case "role":
		candidates = append(candidates, fmt.Sprintf("[role='%s']", s.Value))
		candidates = append(candidates, fmt.Sprintf("%s[role='%s']", s.ElementType, s.Value))
	case "text":
		candidates = append(candidates, fmt.Sprintf("text('%s')", s.Value))
		candidates = append(candidates, fmt.Sprintf("*:text('%s')", s.Value))
		candidates = append(candidates, fmt.Sprintf("%s:has-text('%s')", s.ElementType, s.Value))
	}

	// Deduplicate
	seen := make(map[string]bool)
	var unique []string
	for _, c := range candidates {
		if !seen[c] {
			seen[c] = true
			unique = append(unique, c)
		}
	}

	return unique
}

// ToXPath generates a primary XPath selector
func (s *SelectorInfo) ToXPath() string {
	switch s.Type {
	case "testid":
		return fmt.Sprintf("//*[@data-testid='%s']", s.Value)
	case "id":
		return fmt.Sprintf("//*[@id='%s']", s.Value)
	case "name":
		return fmt.Sprintf("//*[@name='%s']", s.Value)
	case "placeholder":
		return fmt.Sprintf("//*[@placeholder='%s']", s.Value)
	case "aria-label":
		return fmt.Sprintf("//*[@aria-label='%s']", s.Value)
	case "role":
		return fmt.Sprintf("//*[@role='%s']", s.Value)
	case "text":
		return fmt.Sprintf("//*[normalize-space(.)='%s']", s.Value)
	default:
		return fmt.Sprintf("//*[@%s='%s']", s.Type, s.Value)
	}
}

// ToXPathCandidates generates alternative XPath selectors
func (s *SelectorInfo) ToXPathCandidates() []string {
	var candidates []string

	// Primary XPath
	candidates = append(candidates, s.ToXPath())

	// Generate alternatives
	switch s.Type {
	case "testid":
		candidates = append(candidates, fmt.Sprintf("//*[@data-testid='%s']", s.Value))
		if s.ElementType != "" {
			candidates = append(candidates, fmt.Sprintf("//%s[@data-testid='%s']", s.ElementType, s.Value))
		}
	case "id":
		candidates = append(candidates, fmt.Sprintf("//*[@id='%s']", s.Value))
		candidates = append(candidates, fmt.Sprintf("//input[@id='%s']", s.Value))
		candidates = append(candidates, fmt.Sprintf("//button[@id='%s']", s.Value))
	case "name":
		candidates = append(candidates, fmt.Sprintf("//*[@name='%s']", s.Value))
		candidates = append(candidates, fmt.Sprintf("//input[@name='%s']", s.Value))
		candidates = append(candidates, fmt.Sprintf("//textarea[@name='%s']", s.Value))
	case "aria-label":
		candidates = append(candidates, fmt.Sprintf("//*[@aria-label='%s']", s.Value))
		candidates = append(candidates, fmt.Sprintf("//*[@role and @aria-label='%s']", s.Value))
	case "role":
		candidates = append(candidates, fmt.Sprintf("//*[@role='%s']", s.Value))
		candidates = append(candidates, fmt.Sprintf("//button[@role='%s']", s.Value))
		candidates = append(candidates, fmt.Sprintf("//a[@role='%s']", s.Value))
	case "text":
		candidates = append(candidates, fmt.Sprintf("//*[normalize-space(.)='%s']", s.Value))
		candidates = append(candidates, fmt.Sprintf("//button[normalize-space(.)='%s']", s.Value))
		candidates = append(candidates, fmt.Sprintf("//a[normalize-space(.)='%s']", s.Value))
		candidates = append(candidates, fmt.Sprintf("//span[normalize-space(.)='%s']", s.Value))
	}

	// Deduplicate
	seen := make(map[string]bool)
	var unique []string
	for _, c := range candidates {
		if !seen[c] {
			seen[c] = true
			unique = append(unique, c)
		}
	}

	return unique
}

// =============================================================================
// TOOL 6: GENERATE RECORDING FOR TEST CASE (Full Pipeline)
// =============================================================================

type GenerateRecordingInput struct {
	ScenarioID string     `json:"scenarioID"`
	TestCaseID string     `json:"testCaseID"`
	AuthConfig AuthConfig `json:"authConfig,omitempty"`
	ProjectID  string     `json:"projectID,omitempty"`
}

type GenerateRecordingOutput struct {
	Recording  *models.TestRecording `json:"recording"`
	Warnings   []string              `json:"warnings"`
	Issues     []string              `json:"issues"`
	Confidence float64               `json:"confidence"`
	StepsUsed  int                   `json:"stepsUsed"`
}

func generateRecordingForTestCase(ctx tool.Context, input GenerateRecordingInput) (*GenerateRecordingOutput, error) {
	log.Printf("[TestRecordingAgent] generateRecordingForTestCase: %s / %s", input.ScenarioID, input.TestCaseID)

	// Step 1: Analyze
	analysis, err := analyzeTestCase(ctx, AnalyzeTestCaseInput{
		ScenarioID: input.ScenarioID,
		TestCaseID: input.TestCaseID,
	})
	if err != nil {
		return nil, fmt.Errorf("analysis failed: %w", err)
	}

	// Step 2: Decide files
	var filesOutput *DecideFilesOutput
	if len(analysis.TargetRoutes) > 0 {
		filesOutput, err = decideFilesToFetch(ctx, DecideFilesInput{
			TargetRoutes: analysis.TargetRoutes,
			ProjectID:    input.ProjectID,
			ScenarioID:   input.ScenarioID,
		})
		if err != nil {
			return nil, fmt.Errorf("file decision failed: %w", err)
		}
	}

	// Step 3: Fetch
	var fetchOutput *FetchFilesOutput
	if filesOutput != nil && len(filesOutput.FilesToFetch) > 0 {
		fetchOutput, err = fetchSourceFiles(ctx, FetchFilesInput{
			ProjectID:  input.ProjectID,
			ScenarioID: input.ScenarioID,
			FilePaths:  filesOutput.FilesToFetch,
		})
		if err != nil {
			return nil, fmt.Errorf("file fetch failed: %w", err)
		}
	}

	// Step 4: Extract selectors
	var extractOutput *ExtractSelectorsOutput
	if fetchOutput != nil && len(fetchOutput.Files) > 0 {
		extractOutput, err = extractSelectorsFromFiles(ctx, ExtractSelectorsInput{
			Files:   fetchOutput.Files,
			Context: analysis.ModuleName,
		})
		if err != nil {
			return nil, fmt.Errorf("selector extraction failed: %w", err)
		}
	}

	// Step 5: Build recording
	if input.AuthConfig.LoginURL == "" {
		if scenario, err := getScenarioFromRedis(input.ScenarioID); err == nil {
			input.AuthConfig = AuthConfig{
				BaseURL:  scenario.AuthConfig.BaseURL,
				LoginURL: scenario.AuthConfig.LoginURL,
				Username: scenario.AuthConfig.Username,
				Password: scenario.AuthConfig.Password,
			}
		}
	}

	var selectors []SelectorInfo
	if extractOutput != nil {
		selectors = extractOutput.Selectors
	}

	buildOutput, err := buildRecordingSteps(ctx, BuildRecordingInput{
		TestCaseID: input.TestCaseID,
		ScenarioID: input.ScenarioID,
		Selectors:  selectors,
		AuthConfig: input.AuthConfig,
		ProjectID:  input.ProjectID,
	})
	if err != nil {
		return nil, fmt.Errorf("recording build failed: %w", err)
	}

	allWarnings := buildOutput.Warnings
	if extractOutput != nil {
		allWarnings = append(allWarnings, extractOutput.Warnings...)
	}

	issues := []string{}
	for i, step := range buildOutput.Recording.Steps {
		if step.Action != "navigate" && step.Selector == "" {
			issues = append(issues, fmt.Sprintf("Step %d ('%s') has no selector", i+1, step.Description))
		}
	}

	log.Printf("[TestRecordingAgent] Generated recording with %d steps, %d warnings", len(buildOutput.Recording.Steps), len(allWarnings))

	return &GenerateRecordingOutput{
		Recording:  buildOutput.Recording,
		Warnings:   allWarnings,
		Issues:     issues,
		Confidence: analysis.Confidence,
		StepsUsed:  len(buildOutput.Recording.Steps),
	}, nil
}

// =============================================================================
// TOOL 7: GENERATE RECORDINGS FOR SCENARIO (Batch Processing)
// =============================================================================

type GenerateRecordingsInput struct {
	ScenarioID  string     `json:"scenarioID"`
	SheetNames  []string   `json:"sheetNames,omitempty"`
	TestCaseIDs []string   `json:"testCaseIDs,omitempty"`
	AuthConfig  AuthConfig `json:"authConfig,omitempty"`
	ProjectID   string     `json:"projectID,omitempty"`
}

type GenerateRecordingsOutput struct {
	Recordings   []models.TestRecording `json:"recordings"`
	FailedIDs    []string               `json:"failedIDs"`
	TotalCount   int                     `json:"totalCount"`
	SuccessCount int                     `json:"successCount"`
	Warnings     []string                `json:"warnings"`
}

// =============================================================================
// AGENT-BASED TEST RECORDING GENERATION
// The agent uses GitLab tools to navigate repo and find files
// =============================================================================

// TestRecordingAgentInput is the prompt/input for the agent to generate recordings
type TestRecordingAgentInput struct {
	ScenarioID string   `json:"scenarioID"`
	SheetNames []string `json:"sheetNames,omitempty"`
}

// RunAgentForTestGeneration runs the QA agent to generate recordings
// The agent will use GitLab tools to navigate repo and find files
func RunAgentForTestGeneration(ctx context.Context, input TestRecordingAgentInput) (*GenerateRecordingsOutput, error) {
	log.Printf("[TestRecordingAgent] RunAgentForTestGeneration: %s", input.ScenarioID)

	// Get scenario from Redis
	scenario, err := getScenarioFromRedis(input.ScenarioID)
	if err != nil {
		return nil, fmt.Errorf("scenario not found: %w", err)
	}

	// Run the agent - it will use its GitLab tools to:
	// 1. Get project ID from scenario
	// 2. List repo tree to find routes
	// 3. Get files for relevant pages
	// 4. Extract selectors
	// 5. Build recordings
	
	// For now, use direct tool calls mimicking what the agent would do
	output := &GenerateRecordingsOutput{
		Recordings: []models.TestRecording{},
		FailedIDs:  []string{},
		Warnings:   []string{},
	}

	// Collect test cases to process
	var targetCases []*models.ParsedTestCase
	for _, sheet := range scenario.Sheets {
		if len(input.SheetNames) > 0 {
			found := false
			for _, sn := range input.SheetNames {
				if sheet.Name == sn {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}
		for i := range sheet.TestCases {
			targetCases = append(targetCases, &sheet.TestCases[i])
		}
	}

	output.TotalCount = len(targetCases)
	log.Printf("[TestRecordingAgent] Processing %d test cases", output.TotalCount)

	// Get GitLab client from scenario's project
	glClient, err := getGitLabClientFromScenarioProject(ctx, scenario)
	if err != nil {
		log.Printf("[TestRecordingAgent] Failed to get GitLab client: %v", err)
		// Continue anyway - recordings will have no selectors
	}

	var branch string
	if glClient != nil && scenario.ProjectID != "" {
		if project, _, err := glClient.Projects.GetProject(scenario.ProjectID, nil); err == nil {
			branch = project.DefaultBranch
		}
	}
	if branch == "" {
		branch = "main"
	}

	// For each test case, let the agent figure out what to fetch
	// Agent will look at test case name → infer what files are needed
	for _, tc := range targetCases {
		recording, warnings, err := agentGenerateSingleRecording(ctx, glClient, scenario.ProjectID, branch, tc)
		if err != nil {
			log.Printf("[TestRecordingAgent] Failed for %s: %v", tc.ID, err)
			output.FailedIDs = append(output.FailedIDs, tc.ID)
			continue
		}

		output.Recordings = append(output.Recordings, *recording)
		output.SuccessCount++
		output.Warnings = append(output.Warnings, warnings...)
	}

	log.Printf("[TestRecordingAgent] Completed: %d/%d recordings", output.SuccessCount, output.TotalCount)
	return output, nil
}

// fetchSingleFileWithClient fetches a single file using provided GitLab client
func fetchSingleFileWithClient(glClient *gitlab.Client, projectID, branch, filePath string) (string, error) {
	fileOpt := &gitlab.GetFileOptions{Ref: gitlab.Ptr(branch)}
	file, _, err := glClient.RepositoryFiles.GetFile(projectID, filePath, fileOpt)
	if err != nil {
		return "", err
	}

	contentBytes, err := base64.StdEncoding.DecodeString(file.Content)
	if err != nil {
		return "", err
	}

	return string(contentBytes), nil
}

// agentGenerateSingleRecording uses GitLab tools to figure out what files to fetch
// based on the test case steps content
func agentGenerateSingleRecording(ctx context.Context, glClient *gitlab.Client, projectID, branch string, tc *models.ParsedTestCase) (*models.TestRecording, []string, error) {
	log.Printf("[TestRecordingAgent] agentGenerateSingleRecording: %s - %s", tc.ID, tc.Name)

	warnings := []string{}
	recording := &models.TestRecording{
		ID:     uuid.NewString(),
		Status: "generated",
		Steps:  []models.RecordingStep{},
		Name:   fmt.Sprintf("[%s] %s", tc.ID, tc.Name),
		Description: tc.PreCondition,
		ProjectID: projectID,
	}

	// Build context from ALL step content - let the agent figure out what files are needed
	stepContext := buildStepContext(tc.Steps)
	log.Printf("[TestRecordingAgent] Step context for %s: %s", tc.ID, stepContext)

	// Use test case name + steps content to find relevant files in repo
	fetchedFiles, fetchWarnings := agentFetchRelevantFilesFromContext(ctx, glClient, projectID, branch, tc.Name, stepContext)
	warnings = append(warnings, fetchWarnings...)

	// Agent extracts selectors from fetched files
	var selectors []SelectorInfo
	for _, file := range fetchedFiles {
		extracted := extractSelectorsFromFile(file.Path, file.Content)
		for i := range extracted {
			extracted[i].SemanticName = inferSemanticName(extracted[i])
		}
		selectors = append(selectors, extracted...)
	}
	if len(selectors) == 0 {
		warnings = append(warnings, fmt.Sprintf("No selectors found for %s", tc.Name))
	}

	// Build selector map
	selectorMap := buildSelectorMapFromSelectorInfo(selectors)

	// Build login steps
	authConfig := getAuthConfigFromScenario(tc)
	if authConfig.LoginURL != "" {
		recording.Steps = append(recording.Steps,
			models.RecordingStep{Action: "navigate", Description: "Navigate to login page", Value: authConfig.LoginURL},
			models.RecordingStep{Action: "type", Description: "Enter username", Selector: findSelectorByKeyword(selectorMap, "username", "email", "user"), Value: authConfig.Username},
			models.RecordingStep{Action: "type", Description: "Enter password", Selector: findSelectorByKeyword(selectorMap, "password"), Value: authConfig.Password},
			models.RecordingStep{Action: "click", Description: "Click login button", Selector: findSelectorByKeyword(selectorMap, "login", "submit", "signin")},
		)
	}

	// Build steps from test case
	for i, step := range tc.Steps {
		action := extractActionType(step.Action)
		if action == "" {
			action = "click"
		}

		recordingStep := models.RecordingStep{
			Action:      action,
			Description: step.Action,
			Value:       step.InputData,
		}

		selector := findSelectorForAction(selectorMap, action, step.Action, step.InputData)
		if selector != "" {
			recordingStep.Selector = selector
			// Also populate elementHints, xpath, xpathCandidates from selector
			if sel := getBestSelectorFromMap(selectorMap, action, step.Action, step.InputData); sel != nil {
				recordingStep.ElementHints = sel.ToElementHints()
				recordingStep.XPath = sel.ToXPath()
				recordingStep.XPathCandidates = sel.ToXPathCandidates()
			}
		} else {
			warnings = append(warnings, fmt.Sprintf("Step %d: No selector for '%s'", i+1, step.Action))
		}

		if action == "assert" || strings.Contains(strings.ToLower(step.Action), "verify") {
			recordingStep.AssertionType = "visible"
			recordingStep.ExpectedValue = step.ExpectedResult
		}

		recording.Steps = append(recording.Steps, recordingStep)
	}

	return recording, warnings, nil
}

// agentFetchRelevantFilesFromContext uses GitLab API to find and fetch relevant files
// based on test case name and steps content
func agentFetchRelevantFilesFromContext(ctx context.Context, glClient *gitlab.Client, projectID, branch, testCaseName, stepContext string) ([]FetchedFile, []string) {
	var files []FetchedFile
	var warnings []string

	if glClient == nil || projectID == "" {
		return files, warnings
	}

	// Extract keywords from test case name + step content
	keywords := extractKeywordsFromName(testCaseName)
	
	// Add keywords from step context
	contextKeywords := extractKeywordsFromContext(stepContext)
	keywords = append(keywords, contextKeywords...)
	
	// Deduplicate
	seen := make(map[string]bool)
	var uniqueKeywords []string
	for _, kw := range keywords {
		kwLower := strings.ToLower(kw)
		if !seen[kwLower] && len(kwLower) >= 2 {
			seen[kwLower] = true
			uniqueKeywords = append(uniqueKeywords, kw)
		}
	}
	
	log.Printf("[TestRecordingAgent] Searching for keywords: %v", uniqueKeywords)

	// Search the app directory for matching modules
	searchPaths := buildSearchPathsFromKeywords(uniqueKeywords)

	// Fetch each potential file
	fetched := make(map[string]bool)
	for _, path := range searchPaths {
		if fetched[path] {
			continue
		}

		content, err := fetchSingleFileWithClient(glClient, projectID, branch, path)
		if err != nil {
			continue
		}

		files = append(files, FetchedFile{
			Path:    path,
			Content: content,
		})
		fetched[path] = true
		log.Printf("[TestRecordingAgent] Fetched: %s", path)
	}

	// If no files found, search the repo tree for modules matching keywords
	if len(files) == 0 {
		files, _ = searchRepoTreeForModules(ctx, glClient, projectID, branch, uniqueKeywords)
	}

	if len(files) == 0 {
		warnings = append(warnings, fmt.Sprintf("Could not find files for test case '%s'", testCaseName))
	}

	return files, warnings
}

// buildSearchPathsFromKeywords builds file paths to search from keywords
func buildSearchPathsFromKeywords(keywords []string) []string {
	var paths []string

	for _, kw := range keywords {
		kwLower := strings.ToLower(kw)

		// Common page patterns
		paths = append(paths,
			fmt.Sprintf("app/%s/page.tsx", kwLower),
			fmt.Sprintf("app/%s", kwLower),
			fmt.Sprintf("app/(group)/%s/page.tsx", kwLower),
		)

		// Component patterns
		paths = append(paths,
			fmt.Sprintf("components/%s.tsx", kwLower),
			fmt.Sprintf("components/%sForm.tsx", kwLower),
			fmt.Sprintf("components/%sTable.tsx", kwLower),
			fmt.Sprintf("components/%sDialog.tsx", kwLower),
			fmt.Sprintf("components/%sModal.tsx", kwLower),
		)
	}

	// Also try with uppercase (for entity codes like MED)
	paths = append(paths,
		fmt.Sprintf("app/%s/page.tsx", strings.ToUpper(keywords[0])),
		fmt.Sprintf("app/%s", strings.ToUpper(keywords[0])),
	)

	return paths
}

// extractKeywordsFromContext extracts keywords from step context
func extractKeywordsFromContext(context string) []string {
	if context == "" {
		return nil
	}

	// Look for entity names, form fields, button labels
	// Common patterns: "invoice", "form", "submit", "input", "field", etc.

	var keywords []string

	// Extract words that look like entity/module names
	// e.g., "Invoice", "MED", "Entity", "District"
	re := regexp.MustCompile(`[A-Z][a-z]+|[A-Z]{2,}`)
	matches := re.FindAllString(context, -1)
	for _, m := range matches {
		if len(m) >= 2 {
			keywords = append(keywords, strings.ToLower(m))
		}
	}

	// Also look for common UI terms that might indicate files
	uiTerms := []string{"form", "table", "dialog", "modal", "list", "create", "edit", "view"}
	for _, term := range uiTerms {
		if strings.Contains(strings.ToLower(context), term) {
			keywords = append(keywords, term)
		}
	}

	return keywords
}

// searchRepoTreeForModules searches the repo tree for modules matching keywords
func searchRepoTreeForModules(ctx context.Context, glClient *gitlab.Client, projectID, branch string, keywords []string) ([]FetchedFile, []string) {
	var files []FetchedFile
	var warnings []string

	treeNodes, _, err := glClient.Repositories.ListTree(projectID, &gitlab.ListTreeOptions{
		Path:      gitlab.Ptr("app"),
		Recursive: gitlab.Ptr(false),
	})
	if err != nil {
		warnings = append(warnings, fmt.Sprintf("Failed to list app directory: %v", err))
		return files, warnings
	}

	for _, node := range treeNodes {
		if node.Type != "tree" {
			continue
		}

		nodeName := strings.ToLower(node.Name)

		// Check if any keyword matches this module
		for _, kw := range keywords {
			kwLower := strings.ToLower(kw)
			if strings.Contains(nodeName, kwLower) || strings.Contains(kwLower, nodeName) {
				// Try to fetch page.tsx in this module
				pagePath := fmt.Sprintf("app/%s/page.tsx", node.Name)
				content, err := fetchSingleFileWithClient(glClient, projectID, branch, pagePath)
				if err == nil {
					files = append(files, FetchedFile{
						Path:    pagePath,
						Content: content,
					})
					log.Printf("[TestRecordingAgent] Found module: %s", node.Name)
				}

				// Also fetch index.tsx if exists
				indexPath := fmt.Sprintf("app/%s/index.tsx", node.Name)
				content, err = fetchSingleFileWithClient(glClient, projectID, branch, indexPath)
				if err == nil {
					files = append(files, FetchedFile{
						Path:    indexPath,
						Content: content,
					})
				}
				break // Found a match, don't search for other keywords in this module
			}
		}
	}

	return files, warnings
}

// buildStepContext creates a context string from all steps
func buildStepContext(steps []models.ParsedStep) string {
	var lines []string
	for _, step := range steps {
		if step.Action != "" {
			lines = append(lines, step.Action)
		}
		if step.InputData != "" {
			lines = append(lines, step.InputData)
		}
		if step.ExpectedResult != "" {
			lines = append(lines, step.ExpectedResult)
		}
	}
	return strings.Join(lines, " ")
}

// extractKeywordsFromName extracts meaningful keywords from test case name
func extractKeywordsFromName(name string) []string {
	// Remove common action words
	skipWords := map[string]bool{
		"navigate": true, "to": true, "the": true,
		"edit": true, "create": true, "delete": true, "view": true,
		"cancel": true, "submit": true, "save": true,
		"go": true, "open": true, "close": true,
		"test": true, "case": true, "tc": true, "med": true,
	}

	nameLower := strings.ToLower(name)
	words := strings.Fields(nameLower)
	
	var keywords []string
	for _, word := range words {
		if skipWords[word] {
			continue
		}
		if len(word) > 2 {
			keywords = append(keywords, word)
		}
	}
	
	// Also try original casing
	origWords := strings.Fields(name)
	for _, word := range origWords {
		wordLower := strings.ToLower(word)
		if !skipWords[wordLower] && len(word) > 2 {
			// Add with original capitalization
			keywords = append(keywords, word)
		}
	}

	return keywords
}

func buildSelectorMapFromSelectorInfo(selectors []SelectorInfo) map[string][]SelectorInfo {
	m := make(map[string][]SelectorInfo)
	for _, sel := range selectors {
		// Index by semantic name keywords
		keywords := strings.FieldsFunc(strings.ToLower(sel.SemanticName), func(r rune) bool {
			return r == ' ' || r == '-' || r == '_'
		})
		for _, kw := range keywords {
			m[kw] = append(m[kw], sel)
		}
		m[sel.Type] = append(m[sel.Type], sel)
		valueKeywords := strings.FieldsFunc(strings.ToLower(sel.Value), func(r rune) bool {
			return r == '-' || r == '_'
		})
		for _, kw := range valueKeywords {
			m[kw] = append(m[kw], sel)
		}
	}
	return m
}

func getAuthConfigFromScenario(tc *models.ParsedTestCase) AuthConfig {
	// Auth config should come from the scenario, not individual test cases
	// For now, return empty - will be populated by caller
	return AuthConfig{}
}

func getGitLabClientFromScenarioProject(ctx context.Context, scenario *models.TestScenario) (*gitlab.Client, error) {
	if scenario.ProjectID == "" {
		return nil, fmt.Errorf("no project ID in scenario")
	}
	
	// Get token from somewhere - this is a simplified version
	// In production, you'd get this from the session/token
	return nil, fmt.Errorf("need to implement token retrieval")
}

func buildGenerationPrompt(scenario *models.TestScenario, sheetNames []string) string {
	// Build prompt for the agent to understand what needs to be generated
	var lines []string
	lines = append(lines, "You are generating test recordings from a test scenario.")
	lines = append(lines, "")
	lines = append(lines, fmt.Sprintf("Scenario: %s", scenario.FileName))
	lines = append(lines, "")
	
	for _, sheet := range scenario.Sheets {
		if len(sheetNames) > 0 {
			found := false
			for _, sn := range sheetNames {
				if sheet.Name == sn {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}
		
		lines = append(lines, fmt.Sprintf("Sheet: %s (%d test cases)", sheet.Name, len(sheet.TestCases)))
		for _, tc := range sheet.TestCases {
			lines = append(lines, fmt.Sprintf("  - %s: %s", tc.ID, tc.Name))
		}
		lines = append(lines, "")
	}
	
	return strings.Join(lines, "\n")
}

// inferRouteFromTestCaseName uses GitLab tools to find the correct route
// by searching the repo tree for matching files
func inferRouteFromTestCaseName(name string, glClient *gitlab.Client, projectID, branch string) string {
	if glClient == nil || projectID == "" {
		return ""
	}

	// Extract entity from test case name
	// e.g., "Navigate to Edit MED02-10" → extract "med"
	// e.g., "Create Medium" → extract "medium"
	
	entity := extractEntityFromName(name)
	if entity == "" {
		return ""
	}

	// Search the repo tree for files containing this entity
	entityLower := strings.ToLower(entity)
	
	// Try common routes
	searchPaths := []string{
		fmt.Sprintf("app/%s", entityLower),
		fmt.Sprintf("app/%s/edit", entityLower),
		fmt.Sprintf("app/%s/create", entityLower),
		fmt.Sprintf("app/%s/list", entityLower),
		fmt.Sprintf("app/(group)/%s", entityLower),
	}
	
	for _, path := range searchPaths {
		// Check if page.tsx exists at this path
		pagePath := fmt.Sprintf("%s/page.tsx", path)
		_, err := fetchSingleFileWithClient(glClient, projectID, branch, pagePath)
		if err == nil {
			// Found a route
			route := strings.TrimPrefix(path, "app/")
			route = strings.TrimPrefix(route, "(group)/")
			return "/" + route
		}
	}
	
	// Try listing the app directory to find matching module
	treeNodes, _, err := glClient.Repositories.ListTree(projectID, &gitlab.ListTreeOptions{
		Path:      gitlab.Ptr("app"),
		Recursive: gitlab.Ptr(false),
	})
	if err == nil {
		for _, node := range treeNodes {
			if node.Type == "tree" {
				nodeName := strings.ToLower(node.Name)
				// Match if entity appears in directory name
				if strings.Contains(nodeName, entityLower) || strings.Contains(entityLower, nodeName) {
					// Try to find page.tsx in this directory
					pagePath := fmt.Sprintf("app/%s/page.tsx", node.Name)
					_, err := fetchSingleFileWithClient(glClient, projectID, branch, pagePath)
					if err == nil {
						return fmt.Sprintf("/%s", node.Name)
					}
				}
			}
		}
	}

	return ""
}

// extractEntityFromName extracts the main entity/module from test case name
func extractEntityFromName(name string) string {
	// Get words from name
	words := strings.Fields(name)

	// Try to find CamelCase or uppercase words (entity codes like MED, INV)
	re := regexp.MustCompile(`([A-Z]{2,}[0-9]*|[A-Z][a-z]+)`)
	matches := re.FindAllString(name, -1)

	if len(matches) > 0 {
		for _, match := range matches {
			if len(match) >= 2 {
				return strings.ToLower(match)
			}
		}
	}

	// Fallback: use last word >= 3 chars
	for i := len(words) - 1; i >= 0; i-- {
		cleanLower := strings.ToLower(strings.Trim(words[i], "-,_"))
		if len(cleanLower) >= 3 {
			return cleanLower
		}
	}

	return ""
}

// extractRouteFromSteps looks at test case steps to find the route/navigate action
func extractRouteFromSteps(steps []models.ParsedStep) string {
	for _, step := range steps {
		actionLower := strings.ToLower(step.Action)

		// Look for navigate, go to, open actions
		if strings.Contains(actionLower, "navigate") ||
			strings.Contains(actionLower, "go to") ||
			strings.Contains(actionLower, "open") ||
			strings.Contains(actionLower, "access") {

			url := step.InputData
			if url == "" {
				url = extractUrlFromText(step.Action)
			}

			if url != "" {
				route := cleanUrlToRoute(url)
				if route != "" {
					return route
				}
			}
		}
	}
	return ""
}

// extractUrlFromText extracts URL from action text
func extractUrlFromText(text string) string {
	urlRe := regexp.MustCompile(`https?://[^\s]+`)
	matches := urlRe.FindString(text)
	if matches != "" {
		return matches
	}

	pathRe := regexp.MustCompile(`/([a-zA-Z0-9_-]+/?)+`)
	matches = pathRe.FindString(text)
	return matches
}

// cleanUrlToRoute converts a URL to a route path
func cleanUrlToRoute(url string) string {
	if url == "" {
		return ""
	}

	url = strings.TrimSpace(url)

	if strings.HasPrefix(url, "http://") || strings.HasPrefix(url, "https://") {
		urlRe := regexp.MustCompile(`https?://[^/]+(.*)`)
		matches := urlRe.FindStringSubmatch(url)
		if len(matches) > 1 {
			url = matches[1]
		}
	}

	url = strings.Trim(url, "/")

	if url == "" {
		return ""
	}

	return "/" + url
}

func generateRecordingsForScenario(ctx tool.Context, input GenerateRecordingsInput) (*GenerateRecordingsOutput, error) {
	log.Printf("[TestRecordingAgent] generateRecordingsForScenario (tool): %s", input.ScenarioID)

	output := &GenerateRecordingsOutput{
		Recordings: []models.TestRecording{},
		FailedIDs:  []string{},
		Warnings:   []string{},
	}

	scenario, err := getScenarioFromRedis(input.ScenarioID)
	if err != nil {
		return nil, fmt.Errorf("scenario not found: %w", err)
	}

	var targetCases []*models.ParsedTestCase
	for _, sheet := range scenario.Sheets {
		if len(input.SheetNames) > 0 {
			found := false
			for _, sn := range input.SheetNames {
				if sheet.Name == sn {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}

		for i := range sheet.TestCases {
			tc := &sheet.TestCases[i]
			if len(input.TestCaseIDs) > 0 {
				found := false
				for _, tcID := range input.TestCaseIDs {
					if tc.ID == tcID || tc.Name == tcID {
						found = true
						break
					}
				}
				if !found {
					continue
				}
			}
			targetCases = append(targetCases, tc)
		}
	}

	output.TotalCount = len(targetCases)
	log.Printf("[TestRecordingAgent] Processing %d test cases", output.TotalCount)

	if input.AuthConfig.LoginURL == "" {
		input.AuthConfig = AuthConfig{
			BaseURL:  scenario.AuthConfig.BaseURL,
			LoginURL: scenario.AuthConfig.LoginURL,
			Username: scenario.AuthConfig.Username,
			Password: scenario.AuthConfig.Password,
		}
	}
	if input.ProjectID == "" {
		input.ProjectID = scenario.ProjectID
	}

	batchSize := 5
	for i := 0; i < len(targetCases); i += batchSize {
		end := i + batchSize
		if end > len(targetCases) {
			end = len(targetCases)
		}
		batch := targetCases[i:end]

		for _, tc := range batch {
			result, err := generateRecordingForTestCase(ctx, GenerateRecordingInput{
				ScenarioID: input.ScenarioID,
				TestCaseID: tc.ID,
				AuthConfig: input.AuthConfig,
				ProjectID:  input.ProjectID,
			})

			if err != nil {
				log.Printf("[TestRecordingAgent] Failed for %s: %v", tc.ID, err)
				output.FailedIDs = append(output.FailedIDs, tc.ID)
				continue
			}

			if result.Recording != nil {
				output.Recordings = append(output.Recordings, *result.Recording)
				output.SuccessCount++
				output.Warnings = append(output.Warnings, result.Warnings...)
			}
		}
	}

	log.Printf("[TestRecordingAgent] Completed: %d/%d recordings", output.SuccessCount, output.TotalCount)
	return output, nil
}

// =============================================================================
// HELPERS
// =============================================================================

func getScenarioFromRedis(scenarioID string) (*models.TestScenario, error) {
	val, err := database.RedisClient.Get(context.Background(), fmt.Sprintf("scenario:%s", scenarioID)).Result()
	if err != nil {
		return nil, err
	}
	var scenario models.TestScenario
	if err := json.Unmarshal([]byte(val), &scenario); err != nil {
		return nil, err
	}
	return &scenario, nil
}

func getModuleCatalogFromCache(projectID string) (*services.ModuleCatalog, error) {
	graphMapper := services.NewGraphMapper()
	catalog, err := graphMapper.GetCachedCatalog(context.Background(), projectID, "main")
	if err != nil || catalog == nil {
		return nil, fmt.Errorf("catalog not in cache")
	}
	return catalog, nil
}

func extractActionType(action string) string {
	action = strings.ToLower(strings.TrimSpace(action))
	if strings.Contains(action, "click") || strings.Contains(action, "submit") || strings.Contains(action, "select") {
		return "click"
	}
	if strings.Contains(action, "type") || strings.Contains(action, "fill") || strings.Contains(action, "enter") || strings.Contains(action, "input") {
		return "type"
	}
	if strings.Contains(action, "navigate") || strings.Contains(action, "go to") || strings.Contains(action, "open") {
		return "navigate"
	}
	if strings.Contains(action, "press") || strings.Contains(action, "keyboard") {
		return "press"
	}
	if strings.Contains(action, "assert") || strings.Contains(action, "check") || strings.Contains(action, "verify") || strings.Contains(action, "should") {
		return "assert"
	}
	if strings.Contains(action, "wait") {
		return "wait"
	}
	return ""
}

func isRouteSegment(s string) bool {
	segments := map[string]bool{
		"create": true, "edit": true, "view": true, "list": true,
		"delete": true, "show": true, "new": true, "detail": true,
		"index": true, "page": true,
	}
	return segments[s]
}

func inferRouteFromName(name string) string {
	nameLower := strings.ToLower(name)

	// Pattern: "Navigate to Edit" → infer which entity and action
	// Skip action phrases like "navigate to", "go to", "cancel", "edit"
	
	skipWords := map[string]bool{
		"navigate": true, "goto": true, "go": true,
		"cancel": true, "edit": true, "delete": true, "remove": true,
		"create": true, "new": true, "add": true,
		"view": true, "show": true, "open": true,
		"to": true, "the": true,
	}

	// Remove action words from name to get entity
	words := strings.Fields(nameLower)
	var entityWords []string
	
	for i, word := range words {
		if skipWords[word] {
			continue
		}
		// If word is followed by action word, it's probably the entity
		if i+1 < len(words) {
			nextWord := words[i+1]
			if skipWords[nextWord] {
				entityWords = append(entityWords, word)
				continue
			}
		}
		entityWords = append(entityWords, word)
	}

	if len(entityWords) == 0 {
		return ""
	}

	entity := strings.Join(entityWords, "-")
	
	// Try to infer action from the original name
	var action string
	nameForAction := strings.ToLower(name)
	if strings.Contains(nameForAction, "edit") || strings.Contains(nameForAction, "update") {
		action = "edit"
	} else if strings.Contains(nameForAction, "create") || strings.Contains(nameForAction, "add") || strings.Contains(nameForAction, "new") {
		action = "create"
	} else if strings.Contains(nameForAction, "delete") || strings.Contains(nameForAction, "remove") {
		action = "delete"
	} else if strings.Contains(nameForAction, "view") || strings.Contains(nameForAction, "show") {
		action = "view"
	} else if strings.Contains(nameForAction, "cancel") {
		action = "edit" // cancel is usually on edit page
	} else {
		action = "list" // default to list
	}

	return fmt.Sprintf("/%s/%s", entity, action)
}

func inferRouteFromTestCaseRoute(route string) string {
	// If route exists in XLSX, clean it up
	if route == "" {
		return ""
	}
	
	// Clean the route
	route = strings.TrimSpace(route)
	route = strings.TrimPrefix(route, "/")
	route = strings.TrimSuffix(route, "/")
	
	if route == "" {
		return ""
	}
	
	// Validate it looks like a real route (has at least one segment)
	parts := strings.Split(route, "/")
	if len(parts) < 1 || parts[0] == "" {
		return ""
	}
	
	// Must start with alphanumeric (module name)
	if matched, _ := regexp.MatchString(`^[a-zA-Z]`, parts[0]); !matched {
		return ""
	}
	
	return "/" + route
}

func findRouteCandidatesFromCatalog(catalog *services.ModuleCatalog, tc *models.ParsedTestCase, analysis *AnalyzeTestCaseOutput) []RouteCandidate {
	var candidates []RouteCandidate
	nameLower := strings.ToLower(tc.Name)

	for moduleKey, module := range catalog.Modules {
		for route := range module.Routes {
			confidence := 0.3
			routeLower := strings.ToLower(route)
			moduleLower := strings.ToLower(moduleKey)

			if strings.Contains(nameLower, moduleLower) {
				confidence += 0.3
			}
			routeParts := strings.Split(strings.TrimPrefix(route, "/"), "/")
			for _, part := range routeParts {
				if strings.Contains(nameLower, part) {
					confidence += 0.1
				}
			}
			for _, action := range []string{"create", "edit", "delete", "list", "view"} {
				if strings.Contains(nameLower, action) && strings.Contains(routeLower, action) {
					confidence += 0.15
				}
			}

			if confidence > 1.0 {
				confidence = 1.0
			}

			if confidence > 0.3 {
				candidates = append(candidates, RouteCandidate{
					Route:      route,
					Confidence: confidence,
					Reason:     fmt.Sprintf("Module '%s'", module.DisplayName),
				})
			}
		}
	}

	return candidates
}

// SaveRecording saves a generated recording to Redis
func SaveRecording(recording *models.TestRecording) error {
	ctx := context.Background()
	recKey := fmt.Sprintf("recording:%s", recording.ID)
	recVal, err := json.Marshal(recording)
	if err != nil {
		return fmt.Errorf("failed to marshal recording: %w", err)
	}

	err = database.RedisClient.Set(ctx, recKey, recVal, 0).Err()
	if err != nil {
		return fmt.Errorf("failed to save recording to redis: %w", err)
	}

	database.RedisClient.SAdd(ctx, "recordings", recording.ID)
	if recording.CreatorID != 0 {
		database.RedisClient.SAdd(ctx, fmt.Sprintf("recordings:user:%d", recording.CreatorID), recording.ID)
	} else {
		database.RedisClient.SAdd(ctx, "recordings:legacy", recording.ID)
	}

	if recording.ProjectID != "" {
		database.RedisClient.SAdd(ctx, fmt.Sprintf("recordings:project:%s", recording.ProjectID), recording.ID)
	}

	return nil
}