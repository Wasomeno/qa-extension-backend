package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"qa-extension-backend/client"
	"qa-extension-backend/database"
	"qa-extension-backend/internal/models"
	"qa-extension-backend/identity"
	"qa-extension-backend/services"
	"qa-extension-backend/auth"
	"sort"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	gitlab "gitlab.com/gitlab-org/api/client-go"
	"golang.org/x/oauth2"
)

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

	// Sort by CreatedAt desc
	sort.Slice(scenarios, func(i, j int) bool {
		return scenarios[i].CreatedAt.After(scenarios[j].CreatedAt)
	})

	c.JSON(http.StatusOK, scenarios)
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

	go func(scenario models.TestScenario, sheetNames []string, gitlabClient interface{}) { // We cannot pass *gitlab.Client directly over to a goroutine without issues if it references expiring context, but let's use a background context
		bgCtx := context.Background()

		// Re-instantiate client with bgCtx if possible, or assume it's fine
		// Actually best to re-auth
		clientObj, _ := client.GetClient(bgCtx, token, nil)
		if clientObj == nil {
			clientObj = gitlabClient.(*gitlab.Client) // fallback
		}

		// Collect test cases from all target sheets
		var targetTestCases []models.ParsedTestCase
		for _, s := range scenario.Sheets {
			for _, name := range sheetNames {
				if s.Name == name {
					targetTestCases = append(targetTestCases, s.TestCases...)
					break
				}
			}
		}

		if len(targetTestCases) == 0 {
			scenario.Status = "failed"
			scenario.Error = "no test cases found in selected sheets"
			updateScenarioStatus(id, scenario)
			return
		}

		// Fetch codebase context
		codebaseCtx, err := services.FetchCodebaseContext(clientObj, scenario.ProjectID)
		if err != nil {
			scenario.Status = "failed"
			scenario.Error = fmt.Sprintf("failed to fetch codebase: %v", err)
			updateScenarioStatus(id, scenario)
			return
		}

		// Generate tests
		recordings, err := services.GenerateTestsForScenario(bgCtx, targetTestCases, codebaseCtx, scenario.AuthConfig)
		if err != nil {
			scenario.Status = "failed"
			scenario.Error = fmt.Sprintf("failed to generate tests: %v", err)
			updateScenarioStatus(id, scenario)
			return
		}

		// Save generated recordings to Redis
		for _, rec := range recordings {
			rec.ProjectID = scenario.ProjectID
			rec.CreatorID = scenario.CreatorID
			rec.CreatedAt = time.Now()

			// Same logic as SaveRecording
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

			// Link
			scenario.GeneratedTests = append(scenario.GeneratedTests, models.TestScenarioRecording{
				ID:   rec.ID,
				Name: rec.Name,
			})
		}

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
