package services

import (
	"fmt"
	"strings"

	"qa-extension-backend/internal/models"
)

// BuildAPITestPrompt creates a concise prompt for a backend developer's local coding agent.
func BuildAPITestPrompt(scenario models.TestScenario, testCase models.TestCase, backendRepoID string, backendRepoName string) string {
	var b strings.Builder
	b.WriteString("Generate backend API tests for the selected test case.\n\n")
	b.WriteString("Context:\n")
	b.WriteString(fmt.Sprintf("- QA project: %s\n", scenario.ProjectName))
	b.WriteString(fmt.Sprintf("- Scenario: %s\n", scenario.Title))
	b.WriteString(fmt.Sprintf("- Backend repo ID: %s\n", backendRepoID))
	if backendRepoName != "" {
		b.WriteString(fmt.Sprintf("- Backend repo: %s\n", backendRepoName))
	}
	b.WriteString("\nTest case:\n")
	b.WriteString(fmt.Sprintf("- ID: %s\n", testCase.ID))
	b.WriteString(fmt.Sprintf("- Code: %s\n", testCase.Code))
	b.WriteString(fmt.Sprintf("- Title: %s\n", testCase.Title))
	if testCase.PreCondition != "" {
		b.WriteString(fmt.Sprintf("- Preconditions:\n%s\n", indentLines(testCase.PreCondition, "  ")))
	}
	b.WriteString("\nSteps and expected results:\n")
	for _, step := range testCase.Steps {
		b.WriteString(fmt.Sprintf("%d. %s\n", step.Order, step.Action))
		if step.Data != "" {
			b.WriteString(fmt.Sprintf("   Input: %s\n", step.Data))
		}
		if step.Expected != "" {
			b.WriteString(fmt.Sprintf("   Expected: %s\n", step.Expected))
		}
	}
	b.WriteString(`
Instructions for the coding agent:
- Inspect the backend repository before writing tests.
- Identify the HTTP route, handler/controller, service, validation, auth/permission checks, and persistence layer involved.
- Create or update automated API tests using the repository's existing test framework and conventions.
- Cover success and failure paths described in the test case.
- Seed data through existing factories/fixtures/helpers; avoid brittle sleeps and external dependencies.
- Assert status codes, response body, validation messages, side effects, and permission behavior.
- Run the relevant test command and fix failures.
`)
	return strings.TrimSpace(b.String())
}

func indentLines(value string, prefix string) string {
	lines := strings.Split(strings.TrimSpace(value), "\n")
	for i := range lines {
		lines[i] = prefix + lines[i]
	}
	return strings.Join(lines, "\n")
}
