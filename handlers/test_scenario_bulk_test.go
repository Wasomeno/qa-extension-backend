package handlers

import (
	"testing"

	"qa-extension-backend/internal/models"
)

func TestScenarioAutomationTargetsGroupsByAutomationType(t *testing.T) {
	api := models.AutomationCategoryAPI
	e2e := models.AutomationCategoryE2E
	manual := models.AutomationCategoryManual
	scenario := &models.TestScenario{
		Sections: []models.TestSection{{
			TestCases: []models.TestCase{
				{ID: "tc-api", AutomationType: &api},
				{ID: "tc-e2e", AutomationType: &e2e},
				{ID: "tc-manual", AutomationType: &manual},
				{ID: "tc-unassigned"},
			},
		}},
	}

	apiIDs, e2eIDs, manualSkipped, unassignedSkipped := scenarioAutomationTargets(scenario)

	if len(apiIDs) != 1 || apiIDs[0] != "tc-api" {
		t.Fatalf("apiIDs = %#v, want [tc-api]", apiIDs)
	}
	if len(e2eIDs) != 1 || e2eIDs[0] != "tc-e2e" {
		t.Fatalf("e2eIDs = %#v, want [tc-e2e]", e2eIDs)
	}
	if manualSkipped != 1 {
		t.Fatalf("manualSkipped = %d, want 1", manualSkipped)
	}
	if unassignedSkipped != 1 {
		t.Fatalf("unassignedSkipped = %d, want 1", unassignedSkipped)
	}
}
