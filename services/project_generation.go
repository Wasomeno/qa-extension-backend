package services

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	"qa-extension-backend/database"
	"qa-extension-backend/internal/models"

	"golang.org/x/oauth2"
)

// AutoGenerateFunc is the function type for project-level auto-generation.
// It is injected from outside to avoid circular imports (services -> agent -> services).
// Set via init() in the handlers package.
var AutoGenerateFunc func(ctx context.Context, project *models.AppProject, importedScenarios []models.TestScenario, token *oauth2.Token, actorID int)

// RunProjectAutoGeneration invokes the registered auto-generation function.
func RunProjectAutoGeneration(ctx context.Context, project *models.AppProject, importedScenarios []models.TestScenario, token *oauth2.Token, actorID int) {
	if AutoGenerateFunc == nil {
		log.Printf("[ProjectGeneration] AutoGenerateFunc not registered, skipping auto-generation projectID=%s", project.ID)
		return
	}
	AutoGenerateFunc(ctx, project, importedScenarios, token, actorID)
}

// PublishGenerationProgress publishes an SSE event for the project-level generation progress.
func PublishGenerationProgress(ctx context.Context, projectID, message string, currentStep, totalSteps int, stepName string) {
	_ = database.PublishStreamEvent(ctx, database.StreamEvent{
		Type:         "generation",
		ResourceType: "project",
		ResourceID:   projectID,
		Stage:        "progress",
		Message:      message,
		StepInfo: &database.StreamStepInfo{
			CurrentStep: currentStep,
			TotalSteps:  totalSteps,
			StepName:    stepName,
		},
	})
}

// SetScenarioGenerationStatus updates a scenario's processing status to reflect
// whether generation is in progress.
func SetScenarioGenerationStatus(ctx context.Context, scenarioID string, running bool) {
	s, err := GetScenarioFromRedis(ctx, scenarioID)
	if err != nil || s == nil {
		return
	}
	for si := range s.Sections {
		for ti := range s.Sections[si].TestCases {
			tc := &s.Sections[si].TestCases[ti]
			if running {
				if tc.AutomationTest == nil {
					tc.AutomationTest = &models.AutomationTest{
						ID:     fmt.Sprintf("auto-%s", tc.ID),
						Status: models.AutomationStatusRunning,
					}
				} else {
					tc.AutomationTest.Status = models.AutomationStatusRunning
				}
			}
		}
	}
	s.ComputeStats()
	SaveScenarioToRedis(ctx, s)
}

// SetTestCaseBatchStatus sets the automation status for a specific set of test case IDs.
func SetTestCaseBatchStatus(ctx context.Context, scenarioID string, testCaseIDs []string, status models.AutomationRunStatus) {
	s, err := GetScenarioFromRedis(ctx, scenarioID)
	if err != nil || s == nil {
		return
	}
	idMap := make(map[string]bool, len(testCaseIDs))
	for _, id := range testCaseIDs {
		idMap[id] = true
	}
	for si := range s.Sections {
		for ti := range s.Sections[si].TestCases {
			tc := &s.Sections[si].TestCases[ti]
			if idMap[tc.ID] {
				if status == models.AutomationStatusRunning {
					if tc.AutomationTest == nil {
						tc.AutomationTest = &models.AutomationTest{
							ID:     fmt.Sprintf("auto-%s", tc.ID),
							Status: status,
						}
					} else {
						tc.AutomationTest.Status = status
					}
				} else if tc.AutomationTest != nil {
					tc.AutomationTest.Status = status
				}
			}
		}
	}
	s.ComputeStats()
	SaveScenarioToRedis(ctx, s)
}

// GetScenarioFromRedis loads a scenario from Redis by ID.
func GetScenarioFromRedis(ctx context.Context, scenarioID string) (*models.TestScenario, error) {
	val, err := database.RedisClient.Get(ctx, fmt.Sprintf("scenario:%s", scenarioID)).Result()
	if err != nil {
		return nil, err
	}
	var s models.TestScenario
	if err := json.Unmarshal([]byte(val), &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// SaveScenarioToRedis persists a scenario to Redis.
func SaveScenarioToRedis(ctx context.Context, s *models.TestScenario) error {
	val, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return database.RedisClient.Set(ctx, fmt.Sprintf("scenario:%s", s.ID), val, 0).Err()
}
