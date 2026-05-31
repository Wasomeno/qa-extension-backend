package routes

import (
	"context"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"qa-extension-backend/client"
	"strconv"
	"strings"

	"qa-extension-backend/identity"
	"qa-extension-backend/internal/models"
	"qa-extension-backend/services"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	gitlab "gitlab.com/gitlab-org/api/client-go"
	"golang.org/x/oauth2"
)

func CreateAppProject(c *gin.Context) {
	var req models.CreateAppProjectRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	req.Description = strings.TrimSpace(req.Description)
	req.TestContextMarkdown = strings.TrimSpace(req.TestContextMarkdown)
	if req.Name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name is required"})
		return
	}
	if req.IssueRepoID <= 0 || req.SpecsRepoID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "issueRepoId and specsRepoId must be valid GitLab repo IDs"})
		return
	}

	actorID, _ := identity.GetCurrentUserID(c)
	log.Printf("[ProjectCreation] start name=%q issueRepoID=%d specsRepoID=%d actorID=%d hasTestContext=%t", req.Name, req.IssueRepoID, req.SpecsRepoID, actorID, req.TestContextMarkdown != "")
	project, err := services.CreateAppProject(c.Request.Context(), req, actorID)
	if err != nil {
		log.Printf("[ProjectCreation] failed to create project name=%q actorID=%d error=%v", req.Name, actorID, err)
		if strings.Contains(err.Error(), "too large") {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	log.Printf("[ProjectCreation] created projectID=%s name=%q actorID=%d", project.ID, project.Name, actorID)

	importedCount := 0
	syncStarted := false
	glClient, hasGitLab := gitLabClientFromContext(c)
	if hasGitLab {
		log.Printf("[ProjectCreation] starting background markdown scenario sync projectID=%s specsRepoID=%d actorID=%d", project.ID, project.SpecsRepoID, actorID)
		token, _ := c.Get("token")
		oauthToken, _ := token.(*oauth2.Token)
		services.StartMarkdownScenarioSyncJob(glClient, project, actorID, oauthToken)
		syncStarted = true
	} else {
		log.Printf("[ProjectCreation] skipping scenario sync projectID=%s reason=missing_gitlab_token", project.ID)
	}

	// Convert to response format with repo names
	var issueRepoName, specsRepoName string
	if hasGitLab {
		issueRepoName = fetchRepoName(glClient, project.IssueRepoID)
		specsRepoName = fetchRepoName(glClient, project.SpecsRepoID)
	}
	log.Printf("[ProjectCreation] complete projectID=%s imported=%d syncStarted=%t", project.ID, importedCount, syncStarted)
	c.JSON(http.StatusCreated, gin.H{"project": project.ToResponse(issueRepoName, specsRepoName), "scenariosImported": importedCount, "scenarioSyncStarted": syncStarted})
}

func ListAppProjects(c *gin.Context) {
	projects, err := services.ListAppProjects(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Convert to response format with repo names
	var responses []models.AppProjectResponse
	glClient, hasGitLab := gitLabClientFromContext(c)

	for _, project := range projects {
		var issueRepoName, specsRepoName string
		if hasGitLab {
			issueRepoName = fetchRepoName(glClient, project.IssueRepoID)
			specsRepoName = fetchRepoName(glClient, project.SpecsRepoID)
		}
		responses = append(responses, project.ToResponse(issueRepoName, specsRepoName))
	}

	c.JSON(http.StatusOK, gin.H{"projects": responses})
}

func GetAppProject(c *gin.Context) {
	project, err := services.GetAppProject(c.Request.Context(), c.Param("id"))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "project not found"})
		return
	}

	// Convert to response format with repo names
	var issueRepoName, specsRepoName string
	glClient, hasGitLab := gitLabClientFromContext(c)
	if hasGitLab {
		issueRepoName = fetchRepoName(glClient, project.IssueRepoID)
		specsRepoName = fetchRepoName(glClient, project.SpecsRepoID)
	}

	c.JSON(http.StatusOK, project.ToResponse(issueRepoName, specsRepoName))
}

func GetProjectTestContext(c *gin.Context) {
	project, err := services.GetAppProject(c.Request.Context(), c.Param("id"))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "project not found"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"projectId": project.ID,
		"markdown":  project.TestContextMarkdown,
		"template":  services.ProjectTestContextTemplate(),
		"maxBytes":  services.MaxProjectTestContextBytes,
	})
}

func UpdateProjectTestContext(c *gin.Context) {
	var req models.UpdateProjectTestContextRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	actorID, _ := identity.GetCurrentUserID(c)
	project, err := services.UpdateProjectTestContext(c.Request.Context(), c.Param("id"), req.Markdown, actorID)
	if err != nil {
		if strings.Contains(err.Error(), "too large") {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusNotFound, gin.H{"error": "project not found"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"projectId": project.ID,
		"markdown":  project.TestContextMarkdown,
		"maxBytes":  services.MaxProjectTestContextBytes,
	})
}

// fetchRepoName fetches the path with namespace for a GitLab project ID.
// Returns empty string if the fetch fails.
func fetchRepoName(glClient *gitlab.Client, projectID int64) string {
	if projectID <= 0 {
		return ""
	}
	project, _, err := glClient.Projects.GetProject(projectID, nil)
	if err != nil {
		return ""
	}
	return project.PathWithNamespace
}

func UpdateAppProject(c *gin.Context) {
	var req models.UpdateAppProjectRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if req.Name != nil {
		trimmed := strings.TrimSpace(*req.Name)
		if trimmed == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "name cannot be empty"})
			return
		}
		req.Name = &trimmed
	}
	if req.Description != nil {
		trimmed := strings.TrimSpace(*req.Description)
		req.Description = &trimmed
	}
	if req.TestContextMarkdown != nil {
		trimmed := strings.TrimSpace(*req.TestContextMarkdown)
		req.TestContextMarkdown = &trimmed
	}
	if req.IssueRepoID != nil && *req.IssueRepoID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "issueRepoId must be a valid GitLab repo ID"})
		return
	}
	if req.SpecsRepoID != nil && *req.SpecsRepoID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "specsRepoId must be a valid GitLab repo ID"})
		return
	}

	actorID, _ := identity.GetCurrentUserID(c)
	project, err := services.UpdateAppProject(c.Request.Context(), c.Param("id"), req, actorID)
	if err != nil {
		if strings.Contains(err.Error(), "too large") {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusNotFound, gin.H{"error": "project not found"})
		return
	}

	importedCount := 0
	glClient, hasGitLab := gitLabClientFromContext(c)
	if hasGitLab {
		imported, syncErr := services.SyncMarkdownTestScenarios(c.Request.Context(), glClient, project, actorID)
		if syncErr != nil {
			issueRepoName := fetchRepoName(glClient, project.IssueRepoID)
			specsRepoName := fetchRepoName(glClient, project.SpecsRepoID)
			c.JSON(http.StatusOK, gin.H{"project": project.ToResponse(issueRepoName, specsRepoName), "scenariosImported": importedCount, "warning": syncErr.Error()})
			return
		}
		importedCount = len(imported)
	}

	// Convert to response format with repo names
	var issueRepoName, specsRepoName string
	if hasGitLab {
		issueRepoName = fetchRepoName(glClient, project.IssueRepoID)
		specsRepoName = fetchRepoName(glClient, project.SpecsRepoID)
	}
	c.JSON(http.StatusOK, gin.H{"project": project.ToResponse(issueRepoName, specsRepoName), "scenariosImported": importedCount})
}

func SyncAppProjectTestScenarios(c *gin.Context) {
	project, err := services.GetAppProject(c.Request.Context(), c.Param("id"))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "project not found"})
		return
	}
	glClient, ok := gitLabClientFromContext(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	actorID, _ := identity.GetCurrentUserID(c)
	imported, err := services.SyncMarkdownTestScenarios(c.Request.Context(), glClient, project, actorID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"scenarios": imported, "count": len(imported)})
}

func gitLabClientFromContext(c *gin.Context) (*gitlab.Client, bool) {
	token, ok := c.Get("token")
	if !ok {
		return nil, false
	}
	oauthToken, ok := token.(*oauth2.Token)
	if !ok {
		return nil, false
	}
	glClient, err := client.GetClient(context.Background(), oauthToken, nil)
	if err != nil {
		return nil, false
	}
	return glClient, true
}

func DeleteAppProject(c *gin.Context) {
	actorID, _ := identity.GetCurrentUserID(c)
	if err := services.DeleteAppProject(c.Request.Context(), c.Param("id"), actorID); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "project not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "project deleted successfully", "id": c.Param("id")})
}

func ListAppProjectActivity(c *gin.Context) {
	if _, err := services.GetAppProject(c.Request.Context(), c.Param("id")); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "project not found"})
		return
	}
	activity, err := services.ListAppProjectActivity(c.Request.Context(), c.Param("id"))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"activity": activity})
}

func WithIssueRepo(handler gin.HandlerFunc) gin.HandlerFunc {
	return withAppProjectRepo(handler, true)
}

func WithSpecsRepo(handler gin.HandlerFunc) gin.HandlerFunc {
	return withAppProjectRepo(handler, false)
}

func withAppProjectRepo(handler gin.HandlerFunc, issueRepo bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		project, err := services.GetAppProject(c.Request.Context(), c.Param("id"))
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "project not found"})
			return
		}

		repoID := project.SpecsRepoID
		if issueRepo {
			repoID = project.IssueRepoID
		}
		c.Set("app_project", project)
		setGinParam(c, "app_project_id", project.ID)
		setGinParam(c, "id", strconv.FormatInt(repoID, 10))
		handler(c)
	}
}

func setGinParam(c *gin.Context, key string, value string) {
	for i := range c.Params {
		if c.Params[i].Key == key {
			c.Params[i].Value = value
			return
		}
	}
	c.Params = append(c.Params, gin.Param{Key: key, Value: value})
}

// UploadFile handles file uploads to R2 storage for a given project.
// Accepts a multipart form with a "file" field.
// Returns the public URL of the uploaded file.
func UploadFile(c *gin.Context) {
	projectID := c.Param("id")
	if projectID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "project id is required"})
		return
	}

	if err := c.Request.ParseMultipartForm(64 << 20); err != nil { // 64 MB max
		c.JSON(http.StatusBadRequest, gin.H{"error": "failed to parse multipart form"})
		return
	}

	file, header, err := c.Request.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "file is required"})
		return
	}
	defer file.Close()

	r2, err := client.NewR2Client()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "storage is not configured"})
		return
	}

	tmp, err := os.CreateTemp("", "project-upload-*")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create temp file"})
		return
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if _, err := io.Copy(tmp, file); err != nil {
		tmp.Close()
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to read uploaded file"})
		return
	}
	if err := tmp.Close(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to close temp file"})
		return
	}

	ext := filepath.Ext(header.Filename)
	cleanName := strings.ReplaceAll(strings.TrimSuffix(header.Filename, ext), " ", "_")
	key := fmt.Sprintf("project-uploads/%s/%s-%s%s", projectID, cleanName, uuid.NewString(), ext)

	contentType := header.Header.Get("Content-Type")
	if contentType == "" || contentType == "application/octet-stream" {
		if detected := mime.TypeByExtension(filepath.Ext(header.Filename)); detected != "" {
			contentType = detected
		} else if contentType == "" {
			contentType = "application/octet-stream"
		}
	}

	url, err := r2.UploadFile(c.Request.Context(), tmpPath, key, contentType)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to upload file: %v", err)})
		return
	}

	c.JSON(http.StatusOK, gin.H{"url": url})
}

// ProxyFile streams a file from R2 storage with proper inline headers,
// bypassing the .r2.dev domain which always forces Content-Disposition: attachment.
// Accepts a "url" query parameter pointing to the R2 public URL.
func ProxyFile(c *gin.Context) {
	fileURL := c.Query("url")
	if fileURL == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "url query parameter is required"})
		return
	}

	r2, err := client.NewR2Client()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "storage not configured"})
		return
	}

	// Extract the object key from the public URL by stripping the public URL prefix
	parsedURL, err := url.Parse(fileURL)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid url"})
		return
	}

	key := strings.TrimPrefix(parsedURL.Path, "/")

	obj, err := r2.S3Client.GetObject(c.Request.Context(), &s3.GetObjectInput{
		Bucket: aws.String(r2.BucketName),
		Key:    aws.String(key),
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to fetch file: %v", err)})
		return
	}
	defer obj.Body.Close()

	// Resolve content type: prefer S3 object metadata, fallback to extension detection,
	// then the caller-provided hint, and finally application/octet-stream.
	contentType := "application/octet-stream"
	if obj.ContentType != nil && *obj.ContentType != "" && *obj.ContentType != "application/octet-stream" {
		contentType = *obj.ContentType
	} else {
		if detected := mime.TypeByExtension(filepath.Ext(parsedURL.Path)); detected != "" {
			contentType = detected
		}
	}

	// Build Content-Disposition with filename so browsers display it properly
	filename := filepath.Base(parsedURL.Path)

	var contentLength int64 = -1
	if obj.ContentLength != nil {
		contentLength = *obj.ContentLength
	}

	c.Header("Content-Disposition", fmt.Sprintf(`inline; filename="%s"`, filename))
	c.Header("Cache-Control", "public, max-age=31536000, immutable")
	c.DataFromReader(http.StatusOK, contentLength, contentType, obj.Body, nil)
}
