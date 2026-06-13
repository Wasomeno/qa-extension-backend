package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"qa-extension-backend/agent"
	"qa-extension-backend/client"
	"qa-extension-backend/database"
	"qa-extension-backend/identity"
	"qa-extension-backend/internal/models"
	"qa-extension-backend/services"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"golang.org/x/oauth2"
)

// pluralize returns "s" if n != 1, else ""
func pluralize(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func routeProjectID(c *gin.Context) string {
	if strings.Contains(c.FullPath(), "/projects/:id/") {
		return c.Param("id")
	}
	return c.Param("project_id")
}

func routeScenarioID(c *gin.Context) string {
	if id := c.Param("scenario_id"); id != "" {
		return id
	}
	return c.Param("id")
}

func ensureScenarioProject(c *gin.Context, scenario models.TestScenario) bool {
	projectID := routeProjectID(c)
	if projectID == "" {
		return true
	}
	if scenario.ProjectID != projectID {
		c.JSON(http.StatusNotFound, gin.H{"error": "scenario not found in project"})
		return false
	}
	return true
}

// ListScenarios lists all test scenarios
func ListScenarios(c *gin.Context) {
	ctx := c.Request.Context()
	projectID := routeProjectID(c)
	search := c.Query("search")
	status := c.Query("status") // Backward-compatible alias for processing_status or automation_status
	processingStatus := c.Query("processing_status")
	automationStatus := c.Query("automation_status")
	sortBy := c.Query("sort_by") // "created_at", "title", "processing_status", "automation_status"
	order := c.Query("order")    // "asc", "desc"
	page := 1
	limit := 20

	if p := c.Query("page"); p != "" {
		if parsed, err := strconv.Atoi(p); err == nil && parsed > 0 {
			page = parsed
		}
	}
	if p := c.Query("per_page"); p != "" {
		if parsed, err := strconv.Atoi(p); err == nil && parsed > 0 && parsed <= 100 {
			limit = parsed
		}
	}

	var ids []string
	var err error

	if projectID != "" {
		ids, err = database.RedisClient.SMembers(ctx, fmt.Sprintf("scenarios:project:%s", projectID)).Result()
	} else {
		ids, err = database.RedisClient.SMembers(ctx, "scenarios").Result()
	}

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list scenarios"})
		return
	}

	var scenarios []models.TestScenario
	processedIDs := make(map[string]bool)

	for _, id := range ids {
		if processedIDs[id] {
			continue
		}
		val, err := database.RedisClient.Get(ctx, fmt.Sprintf("scenario:%s", id)).Result()
		if err == nil {
			var s models.TestScenario
			if json.Unmarshal([]byte(val), &s) == nil {
				if projectID != "" && s.ProjectID != projectID {
					continue
				}
				// For lists, we don't need the full parsed sheets or massive test cases payload.
				// Compute a compact automation summary and clear out the heavy parts.
				s.ComputeAutomationSummary(5)
				s.Sections = nil
				s.Sheets = nil
				scenarios = append(scenarios, s)
				processedIDs[id] = true
			}
		}
	}

	// Apply filters
	if status != "" {
		processingStatus = status
		automationStatus = status
	}
	if status != "" {
		filtered := make([]models.TestScenario, 0)
		for _, s := range scenarios {
			if string(s.ProcessingStatus) == processingStatus || string(s.AutomationStatus) == automationStatus {
				filtered = append(filtered, s)
			}
		}
		scenarios = filtered
	}
	if status == "" && processingStatus != "" {
		filtered := make([]models.TestScenario, 0)
		for _, s := range scenarios {
			if string(s.ProcessingStatus) == processingStatus {
				filtered = append(filtered, s)
			}
		}
		scenarios = filtered
	}
	if status == "" && automationStatus != "" {
		filtered := make([]models.TestScenario, 0)
		for _, s := range scenarios {
			if string(s.AutomationStatus) == automationStatus {
				filtered = append(filtered, s)
			}
		}
		scenarios = filtered
	}

	// Apply search
	if search != "" {
		searchLower := strings.ToLower(search)
		filtered := make([]models.TestScenario, 0)
		for _, s := range scenarios {
			if strings.Contains(strings.ToLower(s.Title), searchLower) ||
				strings.Contains(strings.ToLower(s.ProjectName), searchLower) {
				filtered = append(filtered, s)
			}
		}
		scenarios = filtered
	}

	// Default sort
	if sortBy == "" {
		sortBy = "created_at"
	}
	if order == "" {
		order = "desc"
	}

	sort.Slice(scenarios, func(i, j int) bool {
		var condition bool
		switch sortBy {
		case "title", "file_name":
			if order == "asc" {
				condition = scenarios[i].Title < scenarios[j].Title
			} else {
				condition = scenarios[i].Title > scenarios[j].Title
			}
		case "processing_status", "processingStatus":
			if order == "asc" {
				condition = scenarios[i].ProcessingStatus < scenarios[j].ProcessingStatus
			} else {
				condition = scenarios[i].ProcessingStatus > scenarios[j].ProcessingStatus
			}
		case "automation_status", "automationStatus", "status":
			if order == "asc" {
				condition = scenarios[i].AutomationStatus < scenarios[j].AutomationStatus
			} else {
				condition = scenarios[i].AutomationStatus > scenarios[j].AutomationStatus
			}
		case "created_at":
			fallthrough
		default:
			if order == "asc" {
				condition = scenarios[i].CreatedAt.Before(scenarios[j].CreatedAt)
			} else {
				condition = scenarios[i].CreatedAt.After(scenarios[j].CreatedAt)
			}
		}
		return condition
	})

	total := len(scenarios)
	totalPages := (total + limit - 1) / limit
	start := (page - 1) * limit
	end := start + limit

	if start >= total {
		c.JSON(http.StatusOK, gin.H{
			"data":       []models.TestScenario{},
			"pagination": gin.H{"page": page, "per_page": limit, "total": total, "totalPages": totalPages},
		})
		return
	}
	if end > total {
		end = total
	}

	c.JSON(http.StatusOK, gin.H{
		"data":       scenarios[start:end],
		"pagination": gin.H{"page": page, "per_page": limit, "total": total, "totalPages": totalPages},
	})
}

// GetScenario gets a specific test scenario
func GetScenario(c *gin.Context) {
	id := routeScenarioID(c)
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "scenario id is required"})
		return
	}

	scenario, err := getScenario(c.Request.Context(), id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "scenario not found"})
		return
	}

	if !ensureScenarioProject(c, scenario) {
		return
	}

	// Ensure stats are computed
	scenario.ComputeStats()

	// Exclude sheets from response to save bandwidth
	scenario.Sheets = nil

	c.JSON(http.StatusOK, scenario)
}

// UpdateScenario updates top-level scenario fields
func UpdateScenario(c *gin.Context) {
	id := routeScenarioID(c)
	var req models.UpdateScenarioRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx := c.Request.Context()
	scenario, err := getScenario(ctx, id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "scenario not found"})
		return
	}
	if !ensureScenarioProject(c, scenario) {
		return
	}

	if req.Title != nil {
		scenario.Title = *req.Title
	}
	if req.Description != nil {
		scenario.Description = *req.Description
	}

	scenario.UpdatedAt = time.Now()
	saveScenario(ctx, &scenario)

	scenario.Sheets = nil
	c.JSON(http.StatusOK, scenario)
}

// UpdateTestCase updates fields of a specific test case
func UpdateTestCase(c *gin.Context) {
	id := routeScenarioID(c)
	sectionID := c.Param("sectionId")
	tcID := c.Param("tcId")

	var req models.UpdateTestCaseRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx := c.Request.Context()
	scenario, err := getScenario(ctx, id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "scenario not found"})
		return
	}
	if !ensureScenarioProject(c, scenario) {
		return
	}

	updated := false
	for i := range scenario.Sections {
		if scenario.Sections[i].ID == sectionID {
			for j := range scenario.Sections[i].TestCases {
				if scenario.Sections[i].TestCases[j].ID == tcID {
					tc := &scenario.Sections[i].TestCases[j]

					if req.Title != nil {
						tc.Title = *req.Title
					}
					if req.Description != nil {
						tc.Description = *req.Description
					}
					if req.PreCondition != nil {
						tc.PreCondition = *req.PreCondition
					}
					if req.Tags != nil {
						tc.Tags = *req.Tags
					}
					if req.Priority != nil {
						tc.Priority = *req.Priority
					}
					if req.Type != nil {
						tc.Type = *req.Type
					}
					if req.Note != nil {
						tc.Note = *req.Note
					}
					if req.Steps != nil {
						tc.Steps = *req.Steps
						// Enforce order
						for k := range tc.Steps {
							tc.Steps[k].Order = k + 1
						}
					}

					tc.UpdatedAt = time.Now().Format(time.RFC3339)
					updated = true
					break
				}
			}
			break
		}
	}

	if !updated {
		c.JSON(http.StatusNotFound, gin.H{"error": "section or test case not found"})
		return
	}

	scenario.UpdatedAt = time.Now()
	scenario.ComputeStats()
	saveScenario(ctx, &scenario)

	scenario.Sheets = nil
	c.JSON(http.StatusOK, scenario)
}

// UpdateTestCaseAutomationCategory updates only a test case's automation category.
// The request body accepts either {"category":"api|e2e|manual"} or
// {"automationType":"api|e2e|manual"}. Send null or an empty string to clear it.
func UpdateTestCaseAutomationCategory(c *gin.Context) {
	id := routeScenarioID(c)
	sectionID := c.Param("sectionId")
	tcID := c.Param("tcId")

	if id == "" || tcID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "scenario id and test case id are required"})
		return
	}

	var payload map[string]json.RawMessage
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	rawCategory, ok := payload["category"]
	if !ok {
		rawCategory, ok = payload["automationType"]
	}
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "category is required"})
		return
	}

	category, err := parseNullableAutomationCategory(rawCategory)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx := c.Request.Context()
	scenario, err := getScenario(ctx, id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "scenario not found"})
		return
	}
	if !ensureScenarioProject(c, scenario) {
		return
	}

	var updatedTestCase *models.TestCase
	for i := range scenario.Sections {
		if sectionID != "" && scenario.Sections[i].ID != sectionID {
			continue
		}
		for j := range scenario.Sections[i].TestCases {
			if scenario.Sections[i].TestCases[j].ID != tcID {
				continue
			}

			tc := &scenario.Sections[i].TestCases[j]
			tc.AutomationType = category
			if tc.AutomationTest != nil {
				if category == nil {
					tc.AutomationTest.Category = ""
				} else {
					tc.AutomationTest.Category = *category
				}
			}
			tc.UpdatedAt = time.Now().Format(time.RFC3339)
			updatedTestCase = tc
			break
		}
		if updatedTestCase != nil {
			break
		}
	}

	if updatedTestCase == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "test case not found"})
		return
	}

	scenario.UpdatedAt = time.Now()
	scenario.ComputeStats()
	if err := saveScenario(ctx, &scenario); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save scenario"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"testCase": updatedTestCase})
}

func parseNullableAutomationCategory(raw json.RawMessage) (*models.AutomationCategory, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "null" || trimmed == `""` {
		return nil, nil
	}

	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, fmt.Errorf("category must be api, e2e, manual, null, or an empty string")
	}

	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return nil, nil
	}

	category := models.AutomationCategory(value)
	switch category {
	case models.AutomationCategoryAPI, models.AutomationCategoryE2E, models.AutomationCategoryManual:
		return models.AutomationCategoryPtr(category), nil
	default:
		return nil, fmt.Errorf("category must be api, e2e, manual, null, or an empty string")
	}
}

// ReorderTestCases updates the order of test cases in a section
func ReorderTestCases(c *gin.Context) {
	id := routeScenarioID(c)
	sectionID := c.Param("sectionId")

	var req models.ReorderTestCasesRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx := c.Request.Context()
	scenario, err := getScenario(ctx, id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "scenario not found"})
		return
	}
	if !ensureScenarioProject(c, scenario) {
		return
	}

	updated := false
	for i := range scenario.Sections {
		if scenario.Sections[i].ID == sectionID {
			// Create map for fast lookup
			tcMap := make(map[string]*models.TestCase)
			for j := range scenario.Sections[i].TestCases {
				tcMap[scenario.Sections[i].TestCases[j].ID] = &scenario.Sections[i].TestCases[j]
			}

			// Rebuild array based on requested order
			var newCases []models.TestCase
			for idx, tcID := range req.OrderedIDs {
				if tc, ok := tcMap[tcID]; ok {
					tc.Order = idx + 1
					newCases = append(newCases, *tc)
					delete(tcMap, tcID)
				}
			}

			// Append any remaining test cases that weren't in the ordered list
			idx := len(newCases)
			for _, tc := range tcMap {
				tc.Order = idx + 1
				newCases = append(newCases, *tc)
				idx++
			}

			scenario.Sections[i].TestCases = newCases
			updated = true
			break
		}
	}

	if !updated {
		c.JSON(http.StatusNotFound, gin.H{"error": "section not found"})
		return
	}

	scenario.UpdatedAt = time.Now()
	saveScenario(ctx, &scenario)

	scenario.Sheets = nil
	c.JSON(http.StatusOK, scenario)
}

// AddTestCase adds a new test case to a section
func AddTestCase(c *gin.Context) {
	id := routeScenarioID(c)
	sectionID := c.Param("sectionId")

	var req models.CreateTestCaseRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx := c.Request.Context()
	scenario, err := getScenario(ctx, id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "scenario not found"})
		return
	}
	if !ensureScenarioProject(c, scenario) {
		return
	}

	updated := false
	for i := range scenario.Sections {
		if scenario.Sections[i].ID == sectionID {
			now := time.Now().Format(time.RFC3339)

			// Enforce step orders
			for k := range req.Steps {
				req.Steps[k].Order = k + 1
				if req.Steps[k].ID == "" {
					req.Steps[k].ID = models.NewTestStepID()
				}
			}

			// Generate TC-XXX code
			maxCode := 0
			for _, s := range scenario.Sections {
				for _, tc := range s.TestCases {
					if strings.HasPrefix(tc.Code, "TC-") {
						var num int
						fmt.Sscanf(tc.Code, "TC-%d", &num)
						if num > maxCode {
							maxCode = num
						}
					}
				}
			}
			code := fmt.Sprintf("TC-%03d", maxCode+1)

			newTC := models.TestCase{
				ID:           models.NewTestCaseID(),
				Order:        len(scenario.Sections[i].TestCases) + 1,
				Code:         code,
				Title:        req.Title,
				Description:  req.Description,
				PreCondition: req.PreCondition,
				Steps:        req.Steps,
				Tags:         req.Tags,
				Priority:     req.Priority,
				Type:         req.Type,
				CreatedAt:    now,
				UpdatedAt:    now,
			}

			if newTC.Priority == "" {
				newTC.Priority = models.PriorityMedium
			}
			if newTC.Type == "" {
				newTC.Type = "positive"
			}
			scenario.Sections[i].TestCases = append(scenario.Sections[i].TestCases, newTC)
			updated = true
			break
		}
	}

	if !updated {
		c.JSON(http.StatusNotFound, gin.H{"error": "section not found"})
		return
	}

	scenario.UpdatedAt = time.Now()
	scenario.ComputeStats()
	saveScenario(ctx, &scenario)

	scenario.Sheets = nil
	c.JSON(http.StatusOK, scenario)
}

// DeleteTestCase removes a test case
func DeleteTestCase(c *gin.Context) {
	id := routeScenarioID(c)
	sectionID := c.Param("sectionId")
	tcID := c.Param("tcId")

	ctx := c.Request.Context()
	scenario, err := getScenario(ctx, id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "scenario not found"})
		return
	}
	if !ensureScenarioProject(c, scenario) {
		return
	}

	updated := false
	for i := range scenario.Sections {
		if scenario.Sections[i].ID == sectionID {
			var newCases []models.TestCase
			for j := range scenario.Sections[i].TestCases {
				tc := &scenario.Sections[i].TestCases[j]
				if tc.ID == tcID {
					updated = true
				} else {
					tc.Order = len(newCases) + 1
					newCases = append(newCases, *tc)
				}
			}
			scenario.Sections[i].TestCases = newCases
			break
		}
	}

	if !updated {
		c.JSON(http.StatusNotFound, gin.H{"error": "section or test case not found"})
		return
	}

	scenario.UpdatedAt = time.Now()
	scenario.ComputeStats()
	saveScenario(ctx, &scenario)

	scenario.Sheets = nil
	c.JSON(http.StatusOK, scenario)
}

// DeleteScenario deletes a scenario
func DeleteScenario(c *gin.Context) {
	id := routeScenarioID(c)
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "scenario id is required"})
		return
	}

	ctx := c.Request.Context()
	scenario, err := getScenario(ctx, id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "scenario not found"})
		return
	}
	if !ensureScenarioProject(c, scenario) {
		return
	}

	err = database.RedisClient.Del(ctx, fmt.Sprintf("scenario:%s", id)).Err()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete scenario from redis"})
		return
	}

	database.RedisClient.SRem(ctx, "scenarios", id)
	database.RedisClient.SRem(ctx, fmt.Sprintf("scenarios:project:%s", scenario.ProjectID), id)
	if scenario.CreatorID != 0 {
		database.RedisClient.SRem(ctx, fmt.Sprintf("scenarios:user:%d", scenario.CreatorID), id)
	} else {
		database.RedisClient.SRem(ctx, "scenarios:legacy", id)
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "scenario deleted successfully",
		"id":      id,
	})
}

// BulkDeleteScenarios deletes multiple test scenarios by their IDs
func BulkDeleteScenarios(c *gin.Context) {
	var req struct {
		IDs []string `json:"ids"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if len(req.IDs) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ids array is required and cannot be empty"})
		return
	}

	ctx := c.Request.Context()
	deletedCount := 0
	var notFound []string
	var errors []string

	projectID := routeProjectID(c)
	for _, id := range req.IDs {
		// Check if scenario exists
		scenario, err := getScenario(ctx, id)
		if err != nil {
			notFound = append(notFound, id)
			continue
		}
		if projectID != "" && scenario.ProjectID != projectID {
			notFound = append(notFound, id)
			continue
		}

		err = database.RedisClient.Del(ctx, fmt.Sprintf("scenario:%s", id)).Err()
		if err != nil {
			errors = append(errors, fmt.Sprintf("%s: %v", id, err))
			continue
		}

		database.RedisClient.SRem(ctx, "scenarios", id)
		database.RedisClient.SRem(ctx, fmt.Sprintf("scenarios:project:%s", scenario.ProjectID), id)
		if scenario.CreatorID != 0 {
			database.RedisClient.SRem(ctx, fmt.Sprintf("scenarios:user:%d", scenario.CreatorID), id)
		} else {
			database.RedisClient.SRem(ctx, "scenarios:legacy", id)
		}
		deletedCount++
	}

	c.JSON(http.StatusOK, gin.H{
		"message":      fmt.Sprintf("deleted %d scenarios", deletedCount),
		"deletedCount": deletedCount,
		"notFound":     notFound,
		"errors":       errors,
	})
}

// GenerateTests triggers AI generation. It can take sectionIds or testCaseIds.
func GenerateTests(c *gin.Context) {
	id := routeScenarioID(c)
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "scenario id is required"})
		return
	}

	var req struct {
		SectionIDs  []string `json:"sectionIds"`
		TestCaseIDs []string `json:"testCaseIds"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if len(req.SectionIDs) == 0 && len(req.TestCaseIDs) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "must provide sectionIds or testCaseIds"})
		return
	}

	ctx := c.Request.Context()
	scenario, err := getScenario(ctx, id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "scenario not found"})
		return
	}
	if !ensureScenarioProject(c, scenario) {
		return
	}

	// Resolve the list of target test case IDs based on the request
	var targetTestCaseIDs []string

	if len(req.TestCaseIDs) > 0 {
		targetTestCaseIDs = req.TestCaseIDs
	} else if len(req.SectionIDs) > 0 {
		for _, sId := range req.SectionIDs {
			for _, sec := range scenario.Sections {
				if sec.ID == sId {
					for _, tc := range sec.TestCases {
						targetTestCaseIDs = append(targetTestCaseIDs, tc.ID)
					}
				}
			}
		}
	}

	if len(targetTestCaseIDs) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no matching test cases found"})
		return
	}

	token, ok := c.MustGet("token").(*oauth2.Token)
	if !ok {
		scenario.Error = "unauthorized: missing GitLab token"
		saveScenario(ctx, &scenario)
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	sessionID, _ := c.Get("session_id")
	authSessionID, _ := sessionID.(string)
	if authSessionID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized: missing session"})
		return
	}

	_, err = client.GetClient(ctx, token, nil)
	if err != nil {
		scenario.Error = fmt.Sprintf("failed to get gitlab client: %v", err)
		saveScenario(ctx, &scenario)
		c.JSON(http.StatusInternalServerError, gin.H{"error": scenario.Error})
		return
	}

	setAutomationCategory(&scenario, targetTestCaseIDs, models.AutomationCategoryE2E, scenario.GitLabSpecsProjectID(), models.AutomationStatusRunning)
	if err := saveScenario(ctx, &scenario); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save scenario"})
		return
	}

	job, err := agent.EnqueueE2EGenerationJob(ctx, agent.EnqueueE2EGenerationJobInput{
		ScenarioID:    id,
		RepoID:        scenario.GitLabSpecsProjectID(),
		TestCaseIDs:   targetTestCaseIDs,
		AuthSessionID: authSessionID,
	})
	if err != nil {
		setTestCasesAutomationStatus(ctx, id, targetTestCaseIDs, models.AutomationStatusFail)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to enqueue generation job"})
		return
	}

	c.JSON(http.StatusAccepted, gin.H{"message": "generation started", "id": id, "jobId": job.ID})
}

// GenerateTestCaseAutomations creates automation artifacts for selected test cases.
func GenerateTestCaseAutomations(c *gin.Context) {
	scenarioID := routeScenarioID(c)
	if scenarioID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "scenario id is required"})
		return
	}

	var req models.GenerateAutomationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if len(req.TestCaseIDs) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "testCaseIds is required"})
		return
	}

	ctx := c.Request.Context()
	scenario, err := getScenario(ctx, scenarioID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "scenario not found"})
		return
	}
	if !ensureScenarioProject(c, scenario) {
		return
	}

	switch req.Category {
	case models.AutomationCategoryAPI:
		generateAPIAutomationPrompts(c, &scenario, req)
	case models.AutomationCategoryE2E:
		generateE2EAutomations(c, &scenario, req)
	case models.AutomationCategoryManual:
		markManualAutomation(c, &scenario, req.TestCaseIDs)
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "category must be api, e2e, or manual"})
	}
}

func generateAPIAutomationPrompts(c *gin.Context, scenario *models.TestScenario, req models.GenerateAutomationRequest) {
	if strings.TrimSpace(req.BackendRepoID) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "backendRepoId is required for api automation"})
		return
	}

	ctx := c.Request.Context()
	backendRepoName := ""
	if tokenValue, exists := c.Get("token"); exists {
		if token, ok := tokenValue.(*oauth2.Token); ok {
			if glClient, err := client.GetClient(ctx, token, nil); err == nil {
				if project, _, err := glClient.Projects.GetProject(req.BackendRepoID, nil); err == nil && project != nil {
					backendRepoName = project.PathWithNamespace
				}
			}
		}
	}

	targets := make(map[string]bool, len(req.TestCaseIDs))
	for _, id := range req.TestCaseIDs {
		targets[id] = true
	}
	prompts := make([]gin.H, 0, len(req.TestCaseIDs))
	found := 0
	now := time.Now().Format(time.RFC3339)
	for si := range scenario.Sections {
		for ti := range scenario.Sections[si].TestCases {
			tc := &scenario.Sections[si].TestCases[ti]
			if !targets[tc.ID] {
				continue
			}
			found++
			prompt, err := services.GenerateAPITestPromptWithLLM(ctx, *scenario, *tc, req.BackendRepoID, backendRepoName)
			if err != nil {
				log.Printf("[GenerateAPIAutomationPrompts] failed to generate API test prompt for test case %s: %v", tc.ID, err)
				c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate API test prompt"})
				return
			}
			tc.AutomationType = models.AutomationCategoryPtr(models.AutomationCategoryAPI)
			tc.AutomationTest = &models.AutomationTest{
				ID:        fmt.Sprintf("api-prompt-%s", tc.ID),
				Name:      fmt.Sprintf("%s API test prompt", tc.Code),
				Category:  models.AutomationCategoryAPI,
				Status:    models.AutomationStatusIdle,
				RepoID:    req.BackendRepoID,
				Prompt:    prompt,
				LastRunAt: now,
			}
			prompts = append(prompts, gin.H{"testCaseId": tc.ID, "prompt": prompt})
		}
	}
	if found == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no matching test cases found"})
		return
	}
	scenario.UpdatedAt = time.Now()
	scenario.ComputeStats()
	if err := saveScenario(ctx, scenario); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save scenario"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"category": req.Category, "backendRepoId": req.BackendRepoID, "prompts": prompts})
}

func markManualAutomation(c *gin.Context, scenario *models.TestScenario, testCaseIDs []string) {
	targets := make(map[string]bool, len(testCaseIDs))
	for _, id := range testCaseIDs {
		targets[id] = true
	}
	found := 0
	for si := range scenario.Sections {
		for ti := range scenario.Sections[si].TestCases {
			tc := &scenario.Sections[si].TestCases[ti]
			if !targets[tc.ID] {
				continue
			}
			found++
			tc.AutomationType = models.AutomationCategoryPtr(models.AutomationCategoryManual)
			tc.AutomationTest = nil
		}
	}
	if found == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no matching test cases found"})
		return
	}
	scenario.UpdatedAt = time.Now()
	scenario.ComputeStats()
	if err := saveScenario(c.Request.Context(), scenario); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save scenario"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"category": models.AutomationCategoryManual, "updated": found})
}

func generateE2EAutomations(c *gin.Context, scenario *models.TestScenario, req models.GenerateAutomationRequest) {
	if strings.TrimSpace(req.FrontendRepoID) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "frontendRepoId is required for e2e automation"})
		return
	}

	targetIDs := filterExistingTestCaseIDs(scenario, req.TestCaseIDs)
	if len(targetIDs) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no matching test cases found"})
		return
	}

	_, ok := c.MustGet("token").(*oauth2.Token)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	sessionID, _ := c.Get("session_id")
	authSessionID, _ := sessionID.(string)
	if authSessionID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized: missing session"})
		return
	}

	scenario.UpdatedAt = time.Now()
	setAutomationCategory(scenario, targetIDs, models.AutomationCategoryE2E, req.FrontendRepoID, models.AutomationStatusRunning)
	if err := saveScenario(c.Request.Context(), scenario); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save scenario"})
		return
	}

	job, err := agent.EnqueueE2EGenerationJob(c.Request.Context(), agent.EnqueueE2EGenerationJobInput{
		ScenarioID:    scenario.ID,
		RepoID:        req.FrontendRepoID,
		TestCaseIDs:   targetIDs,
		AuthSessionID: authSessionID,
	})
	if err != nil {
		setTestCasesAutomationStatus(c.Request.Context(), scenario.ID, targetIDs, models.AutomationStatusFail)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to enqueue e2e generation job"})
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"message": "e2e generation started", "jobId": job.ID, "id": scenario.ID, "testCaseIds": targetIDs})
}

func filterExistingTestCaseIDs(scenario *models.TestScenario, requested []string) []string {
	requestedSet := make(map[string]bool, len(requested))
	for _, id := range requested {
		requestedSet[id] = true
	}
	var ids []string
	for _, section := range scenario.Sections {
		for _, tc := range section.TestCases {
			if requestedSet[tc.ID] {
				ids = append(ids, tc.ID)
			}
		}
	}
	return ids
}

func setAutomationCategory(scenario *models.TestScenario, targetIDs []string, category models.AutomationCategory, repoID string, status models.AutomationRunStatus) {
	idMap := make(map[string]bool, len(targetIDs))
	for _, id := range targetIDs {
		idMap[id] = true
	}
	for si := range scenario.Sections {
		for ti := range scenario.Sections[si].TestCases {
			tc := &scenario.Sections[si].TestCases[ti]
			if !idMap[tc.ID] {
				continue
			}
			tc.AutomationType = models.AutomationCategoryPtr(category)
			tc.AutomationTest = &models.AutomationTest{
				ID:       fmt.Sprintf("auto-pending-%s-%d", tc.ID, time.Now().UnixNano()),
				Name:     fmt.Sprintf("%s_%s", tc.Code, strings.ToUpper(string(category))),
				Category: category,
				Status:   status,
				RepoID:   repoID,
			}
		}
	}
	scenario.ComputeStats()
}

// CreateManualTestResult stores a manual execution result and optionally uploads evidence to R2.
func CreateManualTestResult(c *gin.Context) {
	scenarioID := routeScenarioID(c)
	tcID := c.Param("tcId")
	if scenarioID == "" || tcID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "scenario id and test case id are required"})
		return
	}

	ctx := c.Request.Context()
	scenario, err := getScenario(ctx, scenarioID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "scenario not found"})
		return
	}
	if !ensureScenarioProject(c, scenario) {
		return
	}

	if err := c.Request.ParseMultipartForm(64 << 20); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "failed to parse multipart form"})
		return
	}
	status := models.ManualTestStatus(strings.TrimSpace(c.Request.FormValue("status")))
	description := strings.TrimSpace(c.Request.FormValue("description"))
	if status != models.ManualTestStatusPassed && status != models.ManualTestStatusFailed {
		c.JSON(http.StatusBadRequest, gin.H{"error": "status must be passed or failed"})
		return
	}
	if description == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "description is required"})
		return
	}

	sectionID, ok := ensureManualTestCase(&scenario, tcID)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "test case not found"})
		return
	}

	files := c.Request.MultipartForm.File["evidence"]
	files = append(files, c.Request.MultipartForm.File["evidence[]"]...)

	resultID := uuid.NewString()
	evidence := make([]models.ManualEvidenceFile, 0, len(files))
	if len(files) > 0 {
		r2, err := client.NewR2Client()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "R2 storage is not configured"})
			return
		}

		for _, header := range files {
			url, err := uploadManualEvidence(ctx, r2, scenario.ProjectID, scenarioID, tcID, resultID, header.Filename, header)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to upload evidence: %v", err)})
				return
			}
			evidence = append(evidence, models.ManualEvidenceFile{
				Name:        header.Filename,
				URL:         url,
				ContentType: header.Header.Get("Content-Type"),
				Size:        header.Size,
				UploadedAt:  time.Now(),
			})
		}
	}

	testerID, _ := identity.GetCurrentUserID(c)
	now := time.Now()
	result := models.ManualTestResult{
		ID:          resultID,
		ProjectID:   scenario.ProjectID,
		ScenarioID:  scenarioID,
		SectionID:   sectionID,
		TestCaseID:  tcID,
		Status:      status,
		Description: description,
		Evidence:    evidence,
		TesterID:    testerID,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := saveManualTestResult(ctx, result); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save manual test result"})
		return
	}
	scenario.ComputeStats()
	_ = saveScenario(ctx, &scenario)
	c.JSON(http.StatusCreated, result)
}

func ListManualTestResults(c *gin.Context) {
	scenarioID := routeScenarioID(c)
	tcID := c.Param("tcId")
	if scenarioID == "" || tcID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "scenario id and test case id are required"})
		return
	}
	scenario, err := getScenario(c.Request.Context(), scenarioID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "scenario not found"})
		return
	}
	if !ensureScenarioProject(c, scenario) {
		return
	}

	ids, err := database.RedisClient.SMembers(c.Request.Context(), fmt.Sprintf("manual_test_results:scenario:%s:test_case:%s", scenarioID, tcID)).Result()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list manual test results"})
		return
	}
	results := make([]models.ManualTestResult, 0, len(ids))
	for _, id := range ids {
		val, err := database.RedisClient.Get(c.Request.Context(), fmt.Sprintf("manual_test_result:%s", id)).Result()
		if err != nil {
			continue
		}
		var result models.ManualTestResult
		if err := json.Unmarshal([]byte(val), &result); err == nil {
			results = append(results, result)
		}
	}
	sort.Slice(results, func(i, j int) bool { return results[i].CreatedAt.After(results[j].CreatedAt) })
	c.JSON(http.StatusOK, gin.H{"manualResults": results})
}

func ensureManualTestCase(scenario *models.TestScenario, tcID string) (string, bool) {
	for si := range scenario.Sections {
		for ti := range scenario.Sections[si].TestCases {
			if scenario.Sections[si].TestCases[ti].ID != tcID {
				continue
			}
			tc := &scenario.Sections[si].TestCases[ti]
			tc.AutomationType = models.AutomationCategoryPtr(models.AutomationCategoryManual)
			tc.AutomationTest = nil
			return scenario.Sections[si].ID, true
		}
	}
	return "", false
}

func uploadManualEvidence(ctx context.Context, r2 *client.R2Client, projectID string, scenarioID string, tcID string, resultID string, name string, header *multipart.FileHeader) (string, error) {
	file, err := header.Open()
	if err != nil {
		return "", err
	}
	defer file.Close()

	tmp, err := os.CreateTemp("", "manual-evidence-*")
	if err != nil {
		return "", err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := io.Copy(tmp, file); err != nil {
		_ = tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}

	cleanName := strings.ReplaceAll(filepath.Base(name), " ", "_")
	key := fmt.Sprintf("manual-evidence/%s/%s/%s/%s/%s", projectID, scenarioID, tcID, resultID, cleanName)
	contentType := header.Header.Get("Content-Type")
	if contentType == "" || contentType == "application/octet-stream" {
		if detected := mime.TypeByExtension(filepath.Ext(name)); detected != "" {
			contentType = detected
		} else if contentType == "" {
			contentType = "application/octet-stream"
		}
	}
	return r2.UploadFile(ctx, tmpPath, key, contentType)
}

func saveManualTestResult(ctx context.Context, result models.ManualTestResult) error {
	data, err := json.Marshal(result)
	if err != nil {
		return err
	}
	key := fmt.Sprintf("manual_test_result:%s", result.ID)
	if err := database.RedisClient.Set(ctx, key, data, 0).Err(); err != nil {
		return err
	}
	database.RedisClient.SAdd(ctx, fmt.Sprintf("manual_test_results:scenario:%s:test_case:%s", result.ScenarioID, result.TestCaseID), result.ID)
	database.RedisClient.SAdd(ctx, fmt.Sprintf("manual_test_results:project:%s", result.ProjectID), result.ID)
	return nil
}

// SyncScenario re-fetches a single markdown-sourced scenario from its GitLab source file
// and re-parses it, preserving all existing automation tests and notes.
func SyncScenario(c *gin.Context) {
	scenarioID := routeScenarioID(c)
	if scenarioID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "scenario id is required"})
		return
	}

	ctx := c.Request.Context()
	scenario, err := getScenario(ctx, scenarioID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "scenario not found"})
		return
	}
	if !ensureScenarioProject(c, scenario) {
		return
	}
	if scenario.SourceType != "markdown" || scenario.SourcePath == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "scenario was not imported from a markdown file and cannot be synced"})
		return
	}

	projectID := routeProjectID(c)
	project, err := services.GetAppProject(ctx, projectID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "project not found"})
		return
	}

	token, ok := c.MustGet("token").(*oauth2.Token)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	glClient, err := client.GetClient(ctx, token, nil)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create gitlab client"})
		return
	}

	actorID, _ := identity.GetCurrentUserID(c)
	updated, err := services.SyncSingleMarkdownScenario(ctx, glClient, project, scenarioID, actorID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, updated)
}

// ─────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────

func getScenario(ctx context.Context, id string) (models.TestScenario, error) {
	var scenario models.TestScenario
	val, err := database.RedisClient.Get(ctx, fmt.Sprintf("scenario:%s", id)).Result()
	if err != nil {
		return scenario, err
	}
	err = json.Unmarshal([]byte(val), &scenario)
	return scenario, err
}

func saveScenario(ctx context.Context, scenario *models.TestScenario) error {
	val, err := json.Marshal(scenario)
	if err != nil {
		return err
	}
	return database.RedisClient.Set(ctx, fmt.Sprintf("scenario:%s", scenario.ID), val, 0).Err()
}

func setTestCasesAutomationStatus(ctx context.Context, scenarioID string, targetTestCaseIDs []string, status models.AutomationRunStatus) {
	scenario, err := getScenario(ctx, scenarioID)
	if err != nil {
		return
	}

	idMap := make(map[string]bool)
	for _, id := range targetTestCaseIDs {
		idMap[id] = true
	}

	for i := range scenario.Sections {
		for j := range scenario.Sections[i].TestCases {
			tc := &scenario.Sections[i].TestCases[j]
			if idMap[tc.ID] {
				if status == models.AutomationStatusRunning {
					var category models.AutomationCategory
					repoID := ""
					if tc.AutomationTest != nil {
						if tc.AutomationTest.Category != "" {
							category = tc.AutomationTest.Category
						}
						repoID = tc.AutomationTest.RepoID
					}
					tc.AutomationTest = &models.AutomationTest{
						ID:       fmt.Sprintf("auto-pending-%d", time.Now().UnixNano()),
						Name:     fmt.Sprintf("%s_Automation", tc.Code),
						Category: category,
						RepoID:   repoID,
						Status:   status,
					}
				} else if tc.AutomationTest == nil {
					tc.AutomationTest = &models.AutomationTest{
						ID:           fmt.Sprintf("auto-pending-%d", time.Now().UnixNano()),
						Name:         fmt.Sprintf("%s_Automation", tc.Code),
						Status:       status,
						ErrorMessage: "Failed to generate automation for this test case.",
					}
				} else {
					tc.AutomationTest.Status = status
					if status == models.AutomationStatusFail {
						tc.AutomationTest.ErrorMessage = "Failed to generate automation for this test case."
					}
				}
			}
		}
	}

	scenario.ComputeStats()
	saveScenario(ctx, &scenario)
}

// RunScenarioTestCase runs a single test case's automation from a scenario.
func RunScenarioTestCase(c *gin.Context) {
	scenarioID := routeScenarioID(c)
	sectionID := c.Param("sectionId")
	tcID := c.Param("tcId")

	if scenarioID == "" || sectionID == "" || tcID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "scenario id, section id, and test case id are required"})
		return
	}

	ctx := c.Request.Context()
	scenario, err := getScenario(ctx, scenarioID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "scenario not found"})
		return
	}
	if !ensureScenarioProject(c, scenario) {
		return
	}

	// Find the test case
	var targetCase *models.TestCase
	var targetSectionIdx, targetCaseIdx int
	for si := range scenario.Sections {
		if scenario.Sections[si].ID != sectionID {
			continue
		}
		for ti := range scenario.Sections[si].TestCases {
			if scenario.Sections[si].TestCases[ti].ID == tcID {
				targetCase = &scenario.Sections[si].TestCases[ti]
				targetSectionIdx = si
				targetCaseIdx = ti
				break
			}
		}
		if targetCase != nil {
			break
		}
	}

	if targetCase == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "test case not found"})
		return
	}

	if targetCase.AutomationTest == nil || len(targetCase.AutomationTest.Steps) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "this test case has not been generated yet"})
		return
	}

	// Mark as running and save
	scenario.Sections[targetSectionIdx].TestCases[targetCaseIdx].AutomationTest.Status = models.AutomationStatusRunning
	scenario.Sections[targetSectionIdx].TestCases[targetCaseIdx].AutomationTest.LastRunAt = time.Now().Format(time.RFC3339)
	scenario.ComputeStats()
	_ = saveScenario(ctx, &scenario)

	// Run in goroutine so HTTP doesn't block
	go func() {
		bgCtx := context.Background()
		run := &models.TestRun{
			ID:    targetCase.AutomationTest.ID,
			Name:  targetCase.AutomationTest.Name,
			Steps: targetCase.AutomationTest.Steps,
		}

		timeoutCtx, cancel := context.WithTimeout(bgCtx, 5*time.Minute)
		defer cancel()

		result, err := agent.RunTest(timeoutCtx, run)

		// Re-fetch scenario to avoid overwriting concurrent changes
		scenario, fetchErr := getScenario(bgCtx, scenarioID)
		if fetchErr != nil {
			log.Printf("[RunScenarioTestCase] failed to re-fetch scenario after run: %v", fetchErr)
			return
		}

		// Find the test case again in the refreshed scenario
		for si := range scenario.Sections {
			if scenario.Sections[si].ID != sectionID {
				continue
			}
			for ti := range scenario.Sections[si].TestCases {
				at := scenario.Sections[si].TestCases[ti].AutomationTest
				if at != nil && at.ID == targetCase.AutomationTest.ID {
					if err != nil {
						scenario.Sections[si].TestCases[ti].AutomationTest.Status = models.AutomationStatusFail
						scenario.Sections[si].TestCases[ti].AutomationTest.ErrorMessage = err.Error()
					} else {
						scenario.Sections[si].TestCases[ti].AutomationTest.Status = mapResultStatus(result.Status)
						scenario.Sections[si].TestCases[ti].AutomationTest.RunDurationMs = result.RunDurationMs
						scenario.Sections[si].TestCases[ti].AutomationTest.VideoURL = result.VideoURL
						scenario.Sections[si].TestCases[ti].AutomationTest.Log = result.Log
						scenario.Sections[si].TestCases[ti].AutomationTest.ErrorMessage = ""
						at := scenario.Sections[si].TestCases[ti].AutomationTest
						for i := range at.Steps {
							at.Steps[i].ResultStatus = ""
							at.Steps[i].ResultError = ""
							at.Steps[i].ResultScreenshot = ""
						}
						for _, sr := range result.StepResults {
							if sr.StepIndex < len(at.Steps) {
								at.Steps[sr.StepIndex].ResultStatus = sr.Status
								at.Steps[sr.StepIndex].ResultError = sr.Error
								at.Steps[sr.StepIndex].ResultScreenshot = sr.Screenshot
							}
						}
						if result.Status == "failed" {
							for _, sr := range result.StepResults {
								if sr.Status == "failure" {
									scenario.Sections[si].TestCases[ti].AutomationTest.ErrorMessage = sr.Error
									break
								}
							}
						}
					}
					break
				}
			}
		}

		scenario.ComputeStats()
		_ = saveScenario(bgCtx, &scenario)
	}()

	c.JSON(http.StatusAccepted, gin.H{
		"message": "test execution started",
		"id":      tcID,
	})
}

func mapResultStatus(s string) models.AutomationRunStatus {
	switch s {
	case "passed":
		return models.AutomationStatusPass
	case "failed", "timeout":
		return models.AutomationStatusFail
	default:
		return models.AutomationStatusIdle
	}
}
