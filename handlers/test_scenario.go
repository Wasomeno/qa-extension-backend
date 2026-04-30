package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"qa-extension-backend/agent"
	"qa-extension-backend/auth"
	"qa-extension-backend/client"
	"qa-extension-backend/database"
	"qa-extension-backend/internal/models"
	"qa-extension-backend/identity"
	"qa-extension-backend/services"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	gitlab "gitlab.com/gitlab-org/api/client-go"
	"golang.org/x/oauth2"
)

// pluralize returns "s" if n != 1, else ""
func pluralize(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// UploadScenario handles uploading an XLSX file and parsing it into a TestScenario
func UploadScenario(c *gin.Context) {
	err := c.Request.ParseMultipartForm(10 << 20) // 10 MB limit
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "failed to parse multipart form"})
		return
	}

	file, header, err := c.Request.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "file is required"})
		return
	}
	defer file.Close()

	projectID := c.Request.FormValue("projectId")

	var authConfig models.AuthConfig
	authConfigStr := c.Request.FormValue("authConfig")
	if authConfigStr != "" {
		if err := json.Unmarshal([]byte(authConfigStr), &authConfig); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid authConfig format"})
			return
		}
	}

	sheets, err := services.ParseXLSX(file)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("failed to parse xlsx: %v", err)})
		return
	}

	projectName := ""
	if projectID != "" {
		token, _ := c.MustGet("token").(*oauth2.Token)
		sessionID, _ := c.MustGet("session_id").(string)
		if token != nil {
			tokenSaver := func(ctx context.Context, t *oauth2.Token) error {
				return auth.UpdateSession(ctx, sessionID, t)
			}
			gitlabClient, err := client.GetClient(c, token, tokenSaver)
			if err == nil {
				project, _, err := gitlabClient.Projects.GetProject(projectID, &gitlab.GetProjectOptions{})
				if err == nil && project != nil {
					projectName = project.NameWithNamespace
				}
			}
		}
	}

	userID, err := identity.GetCurrentUserID(c)
	if err != nil {
		userID = 0
	}

	scenario := services.BuildScenarioFromXLSX(header.Filename, sheets, projectID, projectName, authConfig, userID)
	scenario.ID = uuid.NewString()

	// Save to Redis
	ctx := c.Request.Context()
	key := fmt.Sprintf("scenario:%s", scenario.ID)

	val, err := json.Marshal(scenario)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to marshal scenario"})
		return
	}

	err = database.RedisClient.Set(ctx, key, val, 0).Err()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save to redis"})
		return
	}

	// Add to set of scenarios
	database.RedisClient.SAdd(ctx, "scenarios", scenario.ID)
	if scenario.CreatorID != 0 {
		database.RedisClient.SAdd(ctx, fmt.Sprintf("scenarios:user:%d", scenario.CreatorID), scenario.ID)
	} else {
		database.RedisClient.SAdd(ctx, "scenarios:legacy", scenario.ID)
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "scenario uploaded and parsed successfully",
		"id":      scenario.ID,
		"sections": len(scenario.Sections),
	})
}

// ListScenarios lists all test scenarios
func ListScenarios(c *gin.Context) {
	ctx := c.Request.Context()
	userID, _ := identity.GetCurrentUserID(c)
	search := c.Query("search")
	status := c.Query("status")
	sortBy := c.Query("sort_by") // "created_at", "title", "status"
	order := c.Query("order")    // "asc", "desc"
	page := 1
	limit := 20

	if p := c.Query("page"); p != "" {
		if parsed, err := strconv.Atoi(p); err == nil && parsed > 0 {
			page = parsed
		}
	}
	if l := c.Query("limit"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 && parsed <= 100 {
			limit = parsed
		}
	}

	var ids []string
	var err error

	if userID != 0 {
		userKey := fmt.Sprintf("scenarios:user:%d", userID)
		ids, err = database.RedisClient.SUnion(ctx, "scenarios:legacy", userKey).Result()
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
				if userID == 0 || s.CreatorID == 0 || s.CreatorID == userID {
					// For lists, we don't need the full parsed sheets or massive test cases payload
					// We can just compute stats and clear out the heavy parts
					s.ComputeStats()
					s.Sections = nil
					s.Sheets = nil
					scenarios = append(scenarios, s)
					processedIDs[id] = true
				}
			}
		}
	}

	// Apply filters
	if status != "" {
		filtered := make([]models.TestScenario, 0)
		for _, s := range scenarios {
			if string(s.Status) == status {
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
		case "status":
			if order == "asc" {
				condition = scenarios[i].Status < scenarios[j].Status
			} else {
				condition = scenarios[i].Status > scenarios[j].Status
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
			"data":        []models.TestScenario{},
			"pagination": gin.H{"page": page, "limit": limit, "total": total, "totalPages": totalPages},
		})
		return
	}
	if end > total {
		end = total
	}

	c.JSON(http.StatusOK, gin.H{
		"data":        scenarios[start:end],
		"pagination": gin.H{"page": page, "limit": limit, "total": total, "totalPages": totalPages},
	})
}

// GetScenario gets a specific test scenario
func GetScenario(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "scenario id is required"})
		return
	}

	scenario, err := getScenario(c.Request.Context(), id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "scenario not found"})
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
	id := c.Param("id")
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
	id := c.Param("id")
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

	updated := false
	for i := range scenario.Sections {
		if scenario.Sections[i].ID == sectionID {
			for j := range scenario.Sections[i].TestCases {
				if scenario.Sections[i].TestCases[j].ID == tcID {
					tc := &scenario.Sections[i].TestCases[j]
					
					if req.Title != nil { tc.Title = *req.Title }
					if req.Description != nil { tc.Description = *req.Description }
					if req.PreCondition != nil { tc.PreCondition = *req.PreCondition }
					if req.Tags != nil { tc.Tags = *req.Tags }
					if req.Priority != nil { tc.Priority = *req.Priority }
					if req.Type != nil { tc.Type = *req.Type }
					if req.Status != nil { tc.Status = *req.Status }
					if req.Note != nil { tc.Note = *req.Note }
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

// ReorderTestCases updates the order of test cases in a section
func ReorderTestCases(c *gin.Context) {
	id := c.Param("id")
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
	id := c.Param("id")
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
				Status:       req.Status,
				CreatedAt:    now,
				UpdatedAt:    now,
			}

			if newTC.Priority == "" { newTC.Priority = models.PriorityMedium }
			if newTC.Type == "" { newTC.Type = "positive" }
			if newTC.Status == "" { newTC.Status = models.TCStatusDraft }

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
	id := c.Param("id")
	sectionID := c.Param("sectionId")
	tcID := c.Param("tcId")

	ctx := c.Request.Context()
	scenario, err := getScenario(ctx, id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "scenario not found"})
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
	id := c.Param("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "scenario id is required"})
		return
	}

	ctx := c.Request.Context()

	err := database.RedisClient.Del(ctx, fmt.Sprintf("scenario:%s", id)).Err()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete scenario from redis"})
		return
	}

	database.RedisClient.SRem(ctx, "scenarios", id)

	c.JSON(http.StatusOK, gin.H{
		"message": "scenario deleted successfully",
		"id":    id,
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

	for _, id := range req.IDs {
		// Check if scenario exists
		_, err := getScenario(ctx, id)
		if err != nil {
			notFound = append(notFound, id)
			continue
		}

		err = database.RedisClient.Del(ctx, fmt.Sprintf("scenario:%s", id)).Err()
		if err != nil {
			errors = append(errors, fmt.Sprintf("%s: %v", id, err))
			continue
		}

		database.RedisClient.SRem(ctx, "scenarios", id)
		deletedCount++
	}

	c.JSON(http.StatusOK, gin.H{
		"message":      fmt.Sprintf("deleted %d scenarios", deletedCount),
		"deletedCount": deletedCount,
		"notFound":     notFound,
		"errors":       errors,
	})
}

// GenerateTests triggers AI generation. It can take either sheetNames (legacy/fallback)
// or sectionIds/testCaseIds.
func GenerateTests(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "scenario id is required"})
		return
	}

	var req struct {
		SheetNames  []string `json:"sheetNames"`
		SectionIDs  []string `json:"sectionIds"`
		TestCaseIDs []string `json:"testCaseIds"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	
	if len(req.SheetNames) == 0 && len(req.SectionIDs) == 0 && len(req.TestCaseIDs) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "must provide sheetNames, sectionIds, or testCaseIds"})
		return
	}

	ctx := c.Request.Context()
	scenario, err := getScenario(ctx, id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "scenario not found"})
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
	} else if len(req.SheetNames) > 0 {
		// Mapping sheets to sections (fallback)
		for _, sName := range req.SheetNames {
			for _, sec := range scenario.Sections {
				if sec.Title == sName {
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

	scenario.Status = models.ScenarioStatusGenerating
	saveScenario(ctx, &scenario)

	token, ok := c.MustGet("token").(*oauth2.Token)
	if !ok {
		scenario.Status = models.ScenarioStatusFailed
		scenario.Error = "unauthorized: missing GitLab token"
		saveScenario(ctx, &scenario)
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	gitlabClient, err := client.GetClient(ctx, token, nil)
	if err != nil {
		scenario.Status = models.ScenarioStatusFailed
		scenario.Error = fmt.Sprintf("failed to get gitlab client: %v", err)
		saveScenario(ctx, &scenario)
		c.JSON(http.StatusInternalServerError, gin.H{"error": scenario.Error})
		return
	}

	c.JSON(http.StatusAccepted, gin.H{"message": "generation started", "id": id})

	go func(scenario *models.TestScenario, targetIDs []string, gitlabClient interface{}) {
		bgCtx := context.Background()
		events := agent.NewGenerationEmitter(bgCtx, id)
		
		clientObj, _ := client.GetClient(bgCtx, token, nil)
		if clientObj == nil {
			clientObj = gitlabClient.(*gitlab.Client)
		}

		projectName := scenario.ProjectName
		if projectName == "" {
			projectName = scenario.ProjectID
		}

		// Update target test cases to running state
		setTestCasesAutomationStatus(bgCtx, id, targetIDs, models.AutomationStatusRunning)

		events.SetTotalSteps(len(targetIDs))
		events.Start("Generating %d automation test%s for '%s'...",
			len(targetIDs), pluralize(len(targetIDs)), projectName)

		var allAutomations []models.GeneratedAutomation
		var allFailedIDs []string

		// Batch execution: 5 test cases at a time to prevent LLM token limits and hallucinations
		batchSize := 5
		for i := 0; i < len(targetIDs); i += batchSize {
			end := i + batchSize
			if end > len(targetIDs) {
				end = len(targetIDs)
			}
			batchIDs := targetIDs[i:end]

			events.Progressf("Generating automations for batch %d to %d (of %d)...", i+1, end, len(targetIDs))

			// Use agent for this batch
			result, err := agent.RunAgentForTestGenerationWithLLM(bgCtx, agent.AutomationAgentInput{
				ScenarioID:  id,
				TestCaseIDs: batchIDs,
			}, token)

			if err != nil {
				log.Printf("[Agent] Batch generation failed: %v", err)
				// Log but keep going with other batches
				allFailedIDs = append(allFailedIDs, batchIDs...)
				continue
			}

			if result != nil {
				allAutomations = append(allAutomations, result.Automations...)
				allFailedIDs = append(allFailedIDs, result.FailedIDs...)
			}
		}

		if len(allAutomations) == 0 && len(allFailedIDs) == len(targetIDs) {
			events.Error(fmt.Sprintf("Agent generation completely failed for all test cases"))

			// Mark all running as failed
			setTestCasesAutomationStatus(bgCtx, id, targetIDs, models.AutomationStatusFail)

			s, _ := getScenario(bgCtx, id)
			s.Status = models.ScenarioStatusFailed
			s.Error = "failed to generate tests: all batches failed"
			saveScenario(bgCtx, &s)
			return
		}

		if len(allFailedIDs) > 0 {
			log.Printf("[Agent] Failed to generate %d test cases: %v", len(allFailedIDs), allFailedIDs)
		}

		events.Progressf("Saving %d generated automation test%s to scenario...", len(allAutomations), pluralize(len(allAutomations)))

		// Reload scenario to get latest state
		s, _ := getScenario(bgCtx, id)

		for _, auto := range allAutomations {
			// Link automation steps to test case
			services.LinkAutomation(&s, &auto)
		}

		// Update any that were running but didn't get an automation to failed
		for i := range s.Sections {
			for j := range s.Sections[i].TestCases {
				tc := &s.Sections[i].TestCases[j]
				if tc.AutomationTest != nil && tc.AutomationTest.Status == models.AutomationStatusRunning {
					tc.AutomationTest.Status = models.AutomationStatusFail
					tc.AutomationTest.ErrorMessage = "Failed to generate automation for this test case."
				}
			}
		}

		s.Status = models.ScenarioStatusReady
		s.Error = ""
		s.ComputeStats()
		saveScenario(bgCtx, &s)

		events.Done("Successfully generated %d automation test%s for '%s'",
			len(allAutomations), pluralize(len(allAutomations)), projectName)
	}(&scenario, targetTestCaseIDs, gitlabClient)
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
				if tc.AutomationTest == nil {
					tc.AutomationTest = &models.AutomationTest{
						ID:     fmt.Sprintf("auto-pending-%d", time.Now().UnixNano()),
						Name:   fmt.Sprintf("%s_Automation", tc.Code),
						Status: status,
					}
				} else {
					tc.AutomationTest.Status = status
				}
			}
		}
	}

	scenario.ComputeStats()
	saveScenario(ctx, &scenario)
}

// RunScenarioTestCase runs a single test case's automation from a scenario.
func RunScenarioTestCase(c *gin.Context) {
	scenarioID := c.Param("id")
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
						scenario.Sections[si].TestCases[ti].AutomationTest.StepResults = result.StepResults
						scenario.Sections[si].TestCases[ti].AutomationTest.Log = result.Log
						scenario.Sections[si].TestCases[ti].AutomationTest.ErrorMessage = ""
						scenario.Sections[si].TestCases[ti].AutomationTest.FailedStepIndex = nil
						if result.Status == "failed" && len(result.StepResults) > 0 {
							for _, sr := range result.StepResults {
								if sr.Status == "failure" {
									scenario.Sections[si].TestCases[ti].AutomationTest.FailedStepIndex = &sr.StepIndex
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
	case "failed":
		return models.AutomationStatusFail
	default:
		return models.AutomationStatusIdle
	}
}
