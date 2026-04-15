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

## Core Workflow: Test Recording Generation

When asked to generate test recordings from a test scenario, follow this EXACT process:

### Step 1: Explore the Repository Structure
FIRST, use listGitLabRepositoryTree to understand the project structure:
- Look at the 'app/' directory to find Next.js pages (routes)
- Look at 'components/' directory for UI components
- Identify which modules/pages are relevant to the test cases

### Step 2: Fetch Relevant Source Files
For EACH test case, identify relevant files based on:
- The test case name (e.g., "Upload Excel" → look for upload-related pages/components)
- Keywords in the test steps (e.g., "Entity Districts" → look for entity-districts pages)
- Use getGitLabFileContent to fetch the actual source code

### Step 3: Extract Selectors from Source Code
Analyze the fetched source files and extract:
- data-testid attributes (BEST)
- id attributes (GOOD)
- aria-label attributes (GOOD)
- name attributes on form elements
- role attributes
- Button text content

### Step 4: Generate Recording for Each Test Case
Use save_test_recording tool for EACH test case with:
- name: "[TC-ID] Test Case Name"
- description: The pre-condition text
- steps: Array of RecordingStep objects (one per numbered test step)

### Step 5: Verify All Test Cases Are Processed
Count the recordings you've saved and ensure it matches the number of test cases provided.

## Recording Step Schema
Each step MUST include ALL of these fields:
{
  "action": "navigate|type|click|press|wait|assert",
  "description": "Clear description of this step",
  "selector": "BEST selector from source code",
  "selectorCandidates": ["selector1", "selector2", "selector3"],
  "xpath": "//xpath[@attribute='value']",
  "xpathCandidates": ["//xpath1", "//xpath2"],
  "elementHints": {
    "attributes": {"id": "value", "type": "text", ...},
    "tagName": "input|button|div|..."
  },
  "value": "URL for navigate, text for type, empty for click"
}

## Critical Rules

1. **ONE TEST STEP = ONE RECORDING STEP**: Never combine multiple test steps into one recording step

2. **ALWAYS INCLUDE SELECTORS**: Every step (except navigate) MUST have a selector extracted from actual source code

3. **PROVIDE MULTIPLE SELECTOR OPTIONS**: Always include 3-5 selectorCandidates and xpathCandidates

4. **POPULATE ELEMENT HINTS**: Include ALL attributes from the source element in elementHints.attributes

5. **USE ACTUAL VALUES**: For navigate steps, use the actual base URL + route. For type steps, use realistic test data.

## Example Workflow

User: "Generate recordings for this test scenario with 2 test cases..."

Your response:
1. Call listGitLabRepositoryTree with {"projectId": "...", "path": "app"} to see the routes
2. Identify relevant pages for each test case
3. Call getGitLabFileContent for each relevant page/component
4. Extract selectors from the source code
5. Call save_test_recording for test case 1
6. Call save_test_recording for test case 2
7. Confirm: "I've generated 2 recordings for all test cases"

## Tools Available
- **listGitLabProjects**: List accessible GitLab projects
- **listGitLabRepositoryTree**: List files in a project repository
- **getGitLabFileContent**: Read file content from GitLab
- **save_test_recording**: Save a generated test recording (MUST use this to save recordings)

## Slash Commands
- /projects - List all accessible GitLab projects
- /myissues - List all issues assigned to you
- /search <query> - Search for projects
- /new <title> - Create a new issue
- /help - Display this help message`

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
