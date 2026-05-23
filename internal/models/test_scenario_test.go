package models

import "testing"

func TestComputeStatsDerivesProcessingAndAutomationState(t *testing.T) {
	scenario := TestScenario{
		Sections: []TestSection{{
			ID: "sec-1",
			TestCases: []TestCase{
				{ID: "tc-not-generated", Code: "TC-001", Title: "Not generated", Steps: []TestStepV2{{ID: "step-1"}}},
				testCaseWithAutomation("tc-generating", "TC-002", "Generating", AutomationStatusRunning, false, ""),
				testCaseWithAutomation("tc-generation-failed", "TC-003", "Generation failed", AutomationStatusFail, false, "generation failed"),
				testCaseWithAutomation("tc-running", "TC-004", "Running", AutomationStatusRunning, true, ""),
				testCaseWithAutomation("tc-passed", "TC-005", "Passed", AutomationStatusPass, true, ""),
				testCaseWithAutomation("tc-failed", "TC-006", "Failed", AutomationStatusFail, true, "run failed"),
				testCaseWithAutomation("tc-idle", "TC-007", "Idle", AutomationStatusIdle, true, ""),
			},
		}},
	}

	scenario.ComputeStats()

	if scenario.ProcessingStatus != ProcessingStatusGenerating {
		t.Fatalf("processing status = %q, want %q", scenario.ProcessingStatus, ProcessingStatusGenerating)
	}
	if scenario.AutomationStatus != ScenarioAutomationRunning {
		t.Fatalf("automation status = %q, want %q", scenario.AutomationStatus, ScenarioAutomationRunning)
	}
	if scenario.Stats == nil || scenario.AutomationStats == nil {
		t.Fatal("expected stats and automationStats to be populated")
	}

	stats := scenario.AutomationStats
	assertInt(t, "total test cases", stats.TotalTestCases, 7)
	assertInt(t, "total steps", stats.TotalSteps, 1)
	assertInt(t, "generated", stats.GeneratedCount, 4)
	assertInt(t, "not generated", stats.NotGeneratedCount, 3)
	assertInt(t, "generating", stats.GeneratingCount, 1)
	assertInt(t, "generation failed", stats.GenerationFailedCount, 1)
	assertInt(t, "running", stats.RunningCount, 1)
	assertInt(t, "passed", stats.PassCount, 1)
	assertInt(t, "failed", stats.FailCount, 1)
	assertInt(t, "idle", stats.IdleCount, 1)
	assertInt(t, "coverage", stats.CoveragePercent, 57)
	assertInt(t, "automated compatibility", stats.AutomatedCount, stats.GeneratedCount)

	assertInt(t, "active generating", len(scenario.ActiveTestCases.Generating), 1)
	assertInt(t, "active generation failed", len(scenario.ActiveTestCases.FailedGeneration), 1)
	assertInt(t, "active running", len(scenario.ActiveTestCases.Running), 1)
	assertInt(t, "active failed run", len(scenario.ActiveTestCases.FailedRun), 1)

	cases := scenario.Sections[0].TestCases
	if cases[1].ProcessingStatus != ProcessingStatusGenerating || cases[1].AutomationStatus != TestCaseAutomationNotGenerated {
		t.Fatalf("generating test case statuses = %q/%q", cases[1].ProcessingStatus, cases[1].AutomationStatus)
	}
	if cases[5].ProcessingStatus != ProcessingStatusIdle || cases[5].AutomationStatus != TestCaseAutomationFailed {
		t.Fatalf("failed test case statuses = %q/%q", cases[5].ProcessingStatus, cases[5].AutomationStatus)
	}
}

func TestComputeAutomationSummaryLimitsActiveTestCases(t *testing.T) {
	scenario := TestScenario{Sections: []TestSection{{TestCases: []TestCase{
		testCaseWithAutomation("tc-1", "TC-001", "One", AutomationStatusRunning, false, ""),
		testCaseWithAutomation("tc-2", "TC-002", "Two", AutomationStatusRunning, false, ""),
		testCaseWithAutomation("tc-3", "TC-003", "Three", AutomationStatusRunning, false, ""),
	}}}}

	scenario.ComputeAutomationSummary(2)

	assertInt(t, "limited generating cases", len(scenario.ActiveTestCases.Generating), 2)
	if scenario.ActiveTestCases.Generating[0].ID != "tc-1" || scenario.ActiveTestCases.Generating[1].ID != "tc-2" {
		t.Fatalf("unexpected limited active cases: %#v", scenario.ActiveTestCases.Generating)
	}
}

func TestComputeStatsScenarioAutomationStatusPriority(t *testing.T) {
	tests := []struct {
		name  string
		cases []TestCase
		want  ScenarioAutomationStatus
	}{
		{name: "not generated", cases: []TestCase{{ID: "tc-1"}}, want: ScenarioAutomationNotGenerated},
		{name: "partial", cases: []TestCase{{ID: "tc-1"}, testCaseWithAutomation("tc-2", "TC-002", "Passed", AutomationStatusPass, true, "")}, want: ScenarioAutomationPartial},
		{name: "passed", cases: []TestCase{testCaseWithAutomation("tc-1", "TC-001", "Passed", AutomationStatusPass, true, "")}, want: ScenarioAutomationPassed},
		{name: "failed", cases: []TestCase{testCaseWithAutomation("tc-1", "TC-001", "Failed", AutomationStatusFail, true, "")}, want: ScenarioAutomationFailed},
		{name: "idle", cases: []TestCase{testCaseWithAutomation("tc-1", "TC-001", "Idle", AutomationStatusIdle, true, "")}, want: ScenarioAutomationIdle},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scenario := TestScenario{Sections: []TestSection{{TestCases: tt.cases}}}
			scenario.ComputeStats()
			if scenario.AutomationStatus != tt.want {
				t.Fatalf("automation status = %q, want %q", scenario.AutomationStatus, tt.want)
			}
		})
	}
}

func testCaseWithAutomation(id, code, title string, status AutomationRunStatus, generated bool, errorMessage string) TestCase {
	tc := TestCase{
		ID:    id,
		Code:  code,
		Title: title,
		AutomationTest: &AutomationTest{
			ID:           "auto-" + id,
			Name:         title + " automation",
			Status:       status,
			ErrorMessage: errorMessage,
		},
	}
	if generated {
		tc.AutomationTest.Steps = []RecordingStep{{Action: "click"}}
	}
	return tc
}

func assertInt(t *testing.T, name string, got, want int) {
	t.Helper()
	if got != want {
		t.Fatalf("%s = %d, want %d", name, got, want)
	}
}
