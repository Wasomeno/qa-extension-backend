package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"qa-extension-backend/database"
	"qa-extension-backend/internal/models"
)

// scenarioWriteMu prevents concurrent read-modify-write races when multiple agents
// save automations for test cases belonging to the same scenario simultaneously.
var scenarioWriteMu sync.Map

// withScenarioLock serializes concurrent read-modify-write operations against a
// single scenario so agents don't overwrite each other's results.
func withScenarioLock(scenarioID string, fn func() error) error {
	mu, _ := scenarioWriteMu.LoadOrStore(scenarioID, &sync.Mutex{})
	lock := mu.(*sync.Mutex)
	lock.Lock()
	defer lock.Unlock()
	return fn()
}

// AuthConfig carries the credentials and entry points the LLM agent uses when
// generating E2E test cases for a scenario.
type AuthConfig struct {
	BaseURL  string `json:"baseUrl"`
	LoginURL string `json:"loginUrl"`
	Username string `json:"username"`
	Password string `json:"password"`
}

// SaveAutomationInput is the body of the save_automation_test LLM tool.
type SaveAutomationInput struct {
	ScenarioID  string               `json:"scenarioID"`
	ProjectID   string               `json:"projectID"`
	CreatorID   int                  `json:"creatorID"`
	TestCaseID  string               `json:"testCaseID"`
	Name        string               `json:"name"`
	Description string               `json:"description"`
	Framework   string               `json:"framework,omitempty"` // nextjs or vite
	Steps       []SaveAutomationStep `json:"steps"`
}

type SaveAutomationStep struct {
	Action             string            `json:"action"` // navigate, click, type, press, assert, wait
	Description        string            `json:"description"`
	ElementHints       ElementHintsInput `json:"elementHints"`
	Selector           string            `json:"selector"`
	SelectorCandidates []string          `json:"selectorCandidates"`
	XPath              string            `json:"xpath"`
	XPathCandidates    []string          `json:"xpathCandidates"`

	// API specific fields
	ApiMethod   string `json:"apiMethod,omitempty"`
	ApiEndpoint string `json:"apiEndpoint,omitempty"`
	ApiPayload  string `json:"apiPayload,omitempty"`
	ApiHeaders  string `json:"apiHeaders,omitempty"`

	Value         string `json:"value"`
	AssertionType string `json:"assertionType,omitempty"`
	ExpectedValue string `json:"expectedValue,omitempty"`
}

type ElementHintsInput struct {
	Attributes map[string]string `json:"attributes"`
	TagName    string            `json:"tagName"`
}

type SaveAutomationOutput struct {
	Status  string `json:"status"`
	Message string `json:"message"`
}

// GenerateAutomationsOutput is the result envelope returned to the background
// generation worker once the LLM agent has produced automations for a scenario.
type GenerateAutomationsOutput struct {
	Automations  []models.GeneratedAutomation `json:"automations"`
	FailedIDs    []string                     `json:"failedIDs"`
	TotalCount   int                          `json:"totalCount"`
	SuccessCount int                          `json:"successCount"`
	Warnings     []string                     `json:"warnings"`
}

// AutomationAgentInput is the prompt/input for the background agent that
// generates automation tests for a scenario.
type AutomationAgentInput struct {
	ScenarioID      string   `json:"scenarioID"`
	RepoID          string   `json:"repoId,omitempty"`
	SheetNames      []string `json:"sheetNames,omitempty"`
	TestCaseIDs     []string `json:"testCaseIds,omitempty"`
	AuthSessionID   string   `json:"authSessionId,omitempty"`
	GenerationJobID string   `json:"generationJobId,omitempty"`
}

// saveAutomation is the live save_automation_test tool the generation worker
// invokes once the LLM has produced a complete automation for one test case.
func saveAutomation(ctx context.Context, input SaveAutomationInput) (*SaveAutomationOutput, error) {
	log.Printf("[AgentTool] saveAutomation called: testCaseID=%s, steps=%d", input.TestCaseID, len(input.Steps))

	if err := validateGenerationSaveScope(ctx, &input); err != nil {
		return nil, err
	}

	steps := make([]models.RecordingStep, len(input.Steps))
	for i, step := range input.Steps {
		steps[i] = models.RecordingStep{
			Action:             step.Action,
			Description:        step.Description,
			Selector:           step.Selector,
			SelectorCandidates: step.SelectorCandidates,
			XPath:              step.XPath,
			XPathCandidates:    step.XPathCandidates,
			ApiMethod:          step.ApiMethod,
			ApiEndpoint:        step.ApiEndpoint,
			ApiPayload:         step.ApiPayload,
			ApiHeaders:         step.ApiHeaders,
			Value:              step.Value,
			AssertionType:      step.AssertionType,
			ExpectedValue:      step.ExpectedValue,
			ElementHints: models.ElementHints{
				Attributes: step.ElementHints.Attributes,
				TagName:    step.ElementHints.TagName,
			},
		}
		if steps[i].SelectorCandidates == nil {
			steps[i].SelectorCandidates = make([]string, 0)
		}
		if steps[i].XPathCandidates == nil {
			steps[i].XPathCandidates = make([]string, 0)
		}
		if steps[i].ElementHints.Attributes == nil {
			steps[i].ElementHints.Attributes = make(map[string]string)
		}
	}

	// withScenarioLock serializes all concurrent saveAutomation calls for the same
	// scenario so that concurrent agents don't overwrite each other's results.
	var found bool
	if err := withScenarioLock(input.ScenarioID, func() error {
		scenario, err := getScenarioFromRedis(input.ScenarioID)
		if err != nil {
			return fmt.Errorf("failed to load scenario: %w", err)
		}

		for i := range scenario.Sections {
			for j := range scenario.Sections[i].TestCases {
				tc := &scenario.Sections[i].TestCases[j]
				if tc.ID == input.TestCaseID {
					tc.AutomationTest = &models.AutomationTest{
						ID:        fmt.Sprintf("auto-%s", input.TestCaseID),
						Name:      input.Name,
						Framework: input.Framework,
						Status:    models.AutomationStatusIdle,
						Steps:     steps,
					}
					found = true
					break
				}
			}
			if found {
				break
			}
		}

		if !found {
			return fmt.Errorf("test case %s not found in scenario %s", input.TestCaseID, input.ScenarioID)
		}

		scenario.UpdatedAt = time.Now()
		scenario.ComputeStats()
		return saveScenarioToRedis(context.Background(), scenario)
	}); err != nil {
		return nil, err
	}

	log.Printf("[AgentTool] saveAutomation success: scenario=%s testCase=%s steps=%d", input.ScenarioID, input.TestCaseID, len(steps))

	return &SaveAutomationOutput{
		Status:  "saved",
		Message: fmt.Sprintf("Automation test saved with %d steps", len(steps)),
	}, nil
}

func validateGenerationSaveScope(ctx context.Context, input *SaveAutomationInput) error {
	if input == nil {
		return fmt.Errorf("save automation input is required")
	}

	if allowedScenarioID, ok := ctx.Value(generationAllowedScenarioContextKey{}).(string); ok && strings.TrimSpace(allowedScenarioID) != "" {
		allowedScenarioID = strings.TrimSpace(allowedScenarioID)
		input.ScenarioID = strings.TrimSpace(input.ScenarioID)
		if input.ScenarioID == "" {
			input.ScenarioID = allowedScenarioID
		}
		if input.ScenarioID != allowedScenarioID {
			return fmt.Errorf("save_automation_test rejected: scenarioID %s is outside active generation scenario %s", input.ScenarioID, allowedScenarioID)
		}
	}

	if allowedTestCaseIDs, ok := ctx.Value(generationAllowedTestCasesContextKey{}).(map[string]bool); ok && len(allowedTestCaseIDs) > 0 {
		input.TestCaseID = strings.TrimSpace(input.TestCaseID)
		if !allowedTestCaseIDs[input.TestCaseID] {
			return fmt.Errorf("save_automation_test rejected: testCaseID %s is outside active generation batch", input.TestCaseID)
		}
	}

	return nil
}

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

func saveScenarioToRedis(ctx context.Context, scenario *models.TestScenario) error {
	val, err := json.Marshal(scenario)
	if err != nil {
		return err
	}
	return database.RedisClient.Set(ctx, fmt.Sprintf("scenario:%s", scenario.ID), val, 0).Err()
}
