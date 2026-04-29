package models

import (
	"fmt"
	"time"
)

// ─────────────────────────────────────────────
// Auth config (used during generation)
// ─────────────────────────────────────────────

type AuthConfig struct {
	BaseURL  string `json:"baseUrl"`
	LoginURL string `json:"loginUrl"`
	Username string `json:"username"`
	Password string `json:"password"`
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

// TestScenarioSheet represents a single sheet within an XLSX file
type TestScenarioSheet struct {
	Name      string           `json:"name"`
	TestCases []ParsedTestCase `json:"testCases"`
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

type TestCaseStatus string

const (
	TCStatusDraft      TestCaseStatus = "draft"
	TCStatusReady      TestCaseStatus = "ready"
	TCStatusBlocked    TestCaseStatus = "blocked"
	TCStatusDeprecated TestCaseStatus = "deprecated"
)

type ScenarioStatus string

const (
	ScenarioStatusDraft      ScenarioStatus = "draft"
	ScenarioStatusReady      ScenarioStatus = "ready"
	ScenarioStatusGenerating ScenarioStatus = "generating"
	ScenarioStatusFailed     ScenarioStatus = "failed"
)

type AutomationRunStatus string

const (
	AutomationStatusIdle    AutomationRunStatus = "idle"
	AutomationStatusRunning AutomationRunStatus = "running"
	AutomationStatusPass    AutomationRunStatus = "pass"
	AutomationStatusFail    AutomationRunStatus = "fail"
)

// ─────────────────────────────────────────────
// Core domain types
// ─────────────────────────────────────────────

// AutomationTest represents the automation linked to a test case
type AutomationTest struct {
	ID              string              `json:"id"`
	Name            string              `json:"name"`
	Status          AutomationRunStatus `json:"status"`
	LastRunAt       string              `json:"lastRunAt,omitempty"`
	RunDurationMs   int64               `json:"runDurationMs,omitempty"`
	RecordingID     string              `json:"recordingId,omitempty"`
	ScreenshotURL   string              `json:"screenshotUrl,omitempty"`
	ErrorMessage    string              `json:"errorMessage,omitempty"`
	FailedStepIndex *int                `json:"failedStepIndex,omitempty"`
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
	ID             string          `json:"id"`
	Order          int             `json:"order"`
	Code           string          `json:"code"`
	Title          string          `json:"title"`
	Description    string          `json:"description,omitempty"`
	PreCondition   string          `json:"preCondition,omitempty"`
	Steps          []TestStepV2    `json:"steps"`
	Tags           []string        `json:"tags"`
	Priority       Priority        `json:"priority"`
	Type           string          `json:"type"`
	Status         TestCaseStatus  `json:"status"`
	AutomationTest *AutomationTest `json:"automationTest,omitempty"`
	Note           string          `json:"note,omitempty"`
	CreatedAt      string          `json:"createdAt"`
	UpdatedAt      string          `json:"updatedAt"`
}

// TestSection groups test cases by functional area
type TestSection struct {
	ID          string     `json:"id"`
	Order       int        `json:"order"`
	Title       string     `json:"title"`
	Description string     `json:"description,omitempty"`
	TestCases   []TestCase `json:"testCases"`
}

// ScenarioStats provides aggregate counts
type ScenarioStats struct {
	TotalSections  int `json:"totalSections"`
	TotalTestCases int `json:"totalTestCases"`
	TotalSteps     int `json:"totalSteps"`
	AutomatedCount int `json:"automatedCount"`
	PassCount      int `json:"passCount"`
	FailCount      int `json:"failCount"`
	DraftCount     int `json:"draftCount"`
}

// TestScenario is the top-level entity stored in Redis
type TestScenario struct {
	ID             string         `json:"id"`
	Title          string         `json:"title"`
	Description    string         `json:"description,omitempty"`
	Sections       []TestSection  `json:"sections"`
	ProjectID      string         `json:"projectId,omitempty"`
	ProjectName    string         `json:"projectName,omitempty"`
	Status         ScenarioStatus `json:"status"`
	Error          string         `json:"error,omitempty"`
	Stats          *ScenarioStats `json:"stats,omitempty"`
	AuthConfig     AuthConfig     `json:"authConfig"`
	CreatorID      int            `json:"creatorId,omitempty"`
	CreatedAt      time.Time      `json:"createdAt"`
	UpdatedAt      time.Time      `json:"updatedAt"`
	CreatedBy      string         `json:"createdBy,omitempty"`

	// Internal: parsed XLSX sheets (kept for generation, not exposed in API)
	Sheets         []TestScenarioSheet `json:"sheets,omitempty"`
}

// ─────────────────────────────────────────────
// Stats computation
// ─────────────────────────────────────────────

func (s *TestScenario) ComputeStats() {
	stats := ScenarioStats{TotalSections: len(s.Sections)}
	for _, sec := range s.Sections {
		stats.TotalTestCases += len(sec.TestCases)
		for _, tc := range sec.TestCases {
			stats.TotalSteps += len(tc.Steps)
			if tc.AutomationTest != nil {
				stats.AutomatedCount++
				switch tc.AutomationTest.Status {
				case AutomationStatusPass:
					stats.PassCount++
				case AutomationStatusFail:
					stats.FailCount++
				}
			}
			if tc.Status == TCStatusDraft {
				stats.DraftCount++
			}
		}
	}
	s.Stats = &stats
}

// ─────────────────────────────────────────────
// API request/response types
// ─────────────────────────────────────────────

type UpdateScenarioRequest struct {
	Title       *string `json:"title,omitempty"`
	Description *string `json:"description,omitempty"`
}

type UpdateTestCaseRequest struct {
	Title        *string         `json:"title,omitempty"`
	Description  *string         `json:"description,omitempty"`
	PreCondition *string         `json:"preCondition,omitempty"`
	Steps        *[]TestStepV2   `json:"steps,omitempty"`
	Tags         *[]string       `json:"tags,omitempty"`
	Priority     *Priority       `json:"priority,omitempty"`
	Type         *string         `json:"type,omitempty"`
	Status       *TestCaseStatus `json:"status,omitempty"`
	Note         *string         `json:"note,omitempty"`
}

type CreateTestCaseRequest struct {
	Title        string       `json:"title"`
	Description  string       `json:"description,omitempty"`
	PreCondition string       `json:"preCondition,omitempty"`
	Steps        []TestStepV2 `json:"steps,omitempty"`
	Tags         []string     `json:"tags,omitempty"`
	Priority     Priority     `json:"priority,omitempty"`
	Type         string       `json:"type,omitempty"`
	Status       TestCaseStatus `json:"status,omitempty"`
}

type ReorderTestCasesRequest struct {
	OrderedIDs []string `json:"orderedIds"`
}

type GenerateTestCaseRequest struct {
	TestCaseIDs []string `json:"testCaseIds"`
}

type GenerateSectionRequest struct {
	SectionIDs []string `json:"sectionIds"`
}

// ─────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────

func NewTestStepID() string {
	return fmt.Sprintf("st-%d", time.Now().UnixNano()%100000000)
}

func NewTestCaseID() string {
	return fmt.Sprintf("tc-%d", time.Now().UnixNano()%100000000)
}

func NewSectionID() string {
	return fmt.Sprintf("sec-%d", time.Now().UnixNano()%100000000)
}
