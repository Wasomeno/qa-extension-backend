package models

import "time"

type ElementHints struct {
	Attributes map[string]string `json:"attributes"`
	TagName    string            `json:"tagName"`
}

type RecordingStep struct {
	Action             string       `json:"action"`
	Description        string       `json:"description"`
	ElementHints       ElementHints `json:"elementHints"`
	Selector           string       `json:"selector"`
	SelectorCandidates []string     `json:"selectorCandidates"`
	XPath              string       `json:"xpath,omitempty"`
	XPathCandidates    []string     `json:"xpathCandidates,omitempty"`
	Value              string       `json:"value,omitempty"`
	AssertionType      string       `json:"assertionType,omitempty"`
	ExpectedValue      string       `json:"expectedValue,omitempty"`
}

type ConsoleLogEntry struct {
	Level     string `json:"level"`
	Message   string `json:"message"`
	Timestamp int64  `json:"timestamp"`
	Source    string `json:"source,omitempty"`
}

type NetworkRequestEntry struct {
	RequestID   string            `json:"requestId"`
	URL         string            `json:"url"`
	Method      string            `json:"method"`
	Status      int               `json:"status,omitempty"`
	StatusText  string            `json:"statusText,omitempty"`
	RequestHeaders map[string]string `json:"requestHeaders,omitempty"`
	ResponseHeaders map[string]string `json:"responseHeaders,omitempty"`
	Timestamp   int64             `json:"timestamp"`
	DurationMs  int64             `json:"durationMs,omitempty"`
	Error       string            `json:"error,omitempty"`
}

type JSErrorEntry struct {
	Message   string `json:"message"`
	Source    string `json:"source,omitempty"`
	Line      int    `json:"line,omitempty"`
	Column    int    `json:"column,omitempty"`
	Stack     string `json:"stack,omitempty"`
	Timestamp int64  `json:"timestamp"`
}

type StorageSnapshot struct {
	Type      string            `json:"type"` // "localStorage" | "sessionStorage" | "cookies"
	Data      map[string]string `json:"data"`
	Timestamp int64             `json:"timestamp"`
}

type DOMMutationEntry struct {
	Type      string `json:"type"` // "childList" | "attributes" | "characterData"
	Target    string `json:"target"`
	Summary   string `json:"summary"`
	Timestamp int64  `json:"timestamp"`
}

type StepContext struct {
	StepIndex          int                   `json:"stepIndex"`
	Timestamp          int64                 `json:"timestamp"`
	Screenshot         string                `json:"screenshot,omitempty"`
	SurroundingLogs    []ConsoleLogEntry     `json:"surroundingLogs,omitempty"`
	SurroundingRequests []NetworkRequestEntry `json:"surroundingRequests,omitempty"`
	SurroundingErrors  []JSErrorEntry        `json:"surroundingErrors,omitempty"`
	DomMutationCount   int                   `json:"domMutationCount,omitempty"`
}

type SessionTelemetry struct {
	RecordingID       string                `json:"recordingId"`
	StartUrl          string                `json:"startUrl"`
	StartTime         int64                 `json:"startTime"`
	EndTime           int64                 `json:"endTime,omitempty"`
	BrowserContext    BrowserContext        `json:"browserContext"`
	ConsoleLogs       []ConsoleLogEntry     `json:"consoleLogs,omitempty"`
	NetworkRequests   []NetworkRequestEntry `json:"networkRequests,omitempty"`
	JSErrors          []JSErrorEntry        `json:"jsErrors,omitempty"`
	StorageSnapshots  []StorageSnapshot     `json:"storageSnapshots,omitempty"`
	DOMMutations      []DOMMutationEntry    `json:"domMutations,omitempty"`
	StepsWithContext  []StepContext         `json:"stepsWithContext,omitempty"`
}

type BrowserContext struct {
	UserAgent string `json:"userAgent"`
	Viewport  struct {
		Width  int `json:"width"`
		Height int `json:"height"`
	} `json:"viewport"`
	URL string `json:"url"`
}

type TestRecording struct {
	ID              string           `json:"id"`
	Name            string           `json:"name"`
	Description     string           `json:"description"`
	Status          string           `json:"status"`
	SourceType      string           `json:"source_type,omitempty"` // "manual" | "test_scenario"
	SourceID        string           `json:"source_id,omitempty"`   // Links to test_scenario ID if source_type is "test_scenario"
	ProjectID       string           `json:"project_id,omitempty"`
	ProjectName     string           `json:"project_name,omitempty"`
	ProjectDetails  *ProjectDetails  `json:"projectDetails,omitempty"`
	IssueID         string           `json:"issue_id,omitempty"`
	CreatorID       int              `json:"creator_id,omitempty"`
	VideoURL        string           `json:"video_url,omitempty"`
	Steps           []RecordingStep  `json:"steps"`
	Parameters      []any            `json:"parameters"`
	Telemetry       *SessionTelemetry `json:"telemetry,omitempty"`
	CreatedAt       time.Time        `json:"created_at"`
}

// RecordingSummary is a lightweight version of TestRecording for list responses
// It excludes Steps and Parameters to keep response size manageable
type RecordingSummary struct {
	ID             string          `json:"id"`
	Name           string          `json:"name"`
	Description    string          `json:"description"`
	Status         string          `json:"status"`
	SourceType     string          `json:"source_type,omitempty"` // "manual" | "test_scenario"
	SourceID       string          `json:"source_id,omitempty"`   // Links to test_scenario ID if source_type is "test_scenario"
	ProjectID      string          `json:"project_id,omitempty"`
	ProjectName    string          `json:"project_name,omitempty"`
	ProjectDetails *ProjectDetails `json:"projectDetails,omitempty"`
	IssueID        string          `json:"issue_id,omitempty"`
	CreatorID      int             `json:"creator_id,omitempty"`
	VideoURL       string          `json:"video_url,omitempty"`
	StepCount      int             `json:"stepCount"`
	CreatedAt      time.Time       `json:"created_at"`
}

type TestStepResult struct {
	StepIndex  int    `json:"stepIndex"`
	Status     string `json:"status"` // "success", "failure"
	Error      string `json:"error,omitempty"`
	Screenshot string `json:"screenshot,omitempty"` // Base64 or URL
}

type TestResult struct {
	TestID      string           `json:"testId"`
	Status      string           `json:"status"` // "passed", "failed"
	StepResults []TestStepResult `json:"stepResults"`
	Log         string           `json:"log,omitempty"`
	VideoURL    string           `json:"videoUrl,omitempty"`
}
