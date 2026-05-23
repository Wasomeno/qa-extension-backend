package models

import (
	"fmt"
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

type ProcessingStatus string

const (
	ProcessingStatusIdle             ProcessingStatus = "idle"
	ProcessingStatusGenerating       ProcessingStatus = "generating"
	ProcessingStatusGenerationFailed ProcessingStatus = "generation_failed"
)

type TestCaseAutomationStatus string

const (
	TestCaseAutomationNotGenerated TestCaseAutomationStatus = "not_generated"
	TestCaseAutomationIdle         TestCaseAutomationStatus = "idle"
	TestCaseAutomationRunning      TestCaseAutomationStatus = "running"
	TestCaseAutomationPassed       TestCaseAutomationStatus = "passed"
	TestCaseAutomationFailed       TestCaseAutomationStatus = "failed"
)

type ScenarioAutomationStatus string

const (
	ScenarioAutomationNotGenerated ScenarioAutomationStatus = "not_generated"
	ScenarioAutomationIdle         ScenarioAutomationStatus = "idle"
	ScenarioAutomationRunning      ScenarioAutomationStatus = "running"
	ScenarioAutomationPassed       ScenarioAutomationStatus = "passed"
	ScenarioAutomationFailed       ScenarioAutomationStatus = "failed"
	ScenarioAutomationPartial      ScenarioAutomationStatus = "partial"
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
	ID              string              `json:"id"`
	Name            string              `json:"name"`
	Category        AutomationCategory  `json:"category,omitempty"`
	Framework       string              `json:"framework,omitempty"` // nextjs or vite
	Status          AutomationRunStatus `json:"status"`
	RepoID          string              `json:"repoId,omitempty"`
	Prompt          string              `json:"prompt,omitempty"`
	LastRunAt       string              `json:"lastRunAt,omitempty"`
	RunDurationMs   int64               `json:"runDurationMs,omitempty"`
	Steps           []RecordingStep     `json:"steps,omitempty"`
	VideoURL        string              `json:"videoUrl,omitempty"`
	ScreenshotURL   string              `json:"screenshotUrl,omitempty"`
	StepResults     []TestStepResult    `json:"stepResults,omitempty"`
	Log             string              `json:"log,omitempty"`
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
	ID               string                   `json:"id"`
	Order            int                      `json:"order"`
	Code             string                   `json:"code"`
	Title            string                   `json:"title"`
	Description      string                   `json:"description,omitempty"`
	PreCondition     string                   `json:"preCondition,omitempty"`
	Steps            []TestStepV2             `json:"steps"`
	Tags             []string                 `json:"tags"`
	Priority         Priority                 `json:"priority"`
	Type             string                   `json:"type"`
	ProcessingStatus ProcessingStatus         `json:"processingStatus,omitempty"`
	AutomationStatus TestCaseAutomationStatus `json:"automationStatus,omitempty"`
	AutomationType   *AutomationCategory      `json:"automationType"`
	AutomationTest   *AutomationTest          `json:"automationTest,omitempty"`
	Note             string                   `json:"note,omitempty"`
	CreatedAt        string                   `json:"createdAt"`
	UpdatedAt        string                   `json:"updatedAt"`
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
	TotalSections         int `json:"totalSections"`
	TotalTestCases        int `json:"totalTestCases"`
	TotalSteps            int `json:"totalSteps"`
	GeneratedCount        int `json:"generatedCount"`
	NotGeneratedCount     int `json:"notGeneratedCount"`
	GeneratingCount       int `json:"generatingCount"`
	GenerationFailedCount int `json:"generationFailedCount"`
	IdleCount             int `json:"idleCount"`
	RunningCount          int `json:"runningCount"`
	PassCount             int `json:"passCount"`
	FailCount             int `json:"failCount"`
	CoveragePercent       int `json:"coveragePercent"`

	// Deprecated compatibility field. Prefer GeneratedCount.
	AutomatedCount int `json:"automatedCount"`
}

type TestCaseSummary struct {
	ID           string `json:"id"`
	Code         string `json:"code"`
	Title        string `json:"title"`
	ErrorMessage string `json:"errorMessage,omitempty"`
}

type ActiveTestCases struct {
	Generating       []TestCaseSummary `json:"generating"`
	Running          []TestCaseSummary `json:"running"`
	FailedGeneration []TestCaseSummary `json:"failedGeneration"`
	FailedRun        []TestCaseSummary `json:"failedRun"`
}

// TestScenario is the top-level entity stored in Redis
type TestScenario struct {
	ID               string                   `json:"id"`
	Title            string                   `json:"title"`
	Description      string                   `json:"description,omitempty"`
	Sections         []TestSection            `json:"sections"`
	ProjectID        string                   `json:"projectId,omitempty"` // public QA project ID
	ProjectName      string                   `json:"projectName,omitempty"`
	IssueRepoID      string                   `json:"issueRepoId,omitempty"`
	SpecsRepoID      string                   `json:"specsRepoId,omitempty"`
	SourceType       string                   `json:"sourceType,omitempty"`
	SourcePath       string                   `json:"sourcePath,omitempty"`
	SourceSHA        string                   `json:"sourceSha,omitempty"`
	SourceDisplay    string                   `json:"sourceDisplay,omitempty"`
	ProcessingStatus ProcessingStatus         `json:"processingStatus,omitempty"`
	AutomationStatus ScenarioAutomationStatus `json:"automationStatus,omitempty"`
	Error            string                   `json:"error,omitempty"`
	Stats            *ScenarioStats           `json:"stats,omitempty"`
	AutomationStats  *ScenarioStats           `json:"automationStats,omitempty"`
	ActiveTestCases  *ActiveTestCases         `json:"activeTestCases,omitempty"`
	AuthConfig       AuthConfig               `json:"authConfig"`
	CreatorID        int                      `json:"creatorId,omitempty"`
	CreatedAt        time.Time                `json:"createdAt"`
	UpdatedAt        time.Time                `json:"updatedAt"`
	CreatedBy        string                   `json:"createdBy,omitempty"`

	// Internal: legacy parsed XLSX sheets (not used for Markdown-backed scenarios)
	Sheets []TestScenarioSheet `json:"sheets,omitempty"`
}

type ManualTestStatus string

const (
	ManualTestStatusPassed ManualTestStatus = "passed"
	ManualTestStatusFailed ManualTestStatus = "failed"
)

type ManualEvidenceFile struct {
	Name        string    `json:"name"`
	URL         string    `json:"url"`
	ContentType string    `json:"contentType,omitempty"`
	Size        int64     `json:"size,omitempty"`
	UploadedAt  time.Time `json:"uploadedAt"`
}

type ManualTestResult struct {
	ID          string               `json:"id"`
	ProjectID   string               `json:"projectId"`
	ScenarioID  string               `json:"scenarioId"`
	SectionID   string               `json:"sectionId,omitempty"`
	TestCaseID  string               `json:"testCaseId"`
	Status      ManualTestStatus     `json:"status"`
	Description string               `json:"description"`
	Evidence    []ManualEvidenceFile `json:"evidence"`
	TesterID    int                  `json:"testerId"`
	CreatedAt   time.Time            `json:"createdAt"`
	UpdatedAt   time.Time            `json:"updatedAt"`
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

func (s *TestScenario) ComputeStats() {
	stats := ScenarioStats{TotalSections: len(s.Sections)}
	active := ActiveTestCases{
		Generating:       []TestCaseSummary{},
		Running:          []TestCaseSummary{},
		FailedGeneration: []TestCaseSummary{},
		FailedRun:        []TestCaseSummary{},
	}
	for si := range s.Sections {
		stats.TotalTestCases += len(s.Sections[si].TestCases)
		for ti := range s.Sections[si].TestCases {
			tc := &s.Sections[si].TestCases[ti]
			stats.TotalSteps += len(tc.Steps)

			tc.ProcessingStatus = ProcessingStatusIdle
			tc.AutomationStatus = TestCaseAutomationNotGenerated

			if tc.AutomationTest == nil {
				stats.NotGeneratedCount++
				continue
			}

			generated := len(tc.AutomationTest.Steps) > 0
			if !generated {
				stats.NotGeneratedCount++
				switch tc.AutomationTest.Status {
				case AutomationStatusRunning:
					tc.ProcessingStatus = ProcessingStatusGenerating
					stats.GeneratingCount++
					appendActiveCase(&active.Generating, tc)
				case AutomationStatusFail:
					tc.ProcessingStatus = ProcessingStatusGenerationFailed
					stats.GenerationFailedCount++
					appendActiveCase(&active.FailedGeneration, tc)
				}
				continue
			}

			stats.GeneratedCount++
			stats.AutomatedCount++
			switch tc.AutomationTest.Status {
			case AutomationStatusRunning:
				tc.AutomationStatus = TestCaseAutomationRunning
				stats.RunningCount++
				appendActiveCase(&active.Running, tc)
			case AutomationStatusPass:
				tc.AutomationStatus = TestCaseAutomationPassed
				stats.PassCount++
			case AutomationStatusFail:
				tc.AutomationStatus = TestCaseAutomationFailed
				stats.FailCount++
				appendActiveCase(&active.FailedRun, tc)
			default:
				tc.AutomationStatus = TestCaseAutomationIdle
				stats.IdleCount++
			}
		}
	}
	if stats.TotalTestCases > 0 {
		stats.CoveragePercent = stats.GeneratedCount * 100 / stats.TotalTestCases
	}

	s.ProcessingStatus = ProcessingStatusIdle
	if stats.GeneratingCount > 0 {
		s.ProcessingStatus = ProcessingStatusGenerating
	} else if stats.GenerationFailedCount > 0 {
		s.ProcessingStatus = ProcessingStatusGenerationFailed
	}

	s.AutomationStatus = ScenarioAutomationNotGenerated
	if stats.GeneratedCount == 0 {
		s.AutomationStatus = ScenarioAutomationNotGenerated
	} else if stats.RunningCount > 0 {
		s.AutomationStatus = ScenarioAutomationRunning
	} else if stats.FailCount > 0 {
		s.AutomationStatus = ScenarioAutomationFailed
	} else if stats.GeneratedCount < stats.TotalTestCases {
		s.AutomationStatus = ScenarioAutomationPartial
	} else if stats.PassCount == stats.GeneratedCount {
		s.AutomationStatus = ScenarioAutomationPassed
	} else {
		s.AutomationStatus = ScenarioAutomationIdle
	}

	s.Stats = &stats
	s.AutomationStats = &stats
	s.ActiveTestCases = &active
}

func (s *TestScenario) ComputeAutomationSummary(activeLimit int) {
	s.ComputeStats()
	if activeLimit <= 0 || s.ActiveTestCases == nil {
		return
	}
	s.ActiveTestCases.Generating = limitTestCaseSummaries(s.ActiveTestCases.Generating, activeLimit)
	s.ActiveTestCases.Running = limitTestCaseSummaries(s.ActiveTestCases.Running, activeLimit)
	s.ActiveTestCases.FailedGeneration = limitTestCaseSummaries(s.ActiveTestCases.FailedGeneration, activeLimit)
	s.ActiveTestCases.FailedRun = limitTestCaseSummaries(s.ActiveTestCases.FailedRun, activeLimit)
}

func limitTestCaseSummaries(items []TestCaseSummary, limit int) []TestCaseSummary {
	if len(items) <= limit {
		return items
	}
	return items[:limit]
}

func appendActiveCase(items *[]TestCaseSummary, tc *TestCase) {
	summary := TestCaseSummary{ID: tc.ID, Code: tc.Code, Title: tc.Title}
	if tc.AutomationTest != nil {
		summary.ErrorMessage = tc.AutomationTest.ErrorMessage
	}
	*items = append(*items, summary)
}

// ─────────────────────────────────────────────
// API request/response types
// ─────────────────────────────────────────────

type UpdateScenarioRequest struct {
	Title       *string `json:"title,omitempty"`
	Description *string `json:"description,omitempty"`
}

type UpdateTestCaseRequest struct {
	Title        *string       `json:"title,omitempty"`
	Description  *string       `json:"description,omitempty"`
	PreCondition *string       `json:"preCondition,omitempty"`
	Steps        *[]TestStepV2 `json:"steps,omitempty"`
	Tags         *[]string     `json:"tags,omitempty"`
	Priority     *Priority     `json:"priority,omitempty"`
	Type         *string       `json:"type,omitempty"`
	Note         *string       `json:"note,omitempty"`
}

type CreateTestCaseRequest struct {
	Title        string       `json:"title"`
	Description  string       `json:"description,omitempty"`
	PreCondition string       `json:"preCondition,omitempty"`
	Steps        []TestStepV2 `json:"steps,omitempty"`
	Tags         []string     `json:"tags,omitempty"`
	Priority     Priority     `json:"priority,omitempty"`
	Type         string       `json:"type,omitempty"`
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

type GenerateAutomationRequest struct {
	Category       AutomationCategory `json:"category" binding:"required"`
	TestCaseIDs    []string           `json:"testCaseIds" binding:"required"`
	BackendRepoID  string             `json:"backendRepoId,omitempty"`
	FrontendRepoID string             `json:"frontendRepoId,omitempty"`
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
