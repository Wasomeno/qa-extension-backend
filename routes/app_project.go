package routes

import (
	"net/http"
	"strconv"
	"strings"

	"qa-extension-backend/identity"
	"qa-extension-backend/internal/models"
	"qa-extension-backend/services"

	"github.com/gin-gonic/gin"
)

func CreateAppProject(c *gin.Context) {
	var req models.CreateAppProjectRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	req.Description = strings.TrimSpace(req.Description)
	if req.Name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name is required"})
		return
	}
	if req.IssueRepoID <= 0 || req.SpecsRepoID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "issueRepoId and specsRepoId must be valid GitLab repo IDs"})
		return
	}

	actorID, _ := identity.GetCurrentUserID(c)
	project, err := services.CreateAppProject(c.Request.Context(), req, actorID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusCreated, project)
}

func ListAppProjects(c *gin.Context) {
	projects, err := services.ListAppProjects(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"projects": projects})
}

func GetAppProject(c *gin.Context) {
	project, err := services.GetAppProject(c.Request.Context(), c.Param("id"))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "project not found"})
		return
	}
	c.JSON(http.StatusOK, project)
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
		c.JSON(http.StatusNotFound, gin.H{"error": "project not found"})
		return
	}
	c.JSON(http.StatusOK, project)
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
