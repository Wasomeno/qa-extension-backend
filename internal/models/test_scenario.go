package models

import (
	"time"
)

// ─────────────────────────────────────────────
// Auth config (used during generation)
// ─────────────────────────────────────────────

type AuthConfig struct {
	BaseURL    string `json:"baseUrl"`
	ApiBaseURL string `json:"apiBaseUrl,omitempty"`
	LoginURL   string `json:"loginUrl"`
	Username   string `json:"username"`
	Password   string `json:"password"`
}

// ─────────────────────────────────────────────
// XLSX parsing types (internal, not stored)
// ─────────────────────────────────────────────

// ParsedStep represents a single step extracted from XLSX
type ParsedStep struct {
	Action         string `json:"action"`
	InputData      string `json:"inputData"`
	ExpectedResult string `json:"expectedResult"`
}

// ParsedTestCase represents a single test case extracted from XLSX
type ParsedTestCase struct {
	ID           string       `json:"id"`
	Route        string       `json:"route,omitempty"`
	UserStory    string       `json:"userStory,omitempty"`
	TestType     string       `json:"testType,omitempty"`
	Name         string       `json:"name"`
	PreCondition string       `json:"preCondition"`
	Steps        []ParsedStep `json:"steps"`
	Status       string       `json:"status"`
	Note         string       `json:"note"`
}

// ─────────────────────────────────────────────
// Enums
// ─────────────────────────────────────────────

type Priority string

const (
	PriorityLow      Priority = "low"
	PriorityMedium   Priority = "medium"
	PriorityHigh     Priority = "high"
	PriorityCritical Priority = "critical"
)

type AutomationRunStatus string

const (
	AutomationStatusIdle    AutomationRunStatus = "idle"
	AutomationStatusRunning AutomationRunStatus = "running"
	AutomationStatusPass    AutomationRunStatus = "pass"
	AutomationStatusFail    AutomationRunStatus = "fail"
)

type AutomationCategory string

const (
	AutomationCategoryAPI    AutomationCategory = "api"
	AutomationCategoryE2E    AutomationCategory = "e2e"
	AutomationCategoryManual AutomationCategory = "manual"
)

func AutomationCategoryPtr(category AutomationCategory) *AutomationCategory {
	return &category
}

// ─────────────────────────────────────────────
// Core domain types
// ─────────────────────────────────────────────

// AutomationTest represents the automation linked to a test case.
// The generated steps are stored inline in the Steps field.
// After each run, VideoURL, StepResults, and Log are populated with the latest result.
type AutomationTest struct {
	ID            string              `json:"id"`
	Name          string              `json:"name"`
	Category      AutomationCategory  `json:"category,omitempty"`
	Framework     string              `json:"framework,omitempty"` // nextjs or vite
	Status        AutomationRunStatus `json:"status"`
	RepoID        string              `json:"repoId,omitempty"`
	Prompt        string              `json:"prompt,omitempty"`
	LastRunAt     string              `json:"lastRunAt,omitempty"`
	RunDurationMs int64               `json:"runDurationMs,omitempty"`
	Steps         []RecordingStep     `json:"steps,omitempty"`
	VideoURL      string              `json:"videoUrl,omitempty"`
	ScreenshotURL string              `json:"screenshotUrl,omitempty"`
	Log           string              `json:"log,omitempty"`
	ErrorMessage  string              `json:"errorMessage,omitempty"`
}

// TestStepV2 is a single step within a test case
type TestStepV2 struct {
	ID       string `json:"id"`
	Order    int    `json:"order"`
	Action   string `json:"action"`
	Data     string `json:"data,omitempty"`
	Expected string `json:"expected"`
}

// TestCase is a single test case
type TestCase struct {
	ID             string              `json:"id"`
	Order          int                 `json:"order"`
	Code           string              `json:"code"`
	Title          string              `json:"title"`
	Description    string              `json:"description,omitempty"`
	PreCondition   string              `json:"preCondition,omitempty"`
	Steps          []TestStepV2        `json:"steps"`
	Tags           []string            `json:"tags"`
	Priority       Priority            `json:"priority"`
	Type           string              `json:"type"`
	AutomationType *AutomationCategory `json:"automationType"`
	AutomationTest *AutomationTest     `json:"automationTest,omitempty"`
	Note           string              `json:"note,omitempty"`
	CreatedAt      string              `json:"createdAt"`
	UpdatedAt      string              `json:"updatedAt"`
}

// TestSection groups test cases by functional area
type TestSection struct {
	ID          string     `json:"id"`
	Order       int        `json:"order"`
	Title       string     `json:"title"`
	Description string     `json:"description,omitempty"`
	TestCases   []TestCase `json:"testCases"`
}

// TestScenario is the top-level entity stored in Redis
type TestScenario struct {
	ID              string            `json:"id"`
	Title           string            `json:"title"`
	Description     string            `json:"description,omitempty"`
	Sections        []TestSection     `json:"sections"`
	ProjectID       string            `json:"projectId,omitempty"` // public QA project ID
	ProjectName     string            `json:"projectName,omitempty"`
	IssueRepoID     string            `json:"issueRepoId,omitempty"`
	SpecsRepoID     string            `json:"specsRepoId,omitempty"`
	SourceType      string            `json:"sourceType,omitempty"`
	SourcePath      string            `json:"sourcePath,omitempty"`
	SourceSHA       string            `json:"sourceSha,omitempty"`
	SourceDisplay   string            `json:"sourceDisplay,omitempty"`
	Error           string            `json:"error,omitempty"`
	AutomationStats *ScenarioStats    `json:"automationStats,omitempty"`
	AuthConfig      AuthConfig        `json:"authConfig"`
	CreatorID       int               `json:"creatorId,omitempty"`
	CreatedAt       time.Time         `json:"createdAt"`
	UpdatedAt       time.Time         `json:"updatedAt"`
	CreatedBy       string            `json:"createdBy,omitempty"`

	// Internal: legacy parsed XLSX sheets (not used for Markdown-backed scenarios)
	Sheets []TestScenarioSheet `json:"sheets,omitempty"`
}

// TestScenarioSheet represents a single sheet within an XLSX file.
// Retained on the wire for backwards compatibility with stored scenarios
// that still carry their legacy XLSX sheets.
type TestScenarioSheet struct {
	Name      string           `json:"name"`
	TestCases []ParsedTestCase `json:"testCases"`
}

// ScenarioStats provides aggregate counts
type ScenarioStats struct {
	TotalSteps        int `json:"totalSteps"`
	GeneratedCount    int `json:"generatedCount"`
	NotGeneratedCount int `json:"notGeneratedCount"`
	RunningCount      int `json:"runningCount"`
	PassCount         int `json:"passCount"`
	FailCount         int `json:"failCount"`
	CoveragePercent   int `json:"coveragePercent"`
}

// ─────────────────────────────────────────────
// Stats computation
// ─────────────────────────────────────────────

func (s *TestScenario) GitLabSpecsProjectID() string {
	if s.SpecsRepoID != "" {
		return s.SpecsRepoID
	}
	return s.ProjectID
}

// ComputeStats walks the scenario tree and updates the AutomationStats field
// with rollups of automation generation/run state. The frontend reads this to
// render the dashboard coverage view.
func (s *TestScenario) ComputeStats() {
	stats := ScenarioStats{}
	for si := range s.Sections {
		for ti := range s.Sections[si].TestCases {
			tc := &s.Sections[si].TestCases[ti]
			stats.TotalSteps += len(tc.Steps)

			if tc.AutomationTest == nil || len(tc.AutomationTest.Steps) == 0 {
				stats.NotGeneratedCount++
				continue
			}

			stats.GeneratedCount++
			switch tc.AutomationTest.Status {
			case AutomationStatusRunning:
				stats.RunningCount++
			case AutomationStatusPass:
				stats.PassCount++
			case AutomationStatusFail:
				stats.FailCount++
			}
		}
	}
	if stats.TotalSteps > 0 {
		// CoveragePercent here is intentionally a coarse progress signal: how
		// much of the work has at least been generated. The historical
		// per-test-case ratio is no longer computed.
		generatedSteps := 0
		for si := range s.Sections {
			for ti := range s.Sections[si].TestCases {
				tc := &s.Sections[si].TestCases[ti]
				if tc.AutomationTest != nil {
					generatedSteps += len(tc.AutomationTest.Steps)
				}
			}
		}
		if stats.TotalSteps > 0 {
			stats.CoveragePercent = generatedSteps * 100 / stats.TotalSteps
		}
	}
	s.AutomationStats = &stats
}

// ─────────────────────────────────────────────
