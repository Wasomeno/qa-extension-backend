package models

import (
	"time"
)

// AuthConfig stores the authentication configuration for the test generator
type AuthConfig struct {
	BaseURL  string `json:"baseUrl"`
	LoginURL string `json:"loginUrl"`
	Username string `json:"username"`
	Password string `json:"password"`
}

// ParsedStep represents a single step within a test case extracted from XLSX
type ParsedStep struct {
	Action         string `json:"action"`
	InputData      string `json:"inputData"`
	ExpectedResult string `json:"expectedResult"`
}

// ParsedTestCase represents a single test case extracted from the XLSX
type ParsedTestCase struct {
	ID           string       `json:"id"`
	Name         string       `json:"name"`
	PreCondition string       `json:"preCondition"`
	Steps        []ParsedStep `json:"steps"`
	Status       string       `json:"status"` // Original status from XLSX
	Note         string       `json:"note"`
}

// TestScenarioSheet represents a single sheet within an XLSX file
type TestScenarioSheet struct {
	Name      string           `json:"name"`
	TestCases []ParsedTestCase `json:"testCases"`
}

// TestScenarioRecording represents a simplified link to a generated recording
type TestScenarioRecording struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// TestScenario is the top-level entity representing an uploaded document
type TestScenario struct {
	ID             string                  `json:"id"`
	FileName       string                  `json:"fileName"`
	ProjectID      string                  `json:"projectId,omitempty"`
	Sheets         []TestScenarioSheet     `json:"sheets"`
	GeneratedTests []TestScenarioRecording `json:"generatedTests"` // Simplified recordings info
	Status         string                  `json:"status"`         // "uploaded", "generating", "ready", "failed"
	Error          string                  `json:"error,omitempty"`
	AuthConfig     AuthConfig              `json:"authConfig"`
	CreatedAt      time.Time               `json:"createdAt"`
}
