package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"reflect"
	"strings"
	"time"

	"golang.org/x/oauth2"
	gitlab "gitlab.com/gitlab-org/api/client-go"

	"qa-extension-backend/client"
	"qa-extension-backend/database"
	"qa-extension-backend/internal/models"
)

// ToolExecutor is a function that executes a tool with given arguments
type ToolExecutor func(ctx context.Context, args map[string]any) (any, error)

// toolExecutorRegistry maps tool names to their executors
var toolExecutorRegistry = make(map[string]ToolExecutor)

// init registers all available tool executors
func init() {
	// GitLab tools
	registerToolExecutor("listGitLabProjects", execListGitLabProjects)
	registerToolExecutor("listAllGitLabIssues", execListAllGitLabIssues)
	registerToolExecutor("createGitLabIssue", execCreateGitLabIssue)
	registerToolExecutor("listGitLabIssues", execListGitLabIssues)
	registerToolExecutor("updateGitLabIssue", execUpdateGitLabIssue)

	// Test tools
	registerToolExecutor("listRecordedTests", execListRecordedTests)
	registerToolExecutor("runRecordedTest", execRunRecordedTest)
	registerToolExecutor("listTestScenarios", execListTestScenarios)
	registerToolExecutor("runTestScenario", execRunTestScenario)
	registerToolExecutor("runScenarioTestCase", execRunScenarioTestCase)
}

func registerToolExecutor(name string, executor ToolExecutor) {
	toolExecutorRegistry[name] = executor
}

// ExecuteTool executes a tool by name with the given arguments
func ExecuteTool(toolName string, ctx context.Context, args map[string]any) (any, error) {
	executor, ok := toolExecutorRegistry[toolName]
	if !ok {
		return nil, fmt.Errorf("tool '%s' not found in executor registry", toolName)
	}
	return executor(ctx, args)
}

// HasToolExecutor checks if a tool has an executor
func HasToolExecutor(toolName string) bool {
	_, ok := toolExecutorRegistry[toolName]
	return ok
}

// --- GitLab Tool Executors ---

func execListGitLabProjects(ctx context.Context, args map[string]any) (any, error) {
	log.Printf("[ToolExecutor] execListGitLabProjects called with args: %+v", args)

	database.PublishStreamEvent(ctx, database.StreamEvent{
		Type:    "agent",
		Stage:   "start",
		Message: "Fetching GitLab projects...",
	})

	gitlabClient, err := getGitLabClientDirect(ctx)
	if err != nil {
		database.PublishStreamEvent(ctx, database.StreamEvent{
			Type:    "agent",
			Stage:   "error",
			Message: "Failed to fetch GitLab projects: " + err.Error(),
		})
		return nil, err
	}

	search, _ := args["search"].(string)
	owned, _ := args["owned"].(bool)
	starred, _ := args["starred"].(bool)

	opts := &gitlab.ListProjectsOptions{
		Membership: gitlab.Ptr(true),
		Simple:     gitlab.Ptr(true),
		ListOptions: gitlab.ListOptions{
			PerPage: 100,
		},
	}

	if search != "" {
		opts.Search = &search
	}
	if owned {
		opts.Owned = gitlab.Ptr(true)
	}
	if starred {
		opts.Starred = gitlab.Ptr(true)
	}

	projects, _, err := gitlabClient.Projects.ListProjects(opts)
	if err != nil {
		database.PublishStreamEvent(ctx, database.StreamEvent{
			Type:    "agent",
			Stage:   "error",
			Message: "Failed to list GitLab projects: " + err.Error(),
		})
		return nil, err
	}

	var result []ProjectShortInfo
	for _, p := range projects {
		result = append(result, ProjectShortInfo{
			ID:                p.ID,
			Name:              p.Name,
			PathWithNamespace: p.PathWithNamespace,
			WebURL:            p.WebURL,
			Description:       p.Description,
		})
	}

	database.PublishStreamEvent(ctx, database.StreamEvent{
		Type:    "agent",
		Stage:   "done",
		Message: fmt.Sprintf("Loaded %d GitLab projects", len(result)),
	})

	return &ListProjectsResponse{Projects: result}, nil
}

func execListAllGitLabIssues(ctx context.Context, args map[string]any) (any, error) {
	log.Printf("[ToolExecutor] execListAllGitLabIssues called with args: %+v", args)

	database.PublishStreamEvent(ctx, database.StreamEvent{
		Type:    "agent",
		Stage:   "start",
		Message: "Fetching all GitLab issues...",
	})

	gitlabClient, err := getGitLabClientDirect(ctx)
	if err != nil {
		database.PublishStreamEvent(ctx, database.StreamEvent{
			Type:    "agent",
			Stage:   "error",
			Message: "Failed to fetch GitLab issues: " + err.Error(),
		})
		return nil, err
	}

	state, _ := args["state"].(string)
	opt := &gitlab.ListIssuesOptions{
		Scope: gitlab.Ptr("all"),
	}
	if state != "" {
		opt.State = &state
	}

	issues, err := client.ListIssuesRelatedToMe(gitlabClient, opt)
	if err != nil {
		database.PublishStreamEvent(ctx, database.StreamEvent{
			Type:    "agent",
			Stage:   "error",
			Message: "Failed to list GitLab issues: " + err.Error(),
		})
		return nil, err
	}

	var result []IssueShortInfo
	for _, i := range issues {
		result = append(result, IssueShortInfo{
			ID:          i.ID,
			IID:         i.IID,
			ProjectID:   i.ProjectID,
			Title:       i.Title,
			State:       i.State,
			Labels:      i.Labels,
			WebURL:      i.WebURL,
			Description: i.Description,
		})
	}

	database.PublishStreamEvent(ctx, database.StreamEvent{
		Type:    "agent",
		Stage:   "done",
		Message: fmt.Sprintf("Loaded %d GitLab issues", len(result)),
	})

	return &ListIssuesResponse{Issues: result}, nil
}

func execCreateGitLabIssue(ctx context.Context, args map[string]any) (any, error) {
	log.Printf("[ToolExecutor] execCreateGitLabIssue called with args: %+v", args)

	database.PublishStreamEvent(ctx, database.StreamEvent{
		Type:    "agent",
		Stage:   "start",
		Message: "Creating GitLab issue...",
	})

	gitlabClient, err := getGitLabClientDirect(ctx)
	if err != nil {
		database.PublishStreamEvent(ctx, database.StreamEvent{
			Type:    "agent",
			Stage:   "error",
			Message: "Failed to create GitLab issue: " + err.Error(),
		})
		return nil, err
	}

	projectID, _ := args["projectId"].(float64)
	title, _ := args["title"].(string)
	description, _ := args["description"].(string)
	labels, _ := args["labels"].([]any)

	opt := &gitlab.CreateIssueOptions{
		Title:       &title,
		Description: &description,
	}

	if len(labels) > 0 {
		var strLabels []string
		for _, l := range labels {
			if s, ok := l.(string); ok {
				strLabels = append(strLabels, s)
			}
		}
		l := gitlab.LabelOptions(strLabels)
		opt.Labels = &l
	}

	issue, _, err := gitlabClient.Issues.CreateIssue(fmt.Sprintf("%d", int(projectID)), opt)
	if err != nil {
		database.PublishStreamEvent(ctx, database.StreamEvent{
			Type:    "agent",
			Stage:   "error",
			Message: "Failed to create GitLab issue: " + err.Error(),
		})
		return nil, err
	}

	database.PublishStreamEvent(ctx, database.StreamEvent{
		Type:        "agent",
		Stage:       "done",
		Message:     "Created GitLab issue: " + issue.Title,
	})

	return issue, nil
}

func execListGitLabIssues(ctx context.Context, args map[string]any) (any, error) {
	log.Printf("[ToolExecutor] execListGitLabIssues called with args: %+v", args)

	database.PublishStreamEvent(ctx, database.StreamEvent{
		Type:    "agent",
		Stage:   "start",
		Message: "Fetching project GitLab issues...",
	})

	gitlabClient, err := getGitLabClientDirect(ctx)
	if err != nil {
		database.PublishStreamEvent(ctx, database.StreamEvent{
			Type:    "agent",
			Stage:   "error",
			Message: "Failed to fetch project GitLab issues: " + err.Error(),
		})
		return nil, err
	}

	projectID, _ := args["projectID"].(float64)
	state, _ := args["state"].(string)

	opt := &gitlab.ListProjectIssuesOptions{
		Scope: gitlab.Ptr("all"),
	}
	if state != "" {
		opt.State = &state
	}

	issues, _, err := gitlabClient.Issues.ListProjectIssues(fmt.Sprintf("%d", int(projectID)), opt)
	if err != nil {
		database.PublishStreamEvent(ctx, database.StreamEvent{
			Type:    "agent",
			Stage:   "error",
			Message: "Failed to list project GitLab issues: " + err.Error(),
		})
		return nil, err
	}

	var result []IssueShortInfo
	for _, i := range issues {
		result = append(result, IssueShortInfo{
			ID:          i.ID,
			IID:         i.IID,
			ProjectID:   i.ProjectID,
			Title:       i.Title,
			State:       i.State,
			Labels:      i.Labels,
			WebURL:      i.WebURL,
			Description: i.Description,
		})
	}

	database.PublishStreamEvent(ctx, database.StreamEvent{
		Type:    "agent",
		Stage:   "done",
		Message: fmt.Sprintf("Loaded %d project issues", len(result)),
	})

	return &ListIssuesResponse{Issues: result}, nil
}

func execUpdateGitLabIssue(ctx context.Context, args map[string]any) (any, error) {
	log.Printf("[ToolExecutor] execUpdateGitLabIssue called with args: %+v", args)

	database.PublishStreamEvent(ctx, database.StreamEvent{
		Type:    "agent",
		Stage:   "start",
		Message: "Updating GitLab issue...",
	})

	gitlabClient, err := getGitLabClientDirect(ctx)
	if err != nil {
		database.PublishStreamEvent(ctx, database.StreamEvent{
			Type:    "agent",
			Stage:   "error",
			Message: "Failed to update GitLab issue: " + err.Error(),
		})
		return nil, err
	}

	projectID, _ := args["projectId"].(float64)
	issueIID, _ := args["issueIid"].(float64)
	updates, _ := args["updates"].(map[string]any)

	opt := &gitlab.UpdateIssueOptions{}
	if title, ok := updates["title"].(string); ok {
		opt.Title = &title
	}
	if desc, ok := updates["description"].(string); ok {
		opt.Description = &desc
	}
	if state, ok := updates["state"].(string); ok {
		opt.StateEvent = &state
	}

	issue, _, err := gitlabClient.Issues.UpdateIssue(fmt.Sprintf("%d", int(projectID)), int64(issueIID), opt)
	if err != nil {
		database.PublishStreamEvent(ctx, database.StreamEvent{
			Type:    "agent",
			Stage:   "error",
			Message: "Failed to update GitLab issue: " + err.Error(),
		})
		return nil, err
	}

	database.PublishStreamEvent(ctx, database.StreamEvent{
		Type:        "agent",
		Stage:       "done",
		Message:     "Updated GitLab issue: " + issue.Title,
	})

	return issue, nil
}

func getGitLabClientDirect(ctx context.Context) (*gitlab.Client, error) {
	token, ok := ctx.Value("token").(*oauth2.Token)
	if !ok {
		return nil, fmt.Errorf("unauthorized: missing GitLab token in context")
	}

	return client.GetClient(ctx, token, nil)
}

// --- Test Tool Executors ---

func execListRecordedTests(ctx context.Context, args map[string]any) (any, error) {
	log.Printf("[ToolExecutor] execListRecordedTests called with args: %+v", args)

	database.PublishStreamEvent(ctx, database.StreamEvent{
		Type:    "agent",
		Stage:   "start",
		Message: "Fetching recorded tests...",
	})

	result, err := listRecordedTestsDirect(ctx, args)
	if err != nil {
		database.PublishStreamEvent(ctx, database.StreamEvent{
			Type:    "agent",
			Stage:   "error",
			Message: "Failed to fetch recorded tests: " + err.Error(),
		})
		return nil, err
	}

	database.PublishStreamEvent(ctx, database.StreamEvent{
		Type:    "agent",
		Stage:   "done",
		Message: fmt.Sprintf("Loaded %d recorded tests", len(result.Recordings)),
	})

	return result, nil
}

func execRunRecordedTest(ctx context.Context, args map[string]any) (any, error) {
	testID, _ := args["testID"].(string)
	log.Printf("[ToolExecutor] execRunRecordedTest called with testID: %s", testID)

	database.PublishStreamEvent(ctx, database.StreamEvent{
		Type:         "agent",
		ResourceType: "tool",
		ResourceID:   testID,
		Stage:        "start",
		Message:      fmt.Sprintf("Running recorded test '%s'...", testID),
	})

	result, err := runRecordedTestDirect(ctx, args)
	if err != nil {
		database.PublishStreamEvent(ctx, database.StreamEvent{
			Type:         "agent",
			ResourceType: "tool",
			ResourceID:   testID,
			Stage:        "error",
			Message:      fmt.Sprintf("Test '%s' failed: %v", testID, err),
		})
		return nil, err
	}

	database.PublishStreamEvent(ctx, database.StreamEvent{
		Type:         "agent",
		ResourceType: "tool",
		ResourceID:   testID,
		Stage:        "done",
		Message:      fmt.Sprintf("Test '%s' finished: %s", testID, result.Status),
	})

	return result, nil
}

func execListTestScenarios(ctx context.Context, args map[string]any) (any, error) {
	log.Printf("[ToolExecutor] execListTestScenarios called with args: %+v", args)

	database.PublishStreamEvent(ctx, database.StreamEvent{
		Type:    "agent",
		Stage:   "start",
		Message: "Fetching test scenarios...",
	})

	result, err := listTestScenariosDirect(ctx)
	if err != nil {
		database.PublishStreamEvent(ctx, database.StreamEvent{
			Type:    "agent",
			Stage:   "error",
			Message: "Failed to fetch test scenarios: " + err.Error(),
		})
		return nil, err
	}

	database.PublishStreamEvent(ctx, database.StreamEvent{
		Type:    "agent",
		Stage:   "done",
		Message: fmt.Sprintf("Loaded %d test scenarios", len(result.Scenarios)),
	})

	return result, nil
}

func execRunTestScenario(ctx context.Context, args map[string]any) (any, error) {
	scenarioID, _ := args["scenarioID"].(string)
	log.Printf("[ToolExecutor] execRunTestScenario called with scenarioID: %s", scenarioID)

	database.PublishStreamEvent(ctx, database.StreamEvent{
		Type:         "agent",
		ResourceType: "tool",
		ResourceID:   scenarioID,
		Stage:        "start",
		Message:      fmt.Sprintf("Running test scenario '%s'...", scenarioID),
	})

	result, err := runTestScenarioDirect(ctx, args)
	if err != nil {
		database.PublishStreamEvent(ctx, database.StreamEvent{
			Type:         "agent",
			ResourceType: "tool",
			ResourceID:   scenarioID,
			Stage:        "error",
			Message:      fmt.Sprintf("Scenario '%s' failed: %v", scenarioID, err),
		})
		return nil, err
	}

	database.PublishStreamEvent(ctx, database.StreamEvent{
		Type:         "agent",
		ResourceType: "tool",
		ResourceID:   scenarioID,
		Stage:        "done",
		Message:      fmt.Sprintf("Scenario '%s' finished: %s", scenarioID, result.Summary),
	})

	return result, nil
}

func execRunScenarioTestCase(ctx context.Context, args map[string]any) (any, error) {
	scenarioID, _ := args["scenarioID"].(string)
	testCaseID, _ := args["testCaseID"].(string)
	resourceID := scenarioID + ":" + testCaseID

	log.Printf("[ToolExecutor] execRunScenarioTestCase called with scenarioID: %s, testCaseID: %s", scenarioID, testCaseID)

	database.PublishStreamEvent(ctx, database.StreamEvent{
		Type:         "agent",
		ResourceType: "tool",
		ResourceID:   resourceID,
		Stage:        "start",
		Message:      fmt.Sprintf("Running test case '%s' from scenario '%s'...", testCaseID, scenarioID),
	})

	result, err := runScenarioTestCaseDirect(ctx, args)
	if err != nil {
		database.PublishStreamEvent(ctx, database.StreamEvent{
			Type:         "agent",
			ResourceType: "tool",
			ResourceID:   resourceID,
			Stage:        "error",
			Message:      fmt.Sprintf("Test case '%s' failed: %v", testCaseID, err),
		})
		return nil, err
	}

	database.PublishStreamEvent(ctx, database.StreamEvent{
		Type:         "agent",
		ResourceType: "tool",
		ResourceID:   resourceID,
		Stage:        "done",
		Message:      fmt.Sprintf("Test case '%s' finished: %s", testCaseID, result.Status),
	})

	return result, nil
}

// Direct implementations that don't use ADK tool.Context

func listRecordedTestsDirect(ctx context.Context, args map[string]any) (*ListRecordedTestsResponse, error) {
	projectID, _ := args["projectID"].(string)
	issueID, _ := args["issueID"].(string)

	var ids []string
	var err error

	if issueID != "" {
		ids, err = database.RedisClient.SMembers(ctx, fmt.Sprintf("recordings:issue:%s", issueID)).Result()
	} else if projectID != "" {
		ids, err = database.RedisClient.SMembers(ctx, fmt.Sprintf("recordings:project:%s", projectID)).Result()
	} else {
		ids, err = database.RedisClient.SMembers(ctx, "recordings").Result()
	}

	if err != nil {
		return nil, fmt.Errorf("failed to fetch recordings: %w", err)
	}

	// Return summaries instead of full recordings to keep response size manageable
	var summaries []models.RecordingSummary
	for _, id := range ids {
		val, err := database.RedisClient.Get(ctx, fmt.Sprintf("recording:%s", id)).Result()
		if err != nil {
			continue
		}

		var r models.TestRecording
		if err := json.Unmarshal([]byte(val), &r); err != nil {
			continue
		}

		summaries = append(summaries, models.RecordingSummary{
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

	return &ListRecordedTestsResponse{Recordings: summaries}, nil
}

func runRecordedTestDirect(ctx context.Context, args map[string]any) (*models.TestResult, error) {
	testID, _ := args["testID"].(string)
	overrides, _ := args["overrides"].([]any)

	val, err := database.RedisClient.Get(ctx, fmt.Sprintf("recording:%s", testID)).Result()
	if err != nil {
		return nil, fmt.Errorf("recording not found: %s", testID)
	}

	var recording models.TestRecording
	if err := json.Unmarshal([]byte(val), &recording); err != nil {
		return nil, fmt.Errorf("failed to parse recording: %w", err)
	}

	// Apply overrides
	if len(overrides) > 0 {
		for i, step := range recording.Steps {
			if step.Action == "type" || step.Action == "fill" {
				for _, o := range overrides {
					if override, ok := o.(map[string]any); ok {
						desc, _ := override["selectorDescription"].(string)
						newValue, _ := override["newValue"].(string)
						if desc != "" && newValue != "" {
							if strings.Contains(strings.ToLower(step.Value), strings.ToLower(desc)) ||
								strings.Contains(strings.ToLower(step.Description), strings.ToLower(desc)) {
								recording.Steps[i].Value = newValue
							}
						}
					}
				}
			}
		}
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	return RunTest(timeoutCtx, &recording)
}

func listTestScenariosDirect(ctx context.Context) (*ListTestScenariosResponse, error) {
	ids, err := database.RedisClient.SMembers(ctx, "scenarios").Result()
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

func runTestScenarioDirect(ctx context.Context, args map[string]any) (*RunTestScenarioResponse, error) {
	scenarioID, _ := args["scenarioID"].(string)

	val, err := database.RedisClient.Get(ctx, fmt.Sprintf("scenario:%s", scenarioID)).Result()
	if err != nil {
		return nil, fmt.Errorf("scenario not found: %s", scenarioID)
	}

	var scenario models.TestScenario
	if err := json.Unmarshal([]byte(val), &scenario); err != nil {
		return nil, fmt.Errorf("failed to parse scenario: %w", err)
	}

	if len(scenario.GeneratedTests) == 0 {
		return nil, fmt.Errorf("no tests have been generated for this scenario yet")
	}

	var recordings []models.TestRecording
	for _, gt := range scenario.GeneratedTests {
		rVal, err := database.RedisClient.Get(ctx, fmt.Sprintf("recording:%s", gt.ID)).Result()
		if err != nil {
			continue
		}

		var r models.TestRecording
		if err := json.Unmarshal([]byte(rVal), &r); err == nil {
			recordings = append(recordings, r)
		}
	}

	if len(recordings) == 0 {
		return nil, fmt.Errorf("failed to load any recordings for the scenario")
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()

	results := RunTestsParallel(timeoutCtx, recordings)

	passed := 0
	failed := 0
	for _, res := range results {
		if res.Status == "passed" {
			passed++
		} else {
			failed++
		}
		_ = database.SaveTestResult(ctx, res)
		for i := range res.StepResults {
			res.StepResults[i].Screenshot = ""
		}
	}

	summary := fmt.Sprintf("Execution completed. Passed: %d, Failed: %d", passed, failed)
	return &RunTestScenarioResponse{Summary: summary, Results: results}, nil
}

func runScenarioTestCaseDirect(ctx context.Context, args map[string]any) (*models.TestResult, error) {
	scenarioID, _ := args["scenarioID"].(string)
	testCaseID, _ := args["testCaseID"].(string)

	val, err := database.RedisClient.Get(ctx, fmt.Sprintf("scenario:%s", scenarioID)).Result()
	if err != nil {
		return nil, fmt.Errorf("scenario not found: %s", scenarioID)
	}

	var scenario models.TestScenario
	if err := json.Unmarshal([]byte(val), &scenario); err != nil {
		return nil, fmt.Errorf("failed to parse scenario: %w", err)
	}

	var targetCase *models.ParsedTestCase
	for _, sheet := range scenario.Sheets {
		for _, tc := range sheet.TestCases {
			if tc.ID == testCaseID {
				targetCase = &tc
				break
			}
		}
		if targetCase != nil {
			break
		}
	}

	if targetCase == nil {
		return nil, fmt.Errorf("test case %s not found in scenario %s", testCaseID, scenarioID)
	}

	var recording *models.TestRecording
	for _, gt := range scenario.GeneratedTests {
		if strings.Contains(gt.Name, testCaseID) {
			rVal, err := database.RedisClient.Get(ctx, fmt.Sprintf("recording:%s", gt.ID)).Result()
			if err == nil {
				var r models.TestRecording
				if err := json.Unmarshal([]byte(rVal), &r); err == nil {
					recording = &r
					break
				}
			}
		}
	}

	if recording == nil {
		return nil, fmt.Errorf("this test case has not been generated yet")
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	result, err := RunTest(timeoutCtx, recording)
	if err != nil {
		return nil, err
	}

	_ = database.SaveTestResult(ctx, result)
	for i := range result.StepResults {
		result.StepResults[i].Screenshot = ""
	}

	return result, nil
}

// RegisterToolFromFunc registers a tool executor from a reflection-based function
// This allows us to use the existing tool functions with minimal duplication
func RegisterToolFromFunc(toolName string, fn interface{}, argsType reflect.Type) {
	executor := func(ctx context.Context, args map[string]any) (any, error) {
		// Convert map to the appropriate struct type
		argsJson, err := json.Marshal(args)
		if err != nil {
			return nil, err
		}

		// Create a new instance of the args type
		argsVal := reflect.New(argsType)
		if err := json.Unmarshal(argsJson, argsVal.Interface()); err != nil {
			return nil, err
		}

		// Get the function value
		fnVal := reflect.ValueOf(fn)

		// Call the function with (tool.Context, argsType)
		results := fnVal.Call([]reflect.Value{
			reflect.ValueOf(ctx),
			argsVal.Elem(),
		})

		// Get the result and error
		var result, errVal any
		if len(results) >= 1 && !results[0].IsNil() {
			result = results[0].Interface()
		}
		if len(results) >= 2 && !results[1].IsNil() {
			errVal = results[1].Interface().(error)
		}

		if errVal != nil {
			return nil, errVal.(error)
		}
		return result, nil
	}

	registerToolExecutor(toolName, executor)
}