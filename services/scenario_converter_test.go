package services

import (
	"testing"

	"qa-extension-backend/internal/models"
)

func TestBuildScenarioFromXLSXInitialAutomationTypeIsEmpty(t *testing.T) {
	scenario := BuildScenarioFromXLSX("sample.xlsx", []models.TestScenarioSheet{
		{
			Name: "Login",
			TestCases: []models.ParsedTestCase{
				{
					ID:    "TC-LOGIN-001",
					Name:  "User logs in",
					Steps: []models.ParsedStep{{Action: "Open login page", ExpectedResult: "Login page is shown"}},
				},
			},
		},
	}, "project-1", "Project", models.AuthConfig{}, 7)

	if got := len(scenario.Sections); got != 1 {
		t.Fatalf("sections = %d", got)
	}
	if got := len(scenario.Sections[0].TestCases); got != 1 {
		t.Fatalf("test cases = %d", got)
	}
	if automationType := scenario.Sections[0].TestCases[0].AutomationType; automationType != nil {
		t.Fatalf("initial automation type = %q, want nil", *automationType)
	}
}
