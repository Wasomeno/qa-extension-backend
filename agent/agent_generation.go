package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"qa-extension-backend/database"
	"qa-extension-backend/internal/models"
	"strings"
	"time"

	"golang.org/x/oauth2"
	"google.golang.org/genai"
	"google.golang.org/adk/agent"
	"google.golang.org/adk/session"
)

// RunAgentForTestGenerationWithLLM runs the actual QA LLM agent to generate recordings
// The agent will use GitLab tools to navigate the repo and find relevant files
func RunAgentForTestGenerationWithLLM(ctx context.Context, input TestRecordingAgentInput, token *oauth2.Token) (*GenerateRecordingsOutput, error) {
	log.Printf("[AgentGeneration] RunAgentForTestGenerationWithLLM: %s", input.ScenarioID)

	// Get scenario from Redis
	scenario, err := getScenarioFromRedis(input.ScenarioID)
	if err != nil {
		return nil, fmt.Errorf("scenario not found: %w", err)
	}

	// Build the prompt with all test case data
	prompt := buildAgentGenerationPrompt(scenario, input.ScenarioID, input.TestCaseIDs)
	
	// Create a unique session ID for this generation task
	scenarioShortID := input.ScenarioID
	if len(scenarioShortID) > 8 {
		scenarioShortID = scenarioShortID[:8]
	}
	sessionID := fmt.Sprintf("gen_%s_%d", scenarioShortID, time.Now().Unix())
	userID := "test_generator"

	// Get the QA runner
	runner, err := GetQARunner(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create agent runner: %w", err)
	}

	// Create session
	sessionService := GetSessionService()
	_, err = sessionService.Create(ctx, &session.CreateRequest{
		AppName:   "qa_extension",
		UserID:    userID,
		SessionID: sessionID,
	})
	if err != nil {
		log.Printf("[AgentGeneration] Session already exists or error: %v", err)
	}

	// Create the content message
	content := &genai.Content{
		Role: genai.RoleUser,
		Parts: []*genai.Part{{Text: prompt}},
	}

	// Store token in context for tools to access
	agentCtx := context.WithValue(ctx, "token", token)
	agentCtx = context.WithValue(agentCtx, "session_id", sessionID)

	log.Printf("[AgentGeneration] Starting agent execution for scenario %s", input.ScenarioID)

	// Run the agent
	eventCh := runner.Run(agentCtx, userID, sessionID, content, agent.RunConfig{})

	// Process events
	var finalResponse string
	for event, err := range eventCh {
		if err != nil {
			log.Printf("[AgentGeneration] Agent error: %v", err)
			continue
		}

		if event == nil {
			continue
		}

		// Check for final response
		if event.IsFinalResponse() {
			for _, part := range event.Content.Parts {
				if part.Text != "" {
					finalResponse += part.Text
				}
			}
		}

		// Log tool calls for debugging
		for _, part := range event.Content.Parts {
			if part.FunctionCall != nil {
				log.Printf("[AgentGeneration] Tool call: %s", part.FunctionCall.Name)
			}
			if part.FunctionResponse != nil {
				log.Printf("[AgentGeneration] Tool response: %s", part.FunctionResponse.Name)
			}
		}
	}

	log.Printf("[AgentGeneration] Agent execution completed")
	log.Printf("[AgentGeneration] Final response length: %d", len(finalResponse))

	// Count total test cases
	totalCases := 0
	for _, section := range scenario.Sections {
		for _, tc := range section.TestCases {
			if len(input.TestCaseIDs) > 0 {
				found := false
				for _, tid := range input.TestCaseIDs {
					if tc.ID == tid {
						found = true
						break
					}
				}
				if !found {
					continue
				}
			}
			totalCases++
		}
	}

	// Collect generated recordings from Redis
	output := &GenerateRecordingsOutput{
		Recordings:   []models.TestRecording{},
		FailedIDs:    []string{},
		Warnings:     []string{},
		TotalCount:   totalCases,
		SuccessCount: 0,
	}

	// Find recordings saved by the agent for this scenario
	allRecordings := collectGeneratedRecordings(input.ScenarioID)
	
	// Filter to only include those requested in this specific run/batch
	var currentBatchRecordings []models.TestRecording
	for _, r := range allRecordings {
		for _, targetID := range input.TestCaseIDs {
			if r.TestCaseID == targetID {
				currentBatchRecordings = append(currentBatchRecordings, r)
				break
			}
		}
	}

	output.Recordings = currentBatchRecordings
	output.SuccessCount = len(currentBatchRecordings)

	// Determine failed IDs in this batch
	var failedIDs []string
	for _, targetID := range input.TestCaseIDs {
		found := false
		for _, r := range currentBatchRecordings {
			if r.TestCaseID == targetID {
				found = true
				break
			}
		}
		if !found {
			failedIDs = append(failedIDs, targetID)
		}
	}
	output.FailedIDs = failedIDs

	log.Printf("[AgentGeneration] Collected %d recordings for this batch (Failed: %d)", len(currentBatchRecordings), len(failedIDs))

	return output, nil
}

// buildAgentGenerationPrompt creates the prompt for the agent to generate recordings
func buildAgentGenerationPrompt(scenario *models.TestScenario, scenarioID string, targetTestCaseIDs []string) string {
	var prompt strings.Builder

	prompt.WriteString(`You are tasked with generating test recordings from a test scenario. 

## Your Task
For EACH test case in the scenario below:
1. Use the GitLab tools to explore the project repository
2. Find the relevant source files (pages, components) for each test case
3. Extract selectors from the source code
4. Generate a complete test recording with proper steps

## Critical Requirements
- Each numbered step in the test case becomes ONE recording step
- Always include selector, elementHints, selectorCandidates, xpath, xpathCandidates for each step
- Use actual selectors from the source code (data-testid, id, aria-label, etc.)
- NEVER use vague selectors like "button" or ".item"

## Project Information
`)
	prompt.WriteString(fmt.Sprintf("- Scenario ID: %s\n", scenarioID))
	prompt.WriteString(fmt.Sprintf("- Project ID: %s\n", scenario.ProjectID))
	prompt.WriteString(fmt.Sprintf("- Creator ID: %d\n", scenario.CreatorID))
	prompt.WriteString(fmt.Sprintf("- Base URL: %s\n", scenario.AuthConfig.BaseURL))
	prompt.WriteString(fmt.Sprintf("- Login URL: %s\n", scenario.AuthConfig.LoginURL))
	prompt.WriteString(fmt.Sprintf("- Username: %s\n", scenario.AuthConfig.Username))
	prompt.WriteString(fmt.Sprintf("- Password: %s\n", scenario.AuthConfig.Password))

	prompt.WriteString("\n## Test Scenario Data\n\n")

	// Add each section and test case
	for _, section := range scenario.Sections {
		// If we only want specific test cases, see if this section contains any of them
		hasTargetCases := false
		if len(targetTestCaseIDs) > 0 {
			for _, tc := range section.TestCases {
				for _, tid := range targetTestCaseIDs {
					if tc.ID == tid {
						hasTargetCases = true
						break
					}
				}
				if hasTargetCases {
					break
				}
			}
			if !hasTargetCases {
				continue
			}
		}

		prompt.WriteString(fmt.Sprintf("### Section: %s\n\n", section.Title))

		for _, tc := range section.TestCases {
			// Skip if this isn't a target test case
			if len(targetTestCaseIDs) > 0 {
				isTarget := false
				for _, tid := range targetTestCaseIDs {
					if tc.ID == tid {
						isTarget = true
						break
					}
				}
				if !isTarget {
					continue
				}
			}
			prompt.WriteString(fmt.Sprintf("#### Test Case: %s - %s\n", tc.ID, tc.Title))
			
			if tc.Description != "" {
				prompt.WriteString(fmt.Sprintf("**Description:** %s\n", tc.Description))
			}
			if tc.PreCondition != "" {
				prompt.WriteString(fmt.Sprintf("**Pre-condition:** %s\n", tc.PreCondition))
			}

			prompt.WriteString("\n**Test Steps:**\n")
			for i, step := range tc.Steps {
				prompt.WriteString(fmt.Sprintf("%d. %s\n", i+1, step.Action))
				if step.Data != "" {
					prompt.WriteString(fmt.Sprintf("   - Input: %s\n", step.Data))
				}
				if step.Expected != "" {
					prompt.WriteString(fmt.Sprintf("   - Expected: %s\n", step.Expected))
				}
			}

			prompt.WriteString("\n---\n\n")
		}
	}

	prompt.WriteString(`
## Instructions
1. First, use listGitLabRepositoryTree to explore the project structure
2. For each test case, identify which pages/components are relevant
3. Use getGitLabFileContent to fetch the source files
4. Extract Playwright-compatible selectors (CSS and XPath) from the source code
5. Use save_test_recording to save each generated recording

NOTE: When generating tests, the system will execute them in parallel (up to 10 at a time). Since each test runs in a completely isolated browser context, you MUST ensure that EVERY SINGLE recording includes the full setup steps (e.g. navigation and login) if required by the test case's precondition.

## Recording Format & Playwright Locators
Each recording must have:
{
  "scenarioID": "` + scenarioID + `",
  "projectID": "` + scenario.ProjectID + `",
  "creatorID": ` + fmt.Sprintf("%d", scenario.CreatorID) + `,
  "testCaseID": "the exact ID of the test case, e.g. tc-123456",
  "name": "[TC-ID] Test Case Name",
  "description": "Pre-condition text",
  "steps": [
    {
      "action": "navigate|type|click|assert",
      "description": "Clear description",
      "selector": "CSS selector (e.g. [data-testid='login-btn'], .submit, #email)",
      "selectorCandidates": ["CSS selector fallback 1", "CSS selector fallback 2"],
      "xpath": "XPath expression (e.g. //button[contains(text(), 'Login')])",
      "xpathCandidates": ["//input[@name='email']"],
      "elementHints": {
        "attributes": {"id": "foo", "type": "button"},
        "tagName": "button"
      },
      "value": "value to type or URL"
    }
  ]
}

CRITICAL: The automation framework runs on Playwright. You MUST extract real CSS and XPath selectors from the source files. DO NOT invent fake selectors. DO NOT leave 'selector' or 'xpath' blank. If you cannot find a file, use semantic locators like "button:has-text('Login')" as fallback.

CRITICAL BRANCH POLICY: When using listGitLabRepositoryTree or getGitLabFileContent, you MUST leave the 'ref' argument empty so the tool automatically uses the default branch. DO NOT use random branch names like 'Prod/25-06-2025'. Always leave 'ref' empty to analyze the default branch.

Generate recordings for ALL test cases now. Use the save_test_recording tool for each one.
`)

	return prompt.String()
}

// collectGeneratedRecordings finds recordings that were saved by the agent for a specific scenario
func collectGeneratedRecordings(scenarioID string) []models.TestRecording {
	ctx := context.Background()
	var recordings []models.TestRecording

	// Get recording IDs from the scenario-specific set
	setKey := fmt.Sprintf("recordings:scenario:%s", scenarioID)
	ids, err := database.RedisClient.SMembers(ctx, setKey).Result()
	if err != nil {
		log.Printf("[AgentGeneration] Failed to get recording IDs for scenario %s: %v", scenarioID, err)
		return recordings
	}

	log.Printf("[AgentGeneration] Found %d recordings for scenario %s", len(ids), scenarioID)

	for _, id := range ids {
		key := fmt.Sprintf("recording:%s", id)
		val, err := database.RedisClient.Get(ctx, key).Result()
		if err != nil {
			log.Printf("[AgentGeneration] Failed to get recording %s: %v", id, err)
			continue
		}

		var recording models.TestRecording
		if err := json.Unmarshal([]byte(val), &recording); err != nil {
			log.Printf("[AgentGeneration] Failed to unmarshal recording %s: %v", id, err)
			continue
		}

		recordings = append(recordings, recording)
	}

	// Clear the scenario set after collection
	database.RedisClient.Del(ctx, setKey)

	return recordings
}
