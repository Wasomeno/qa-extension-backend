package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"qa-extension-backend/database"
	"qa-extension-backend/internal/models"
	"qa-extension-backend/identity"
	"strings"
	"time"

	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
	"golang.org/x/oauth2"
)

func GetTestTools() []tool.Tool {
	tools := []tool.Tool{}

	tt1, _ := functiontool.New(functiontool.Config{
		Name:        "listRecordedTests",
		Description: "List all available recorded automation tests. You can optionally filter by projectID or issueID.",
	}, listRecordedTests)
	tools = append(tools, tt1)

	tt2, _ := functiontool.New(functiontool.Config{
		Name:        "runRecordedTest",
		Description: "Run a recorded automation test by its ID. You can optionally provide 'overrides' to change input values (like email or password) during the test run.",
	}, runRecordedTest)
	tools = append(tools, tt2)

	tt3, _ := functiontool.New(functiontool.Config{
		Name:        "listTestScenarios",
		Description: "List all uploaded test scenarios (XLSX documents).",
	}, listTestScenarios)
	tools = append(tools, tt3)

	tt4, _ := functiontool.New(functiontool.Config{
		Name:        "runTestScenario",
		Description: "Run all generated tests for a specific test scenario. You can optionally filter by sheet names and choose between parallel or chained (sequential) execution.",
	}, runTestScenario)
	tools = append(tools, tt4)

	tt5, _ := functiontool.New(functiontool.Config{
		Name:        "runScenarioTestCase",
		Description: "Run a specific test case from a scenario. If the test hasn't been generated yet, it will be generated on-the-fly.",
	}, runScenarioTestCase)
	tools = append(tools, tt5)

	return tools
}

type ListTestScenariosResponse struct {
	Scenarios []models.TestScenario `json:"scenarios"`
}

func listTestScenarios(ctx tool.Context, _ struct{}) (*ListTestScenariosResponse, error) {
	log.Printf("[AgentTool] listTestScenarios called")

	var ids []string
	var err error

	// Try to get user identity from context
	token, _ := ctx.Value("token").(*oauth2.Token)
	sessionID, _ := ctx.Value("session_id").(string)

	if token != nil && sessionID != "" {
		userID, err := identity.GetCurrentUserIDFromCtx(ctx, token, sessionID)
		if err == nil {
			userKey := fmt.Sprintf("scenarios:user:%d", userID)
			ids, err = database.RedisClient.SUnion(ctx, "scenarios:legacy", userKey).Result()
		} else {
			log.Printf("[AgentTool] listTestScenarios failed to get user identity: %v", err)
			ids, err = database.RedisClient.SMembers(ctx, "scenarios").Result()
		}
	} else {
		ids, err = database.RedisClient.SMembers(ctx, "scenarios").Result()
	}

	if err != nil {
		return nil, fmt.Errorf("failed to list scenarios: %w", err)
	}

	var scenarios []models.TestScenario
	for _, id := range ids {
		val, err := database.RedisClient.Get(ctx, fmt.Sprintf("scenario:%s", id)).Result()
		if err != nil {
			continue
		}

		var s models.TestScenario
		if err := json.Unmarshal([]byte(val), &s); err != nil {
			continue
		}
		scenarios = append(scenarios, s)
	}

	return &ListTestScenariosResponse{Scenarios: scenarios}, nil
}

type RunTestScenarioArgs struct {
	ScenarioID string   `json:"scenarioID"`
	SheetNames []string `json:"sheetNames,omitempty"` // Optional filter
	Chained    bool     `json:"chained,omitempty"`    // If true, runs sequentially in a single session
}

type RunTestScenarioResponse struct {
	Summary string               `json:"summary"`
	Results []*models.TestResult `json:"results"`
}

func runTestScenario(ctx tool.Context, args RunTestScenarioArgs) (*RunTestScenarioResponse, error) {
	log.Printf("[AgentTool] runTestScenario called with args: %+v", args)

	val, err := database.RedisClient.Get(ctx, fmt.Sprintf("scenario:%s", args.ScenarioID)).Result()
	if err != nil {
		return nil, fmt.Errorf("scenario not found: %s", args.ScenarioID)
	}

	var scenario models.TestScenario
	if err := json.Unmarshal([]byte(val), &scenario); err != nil {
		return nil, fmt.Errorf("failed to parse scenario: %w", err)
	}

	// Verify ownership - only the creator can run their scenarios
	if err := verifyScenarioOwnership(ctx, &scenario); err != nil {
		return nil, err
	}

	if scenario.Stats != nil && scenario.Stats.AutomatedCount == 0 {
		return nil, fmt.Errorf("no tests have been generated for this scenario yet. Use 'GenerateTests' endpoint or 'runScenarioTestCase' to generate them.")
	}

	var runs []models.TestRun
	for _, section := range scenario.Sections {
		for _, tc := range section.TestCases {
			if tc.AutomationTest != nil && len(tc.AutomationTest.Steps) > 0 {
				runs = append(runs, models.TestRun{
					ID:    tc.AutomationTest.ID,
					Name:  tc.AutomationTest.Name,
					Steps: tc.AutomationTest.Steps,
				})
			}
		}
	}

	if len(runs) == 0 {
		return nil, fmt.Errorf("failed to load any automation tests for the scenario")
	}

	// Use the 10-minute timeout for batch
	timeoutCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()

	var results []*models.TestResult
	if args.Chained {
		results = RunTestsChained(timeoutCtx, runs)
	} else {
		results = RunTestsParallel(timeoutCtx, runs)
	}

	passed := 0
	failed := 0
	for _, res := range results {
		if res.Status == "passed" {
			passed++
		} else {
			failed++
		}
		// Save to Redis
		_ = database.SaveTestResult(ctx, res)
		// Strip screenshots
		for i := range res.StepResults {
			res.StepResults[i].Screenshot = ""
		}
	}

	summary := fmt.Sprintf("Execution completed. Passed: %d, Failed: %d", passed, failed)
	return &RunTestScenarioResponse{Summary: summary, Results: results}, nil
}

type RunScenarioTestCaseArgs struct {
	ScenarioID string `json:"scenarioID"`
	TestCaseID string `json:"testCaseID"`
}

func runScenarioTestCase(ctx tool.Context, args RunScenarioTestCaseArgs) (*models.TestResult, error) {
	log.Printf("[AgentTool] runScenarioTestCase called with args: %+v", args)

	val, err := database.RedisClient.Get(ctx, fmt.Sprintf("scenario:%s", args.ScenarioID)).Result()
	if err != nil {
		return nil, fmt.Errorf("scenario not found: %s", args.ScenarioID)
	}

	var scenario models.TestScenario
	if err := json.Unmarshal([]byte(val), &scenario); err != nil {
		return nil, fmt.Errorf("failed to parse scenario: %w", err)
	}

	// Verify ownership - only the creator can run their scenarios
	if err := verifyScenarioOwnership(ctx, &scenario); err != nil {
		return nil, err
	}

	// Find the test case in sections
	var targetCase *models.TestCase
	for _, section := range scenario.Sections {
		for _, tc := range section.TestCases {
			if tc.ID == args.TestCaseID {
				tcCopy := tc // Create a local copy to take address of safely in loop
				targetCase = &tcCopy
				break
			}
		}
		if targetCase != nil {
			break
		}
	}

	if targetCase == nil {
		return nil, fmt.Errorf("test case %s not found in scenario %s", args.TestCaseID, args.ScenarioID)
	}

	// Check if already generated
	if targetCase.AutomationTest == nil || len(targetCase.AutomationTest.Steps) == 0 {
		return nil, fmt.Errorf("this test case has not been generated yet. Please trigger generation for the scenario first.")
	}

	run := &models.TestRun{
		ID:    targetCase.AutomationTest.ID,
		Name:  targetCase.AutomationTest.Name,
		Steps: targetCase.AutomationTest.Steps,
	}

	// Run the test
	timeoutCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	result, err := RunTest(timeoutCtx, run)
	if err != nil {
		return nil, err
	}

	_ = database.SaveTestResult(ctx, result)
	for i := range result.StepResults {
		result.StepResults[i].Screenshot = ""
	}

	return result, nil
}

type ListRecordedTestsArgs struct {
	ProjectID string `json:"projectID,omitempty"`
	IssueID   string `json:"issueID,omitempty"`
}

type ListRecordedTestsResponse struct {
	Recordings []models.ManualRecordingSummary `json:"recordings"`
}

func listRecordedTests(ctx tool.Context, args ListRecordedTestsArgs) (*ListRecordedTestsResponse, error) {
	log.Printf("[AgentTool] listRecordedTests called with args: %+v", args)

	events := NewAgentToolEmitter(ctx)
	events.Start("Fetching recorded tests...")
	
	var ids []string
	var err error

	if args.IssueID != "" {
		ids, err = database.RedisClient.SMembers(ctx, fmt.Sprintf("recordings:issue:%s", args.IssueID)).Result()
	} else if args.ProjectID != "" {
		ids, err = database.RedisClient.SMembers(ctx, fmt.Sprintf("recordings:project:%s", args.ProjectID)).Result()
	} else {
		// Scoping by user for general list
		token, _ := ctx.Value("token").(*oauth2.Token)
		sessionID, _ := ctx.Value("session_id").(string)

		if token != nil && sessionID != "" {
			userID, err := identity.GetCurrentUserIDFromCtx(ctx, token, sessionID)
			if err == nil {
				userKey := fmt.Sprintf("recordings:user:%d", userID)
				ids, err = database.RedisClient.SUnion(ctx, "recordings:legacy", userKey).Result()
			} else {
				log.Printf("[AgentTool] listRecordedTests failed to get user identity: %v", err)
				ids, err = database.RedisClient.SMembers(ctx, "recordings").Result()
			}
		} else {
			ids, err = database.RedisClient.SMembers(ctx, "recordings").Result()
		}
	}

	if err != nil {
		log.Printf("[AgentTool] listRecordedTests redis error: %v", err)
		return nil, fmt.Errorf("failed to fetch recordings: %w", err)
	}

	log.Printf("[AgentTool] listRecordedTests found %d recording IDs", len(ids))

	// Return summaries instead of full recordings to keep response size manageable
	var summaries []models.ManualRecordingSummary
	for _, id := range ids {
		val, err := database.RedisClient.Get(ctx, fmt.Sprintf("recording:%s", id)).Result()
		if err != nil {
			log.Printf("[AgentTool] listRecordedTests Get recording:%s error: %v", id, err)
			continue
		}

		var r models.ManualRecording
		if err := json.Unmarshal([]byte(val), &r); err != nil {
			log.Printf("[AgentTool] listRecordedTests Unmarshal recording:%s error: %v", id, err)
			continue
		}

		// Convert to summary (excludes Steps and Parameters)
		summaries = append(summaries, models.ManualRecordingSummary{
			ID:          r.ID,
			Name:        r.Name,
			Description: r.Description,
			Status:      r.Status,
			ProjectID:   r.ProjectID,
			IssueID:     r.IssueID,
			CreatorID:   r.CreatorID,
			VideoURL:    r.VideoURL,
			StepCount:   len(r.Steps),
			CreatedAt:   r.CreatedAt,
		})
	}

	log.Printf("[AgentTool] listRecordedTests success, returning %d recording summaries", len(summaries))

	events.Done("Loaded %d recorded tests", len(summaries))

	return &ListRecordedTestsResponse{Recordings: summaries}, nil
}

type InputOverride struct {
	SelectorDescription string `json:"selectorDescription"` // e.g., "email input", "password field", or exact selector
	NewValue            string `json:"newValue"`
}

type RunRecordedTestArgs struct {
	TestID    string          `json:"testID"`
	Overrides []InputOverride `json:"overrides,omitempty"`
}

func runRecordedTest(ctx tool.Context, args RunRecordedTestArgs) (*models.TestResult, error) {
	log.Printf("[AgentTool] runRecordedTest called with args: %+v", args)

	events := NewAgentToolEmitter(ctx)
	events.Start("Running recorded test %s...", args.TestID)

	// Fetch recording from Redis
	val, err := database.RedisClient.Get(ctx, fmt.Sprintf("recording:%s", args.TestID)).Result()
	if err != nil {
		return nil, fmt.Errorf("recording not found: %s", args.TestID)
	}

	var recording models.ManualRecording
	if err := json.Unmarshal([]byte(val), &recording); err != nil {
		return nil, fmt.Errorf("failed to parse recording: %w", err)
	}

	// Verify ownership - only the creator can run their recordings
	if err := verifyRecordingOwnership(ctx, &recording); err != nil {
		return nil, err
	}

	// Apply overrides if provided
	if len(args.Overrides) > 0 {
		for i, step := range recording.Steps {
			if step.Action == "type" || step.Action == "fill" {
				for _, override := range args.Overrides {
					// Heuristic matching: check if original value, description, or selector contains the override target
					if strings.Contains(strings.ToLower(step.Value), strings.ToLower(override.SelectorDescription)) ||
						strings.Contains(strings.ToLower(step.Description), strings.ToLower(override.SelectorDescription)) ||
						strings.Contains(strings.ToLower(step.Selector), strings.ToLower(override.SelectorDescription)) {
						log.Printf("[AgentTool] Overriding value for step %d (%s): changed from '%s' to '%s'", i+1, step.Description, step.Value, override.NewValue)
						recording.Steps[i].Value = override.NewValue
					}
				}
			}
		}
	}

	// Use a 5-minute timeout for the entire test execution
	timeoutCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	run := &models.TestRun{ID: recording.ID, Name: recording.Name, Steps: recording.Steps}
	result, err := RunTest(timeoutCtx, run)
	if err != nil {
		if errors.Is(timeoutCtx.Err(), context.DeadlineExceeded) {
			log.Printf("[AgentTool] runRecordedTest TIMEOUT reached")
			// Return a specialized timeout result
			timeoutResult := &models.TestResult{
				TestID: args.TestID,
				Status: "timeout",
				Log:    fmt.Sprintf("Test execution timed out after 5 minutes. Last error: %v", err),
			}
			// Save result to Redis (ignore error)
			_ = database.SaveTestResult(ctx, timeoutResult)
			return timeoutResult, nil
		}
		return nil, err
	}
	
	if timeoutCtx.Err() != nil && errors.Is(timeoutCtx.Err(), context.DeadlineExceeded) {
		result.Status = "timeout"
	}
	
	// Save result to Redis (ignore error)
	_ = database.SaveTestResult(ctx, result)

	// Strip screenshots before returning to agent to save tokens
	// The agent primarily needs the status, errors, and video URL.
	// Base64 screenshots are too heavy for the LLM context.
	for i := range result.StepResults {
		result.StepResults[i].Screenshot = ""
	}

	// Publish completion event
	if result.Status == "passed" {
		events.Done("Test passed: %s", recording.Name)
	} else if result.Status == "failed" || result.Status == "timeout" {
		events.Error(fmt.Sprintf("Test %s: %s", result.Status, result.Log))
	} else {
		events.Done("Test completed: %s", recording.Name)
	}

	return result, nil
}

// verifyRecordingOwnership checks if the current user is the creator of the recording
func verifyRecordingOwnership(ctx tool.Context, recording *models.ManualRecording) error {
	token, _ := ctx.Value("token").(*oauth2.Token)
	sessionID, _ := ctx.Value("session_id").(string)

	if token == nil || sessionID == "" {
		return fmt.Errorf("unauthorized: missing authentication context")
	}

	userID, err := identity.GetCurrentUserIDFromCtx(ctx, token, sessionID)
	if err != nil {
		return fmt.Errorf("unauthorized: failed to verify user identity: %w", err)
	}

	if recording.CreatorID != userID {
		return fmt.Errorf("unauthorized: you do not have permission to access this recording")
	}

	return nil
}

// verifyScenarioOwnership checks if the current user is the creator of the scenario
func verifyScenarioOwnership(ctx tool.Context, scenario *models.TestScenario) error {
	token, _ := ctx.Value("token").(*oauth2.Token)
	sessionID, _ := ctx.Value("session_id").(string)

	if token == nil || sessionID == "" {
		return fmt.Errorf("unauthorized: missing authentication context")
	}

	userID, err := identity.GetCurrentUserIDFromCtx(ctx, token, sessionID)
	if err != nil {
		return fmt.Errorf("unauthorized: failed to verify user identity: %w", err)
	}

	if scenario.CreatorID != userID {
		return fmt.Errorf("unauthorized: you do not have permission to access this scenario")
	}

	return nil
}
