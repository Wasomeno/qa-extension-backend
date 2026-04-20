package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"qa-extension-backend/agent"
	"qa-extension-backend/client"
	"qa-extension-backend/database"
	"qa-extension-backend/identity"
	"qa-extension-backend/internal/models"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	gitlab "gitlab.com/gitlab-org/api/client-go"
	"golang.org/x/oauth2"
)

// getProjectName fetches the project name from GitLab API
func getProjectName(c *gin.Context, projectID string) string {
	if projectID == "" {
		return ""
	}

	token, exists := c.Get("token")
	if !exists {
		return ""
	}

	oauthToken, ok := token.(*oauth2.Token)
	if !ok {
		return ""
	}

	gitlabClient, err := client.GetClient(c, oauthToken, nil)
	if err != nil {
		return ""
	}

	project, _, err := gitlabClient.Projects.GetProject(projectID, &gitlab.GetProjectOptions{})
	if err != nil {
		return ""
	}

	return project.NameWithNamespace
}

// getProjectDetails fetches comprehensive project information from GitLab API
// Uses Redis cache to avoid repeated GitLab API calls (5 min TTL)
func getProjectDetails(c *gin.Context, projectID string) *models.ProjectDetails {
	if projectID == "" {
		return nil
	}

	ctx := context.Background()

	// Check cache first
	if cached, ok := database.GetCachedProjectDetails(ctx, projectID); ok {
		return cached
	}

	token, exists := c.Get("token")
	if !exists {
		return nil
	}

	oauthToken, ok := token.(*oauth2.Token)
	if !ok {
		return nil
	}

	gitlabClient, err := client.GetClient(c, oauthToken, nil)
	if err != nil {
		return nil
	}

	project, _, err := gitlabClient.Projects.GetProject(projectID, &gitlab.GetProjectOptions{})
	if err != nil {
		return nil
	}

	details := &models.ProjectDetails{
		ID:                project.ID,
		Name:              project.Name,
		NameWithNamespace: project.NameWithNamespace,
		Path:              project.Path,
		PathWithNamespace: project.PathWithNamespace,
		Description:       project.Description,
		WebURL:            project.WebURL,
		Visibility:        string(project.Visibility),
	}

	if project.DefaultBranch != "" {
		details.DefaultBranch = project.DefaultBranch
	}
	if project.AvatarURL != "" {
		details.AvatarURL = project.AvatarURL
	}
	if project.Namespace != nil {
		details.Namespace = &models.Namespace{
			ID:        project.Namespace.ID,
			Name:      project.Namespace.Name,
			Path:      project.Namespace.Path,
			Kind:      project.Namespace.Kind,
			AvatarURL: project.Namespace.AvatarURL,
			WebURL:    project.Namespace.WebURL,
		}
	}

	// Cache the result for future requests
	database.SetCachedProjectDetails(ctx, projectID, details)

	return details
}

func SaveRecording(c *gin.Context) {
	var recording models.TestRecording
	if err := c.ShouldBindJSON(&recording); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if recording.ID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "recording id is required"})
		return
	}

	// Set CreatedAt if not provided (new recording)
	if recording.CreatedAt.IsZero() {
		recording.CreatedAt = time.Now()
	}

	userID, err := identity.GetCurrentUserID(c)
	if err == nil {
		recording.CreatorID = userID
	}

	// Fetch project name if project_id is provided but project_name is not
	if recording.ProjectID != "" && recording.ProjectName == "" {
		recording.ProjectName = getProjectName(c, recording.ProjectID)
	}

	// Save to Redis
	ctx := context.Background()
	key := fmt.Sprintf("recording:%s", recording.ID)

	val, err := json.Marshal(recording)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to marshal recording"})
		return
	}

	err = database.RedisClient.Set(ctx, key, val, 0).Err()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save to redis"})
		return
	}

	// Also add to a set of all recording IDs for easy listing
	err = database.RedisClient.SAdd(ctx, "recordings", recording.ID).Err()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to index recording"})
		return
	}

	if recording.CreatorID != 0 {
		database.RedisClient.SAdd(ctx, fmt.Sprintf("recordings:user:%d", recording.CreatorID), recording.ID)
	} else {
		database.RedisClient.SAdd(ctx, "recordings:legacy", recording.ID)
	}

	if recording.ProjectID != "" {
		database.RedisClient.SAdd(ctx, fmt.Sprintf("recordings:project:%s", recording.ProjectID), recording.ID)
	}
	if recording.IssueID != "" {
		database.RedisClient.SAdd(ctx, fmt.Sprintf("recordings:issue:%s", recording.IssueID), recording.ID)
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "recording saved successfully",
		"id":      recording.ID,
	})
}

func ListRecordings(c *gin.Context) {
	ctx := context.Background()
	projectID := c.Query("project_id")
	issueID := c.Query("issue_id")
	search := c.Query("search")
	status := c.Query("status")
	sortBy := c.Query("sort_by") // "created_at", "name", "status"
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

	userID, _ := identity.GetCurrentUserID(c)

	// Step 1: Get recording IDs from Redis sets
	var ids []string
	var err error

	if issueID != "" {
		ids, err = database.RedisClient.SMembers(ctx, fmt.Sprintf("recordings:issue:%s", issueID)).Result()
	} else if projectID != "" {
		ids, err = database.RedisClient.SMembers(ctx, fmt.Sprintf("recordings:project:%s", projectID)).Result()
	} else if userID != 0 {
		userKey := fmt.Sprintf("recordings:user:%d", userID)
		ids, err = database.RedisClient.SUnion(ctx, "recordings:legacy", userKey).Result()
	} else {
		ids, err = database.RedisClient.SMembers(ctx, "recordings").Result()
	}

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list recordings"})
		return
	}

	// Step 2: OPTIMIZATION - Batch fetch recordings with MGet instead of N+1 GETs
	// This reduces Redis round-trips from N calls to 1 call
	if len(ids) == 0 {
		c.JSON(http.StatusOK, gin.H{
			"data":       []models.TestRecording{},
			"pagination": gin.H{"page": page, "limit": limit, "total": 0, "totalPages": 0},
		})
		return
	}

	keys := make([]string, len(ids))
	for i, id := range ids {
		keys[i] = fmt.Sprintf("recording:%s", id)
	}

	vals, err := database.RedisClient.MGet(ctx, keys...).Result()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list recordings"})
		return
	}

	// Step 3: Parse and filter recordings
	var recordings []models.TestRecording
	processedIDs := make(map[string]bool)

	for i, val := range vals {
		if val == nil {
			continue
		}

		id := ids[i]
		if processedIDs[id] {
			continue
		}

		var r models.TestRecording
		if err := json.Unmarshal([]byte(val.(string)), &r); err != nil {
			continue
		}

		// Filter for current user or legacy
		if userID == 0 || r.CreatorID == 0 || r.CreatorID == userID {
			recordings = append(recordings, r)
			processedIDs[id] = true
		}
	}

	// Step 4: Apply status filter
	if status != "" {
		filtered := make([]models.TestRecording, 0, len(recordings))
		for _, r := range recordings {
			if r.Status == status {
				filtered = append(filtered, r)
			}
		}
		recordings = filtered
	}

	// Step 5: Apply search filter
	if search != "" {
		searchLower := strings.ToLower(search)
		filtered := make([]models.TestRecording, 0, len(recordings))
		for _, r := range recordings {
			if strings.Contains(strings.ToLower(r.Name), searchLower) ||
				strings.Contains(strings.ToLower(r.Description), searchLower) {
				filtered = append(filtered, r)
			}
		}
		recordings = filtered
	}

	// Step 6: Sort recordings
	if sortBy == "" {
		sortBy = "created_at"
	}
	if order == "" {
		order = "desc"
	}

	sort.Slice(recordings, func(i, j int) bool {
		switch sortBy {
		case "name":
			if order == "asc" {
				return recordings[i].Name < recordings[j].Name
			}
			return recordings[i].Name > recordings[j].Name
		case "status":
			if order == "asc" {
				return recordings[i].Status < recordings[j].Status
			}
			return recordings[i].Status > recordings[j].Status
		case "created_at":
			fallthrough
		default:
			if order == "asc" {
				return recordings[i].CreatedAt.Before(recordings[j].CreatedAt)
			}
			return recordings[i].CreatedAt.After(recordings[j].CreatedAt)
		}
	})

	// Step 7: Paginate
	total := len(recordings)
	totalPages := 0
	if total > 0 {
		totalPages = (total + limit - 1) / limit
	}
	start := (page - 1) * limit
	end := start + limit

	if start >= total {
		c.JSON(http.StatusOK, gin.H{
			"data":       []models.TestRecording{},
			"pagination": gin.H{"page": page, "limit": limit, "total": total, "totalPages": totalPages},
		})
		return
	}
	if end > total {
		end = total
	}

	paginatedRecordings := recordings[start:end]

	// Step 8: OPTIMIZATION - Parallel fetch of project details
	// Collect unique project IDs that need fetching
	projectIDs := make(map[string]int) // projectID -> index in paginatedRecordings
	for i, r := range paginatedRecordings {
		if r.ProjectID != "" && r.ProjectDetails == nil {
			projectIDs[r.ProjectID] = i
		}
	}

	if len(projectIDs) > 0 {
		// Fetch project details in parallel using goroutines
		type projectResult struct {
			id      string
			details *models.ProjectDetails
		}

		results := make(chan projectResult, len(projectIDs))
		var wg sync.WaitGroup

		for pid := range projectIDs {
			wg.Add(1)
			go func(id string) {
				defer wg.Done()
				details := getProjectDetails(c, id)
				results <- projectResult{id: id, details: details}
			}(pid)
		}

		// Wait for all goroutines to complete
		wg.Wait()
		close(results)

		// Build a map of project details
		projectDetailsMap := make(map[string]*models.ProjectDetails)
		for result := range results {
			projectDetailsMap[result.id] = result.details
		}

		// Assign project details to recordings
		for i := range paginatedRecordings {
			if paginatedRecordings[i].ProjectID != "" {
				paginatedRecordings[i].ProjectDetails = projectDetailsMap[paginatedRecordings[i].ProjectID]
			}
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"data":       paginatedRecordings,
		"pagination": gin.H{"page": page, "limit": limit, "total": total, "totalPages": totalPages},
	})
}

func UpdateRecording(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "recording id is required"})
		return
	}

	ctx := context.Background()
	key := fmt.Sprintf("recording:%s", id)

	// Check if exists
	exists, err := database.RedisClient.Exists(ctx, key).Result()
	if err != nil || exists == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "recording not found"})
		return
	}

	// Fetch existing for partial updates
	val, err := database.RedisClient.Get(ctx, key).Result()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to fetch existing recording"})
		return
	}

	var existing models.TestRecording
	if err := json.Unmarshal([]byte(val), &existing); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to unmarshal existing recording"})
		return
	}

	oldProjectID := existing.ProjectID
	oldIssueID := existing.IssueID

	// Bind update data
	var updateData map[string]interface{}
	if err := c.ShouldBindJSON(&updateData); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Simple partial update logic
	if name, ok := updateData["name"].(string); ok {
		existing.Name = name
	}
	if desc, ok := updateData["description"].(string); ok {
		existing.Description = desc
	}
	if status, ok := updateData["status"].(string); ok {
		existing.Status = status
	}
	if projectID, ok := updateData["project_id"].(string); ok {
		existing.ProjectID = projectID
		// Fetch project name when project_id is updated
		if projectID != "" {
			existing.ProjectName = getProjectName(c, projectID)
		} else {
			existing.ProjectName = ""
		}
	}
	// Allow explicit update of project_name
	if projectName, ok := updateData["project_name"].(string); ok {
		existing.ProjectName = projectName
	}
	if issueID, ok := updateData["issue_id"].(string); ok {
		existing.IssueID = issueID
	}
	if videoURL, ok := updateData["video_url"].(string); ok {
		existing.VideoURL = videoURL
	}

	// For full replacement via PUT, we could check the method
	if c.Request.Method == http.MethodPut {
		if steps, ok := updateData["steps"]; ok {
			stepsJSON, _ := json.Marshal(steps)
			json.Unmarshal(stepsJSON, &existing.Steps)
		}
		if params, ok := updateData["parameters"]; ok {
			paramsJSON, _ := json.Marshal(params)
			json.Unmarshal(paramsJSON, &existing.Parameters)
		}
	}

	// Save back
	newVal, err := json.Marshal(existing)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to marshal updated recording"})
		return
	}

	err = database.RedisClient.Set(ctx, key, newVal, 0).Err()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save updated recording"})
		return
	}

	// Update indices if project or issue changed
	if existing.ProjectID != oldProjectID {
		if oldProjectID != "" {
			database.RedisClient.SRem(ctx, fmt.Sprintf("recordings:project:%s", oldProjectID), existing.ID)
		}
		if existing.ProjectID != "" {
			database.RedisClient.SAdd(ctx, fmt.Sprintf("recordings:project:%s", existing.ProjectID), existing.ID)
		}
	}
	if existing.IssueID != oldIssueID {
		if oldIssueID != "" {
			database.RedisClient.SRem(ctx, fmt.Sprintf("recordings:issue:%s", oldIssueID), existing.ID)
		}
		if existing.IssueID != "" {
			database.RedisClient.SAdd(ctx, fmt.Sprintf("recordings:issue:%s", existing.IssueID), existing.ID)
		}
	}

	c.JSON(http.StatusOK, existing)
}

func DeleteRecording(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "recording id is required"})
		return
	}

	ctx := context.Background()
	key := fmt.Sprintf("recording:%s", id)

	// Fetch to get ProjectID and IssueID for index cleanup
	val, err := database.RedisClient.Get(ctx, key).Result()
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "recording not found"})
		return
	}

	var recording models.TestRecording
	if err := json.Unmarshal([]byte(val), &recording); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to unmarshal recording"})
		return
	}

	// Delete from Redis
	err = database.RedisClient.Del(ctx, key).Err()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete recording from redis"})
		return
	}

	// Remove from index set
	database.RedisClient.SRem(ctx, "recordings", id)
	if recording.ProjectID != "" {
		database.RedisClient.SRem(ctx, fmt.Sprintf("recordings:project:%s", recording.ProjectID), id)
	}
	if recording.IssueID != "" {
		database.RedisClient.SRem(ctx, fmt.Sprintf("recordings:issue:%s", recording.IssueID), id)
	}

	c.JSON(http.StatusOK, gin.H{"message": "recording deleted successfully", "id": id})
}

func GetRecording(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "recording id is required"})
		return
	}

	ctx := context.Background()
	key := fmt.Sprintf("recording:%s", id)

	val, err := database.RedisClient.Get(ctx, key).Result()
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "recording not found"})
		return
	}

	var recording models.TestRecording
	if err := json.Unmarshal([]byte(val), &recording); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to unmarshal recording"})
		return
	}

	// Enrich with project details if project_id is present
	if recording.ProjectID != "" && recording.ProjectDetails == nil {
		recording.ProjectDetails = getProjectDetails(c, recording.ProjectID)
	}

	c.JSON(http.StatusOK, recording)
}

// BulkDeleteRecordings deletes multiple recordings by their IDs
func BulkDeleteRecordings(c *gin.Context) {
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

	ctx := context.Background()
	deletedCount := 0
	var notFound []string
	var errors []string

	for _, id := range req.IDs {
		key := fmt.Sprintf("recording:%s", id)

		// Fetch to get ProjectID and IssueID for index cleanup
		val, err := database.RedisClient.Get(ctx, key).Result()
		if err != nil {
			notFound = append(notFound, id)
			continue
		}

		var recording models.TestRecording
		if err := json.Unmarshal([]byte(val), &recording); err != nil {
			errors = append(errors, fmt.Sprintf("%s: %v", id, err))
			continue
		}

		// Delete from Redis
		err = database.RedisClient.Del(ctx, key).Err()
		if err != nil {
			errors = append(errors, fmt.Sprintf("%s: %v", id, err))
			continue
		}

		// Remove from index sets
		database.RedisClient.SRem(ctx, "recordings", id)
		if recording.ProjectID != "" {
			database.RedisClient.SRem(ctx, fmt.Sprintf("recordings:project:%s", recording.ProjectID), id)
		}
		if recording.IssueID != "" {
			database.RedisClient.SRem(ctx, fmt.Sprintf("recordings:issue:%s", recording.IssueID), id)
		}

		deletedCount++
	}

	c.JSON(http.StatusOK, gin.H{
		"message":      fmt.Sprintf("deleted %d recordings", deletedCount),
		"deletedCount": deletedCount,
		"notFound":     notFound,
		"errors":       errors,
	})
}

// RunRecording handles POST /recordings/:id/run - starts execution of a recording
func RunRecording(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "recording id is required"})
		return
	}

	var req struct {
		Overrides []map[string]any `json:"overrides,omitempty"`
	}
	// Optional body - ignore errors as body may be empty
	c.ShouldBindJSON(&req)

	ctx := context.Background()
	key := fmt.Sprintf("recording:%s", id)

	val, err := database.RedisClient.Get(ctx, key).Result()
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "recording not found"})
		return
	}

	var recording models.TestRecording
	if err := json.Unmarshal([]byte(val), &recording); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to unmarshal recording"})
		return
	}

	// Publish start event and execute in goroutine
	events := agent.NewExecutionEmitter(ctx, id)
	events.Start("Starting recording '%s' (%d steps)...", recording.Name, len(recording.Steps))

	// Execute in goroutine to not block HTTP
	go func() {
		bgCtx := context.Background()
		result, err := agent.RunTest(bgCtx, &recording)
		if err != nil {
			events.Error(fmt.Sprintf("Recording '%s' failed: %v", recording.Name, err))
		} else {
			events.Done("Recording '%s' completed: %s", recording.Name, result.Status)
		}
	}()

	c.JSON(http.StatusAccepted, gin.H{
		"message": "execution started",
		"id":      id,
	})
}
