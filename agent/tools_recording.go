package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"qa-extension-backend/database"
	"qa-extension-backend/models"
	"strings"
	"time"

	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
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

	return tools
}

type ListRecordedTestsArgs struct {
	ProjectID string `json:"projectID,omitempty"`
	IssueID   string `json:"issueID,omitempty"`
}

type ListRecordedTestsResponse struct {
	Recordings []models.TestRecording `json:"recordings"`
}

func listRecordedTests(ctx tool.Context, args ListRecordedTestsArgs) (*ListRecordedTestsResponse, error) {
	log.Printf("[AgentTool] listRecordedTests called with args: %+v", args)
	
	var ids []string
	var err error

	if args.IssueID != "" {
		ids, err = database.RedisClient.SMembers(ctx, fmt.Sprintf("recordings:issue:%s", args.IssueID)).Result()
	} else if args.ProjectID != "" {
		ids, err = database.RedisClient.SMembers(ctx, fmt.Sprintf("recordings:project:%s", args.ProjectID)).Result()
	} else {
		ids, err = database.RedisClient.SMembers(ctx, "recordings").Result()
	}

	if err != nil {
		log.Printf("[AgentTool] listRecordedTests redis error: %v", err)
		return nil, fmt.Errorf("failed to fetch recordings: %w", err)
	}

	log.Printf("[AgentTool] listRecordedTests found %d recording IDs", len(ids))

	var recordings []models.TestRecording
	for _, id := range ids {
		val, err := database.RedisClient.Get(ctx, fmt.Sprintf("recording:%s", id)).Result()
		if err != nil {
			log.Printf("[AgentTool] listRecordedTests Get recording:%s error: %v", id, err)
			continue
		}

		var r models.TestRecording
		if err := json.Unmarshal([]byte(val), &r); err != nil {
			log.Printf("[AgentTool] listRecordedTests Unmarshal recording:%s error: %v", id, err)
			continue
		}
		recordings = append(recordings, r)
	}
	
	log.Printf("[AgentTool] listRecordedTests success, returning %d recordings", len(recordings))
	return &ListRecordedTestsResponse{Recordings: recordings}, nil
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

	// Fetch recording from Redis
	val, err := database.RedisClient.Get(ctx, fmt.Sprintf("recording:%s", args.TestID)).Result()
	if err != nil {
		return nil, fmt.Errorf("recording not found: %s", args.TestID)
	}

	var recording models.TestRecording
	if err := json.Unmarshal([]byte(val), &recording); err != nil {
		return nil, fmt.Errorf("failed to parse recording: %w", err)
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

	result, err := RunTest(timeoutCtx, &recording)
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
	
	return result, nil
}
