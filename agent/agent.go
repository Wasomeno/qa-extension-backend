package agent

import (
	"context"
	"fmt"
	"os"

	"google.golang.org/genai"

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
6. **Scenario Management**: You can list uploaded XLSX scenarios and run all tests within them in parallel. If a specific test case from a scenario is requested, use runScenarioTestCase.`

func GetSessionService() session.Service {
	return NewRedisSessionService("qa_extension")
}

func GetQARunner(ctx context.Context) (*runner.Runner, error) {
	// Make sure the env var is parsed globally before we init the runner
	if jsonCreds := os.Getenv("GCP_CREDS_JSON"); jsonCreds != "" {
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

	llm, err := gemini.NewModel(ctx, "gemini-3-flash-preview", &genai.ClientConfig{
		Backend:  genai.BackendVertexAI,
		Project:  projectID,
		Location: location,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create Gemini model: %w", err)
	}

	mainAgent, err := llmagent.New(llmagent.Config{
		Name:        "qa_agent",
		Model:       llm,
		Description: "A QA Assistant that helps with GitLab issues and automation tests.",
		Instruction: SYSTEM_INSTRUCTION,
		Tools:       append(GetGitLabTools(), GetTestTools()...),
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
