package services

import (
	"fmt"
	"io"
	"strings"

	"qa-extension-backend/internal/models"

	"github.com/xuri/excelize/v2"
)

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
			currentTC = &models.ParsedTestCase{
				ID:           id,
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
