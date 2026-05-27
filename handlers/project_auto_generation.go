package handlers

import (
	"context"
	"fmt"
	"log"
	"qa-extension-backend/agent"
	"qa-extension-backend/internal/models"
	"qa-extension-backend/services"
	"time"

	"github.com/google/uuid"
	"golang.org/x/oauth2"
)

// registerAutoGeneration registers the auto-generation function with the services package.
func init() {
	services.AutoGenerateFunc = runProjectAutoGeneration
}

// runProjectAutoGeneration iterates over all imported scenarios and runs
// LLM-based test case generation for each one. It publishes project-level
// SSE events so the frontend sees a continuous progress flow (import -> generation).
func runProjectAutoGeneration(
	ctx context.Context,
	project *models.AppProject,
	importedScenarios []models.TestScenario,
	token *oauth2.Token,
	actorID int,
) {
	if len(importedScenarios) == 0 {
		log.Printf("[ProjectGeneration] no imported scenarios to generate for projectID=%s", project.ID)
		return
	}

	totalScenarios := len(importedScenarios)
	log.Printf("[ProjectGeneration] starting auto-generation projectID=%s scenarios=%d", project.ID, totalScenarios)

	// Publish initial generation start event
	services.PublishGenerationProgress(ctx, project.ID, "Generating test cases...", 0, totalScenarios, "")

	successCount := 0
	failCount := 0
	totalTestCases := 0
	generatedTestCases := 0

	for i, scenario := range importedScenarios {
		scenarioTitle := scenario.Title
		if scenarioTitle == "" {
			scenarioTitle = fmt.Sprintf("Scenario %d", i+1)
		}

		log.Printf("[ProjectGeneration] generating test cases projectID=%s scenario=%s (%d/%d)",
			project.ID, scenario.ID, i+1, totalScenarios)

		// Update scenario status to running
		services.SetScenarioGenerationStatus(ctx, scenario.ID, true)

		// Count total test cases in this scenario
		caseCount := 0
		var allCaseIDs []string
		for _, section := range scenario.Sections {
			for _, tc := range section.TestCases {
				caseCount++
				allCaseIDs = append(allCaseIDs, tc.ID)
			}
		}

		if caseCount == 0 {
			log.Printf("[ProjectGeneration] no test cases in scenario %s, skipping", scenario.ID)
			services.SetScenarioGenerationStatus(ctx, scenario.ID, false)
			failCount++
			continue
		}

		totalTestCases += caseCount

		// Batch process test cases (5 at a time to prevent LLM context limits)
		batchSize := 5
		batchGenerated := 0

		for batchStart := 0; batchStart < len(allCaseIDs); batchStart += batchSize {
			batchEnd := batchStart + batchSize
			if batchEnd > len(allCaseIDs) {
				batchEnd = len(allCaseIDs)
			}
			batchIDs := allCaseIDs[batchStart:batchEnd]

			// Set this batch to running status
			services.SetTestCaseBatchStatus(ctx, scenario.ID, batchIDs, models.AutomationStatusRunning)

			// Generate via the LLM agent
			result, err := agent.RunAgentForTestGenerationWithLLM(ctx, agent.AutomationAgentInput{
				ScenarioID:  scenario.ID,
				TestCaseIDs: batchIDs,
			}, token)

			if err != nil {
				log.Printf("[ProjectGeneration] batch generation failed for scenario=%s batch=%d-%d err=%v",
					scenario.ID, batchStart+1, batchEnd, err)
				services.SetTestCaseBatchStatus(ctx, scenario.ID, batchIDs, models.AutomationStatusFail)
				continue
			}

			if result != nil {
				// Reload scenario and link automations
				s, _ := services.GetScenarioFromRedis(ctx, scenario.ID)
				if s != nil {
					for _, auto := range result.Automations {
						services.LinkAutomation(s, &auto)
					}
					// Mark any still-running test cases as failed
					for si := range s.Sections {
						for ti := range s.Sections[si].TestCases {
							tc := &s.Sections[si].TestCases[ti]
							if tc.AutomationTest != nil && tc.AutomationTest.Status == models.AutomationStatusRunning {
								tc.AutomationTest.Status = models.AutomationStatusFail
								tc.AutomationTest.ErrorMessage = "Failed to generate automation for this test case."
							}
						}
					}
					s.Error = ""
					s.ComputeStats()
					services.SaveScenarioToRedis(ctx, s)
				}
				batchGenerated += len(result.Automations)
				generatedTestCases += len(result.Automations)
			}

			// Brief pause between batches to avoid overwhelming the LLM
			time.Sleep(500 * time.Millisecond)
		}

		if batchGenerated > 0 {
			successCount++
		} else {
			failCount++
		}

		// Publish progress event for this scenario
		services.PublishGenerationProgress(ctx, project.ID,
			fmt.Sprintf("Generated %d test cases for '%s' (%d/%d scenarios)", batchGenerated, scenarioTitle, i+1, totalScenarios),
			i+1, totalScenarios, scenarioTitle)

		// Append project activity
		_ = services.AppendAppProjectActivity(ctx, project.ID, models.AppProjectActivity{
			ID:        uuid.NewString(),
			ProjectID: project.ID,
			ActorID:   actorID,
			Action:    models.AppProjectActivityScenarioSyncCompleted,
			Changes: map[string]models.AppProjectChange{
				"scenarioId":     {Old: nil, New: scenario.ID},
				"scenarioName":   {Old: nil, New: scenarioTitle},
				"generatedCount": {Old: nil, New: batchGenerated},
			},
			CreatedAt: time.Now(),
		})
	}

	log.Printf("[ProjectGeneration] auto-generation complete projectID=%s success=%d fail=%d totalCases=%d generated=%d",
		project.ID, successCount, failCount, totalTestCases, generatedTestCases)
}
