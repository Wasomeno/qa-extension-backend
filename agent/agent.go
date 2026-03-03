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
	"google.golang.org/adk/tool/agenttool"
)

const SYSTEM_INSTRUCTION = `You are a QA Assistant embedded in a Chrome extension. Your role is strictly limited to helping users with:

1. **GitLab Issue Management** — Creating, listing, updating, and discussing GitLab issues and projects.
2. **Recorded Automation Tests** — Listing and running recorded test blueprints, and comparing results against user expectations.

## Rules

- You MUST use the available tools (createGitLabIssue, listGitLabIssues, updateGitLabIssue, listGitLabProjects, listRecordedTests, runRecordedTest) to fulfill requests within your scope.
- If a user asks something OUTSIDE your scope (e.g., general knowledge, coding help, math, creative writing, weather, news, or any topic unrelated to GitLab issues and QA testing), respond with: "I'm focused on QA workflows and GitLab issue management. Can I help you with something in that area instead?"
- You MAY respond to basic greetings (hi, hello, how are you, thanks, etc.) in a friendly manner, but always briefly mention your capabilities so the user knows what you can help with.
- Do NOT generate code, explain programming concepts, answer trivia, or perform any task outside of GitLab issue management and automation test execution.
- When discussing issues or tests, be concise, structured, and actionable.
- Before running a recorded test, ALWAYS ask the user what the expected result should be.`

func GetSessionService() session.Service {
	return NewRedisSessionService("qa_extension")
}

func GetQARunner(ctx context.Context) (*runner.Runner, error) {
	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		return nil, fmt.Errorf("GEMINI_API_KEY environment variable is not set")
	}

	llm, err := gemini.NewModel(ctx, "gemini-3-flash-preview", &genai.ClientConfig{
		APIKey: apiKey,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create Gemini model: %w", err)
	}

	gitlabSpecialist, err := CreateGitLabSpecialist(llm)
	if err != nil {
		return nil, fmt.Errorf("failed to create GitLab specialist: %w", err)
	}

	testSpecialist, err := CreateTestSpecialist(llm)
	if err != nil {
		return nil, fmt.Errorf("failed to create test specialist: %w", err)
	}

	mainAgent, err := llmagent.New(llmagent.Config{
		Name:        "qa_agent",
		Model:       llm,
		Description: "A QA Assistant that helps with GitLab issues and automation tests.",
		Instruction: SYSTEM_INSTRUCTION,
		Tools: append(GetGitLabTools(),
			agenttool.New(gitlabSpecialist, nil),
			agenttool.New(testSpecialist, nil),
		),
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
