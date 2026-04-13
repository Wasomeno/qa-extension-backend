package services

import (
	"fmt"
	"io"
	"regexp"
	"strings"

	"qa-extension-backend/internal/models"

	"github.com/xuri/excelize/v2"
)

// Route normalization helpers
var (
	// Trailing slash pattern
	trailingSlashRegex = regexp.MustCompile(`/?$`)
	// Leading slash pattern
	leadingSlashRegex  = regexp.MustCompile(`^/?`)
	// Route inference patterns - maps test case prefixes to routes
	routeInferencePatterns = []struct {
		prefix   string
		route    string
	}{
		{"test_create_", "/%s/create"},
		{"test_list_", "/%s/list"},
		{"test_edit_", "/%s/edit"},
		{"test_delete_", "/%s/delete"},
		{"test_view_", "/%s/view"},
		{"create_", "/%s/create"},
		{"list_", "/%s/list"},
		{"edit_", "/%s/edit"},
		{"delete_", "/%s/delete"},
		{"view_", "/%s/view"},
	}
)

// normalizeRoute normalizes a route string:
// - Adds leading slash if missing
// - Removes trailing slash
// - Converts backslashes to forward slashes
// - Trims whitespace
func normalizeRoute(route string) string {
	if route == "" {
		return ""
	}

	// Trim whitespace
	route = strings.TrimSpace(route)

	// Convert backslashes to forward slashes
	route = strings.ReplaceAll(route, "\\", "/")

	// Remove trailing slash
	route = trailingSlashRegex.ReplaceAllString(route, "")

	// Add leading slash if missing
	if !strings.HasPrefix(route, "/") {
		route = "/" + route
	}

	return route
}

// inferRouteFromName attempts to infer a route from the test case name
// using common naming conventions.
// e.g., "Test_Create_Invoice" -> "/invoice/create"
func inferRouteFromName(name string) string {
	if name == "" {
		return ""
	}

	// Normalize to lowercase for matching
	lowerName := strings.ToLower(name)

	// Remove "test_" prefix if present
	lowerName = strings.TrimPrefix(lowerName, "test_")

	// Split by underscore
	parts := strings.Split(lowerName, "_")

	// If we have at least 2 parts, try to build a route
	// Pattern: entity_action or action_entity
	if len(parts) >= 2 {
		// Check for action patterns at the start
		for _, pattern := range routeInferencePatterns {
			prefix := strings.TrimPrefix(pattern.prefix, "%s_")
			if strings.HasPrefix(lowerName, prefix) {
				// Extract entity from the remaining parts
				remaining := strings.TrimPrefix(lowerName, prefix)
				remainingParts := strings.Split(remaining, "_")
				if len(remainingParts) >= 1 {
					entity := remainingParts[0]
					return fmt.Sprintf(pattern.route, entity)
				}
			}
		}

		// Try reverse: entity_action pattern
		// Last part is action, rest is entity
		action := parts[len(parts)-1]
		entity := strings.Join(parts[:len(parts)-1], "_")
		if isActionWord(action) {
			return fmt.Sprintf("/%s/%s", entity, action)
		}
	}

	return ""
}

// isActionWord checks if a word is a common test action
func isActionWord(word string) bool {
	actions := map[string]bool{
		"create":  true,
		"list":   true,
		"view":   true,
		"edit":   true,
		"update": true,
		"delete": true,
		"submit": true,
		"search": true,
		"filter": true,
	}
	return actions[word]
}

// ParseXLSX reads an uploaded XLSX file and parses it into a structured array of TestScenarioSheet
func ParseXLSX(fileReader io.Reader) ([]models.TestScenarioSheet, error) {
	f, err := excelize.OpenReader(fileReader)
	if err != nil {
		return nil, fmt.Errorf("failed to open xlsx reader: %w", err)
	}
	defer f.Close()

	var sheets []models.TestScenarioSheet

	for _, sheetName := range f.GetSheetList() {
		testCases, err := parseSheet(f, sheetName)
		if err != nil {
			// Skip sheets that cannot be parsed (e.g., summary sheets without proper headers)
			continue
		}

		if len(testCases) > 0 {
			sheets = append(sheets, models.TestScenarioSheet{
				Name:      sheetName,
				TestCases: testCases,
			})
		}
	}

	return sheets, nil
}

func parseSheet(f *excelize.File, sheetName string) ([]models.ParsedTestCase, error) {
	rows, err := f.GetRows(sheetName)
	if err != nil {
		return nil, err
	}

	if len(rows) == 0 {
		return nil, nil
	}

	var testCases []models.ParsedTestCase

	// Find header row and column indices
	headerMap := make(map[string]int)
	headerRowIdx := -1

	for rIdx, row := range rows {
		// Stop looking for header after 20 rows
		if rIdx > 20 {
			break
		}

		for cIdx, cell := range row {
			val := strings.TrimSpace(strings.ToLower(cell))
			
			// V2 exact matches or fallbacks
			if val == "test id" || strings.Contains(val, "test case id") || val == "id" {
				headerMap["id"] = cIdx
			} else if val == "user story" || strings.Contains(val, "story") {
				headerMap["userstory"] = cIdx
			} else if val == "test type" || strings.Contains(val, "type") {
				headerMap["testtype"] = cIdx
			} else if val == "test scenario" || (strings.Contains(val, "test case") && !strings.Contains(val, "id")) {
				headerMap["name"] = cIdx
			} else if val == "route" || strings.Contains(val, "route path") || val == "path" {
				headerMap["route"] = cIdx
			} else if strings.Contains(val, "pre-condition") || strings.Contains(val, "precondition") {
				headerMap["precondition"] = cIdx
			} else if val == "test step" || strings.Contains(val, "step") {
				headerMap["action"] = cIdx
			} else if strings.Contains(val, "input data") || strings.Contains(val, "input") {
				headerMap["inputdata"] = cIdx
			} else if val == "result" || strings.Contains(val, "expected result") || strings.Contains(val, "expected") {
				headerMap["expectedresult"] = cIdx
			} else if strings.Contains(val, "status") {
				headerMap["status"] = cIdx
			} else if val == "additional note" || val == "note" || val == "notes" {
				headerMap["note"] = cIdx
			}
		}

		// Minimum required headers to consider it a valid test sheet
		if _, hasStep := headerMap["action"]; hasStep {
			if _, hasExpected := headerMap["expectedresult"]; hasExpected {
				headerRowIdx = rIdx
				break
			}
		}
	}

	if headerRowIdx == -1 {
		return nil, fmt.Errorf("could not find required headers in sheet %s", sheetName)
	}

	var currentTC *models.ParsedTestCase

	// Parse data rows
	for rIdx := headerRowIdx + 1; rIdx < len(rows); rIdx++ {
		row := rows[rIdx]

		// Helper to safely get column value
		getCol := func(key string) string {
			idx, ok := headerMap[key]
			if !ok || idx >= len(row) {
				return ""
			}
			return strings.TrimSpace(row[idx])
		}

		id := getCol("id")
		route := getCol("route")
		userStory := getCol("userstory")
		testType := getCol("testtype")
		name := getCol("name")
		preCond := getCol("precondition")
		action := getCol("action")
		inputData := getCol("inputdata")
		expected := getCol("expectedresult")
		status := getCol("status")
		note := getCol("note")

		// If row is completely empty, skip
		if id == "" && name == "" && action == "" && expected == "" {
			continue
		}

		// New test case begins if there is an ID, OR if there's a Name but no currentTC
		if id != "" || (name != "" && currentTC == nil) {
			if currentTC != nil {
				testCases = append(testCases, *currentTC)
			}

			// Normalize route - use provided route or infer from name
			normalizedRoute := normalizeRoute(route)
			if normalizedRoute == "" {
				normalizedRoute = inferRouteFromName(name)
			}

			currentTC = &models.ParsedTestCase{
				ID:           id,
				Route:        normalizedRoute,
				UserStory:    userStory,
				TestType:     testType,
				Name:         name,
				PreCondition: preCond,
				Status:       status,
				Note:         note,
				Steps:        []models.ParsedStep{},
			}
		}

		if currentTC != nil {
			// Update top-level info if missing but present here (some sheets merge cells vertically)
			if currentTC.Route == "" && route != "" {
				currentTC.Route = normalizeRoute(route)
			}
			if currentTC.UserStory == "" && userStory != "" {
				currentTC.UserStory = userStory
			}
			if currentTC.TestType == "" && testType != "" {
				currentTC.TestType = testType
			}
			if currentTC.Name == "" && name != "" {
				currentTC.Name = name
			}
			if currentTC.PreCondition == "" && preCond != "" {
				currentTC.PreCondition = preCond
			}

			// Add the step (even if empty action, it might just be an expected result row)
			if action != "" || expected != "" || inputData != "" {
				currentTC.Steps = append(currentTC.Steps, models.ParsedStep{
					Action:         action,
					InputData:      inputData,
					ExpectedResult: expected,
				})
			}
		}
	}

	if currentTC != nil {
		testCases = append(testCases, *currentTC)
	}

	return testCases, nil
}
