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
	Value              string       `json:"value,omitempty"`
	AssertionType      string       `json:"assertionType,omitempty"`
	ExpectedValue      string       `json:"expectedValue,omitempty"`
}

type TestRecording struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Status      string          `json:"status"`
	ProjectID   string          `json:"project_id,omitempty"`
	IssueID     string          `json:"issue_id,omitempty"`
	VideoURL    string          `json:"video_url,omitempty"`
	Steps       []RecordingStep `json:"steps"`
	Parameters  []any           `json:"parameters"`
	CreatedAt   time.Time       `json:"created_at"`
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
