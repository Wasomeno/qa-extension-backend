package services

import (
	"fmt"
	"strings"

	"qa-extension-backend/internal/models"
)

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

// LinkAutomation links a generated automation to a matching test case using TestCaseID, then name fallback.
func LinkAutomation(scenario *models.TestScenario, auto *models.GeneratedAutomation) bool {
	// First try exact ID match
	if auto.TestCaseID != "" {
		for i := range scenario.Sections {
			for j := range scenario.Sections[i].TestCases {
				tc := &scenario.Sections[i].TestCases[j]
				if tc.ID == auto.TestCaseID || strings.HasPrefix(auto.TestCaseID, tc.ID) || strings.Contains(auto.TestCaseID, tc.ID) || strings.Contains(tc.ID, auto.TestCaseID) {
					tc.AutomationTest = &models.AutomationTest{
						ID:        fmt.Sprintf("auto-%s", auto.ID),
						Name:      auto.Name,
						Category:  models.AutomationCategoryE2E,
						Framework: auto.Framework,
						Status:    models.AutomationStatusIdle,
						Steps:     auto.Steps,
					}
					tc.AutomationType = models.AutomationCategoryPtr(models.AutomationCategoryE2E)
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
					ID:        fmt.Sprintf("auto-%s", auto.ID),
					Name:      auto.Name,
					Category:  models.AutomationCategoryE2E,
					Framework: auto.Framework,
					Status:    models.AutomationStatusIdle,
					Steps:     auto.Steps,
				}
				tc.AutomationType = models.AutomationCategoryPtr(models.AutomationCategoryE2E)
				return true
			}
		}
	}
	return false
}
