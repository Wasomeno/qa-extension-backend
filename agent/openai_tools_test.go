package agent

import (
	"context"
	"encoding/json"
	"testing"
)

func TestGenerationToolAllowlistHidesProjectScenarioTools(t *testing.T) {
	registry := NewQAToolRegistry()
	ctx := context.WithValue(context.Background(), generationToolAllowlistContextKey{}, map[string]bool{
		"findFiles":            true,
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

func TestGenerationToolAllowlistRejectsExecution(t *testing.T) {
	registry := NewQAToolRegistry()
	ctx := context.WithValue(context.Background(), generationToolAllowlistContextKey{}, map[string]bool{
		"findFiles": true,
	})

	_, err := registry.Execute(ctx, "listTestScenarios", json.RawMessage(`{}`))

	if err == nil {
		t.Fatal("Execute returned nil error, want allowlist rejection")
	}
}
