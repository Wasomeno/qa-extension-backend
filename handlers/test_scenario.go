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
	// Parse multipart form
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

	// Parse XLSX
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

	scenario := models.TestScenario{
		ID:             uuid.NewString(),
		FileName:       header.Filename,
		ProjectID:      projectID,
		ProjectName:    projectName,
		Sheets:         sheets,
		GeneratedTests: []models.TestScenarioRecording{},
		Status:         "uploaded",
		AuthConfig:     authConfig,
		CreatedAt:      time.Now(),
	}

	userID, err := identity.GetCurrentUserID(c)
	if err == nil {
		scenario.CreatorID = userID
	}

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
		"sheets":  len(sheets),
	})
}

// ListScenarios lists all test scenarios
func ListScenarios(c *gin.Context) {
	ctx := c.Request.Context()
	userID, _ := identity.GetCurrentUserID(c)
	search := c.Query("search")
	status := c.Query("status")
	sortBy := c.Query("sort_by") // "created_at", "file_name", "status"
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
		// Fetch user's scenarios and legacy (CreatorID == 0)
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
				// Filter for current user or legacy
				if userID == 0 || s.CreatorID == 0 || s.CreatorID == userID {
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
			if s.Status == status {
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
			if strings.Contains(strings.ToLower(s.FileName), searchLower) ||
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
		case "file_name":
			if order == "asc" {
				condition = scenarios[i].FileName < scenarios[j].FileName
			} else {
				condition = scenarios[i].FileName > scenarios[j].FileName
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

	ctx := c.Request.Context()
	key := fmt.Sprintf("scenario:%s", id)

	val, err := database.RedisClient.Get(ctx, key).Result()
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "scenario not found"})
		return
	}

	var scenario models.TestScenario
	if err := json.Unmarshal([]byte(val), &scenario); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to unmarshal scenario"})
		return
	}

	c.JSON(http.StatusOK, scenario)
}

// DeleteScenario deletes a scenario and its associated generated recordings
func DeleteScenario(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "scenario id is required"})
		return
	}

	ctx := c.Request.Context()
	key := fmt.Sprintf("scenario:%s", id)

	val, err := database.RedisClient.Get(ctx, key).Result()
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "scenario not found"})
		return
	}

	var scenario models.TestScenario
	if err := json.Unmarshal([]byte(val), &scenario); err == nil {
		// Clean up generated recordings implicitly
		for _, test := range scenario.GeneratedTests {
			database.RedisClient.Del(ctx, fmt.Sprintf("recording:%s", test.ID))
			database.RedisClient.SRem(ctx, "recordings", test.ID)
			
			if scenario.ProjectID != "" {
				database.RedisClient.SRem(ctx, fmt.Sprintf("recordings:project:%s", scenario.ProjectID), test.ID)
			}
		}
	}

	err = database.RedisClient.Del(ctx, key).Err()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete scenario from redis"})
		return
	}

	database.RedisClient.SRem(ctx, "scenarios", id)

	c.JSON(http.StatusOK, gin.H{"message": "scenario deleted successfully", "id": id})
}

// GenerateTests triggers AI generation for a given sheet inside a scenario
func GenerateTests(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "scenario id is required"})
		return
	}

	var req struct {
		SheetNames []string `json:"sheetNames"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if len(req.SheetNames) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "sheetNames array is required and cannot be empty"})
		return
	}

	ctx := c.Request.Context()
	key := fmt.Sprintf("scenario:%s", id)

	// Fetch scenario
	val, err := database.RedisClient.Get(ctx, key).Result()
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "scenario not found"})
		return
	}

	var scenario models.TestScenario
	if err := json.Unmarshal([]byte(val), &scenario); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to unmarshal scenario"})
		return
	}

	// Update status to generating
	scenario.Status = "generating"
	newVal, _ := json.Marshal(scenario)
	database.RedisClient.Set(ctx, key, newVal, 0)

	// Get auth token for GitLab
	token, ok := c.MustGet("token").(*oauth2.Token)
	if !ok {
		scenario.Status = "failed"
		scenario.Error = "unauthorized: missing GitLab token"
		updateScenarioStatus(id, scenario)
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	gitlabClient, err := client.GetClient(ctx, token, nil)
	if err != nil {
		scenario.Status = "failed"
		scenario.Error = fmt.Sprintf("failed to get gitlab client: %v", err)
		updateScenarioStatus(id, scenario)
		c.JSON(http.StatusInternalServerError, gin.H{"error": scenario.Error})
		return
	}

	// We return immediately and do the heavy lifting in a goroutine
	c.JSON(http.StatusAccepted, gin.H{"message": "generation started", "id": id})

	// Helper to publish contextual events to the unified stream
	publish := func(stage, msg string) {
		database.PublishStreamEvent(context.Background(), database.StreamEvent{
			Type:         "generation",
			ResourceType: "scenario",
			ResourceID:   id,
			Stage:        stage,
			Message:      msg,
		})
	}

	publishError := func(errMsg string) {
		database.PublishStreamEvent(context.Background(), database.StreamEvent{
			Type:         "generation",
			ResourceType: "scenario",
			ResourceID:   id,
			Stage:        "error",
			Message:      errMsg,
		})
	}

	go func(scenario models.TestScenario, sheetNames []string, gitlabClient interface{}) {
		bgCtx := context.Background()

		// Build a descriptive context for messages
		projectName := scenario.ProjectName
		if projectName == "" {
			projectName = scenario.ProjectID
		}

		// Re-instantiate client with bgCtx
		clientObj, _ := client.GetClient(bgCtx, token, nil)
		if clientObj == nil {
			clientObj = gitlabClient.(*gitlab.Client) // fallback
		}

		// Collect test cases from all target sheets
		var targetTestCases []models.ParsedTestCase
		testCaseNames := []string{}
		for _, s := range scenario.Sheets {
			for _, name := range sheetNames {
				if s.Name == name {
					for _, tc := range s.TestCases {
						targetTestCases = append(targetTestCases, tc)
						if len(testCaseNames) < 5 {
							testCaseNames = append(testCaseNames, tc.Name)
						}
					}
					break
				}
			}
		}

		if len(targetTestCases) == 0 {
			publishError("No test cases found in selected sheets")
			scenario.Status = "failed"
			scenario.Error = "no test cases found in selected sheets"
			updateScenarioStatus(id, scenario)
			return
		}

		// Stage: start — contextual message based on actual data
		totalSheets := len(sheetNames)
		totalCases := len(targetTestCases)
		publish("start", fmt.Sprintf(
			"Starting generation of %d test recording%s for '%s' (%d sheet%s: %s)...",
			totalCases, pluralize(totalCases),
			projectName,
			totalSheets, pluralize(totalSheets),
			strings.Join(sheetNames, ", "),
		))

		// Stage: sending to agent
		if totalCases > 1 {
			publish("sending_to_agent", fmt.Sprintf(
				"Generating %d test recordings using QA Agent...",
				totalCases,
			))
		} else {
			tcName := ""
			if len(testCaseNames) > 0 {
				tcName = testCaseNames[0]
			}
			publish("sending_to_agent", fmt.Sprintf(
				"Generating Playwright test for '%s' using QA Agent...",
				tcName,
			))
		}

		// Use agent - it will use GitLab tools to navigate repo and find files
		// Use the LLM-based agent for intelligent code exploration
		result, err := agent.RunAgentForTestGenerationWithLLM(bgCtx, agent.TestRecordingAgentInput{
			ScenarioID: id,
			SheetNames: sheetNames,
		}, token)
		if err != nil {
			publishError(fmt.Sprintf("Agent generation failed: %v", err))
			scenario.Status = "failed"
			scenario.Error = fmt.Sprintf("failed to generate tests: %v", err)
			updateScenarioStatus(id, scenario)
			return
		}

		recordings := result.Recordings

		// Log missing routes if any
		if len(result.FailedIDs) > 0 {
			log.Printf("[Agent] Failed to generate %d test cases: %v", len(result.FailedIDs), result.FailedIDs)
		}

		// Stage: processing AI response
		publish("processing", fmt.Sprintf(
			"Processing %d generated test recording%s from AI...",
			len(recordings), pluralize(len(recordings)),
		))

		// Stage: saving recordings
		publish("saving", fmt.Sprintf(
			"Saving %d generated test recording%s to database...",
			len(recordings), pluralize(len(recordings)),
		))

		// Save generated recordings to Redis
		for _, rec := range recordings {
			rec.ProjectID = scenario.ProjectID
			rec.CreatorID = scenario.CreatorID
			rec.CreatedAt = time.Now()

			recKey := fmt.Sprintf("recording:%s", rec.ID)
			recVal, _ := json.Marshal(rec)

			database.RedisClient.Set(bgCtx, recKey, recVal, 0)
			database.RedisClient.SAdd(bgCtx, "recordings", rec.ID)
			if rec.CreatorID != 0 {
				database.RedisClient.SAdd(bgCtx, fmt.Sprintf("recordings:user:%d", rec.CreatorID), rec.ID)
			} else {
				database.RedisClient.SAdd(bgCtx, "recordings:legacy", rec.ID)
			}

			if rec.ProjectID != "" {
				database.RedisClient.SAdd(bgCtx, fmt.Sprintf("recordings:project:%s", rec.ProjectID), rec.ID)
			}

			scenario.GeneratedTests = append(scenario.GeneratedTests, models.TestScenarioRecording{
				ID:   rec.ID,
				Name: rec.Name,
			})
		}

		// Stage: done
		publish("done", fmt.Sprintf(
			"Successfully generated %d test recording%s for '%s'",
			len(recordings), pluralize(len(recordings)),
			projectName,
		))

		// Update scenario as ready
		scenario.Status = "ready"
		scenario.Error = ""
		updateScenarioStatus(id, scenario)
	}(scenario, req.SheetNames, gitlabClient)
}

func updateScenarioStatus(id string, scenario models.TestScenario) {
	ctx := context.Background()
	key := fmt.Sprintf("scenario:%s", id)
	val, _ := json.Marshal(scenario)
	database.RedisClient.Set(ctx, key, val, 0)
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
		key := fmt.Sprintf("scenario:%s", id)

		val, err := database.RedisClient.Get(ctx, key).Result()
		if err != nil {
			notFound = append(notFound, id)
			continue
		}

		var scenario models.TestScenario
		if err := json.Unmarshal([]byte(val), &scenario); err != nil {
			errors = append(errors, fmt.Sprintf("%s: %v", id, err))
			continue
		}

		// Clean up generated recordings
		for _, test := range scenario.GeneratedTests {
			recKey := fmt.Sprintf("recording:%s", test.ID)
			database.RedisClient.Del(ctx, recKey)
			database.RedisClient.SRem(ctx, "recordings", test.ID)

			if scenario.ProjectID != "" {
				database.RedisClient.SRem(ctx, fmt.Sprintf("recordings:project:%s", scenario.ProjectID), test.ID)
			}
		}

		// Delete scenario
		err = database.RedisClient.Del(ctx, key).Err()
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
