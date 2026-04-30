package services

import (
	"fmt"
	"strings"
	"time"

	"qa-extension-backend/internal/models"
)

// BuildScenarioFromXLSX converts parsed XLSX sheets into a fully populated TestScenario
// with sections, enriched test cases, and computed stats.
func BuildScenarioFromXLSX(
	fileName string,
	sheets []models.TestScenarioSheet,
	projectID string,
	projectName string,
	authConfig models.AuthConfig,
	creatorID int,
) models.TestScenario {
	now := time.Now()

	// Derive title from filename
	title := fileName
	if idx := strings.LastIndex(title, "."); idx > 0 {
		title = title[:idx]
	}

	createdBy := ""
	if creatorID != 0 {
		createdBy = fmt.Sprintf("User %d", creatorID)
	}

	scenario := models.TestScenario{
		ID:          "", // caller sets this
		Title:       title,
		Description: fmt.Sprintf("Imported from %s", fileName),
		ProjectID:   projectID,
		ProjectName: projectName,
		Status:      models.ScenarioStatusDraft,
		AuthConfig:  authConfig,
		CreatorID:   creatorID,
		CreatedAt:   now,
		UpdatedAt:   now,
		CreatedBy:   createdBy,
		Sheets:      sheets, // keep for generation
	}

	// Convert sheets → sections
	globalTCIndex := 0
	for sheetIdx, sheet := range sheets {
		section := models.TestSection{
			ID:    fmt.Sprintf("sec-%d-%d", now.UnixMilli()%100000, sheetIdx),
			Order: sheetIdx + 1,
			Title: sheet.Name,
		}

		for tcIdx, tc := range sheet.TestCases {
			globalTCIndex++
			section.TestCases = append(section.TestCases, convertParsedTestCase(tc, globalTCIndex, sheetIdx, tcIdx, now))
		}

		scenario.Sections = append(scenario.Sections, section)
	}

	scenario.ComputeStats()
	return scenario
}

func convertParsedTestCase(
	tc models.ParsedTestCase,
	globalIndex int,
	sheetIdx int,
	tcIdx int,
	now time.Time,
) models.TestCase {
	code := fmt.Sprintf("TC-%03d", globalIndex)
	nowStr := now.Format(time.RFC3339)

	var steps []models.TestStepV2
	for stepIdx, step := range tc.Steps {
		steps = append(steps, models.TestStepV2{
			ID:       fmt.Sprintf("st-%d-%d-%d-%d", now.UnixMilli()%100000, sheetIdx, tcIdx, stepIdx),
			Order:    stepIdx + 1,
			Action:   step.Action,
			Data:     step.InputData,
			Expected: step.ExpectedResult,
		})
	}

	return models.TestCase{
		ID:           tc.ID,
		Order:        tcIdx + 1,
		Code:         code,
		Title:        tc.Name,
		Description:  tc.UserStory,
		PreCondition: tc.PreCondition,
		Steps:        steps,
		Tags:         inferTags(tc),
		Priority:     inferPriority(tc),
		Type:         inferTestType(tc),
		Status:       mapTCStatus(tc.Status),
		Note:         tc.Note,
		CreatedAt:    nowStr,
		UpdatedAt:    nowStr,
	}
}

func inferPriority(tc models.ParsedTestCase) models.Priority {
	combined := strings.ToLower(tc.Name + " " + tc.Route + " " + tc.UserStory)

	if strings.Contains(combined, "critical") || strings.Contains(combined, "blocker") {
		return models.PriorityCritical
	}
	if strings.Contains(combined, "high") || strings.Contains(combined, "smoke") ||
		strings.Contains(combined, "login") || strings.Contains(combined, "auth") ||
		strings.Contains(combined, "payment") || strings.Contains(combined, "checkout") {
		return models.PriorityHigh
	}
	if strings.Contains(combined, "low") || strings.Contains(combined, "cosmetic") {
		return models.PriorityLow
	}
	return models.PriorityMedium
}

func inferTestType(tc models.ParsedTestCase) string {
	name := strings.ToLower(tc.Name)
	if strings.Contains(name, "invalid") || strings.Contains(name, "error") ||
		strings.Contains(name, "fail") || strings.Contains(name, "negative") ||
		strings.Contains(name, "unauthorized") || strings.Contains(name, "forbidden") ||
		strings.Contains(name, "empty") || strings.Contains(name, "missing") {
		return "negative"
	}
	return "positive"
}

func inferTags(tc models.ParsedTestCase) []string {
	tagSet := make(map[string]bool)

	if tc.Route != "" {
		for _, p := range strings.Split(strings.Trim(tc.Route, "/"), "/") {
			if p != "" {
				tagSet[strings.ToLower(p)] = true
			}
		}
	}
	if tc.TestType != "" {
		tagSet[strings.ToLower(tc.TestType)] = true
	}

	name := strings.ToLower(tc.Name)
	keywords := []string{
		"login", "logout", "register", "auth", "search", "filter", "create",
		"edit", "delete", "view", "list", "upload", "download", "export",
		"import", "approve", "reject", "submit", "validate", "notification",
		"email", "password", "profile", "settings", "dashboard", "report",
		"cart", "checkout", "payment", "order", "product", "catalog",
	}
	for _, kw := range keywords {
		if strings.Contains(name, kw) {
			tagSet[kw] = true
		}
	}
	tagSet["regression"] = true

	tags := make([]string, 0, len(tagSet))
	for t := range tagSet {
		tags = append(tags, t)
	}
	return tags
}

func mapTCStatus(status string) models.TestCaseStatus {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "ready", "approved", "active":
		return models.TCStatusReady
	case "blocked", "blocker":
		return models.TCStatusBlocked
	case "deprecated", "obsolete", "retired":
		return models.TCStatusDeprecated
	default:
		return models.TCStatusDraft
	}
}

// LinkAutomation links a generated automation to a matching test case using TestCaseID, then name fallback.
func LinkAutomation(scenario *models.TestScenario, auto *models.GeneratedAutomation) bool {
	// First try exact ID match
	if auto.TestCaseID != "" {
		for i := range scenario.Sections {
			for j := range scenario.Sections[i].TestCases {
				tc := &scenario.Sections[i].TestCases[j]
				if tc.ID == auto.TestCaseID || strings.HasPrefix(auto.TestCaseID, tc.ID) || strings.Contains(auto.TestCaseID, tc.ID) || strings.Contains(tc.ID, auto.TestCaseID) {
					tc.AutomationTest = &models.AutomationTest{
						ID:     fmt.Sprintf("auto-%s", auto.ID),
						Name:   auto.Name,
						Status: models.AutomationStatusIdle,
						Steps:  auto.Steps,
					}
					return true
				}
			}
		}
	}

	// Fallback to name similarity
	autoLower := strings.ToLower(auto.Name)
	for i := range scenario.Sections {
		for j := range scenario.Sections[i].TestCases {
			tc := &scenario.Sections[i].TestCases[j]
			if tc.AutomationTest != nil && len(tc.AutomationTest.Steps) > 0 {
				continue // already linked
			}
			tcLower := strings.ToLower(tc.Title)
			codeLower := strings.ToLower(tc.Code)
			if strings.Contains(autoLower, codeLower) ||
				strings.Contains(autoLower, strings.ReplaceAll(tcLower, " ", "_")) {
				tc.AutomationTest = &models.AutomationTest{
					ID:     fmt.Sprintf("auto-%s", auto.ID),
					Name:   auto.Name,
					Status: models.AutomationStatusIdle,
					Steps:  auto.Steps,
				}
				return true
			}
		}
	}
	// Fallback: link to first unlinked TC
	for i := range scenario.Sections {
		for j := range scenario.Sections[i].TestCases {
			tc := &scenario.Sections[i].TestCases[j]
			if tc.AutomationTest == nil || len(tc.AutomationTest.Steps) == 0 {
				tc.AutomationTest = &models.AutomationTest{
					ID:     fmt.Sprintf("auto-%s", auto.ID),
					Name:   auto.Name,
					Status: models.AutomationStatusIdle,
					Steps:  auto.Steps,
				}
				return true
			}
		}
	}
	return false
}

// GetTestCasesByIDs returns pointers to test cases matching the given IDs.
func GetTestCasesByIDs(scenario *models.TestScenario, ids []string) []*models.TestCase {
	idSet := make(map[string]bool, len(ids))
	for _, id := range ids {
		idSet[id] = true
	}
	var result []*models.TestCase
	for i := range scenario.Sections {
		for j := range scenario.Sections[i].TestCases {
			if idSet[scenario.Sections[i].TestCases[j].ID] {
				result = append(result, &scenario.Sections[i].TestCases[j])
			}
		}
	}
	return result
}

// GetTestCasesInSection returns all test cases in a specific section.
func GetTestCasesInSection(scenario *models.TestScenario, sectionID string) []*models.TestCase {
	for i := range scenario.Sections {
		if scenario.Sections[i].ID == sectionID {
			var result []*models.TestCase
			for j := range scenario.Sections[i].TestCases {
				result = append(result, &scenario.Sections[i].TestCases[j])
			}
			return result
		}
	}
	return nil
}
