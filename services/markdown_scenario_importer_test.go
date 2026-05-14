package services

import (
	"os"
	"path/filepath"
	"testing"

	"qa-extension-backend/internal/models"
)

func TestBuildScenarioFromMarkdownExample(t *testing.T) {
	path := filepath.Join("..", "test-scenarios-examples", "Company Management FlowG.md")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read example: %v", err)
	}

	project := &models.AppProject{ID: "project-1", Name: "QA Project", SpecsRepoID: 1, IssueRepoID: 2}
	scenario, ok := BuildScenarioFromMarkdown("docs/test-scenarios/Company Management FlowG.md", string(content), project, 7)
	if !ok {
		t.Fatal("expected scenario to be parsed")
	}
	if scenario.Title != "Test Scenarios: Company Management" {
		t.Fatalf("title = %q", scenario.Title)
	}
	if len(scenario.Sections) != 1 {
		t.Fatalf("sections = %d", len(scenario.Sections))
	}
	if got := len(scenario.Sections[0].TestCases); got != 8 {
		t.Fatalf("test cases = %d", got)
	}
	first := scenario.Sections[0].TestCases[0]
	if first.Title != "Daftar Company dengan Pagination" {
		t.Fatalf("first title = %q", first.Title)
	}
	if got := len(first.Steps); got != 4 {
		t.Fatalf("first steps = %d", got)
	}
	if first.PreCondition == "" {
		t.Fatal("expected preconditions")
	}
}
