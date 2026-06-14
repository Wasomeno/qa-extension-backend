package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestGenerationToolAllowlistHidesProjectScenarioTools(t *testing.T) {
	registry := NewQAToolRegistry()
	ctx := context.WithValue(context.Background(), generationToolAllowlistContextKey{}, map[string]bool{
		"repo_find":            true,
		"save_automation_test": true,
	})

	tools := registry.OpenAIToolsForContext(ctx)

	for _, tool := range tools {
		name := tool.Function.Name
		if name == "listTestScenarios" || name == "listAppProjects" || name == "getAppProject" {
			t.Fatalf("tool %q should not be advertised in generation context", name)
		}
	}
	if len(tools) != 2 {
		t.Fatalf("len(tools) = %d, want 2", len(tools))
	}
}

func TestQAToolRegistryUsesBoundedRepoTools(t *testing.T) {
	registry := NewQAToolRegistry()

	for _, name := range []string{"repo_ls", "repo_find", "repo_grep", "repo_read", "repo_branches"} {
		if _, ok := registry.Get(name); !ok {
			t.Fatalf("expected repo tool %q to be registered", name)
		}
	}
	for _, name := range []string{"listGitLabRepositoryTree", "getGitLabFileContent", "searchGitLabCode", "grepRepo", "findFiles", "listGitLabBranches", "listKnowledgeGraphs", "getKnowledgeGraph", "getKnowledgeGraphCoverage"} {
		if _, ok := registry.Get(name); ok {
			t.Fatalf("removed tool %q should not be registered", name)
		}
	}
}

func TestGenerationToolAllowlistRejectsExecution(t *testing.T) {
	registry := NewQAToolRegistry()
	ctx := context.WithValue(context.Background(), generationToolAllowlistContextKey{}, map[string]bool{
		"repo_find": true,
	})

	_, err := registry.Execute(ctx, "listTestScenarios", json.RawMessage(`{}`))

	if err == nil {
		t.Fatal("Execute returned nil error, want allowlist rejection")
	}
}

func TestSystemInstructionDoesNotAdvertiseKnowledgeGraphs(t *testing.T) {
	if strings.Contains(strings.ToLower(SYSTEM_INSTRUCTION), "knowledge graph") {
		t.Fatal("system instruction should not advertise knowledge graph usage")
	}
}
