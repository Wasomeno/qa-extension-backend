package agent

import (
	"context"
	"strings"
	"testing"
)

func TestValidateGenerationSaveScopeRejectsDifferentScenario(t *testing.T) {
	ctx := context.WithValue(context.Background(), generationAllowedScenarioContextKey{}, "scn-selected")
	input := &SaveAutomationInput{
		ScenarioID:  "scn-other",
		TestCaseID:  "tc-1",
		Name:        "test",
		Description: "test",
	}

	err := validateGenerationSaveScope(ctx, input)

	if err == nil || !strings.Contains(err.Error(), "outside active generation scenario") {
		t.Fatalf("err = %v, want active generation scenario rejection", err)
	}
}

func TestValidateGenerationSaveScopeDefaultsScenarioAndAllowsBatchTestCase(t *testing.T) {
	ctx := context.WithValue(context.Background(), generationAllowedScenarioContextKey{}, "scn-selected")
	ctx = context.WithValue(ctx, generationAllowedTestCasesContextKey{}, map[string]bool{"tc-1": true})
	input := &SaveAutomationInput{
		TestCaseID:  "tc-1",
		Name:        "test",
		Description: "test",
	}

	if err := validateGenerationSaveScope(ctx, input); err != nil {
		t.Fatalf("validateGenerationSaveScope returned error: %v", err)
	}
	if input.ScenarioID != "scn-selected" {
		t.Fatalf("ScenarioID = %q, want scn-selected", input.ScenarioID)
	}
}

func TestValidateGenerationSaveScopeRejectsOutsideBatchTestCase(t *testing.T) {
	ctx := context.WithValue(context.Background(), generationAllowedScenarioContextKey{}, "scn-selected")
	ctx = context.WithValue(ctx, generationAllowedTestCasesContextKey{}, map[string]bool{"tc-1": true})
	input := &SaveAutomationInput{
		ScenarioID:  "scn-selected",
		TestCaseID:  "tc-2",
		Name:        "test",
		Description: "test",
	}

	err := validateGenerationSaveScope(ctx, input)

	if err == nil || !strings.Contains(err.Error(), "outside active generation batch") {
		t.Fatalf("err = %v, want active generation batch rejection", err)
	}
}
