package agent

import (
	"google.golang.org/adk/agent"
	"google.golang.org/adk/agent/llmagent"
	"google.golang.org/adk/model"
)

const GITLAB_SPECIALIST_INSTRUCTION = `You are a GitLab Specialist. Your role is strictly limited to GitLab Issue and Project management.
 Use the available tools to list projects, list issues, create issues, or update issues.
 Be concise and report the outcome clearly.`

const TEST_SPECIALIST_INSTRUCTION = `You are a Test Automation Specialist. Your role is strictly limited to listing and running recorded automation tests.
 Use the available tools to list tests and execute them.
 Be concise and report the outcome clearly.`

func CreateGitLabSpecialist(llm model.LLM) (agent.Agent, error) {
	return llmagent.New(llmagent.Config{
		Name:        "gitlab_specialist",
		Model:       llm,
		Description: "Specialized in GitLab Issue and Project management.",
		Instruction: GITLAB_SPECIALIST_INSTRUCTION,
		Tools:       GetGitLabTools(),
	})
}

func CreateTestSpecialist(llm model.LLM) (agent.Agent, error) {
	return llmagent.New(llmagent.Config{
		Name:        "test_specialist",
		Model:       llm,
		Description: "Specialized in listing and running automation tests.",
		Instruction: TEST_SPECIALIST_INSTRUCTION,
		// Tools will be added when Playwright Go integration is ready
	})
}
