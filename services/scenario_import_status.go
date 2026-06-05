package services

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"qa-extension-backend/database"
	"qa-extension-backend/internal/models"

	"github.com/redis/go-redis/v9"
)

const scenarioImportStatusTTL = 24 * time.Hour

func pluralize(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func scenarioImportStatusKey(projectID string) string {
	return fmt.Sprintf("project:%s:scenario-import-status", projectID)
}

func GetScenarioImportStatus(ctx context.Context, projectID string) (*models.ScenarioImportStatus, error) {
	if projectID == "" {
		return models.NewIdleScenarioImportStatus(), nil
	}
	val, err := database.RedisClient.Get(ctx, scenarioImportStatusKey(projectID)).Result()
	if err == redis.Nil {
		return models.NewIdleScenarioImportStatus(), nil
	}
	if err != nil {
		return nil, err
	}
	var status models.ScenarioImportStatus
	if err := json.Unmarshal([]byte(val), &status); err != nil {
		return nil, err
	}
	if status.Feed == nil {
		status.Feed = []models.ScenarioImportFeedItem{}
	}
	return &status, nil
}

func BeginScenarioImportSyncing(ctx context.Context, projectID, indicatorText string) (*models.ScenarioImportStatus, error) {
	now := time.Now().Format(time.RFC3339)
	if indicatorText == "" {
		indicatorText = "Syncing specs repository"
	}
	status := &models.ScenarioImportStatus{
		State:         models.ScenarioImportSyncing,
		IndicatorText: indicatorText,
		Counts:        models.ScenarioImportCounts{},
		Feed:          []models.ScenarioImportFeedItem{},
		StartedAt:     now,
		UpdatedAt:     now,
	}
	return saveScenarioImportStatus(ctx, projectID, status)
}

func SetScenarioImportDiscovered(ctx context.Context, projectID string, feed []models.ScenarioImportFeedItem) (*models.ScenarioImportStatus, error) {
	status, err := GetScenarioImportStatus(ctx, projectID)
	if err != nil {
		return nil, err
	}
	if status.StartedAt == "" {
		status.StartedAt = time.Now().Format(time.RFC3339)
	}
	for i := range feed {
		if feed[i].Status == "" {
			feed[i].Status = models.ScenarioImportItemPending
		}
	}
	status.State = models.ScenarioImportImporting
	status.Feed = feed
	status.Current = nil
	status.Error = ""
	status.IndicatorText = fmt.Sprintf("Importing %d test scenario%s", len(feed), pluralize(len(feed)))
	recomputeScenarioImportCounts(status)
	if len(feed) == 0 {
		status.State = models.ScenarioImportCompleted
		status.IndicatorText = "No test scenarios found in docs/test-scenarios"
		now := time.Now().Format(time.RFC3339)
		status.CompletedAt = now
	}
	return saveScenarioImportStatus(ctx, projectID, status)
}

func MarkScenarioImporting(ctx context.Context, projectID, sourcePath, title string, index, total int) (*models.ScenarioImportStatus, error) {
	status, err := GetScenarioImportStatus(ctx, projectID)
	if err != nil {
		return nil, err
	}
	status.State = models.ScenarioImportImporting
	status.Current = &models.ScenarioImportCurrent{Title: title, SourcePath: sourcePath, Index: index, Total: total}
	status.IndicatorText = fmt.Sprintf("Importing %s", title)
	for i := range status.Feed {
		if status.Feed[i].SourcePath == sourcePath {
			status.Feed[i].Status = models.ScenarioImportItemImporting
			if title != "" {
				status.Feed[i].Title = title
			}
			break
		}
	}
	recomputeScenarioImportCounts(status)
	return saveScenarioImportStatus(ctx, projectID, status)
}

func MarkScenarioImported(ctx context.Context, projectID, sourcePath, scenarioID, title string, testCaseCount int) (*models.ScenarioImportStatus, error) {
	status, err := GetScenarioImportStatus(ctx, projectID)
	if err != nil {
		return nil, err
	}
	status.State = models.ScenarioImportImporting
	status.IndicatorText = fmt.Sprintf("Imported %s", title)
	for i := range status.Feed {
		if status.Feed[i].SourcePath == sourcePath {
			status.Feed[i].ID = scenarioID
			status.Feed[i].Title = title
			status.Feed[i].TestCaseCount = testCaseCount
			status.Feed[i].Status = models.ScenarioImportItemImported
			status.Feed[i].Error = ""
			break
		}
	}
	recomputeScenarioImportCounts(status)
	return saveScenarioImportStatus(ctx, projectID, status)
}

func MarkScenarioImportItemError(ctx context.Context, projectID, sourcePath, title, errText string) (*models.ScenarioImportStatus, error) {
	status, err := GetScenarioImportStatus(ctx, projectID)
	if err != nil {
		return nil, err
	}
	status.State = models.ScenarioImportImporting
	status.IndicatorText = fmt.Sprintf("Import error in %s", title)
	for i := range status.Feed {
		if status.Feed[i].SourcePath == sourcePath {
			if title != "" {
				status.Feed[i].Title = title
			}
			status.Feed[i].Status = models.ScenarioImportItemError
			status.Feed[i].Error = errText
			break
		}
	}
	recomputeScenarioImportCounts(status)
	return saveScenarioImportStatus(ctx, projectID, status)
}

func CompleteScenarioImport(ctx context.Context, projectID string) (*models.ScenarioImportStatus, error) {
	status, err := GetScenarioImportStatus(ctx, projectID)
	if err != nil {
		return nil, err
	}
	recomputeScenarioImportCounts(status)
	status.State = models.ScenarioImportCompleted
	if status.Counts.Failed > 0 {
		status.IndicatorText = fmt.Sprintf("Imported %d of %d test scenarios, %d failed", status.Counts.Imported, status.Counts.Total, status.Counts.Failed)
	} else {
		status.IndicatorText = fmt.Sprintf("Imported %d test scenario%s", status.Counts.Imported, pluralize(status.Counts.Imported))
	}
	now := time.Now().Format(time.RFC3339)
	status.CompletedAt = now
	status.UpdatedAt = now
	return saveScenarioImportStatus(ctx, projectID, status)
}

func FailScenarioImport(ctx context.Context, projectID string, importErr error) (*models.ScenarioImportStatus, error) {
	status, err := GetScenarioImportStatus(ctx, projectID)
	if err != nil {
		status = models.NewIdleScenarioImportStatus()
	}
	status.State = models.ScenarioImportError
	status.IndicatorText = "Import failed"
	if importErr != nil {
		status.Error = importErr.Error()
	}
	recomputeScenarioImportCounts(status)
	now := time.Now().Format(time.RFC3339)
	status.CompletedAt = now
	status.UpdatedAt = now
	return saveScenarioImportStatus(ctx, projectID, status)
}

func recomputeScenarioImportCounts(status *models.ScenarioImportStatus) {
	counts := models.ScenarioImportCounts{Total: len(status.Feed)}
	for _, item := range status.Feed {
		switch item.Status {
		case models.ScenarioImportItemImported:
			counts.Imported++
		case models.ScenarioImportItemError:
			counts.Failed++
		default:
			counts.Pending++
		}
	}
	status.Counts = counts
}

func saveScenarioImportStatus(ctx context.Context, projectID string, status *models.ScenarioImportStatus) (*models.ScenarioImportStatus, error) {
	if status.Feed == nil {
		status.Feed = []models.ScenarioImportFeedItem{}
	}
	status.UpdatedAt = time.Now().Format(time.RFC3339)
	data, err := json.Marshal(status)
	if err != nil {
		return nil, err
	}
	if err := database.RedisClient.Set(ctx, scenarioImportStatusKey(projectID), data, scenarioImportStatusTTL).Err(); err != nil {
		return nil, err
	}
	_ = publishScenarioImportStatus(ctx, projectID, status)
	return status, nil
}

func publishScenarioImportStatus(ctx context.Context, projectID string, status *models.ScenarioImportStatus) error {
	stage := "progress"
	switch status.State {
	case models.ScenarioImportSyncing:
		stage = "start"
	case models.ScenarioImportCompleted:
		stage = "done"
	case models.ScenarioImportError:
		stage = "error"
	}
	return database.PublishStreamEvent(ctx, database.StreamEvent{
		Type:         "generation",
		ResourceType: "project",
		ResourceID:   projectID,
		Stage:        stage,
		Message:      status.IndicatorText,
		ImportStatus: status,
	})
}
