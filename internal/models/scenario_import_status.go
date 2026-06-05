package models

import "time"

type ScenarioImportState string

const (
	ScenarioImportIdle      ScenarioImportState = "idle"
	ScenarioImportSyncing   ScenarioImportState = "syncing"
	ScenarioImportImporting ScenarioImportState = "importing"
	ScenarioImportCompleted ScenarioImportState = "completed"
	ScenarioImportError     ScenarioImportState = "error"
)

type ScenarioImportItemStatus string

const (
	ScenarioImportItemPending   ScenarioImportItemStatus = "pending"
	ScenarioImportItemImporting ScenarioImportItemStatus = "importing"
	ScenarioImportItemImported  ScenarioImportItemStatus = "imported"
	ScenarioImportItemError     ScenarioImportItemStatus = "error"
)

type ScenarioImportCurrent struct {
	Title      string `json:"title,omitempty"`
	SourcePath string `json:"sourcePath,omitempty"`
	Index      int    `json:"index,omitempty"`
	Total      int    `json:"total,omitempty"`
}

type ScenarioImportCounts struct {
	Total    int `json:"total"`
	Imported int `json:"imported"`
	Pending  int `json:"pending"`
	Failed   int `json:"failed"`
}

type ScenarioImportFeedItem struct {
	ID            string                   `json:"id,omitempty"`
	Title         string                   `json:"title"`
	SourcePath    string                   `json:"sourcePath"`
	TestCaseCount int                      `json:"testCaseCount"`
	Status        ScenarioImportItemStatus `json:"status"`
	Error         string                   `json:"error,omitempty"`
}

type ScenarioImportStatus struct {
	State         ScenarioImportState      `json:"state"`
	IndicatorText string                   `json:"indicatorText"`
	Current       *ScenarioImportCurrent   `json:"current,omitempty"`
	Counts        ScenarioImportCounts     `json:"counts"`
	Feed          []ScenarioImportFeedItem `json:"feed"`
	Error         string                   `json:"error,omitempty"`
	StartedAt     string                   `json:"startedAt,omitempty"`
	UpdatedAt     string                   `json:"updatedAt"`
	CompletedAt   string                   `json:"completedAt,omitempty"`
}

func NewIdleScenarioImportStatus() *ScenarioImportStatus {
	now := time.Now().Format(time.RFC3339)
	return &ScenarioImportStatus{
		State:         ScenarioImportIdle,
		IndicatorText: "No import running",
		UpdatedAt:     now,
		Feed:          []ScenarioImportFeedItem{},
	}
}
