package routes

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"qa-extension-backend/auth"
	"qa-extension-backend/identity"
	"qa-extension-backend/services"

	"github.com/gin-gonic/gin"
	gitlab "gitlab.com/gitlab-org/api/client-go"
	"golang.org/x/oauth2"
)

var generateFSDIssueDrafts = services.GenerateFSDIssueDrafts
var getAppProjectForFSDIssues = services.GetAppProject
var getCurrentUserIDForFSDIssues = identity.GetCurrentUserID

type fsdIssuePreviewRequest struct {
	FSDs []fsdIssueSourceRequest `json:"fsds" binding:"required"`
}

type fsdIssueSourceRequest struct {
	Path string `json:"path" binding:"required"`
	Ref  string `json:"ref,omitempty"`
}

type fsdIssueCreateRequest struct {
	Issues []services.FSDIssueDraft `json:"issues" binding:"required"`
}

type fsdIssueCreateResult struct {
	SourcePath string        `json:"sourcePath,omitempty"`
	Title      string        `json:"title"`
	Status     string        `json:"status"`
	Issue      *gitlab.Issue `json:"issue,omitempty"`
	Error      string        `json:"error,omitempty"`
}

// PreviewFSDIssues generates GitLab issue-card drafts from selected FSD files.
func PreviewFSDIssues(c *gin.Context) {
	project, err := getAppProjectForFSDIssues(c.Request.Context(), c.Param("id"))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "project not found"})
		return
	}

	var req fsdIssuePreviewRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request: " + err.Error()})
		return
	}
	if len(req.FSDs) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "at least one FSD is required"})
		return
	}

	glClient, err := gitLabClientWithSaver(c)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create GitLab client: " + err.Error()})
		return
	}

	specsRepoID := strconv.FormatInt(project.SpecsRepoID, 10)
	defaultRef := defaultProjectBranch(c.Request.Context(), glClient, specsRepoID)
	sources := make([]services.FSDIssueSource, 0, len(req.FSDs))
	for _, fsd := range req.FSDs {
		path := strings.TrimSpace(fsd.Path)
		if path == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "FSD path is required"})
			return
		}
		ref := strings.TrimSpace(fsd.Ref)
		if ref == "" {
			ref = defaultRef
		}
		file, err := specsService.GetFile(c, glClient, specsRepoID, path, ref)
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": fmt.Sprintf("failed to read FSD %s: %v", path, err)})
			return
		}
		sources = append(sources, services.FSDIssueSource{
			Path:    path,
			Ref:     ref,
			Content: file.Content,
		})
	}

	actorID, _ := getCurrentUserIDForFSDIssues(c)
	issues, err := generateFSDIssueDrafts(c.Request.Context(), sources, actorID, nil)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"projectId":    project.ID,
		"specsRepoId":  project.SpecsRepoID,
		"issueRepoId":  project.IssueRepoID,
		"issues":       issues,
		"previewCount": len(issues),
	})
}

// CreateFSDIssues creates GitLab issues in the configured issue repository from previewed drafts.
func CreateFSDIssues(c *gin.Context) {
	project, err := getAppProjectForFSDIssues(c.Request.Context(), c.Param("id"))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "project not found"})
		return
	}

	var req fsdIssueCreateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request: " + err.Error()})
		return
	}
	if err := services.ValidateFSDIssueDrafts(req.Issues); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	glClient, err := gitLabClientWithSaver(c)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create GitLab client: " + err.Error()})
		return
	}

	issueRepoID := strconv.FormatInt(project.IssueRepoID, 10)
	results := make([]fsdIssueCreateResult, 0, len(req.Issues))
	successCount := 0
	for _, draft := range req.Issues {
		title := strings.TrimSpace(draft.Title)
		description := strings.TrimSpace(draft.Description)
		labels := gitlab.LabelOptions(draft.Labels)
		opt := &gitlab.CreateIssueOptions{
			Title:       gitlab.Ptr(title),
			Description: gitlab.Ptr(description),
		}
		if len(labels) > 0 {
			opt.Labels = &labels
		}

		issue, _, err := glClient.Issues.CreateIssue(issueRepoID, opt)
		if err != nil {
			results = append(results, fsdIssueCreateResult{
				SourcePath: draft.SourcePath,
				Title:      title,
				Status:     "failed",
				Error:      err.Error(),
			})
			continue
		}

		successCount++
		results = append(results, fsdIssueCreateResult{
			SourcePath: draft.SourcePath,
			Title:      title,
			Status:     "success",
			Issue:      issue,
		})
	}

	status := http.StatusCreated
	if successCount != len(req.Issues) {
		status = http.StatusMultiStatus
	}
	c.JSON(status, gin.H{
		"projectId":    project.ID,
		"issueRepoId":  project.IssueRepoID,
		"createdCount": successCount,
		"failedCount":  len(req.Issues) - successCount,
		"results":      results,
	})
}

func gitLabClientWithSaver(c *gin.Context) (*gitlab.Client, error) {
	token := c.MustGet("token").(*oauth2.Token)
	sessionID := c.MustGet("session_id").(string)
	tokenSaver := func(ctx context.Context, t *oauth2.Token) error {
		return auth.UpdateSession(ctx, sessionID, t)
	}
	return GetGitLabClient(c, token, tokenSaver)
}

func defaultProjectBranch(ctx context.Context, glClient *gitlab.Client, projectID string) string {
	project, _, err := glClient.Projects.GetProject(projectID, nil, gitlab.WithContext(ctx))
	if err != nil || project == nil || strings.TrimSpace(project.DefaultBranch) == "" {
		return "main"
	}
	return project.DefaultBranch
}
