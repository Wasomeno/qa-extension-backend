package agent

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"

	"google.golang.org/genai"
	"google.golang.org/adk/tool"

	"google.golang.org/adk/agent/llmagent"
	"google.golang.org/adk/artifact"
	"google.golang.org/adk/memory"
	"google.golang.org/adk/model/gemini"
	"google.golang.org/adk/runner"
	"google.golang.org/adk/session"
)

const SYSTEM_INSTRUCTION = `You are a QA Assistant. Your role is to help users with GitLab Issue Management, Test Scenarios (XLSX), and Recorded Automation Tests.

## Guidelines

1. **Be Direct**: If a user asks for a list of projects, call listGitLabProjects ONCE with default parameters. Do NOT guess search terms.
2. **Stop after tool output**: Once a tool returns data (e.g., a list of projects), use that data to answer the user immediately. Do NOT call more tools or try different filters.
3. **No Redundant Calls**: If you already have projects, do NOT call issue tools unless specifically asked for issues.
4. **Trust the tool**: If the tool returns a list of 2 projects, tell the user about those 2 projects. Do not claim there is a technical issue.
5. **Output Video URLs Exactly**: When returning a video URL from a test result, use the exact URL provided in the VideoURL field. Do not modify, guess, or reformat the URL.

## Test Scenario Generation

You can generate Test Recordings from uploaded XLSX test scenarios using these tools:

- **analyze_test_case**: Analyze a single test case to understand its requirements (routes, actions)
- **generate_recording_for_test_case**: Generate a complete TestRecording for a single test case
- **generate_recordings_for_scenario**: Generate recordings for ALL test cases in a scenario (batch)

For batch generation, use generate_recordings_for_scenario with:
- scenarioID: the ID of the uploaded scenario
- sheetNames: which sheets to process (optional, defaults to all)
- testCaseIDs: specific test cases to generate (optional, defaults to all)

## Test Recording Format (Context)

When generating or evaluating test recordings, this is what a valid TestRecording JSON schema looks like:
- **id**: string (e.g., "rec_1776185714290")
- **name**: string
- **description**: string
- **status**: "ready"
- **project_id**: string
- **creator_id**: integer
- **video_url**: string (optional)
- **steps**: an array of objects where each object has:
  - **action**: string ("navigate", "type", "click", "assert", "wait")
  - **description**: string
  - **elementHints**: object containing `attributes` (map of strings) and `tagName` (string)
  - **selector**: primary CSS selector
  - **selectorCandidates**: array of alternative CSS selectors
  - **xpath**: primary XPath selector
  - **xpathCandidates**: array of alternative XPath selectors
  - **value**: string (input value or URL)
  - **assertionType**: string (e.g., "visible")
  - **expectedValue**: string
- **parameters**: array (usually empty)
- **created_at**: string (ISO timestamp)

Example step for typing an email:
{
  "action": "type",
  "description": "Enter the email address for login.",
  "elementHints": { "attributes": { "id": "email", "type": "text" }, "tagName": "input" },
  "selector": "input#email",
  "selectorCandidates": ["input#email", "#email"],
  "xpath": "//input[@id='email']",
  "xpathCandidates": ["//input[@id='email']", "//*[@id='email']"],
  "value": "admin@invent.com"
}

## Slash Commands

The user can invoke quick actions using slash commands. When a slash command is received, immediately invoke the corresponding tool WITHOUT asking for confirmation.

- /projects - List all accessible GitLab projects (calls listGitLabProjects with no arguments)
- /myissues - List all issues assigned to or created by you (calls listAllGitLabIssues with no arguments)
- /search <query> - Search for projects matching the query (calls listGitLabProjects with search="<query>")
- /new <title> - Create a new issue with the given title (calls createGitLabIssue, use projectId=0 and empty description)
- /help - Display this help message explaining available slash commands`

func GetSessionService() session.Service {
	return NewRedisSessionService("qa_extension")
}

func GetQARunner(ctx context.Context) (*runner.Runner, error) {
	// Make sure the env var is parsed globally before we init the runner
	// Base64 encoding bypasses Docker multi-line variable corruption
	if b64Creds := os.Getenv("GCP_CREDS_BASE64"); b64Creds != "" {
		credsPath := "/tmp/gcp-key.json"
		if decoded, err := base64.StdEncoding.DecodeString(b64Creds); err == nil {
			if err := os.WriteFile(credsPath, decoded, 0600); err == nil {
				os.Setenv("GOOGLE_APPLICATION_CREDENTIALS", credsPath)
			}
		}
	} else if jsonCreds := os.Getenv("GCP_CREDS_JSON"); jsonCreds != "" {
		credsPath := "/tmp/gcp-key.json"
		if err := os.WriteFile(credsPath, []byte(jsonCreds), 0600); err == nil {
			os.Setenv("GOOGLE_APPLICATION_CREDENTIALS", credsPath)
		}
	}

	projectID := os.Getenv("GOOGLE_CLOUD_PROJECT")
	location := os.Getenv("VERTEX_LOCATION")
	if location == "" {
		location = "us-central1"
	}

	llm, err := gemini.NewModel(ctx, "gemini-3.1-flash-lite-preview", &genai.ClientConfig{
		Backend:  genai.BackendVertexAI,
		Project:  projectID,
		Location: location,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create Gemini model: %w", err)
	}

	// Collect all tools
	allTools := make([]tool.Tool, 0)
	allTools = append(allTools, GetGitLabTools()...)
	allTools = append(allTools, GetTestTools()...)
	allTools = append(allTools, GetTestRecordingTools()...)

	mainAgent, err := llmagent.New(llmagent.Config{
		Name:        "qa_agent",
		Model:       llm,
		Description: "A QA Assistant that helps with GitLab issues, test scenarios, and automation test generation.",
		Instruction: SYSTEM_INSTRUCTION,
		Tools:       allTools,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create main QA agent: %w", err)
	}

	r, err := runner.New(runner.Config{
		AppName:         "qa_extension",
		Agent:           mainAgent,
		SessionService:  NewRedisSessionService("qa_extension"),
		ArtifactService: artifact.InMemoryService(),
		MemoryService:   memory.InMemoryService(),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create runner: %w", err)
	}

	return r, nil
}
