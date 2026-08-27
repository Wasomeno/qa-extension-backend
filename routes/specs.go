package routes

import (
	"context"
	"fmt"
	"net/http"
	"qa-extension-backend/auth"
	"qa-extension-backend/client"
	"qa-extension-backend/identity"
	"qa-extension-backend/internal/models"
	"qa-extension-backend/services"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"golang.org/x/oauth2"
)

var specsService = services.NewSpecsService()

// helper to build a gitlab client from gin context
func getGitLabClient(c *gin.Context) (*services.SpecsService, error) {
	return specsService, nil
}

// --- GET /projects/:id/specs/tree ---
// Query params: path (folder path), ref (branch), recursive (bool)
func GetSpecsTree(c *gin.Context) {
	token := c.MustGet("token").(*oauth2.Token)
	sessionID := c.MustGet("session_id").(string)

	tokenSaver := func(ctx context.Context, t *oauth2.Token) error {
		return auth.UpdateSession(ctx, sessionID, t)
	}

	glClient, err := client.GetClient(c, token, tokenSaver)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create GitLab client: " + err.Error()})
		return
	}

	projectID := c.Param("id")
	specsPath := c.Query("path")
	ref := c.Query("ref")
	recursive := c.Query("recursive") == "true"
	enrich := c.Query("enrich") == "1" || c.Query("enrich") == "true"

	tree, err := specsService.GetFileTree(c, glClient, projectID, specsPath, ref, recursive)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	if enrich {
		specsService.EnrichTreeLastCommits(c.Request.Context(), glClient, projectID, ref, tree)
	}

	c.JSON(http.StatusOK, gin.H{"tree": tree})
}

// --- GET /projects/:id/specs/file ---
// Query params: path (file path), ref (branch)
func GetSpecsFile(c *gin.Context) {
	token := c.MustGet("token").(*oauth2.Token)
	sessionID := c.MustGet("session_id").(string)

	tokenSaver := func(ctx context.Context, t *oauth2.Token) error {
		return auth.UpdateSession(ctx, sessionID, t)
	}

	glClient, err := client.GetClient(c, token, tokenSaver)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create GitLab client: " + err.Error()})
		return
	}

	projectID := c.Param("id")
	filePath := c.Query("path")
	if filePath == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "path query parameter is required"})
		return
	}
	ref := c.Query("ref")

	file, err := specsService.GetFile(c, glClient, projectID, filePath, ref)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "File not found: " + err.Error()})
		return
	}

	c.JSON(http.StatusOK, file)
}

// --- PUT /projects/:id/specs/file ---
// Body: { path, content, branch?, commitMessage?, action: "create"|"update" }
func SaveSpecsFile(c *gin.Context) {
	token := c.MustGet("token").(*oauth2.Token)
	sessionID := c.MustGet("session_id").(string)

	tokenSaver := func(ctx context.Context, t *oauth2.Token) error {
		return auth.UpdateSession(ctx, sessionID, t)
	}

	glClient, err := client.GetClient(c, token, tokenSaver)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create GitLab client: " + err.Error()})
		return
	}

	projectID := c.Param("id")

	var req struct {
		Path          string `json:"path" binding:"required"`
		Content       string `json:"content"`
		Branch        string `json:"branch"`
		CommitMessage string `json:"commitMessage"`
		Action        string `json:"action"` // "create" or "update"
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request: " + err.Error()})
		return
	}

	if req.Action == "" {
		req.Action = "update"
	}

	// Get current user info for commit author
	userName := ""
	userEmail := ""
	if u, exists := c.Get("user"); exists {
		// Try to extract from session - optional
		_ = u
	}

	switch req.Action {
	case "create":
		err = specsService.CreateFile(glClient, projectID, req.Path, req.Content, req.Branch, req.CommitMessage, userName, userEmail)
	case "update":
		err = specsService.UpdateFile(glClient, projectID, req.Path, req.Content, req.Branch, req.CommitMessage, userName, userEmail)
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "action must be 'create' or 'update'"})
		return
	}

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	recordSpecActivity(c, req.Path, req.Action)
	c.JSON(http.StatusOK, gin.H{"success": true, "path": req.Path, "action": req.Action})
}

// --- DELETE /projects/:id/specs/file ---
// Body: { path, branch?, commitMessage? }
func DeleteSpecsFile(c *gin.Context) {
	token := c.MustGet("token").(*oauth2.Token)
	sessionID := c.MustGet("session_id").(string)

	tokenSaver := func(ctx context.Context, t *oauth2.Token) error {
		return auth.UpdateSession(ctx, sessionID, t)
	}

	glClient, err := client.GetClient(c, token, tokenSaver)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create GitLab client: " + err.Error()})
		return
	}

	projectID := c.Param("id")

	var req struct {
		Path          string `json:"path" binding:"required"`
		Branch        string `json:"branch"`
		CommitMessage string `json:"commitMessage"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request: " + err.Error()})
		return
	}

	err = specsService.DeleteFile(glClient, projectID, req.Path, req.Branch, req.CommitMessage, "", "")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	recordSpecActivity(c, req.Path, "delete")
	c.JSON(http.StatusOK, gin.H{"success": true, "path": req.Path})
}

// --- POST /projects/:id/specs/commit ---
// Body: { branch, commitMessage, actions: [{ action, filePath, content, previousPath? }] }
func CommitSpecsFiles(c *gin.Context) {
	token := c.MustGet("token").(*oauth2.Token)
	sessionID := c.MustGet("session_id").(string)

	tokenSaver := func(ctx context.Context, t *oauth2.Token) error {
		return auth.UpdateSession(ctx, sessionID, t)
	}

	glClient, err := client.GetClient(c, token, tokenSaver)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create GitLab client: " + err.Error()})
		return
	}

	projectID := c.Param("id")

	var req struct {
		Branch        string                `json:"branch"`
		CommitMessage string                `json:"commitMessage" binding:"required"`
		Actions       []services.FileAction `json:"actions" binding:"required"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request: " + err.Error()})
		return
	}

	commit, err := specsService.CommitFiles(glClient, projectID, req.Branch, req.CommitMessage, req.Actions, "", "")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	pathHint := ""
	if len(req.Actions) > 0 {
		pathHint = req.Actions[0].FilePath
	}
	recordSpecActivity(c, pathHint, "commit")
	c.JSON(http.StatusOK, gin.H{"success": true, "commit": commit})
}

// recordSpecActivity writes a project audit row when the handler was mounted
// via WithSpecsRepo (app_project is on the gin context).
func recordSpecActivity(c *gin.Context, path, action string) {
	raw, ok := c.Get("app_project")
	if !ok {
		return
	}
	project, ok := raw.(*models.AppProject)
	if !ok || project == nil {
		return
	}
	actorID, _ := identity.GetCurrentUserID(c)
	_ = services.AppendAppProjectActivity(c.Request.Context(), project.ID, models.AppProjectActivity{
		ID:        uuid.NewString(),
		ProjectID: project.ID,
		ActorID:   actorID,
		Action:    models.AppProjectActivitySpecSaved,
		Changes: map[string]models.AppProjectChange{
			"path":   {New: path},
			"action": {New: action},
		},
		CreatedAt: time.Now().UTC(),
	})
}

// --- GET /projects/:id/specs/commits ---
// Query params: path, ref, per_page, page
func GetSpecsCommits(c *gin.Context) {
	token := c.MustGet("token").(*oauth2.Token)
	sessionID := c.MustGet("session_id").(string)

	tokenSaver := func(ctx context.Context, t *oauth2.Token) error {
		return auth.UpdateSession(ctx, sessionID, t)
	}

	glClient, err := client.GetClient(c, token, tokenSaver)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create GitLab client: " + err.Error()})
		return
	}

	projectID := c.Param("id")
	path := c.Query("path")
	ref := c.Query("ref")
	perPage := 20
	page := 1

	if p := c.Query("per_page"); p != "" {
		fmt.Sscanf(p, "%d", &perPage)
	}
	if p := c.Query("page"); p != "" {
		fmt.Sscanf(p, "%d", &page)
	}

	commits, err := specsService.GetCommits(glClient, projectID, path, ref, perPage, page)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"commits": commits})
}

// --- GET /projects/:id/specs/commits/:sha ---
func GetSpecsCommitDetail(c *gin.Context) {
	token := c.MustGet("token").(*oauth2.Token)
	sessionID := c.MustGet("session_id").(string)

	tokenSaver := func(ctx context.Context, t *oauth2.Token) error {
		return auth.UpdateSession(ctx, sessionID, t)
	}

	glClient, err := client.GetClient(c, token, tokenSaver)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create GitLab client: " + err.Error()})
		return
	}

	projectID := c.Param("id")
	sha := c.Param("sha")

	detail, err := specsService.GetCommitDetail(glClient, projectID, sha)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, detail)
}

// --- GET /projects/:id/specs/search ---
// Query: q (search query), path (base path), ref
func SearchSpecs(c *gin.Context) {
	token := c.MustGet("token").(*oauth2.Token)
	sessionID := c.MustGet("session_id").(string)

	tokenSaver := func(ctx context.Context, t *oauth2.Token) error {
		return auth.UpdateSession(ctx, sessionID, t)
	}

	glClient, err := client.GetClient(c, token, tokenSaver)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create GitLab client: " + err.Error()})
		return
	}

	projectID := c.Param("id")
	query := c.Query("q")
	if query == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "q query parameter is required"})
		return
	}
	specsPath := c.Query("path")
	ref := c.Query("ref")

	results, degraded, err := specsService.SearchSpecs(c.Request.Context(), glClient, projectID, specsPath, ref, query)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Keep a flat tree-compatible list for older clients while exposing rich hits.
	nodes := make([]*services.FileTreeNode, 0, len(results))
	for _, r := range results {
		if r != nil && r.FileTreeNode != nil {
			nodes = append(nodes, r.FileTreeNode)
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"results":  nodes,
		"hits":     results,
		"degraded": degraded,
	})
}

// --- GET /projects/:id/specs/blame ---
// Query: path (file path), ref
func GetSpecsFileBlame(c *gin.Context) {
	token := c.MustGet("token").(*oauth2.Token)
	sessionID := c.MustGet("session_id").(string)

	tokenSaver := func(ctx context.Context, t *oauth2.Token) error {
		return auth.UpdateSession(ctx, sessionID, t)
	}

	glClient, err := client.GetClient(c, token, tokenSaver)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create GitLab client: " + err.Error()})
		return
	}

	projectID := c.Param("id")
	filePath := c.Query("path")
	if filePath == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "path query parameter is required"})
		return
	}
	ref := c.Query("ref")

	blame, err := specsService.GetFileBlame(glClient, projectID, filePath, ref)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"blame": blame})
}
