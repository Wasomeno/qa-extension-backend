package routes

import (
	"context"
	"net/http"

	"qa-extension-backend/auth"
	"qa-extension-backend/client"
	"qa-extension-backend/services"

	"github.com/gin-gonic/gin"
	"golang.org/x/oauth2"
)

// GetKnowledgeGraph retrieves the knowledge graph (module catalog) for a project
// It first checks the cache, and if not found, generates it on-the-fly
func GetKnowledgeGraph(c *gin.Context) {
	token := c.MustGet("token").(*oauth2.Token)
	sessionID := c.MustGet("session_id").(string)

	projectID := c.Param("id")
	branch := c.Query("branch")
	forceRefresh := c.Query("force_refresh") == "true"

	tokenSaver := func(ctx context.Context, t *oauth2.Token) error {
		return auth.UpdateSession(ctx, sessionID, t)
	}

	gitlabClient, err := client.GetClient(c, token, tokenSaver)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create GitLab client: " + err.Error()})
		return
	}

	// Get branch if not provided
	if branch == "" {
		project, _, err := gitlabClient.Projects.GetProject(projectID, nil)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get project: " + err.Error()})
			return
		}
		branch = project.DefaultBranch
	}

	ctx := c.Request.Context()
	graphMapper := services.NewGraphMapper()

	// Check cache first (unless force refresh)
	var catalog *services.ModuleCatalog
	if !forceRefresh {
		catalog, err = graphMapper.GetCachedCatalog(ctx, projectID, branch)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to check cache: " + err.Error()})
			return
		}
	}

	if catalog != nil {
		// Return cached version
		c.JSON(http.StatusOK, gin.H{
			"catalog":    catalog,
			"from_cache": true,
		})
		return
	}

	// Generate fresh catalog
	catalog, err = graphMapper.FetchAndEnrichCatalog(ctx, gitlabClient, projectID, branch)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to generate knowledge graph: " + err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"catalog":    catalog,
		"from_cache": false,
	})
}

// GetKnowledgeGraphCoverage returns coverage statistics for a project's knowledge graph
func GetKnowledgeGraphCoverage(c *gin.Context) {
	token := c.MustGet("token").(*oauth2.Token)
	sessionID := c.MustGet("session_id").(string)

	projectID := c.Param("id")
	branch := c.Query("branch")

	tokenSaver := func(ctx context.Context, t *oauth2.Token) error {
		return auth.UpdateSession(ctx, sessionID, t)
	}

	gitlabClient, err := client.GetClient(c, token, tokenSaver)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create GitLab client: " + err.Error()})
		return
	}

	if branch == "" {
		project, _, err := gitlabClient.Projects.GetProject(projectID, nil)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get project: " + err.Error()})
			return
		}
		branch = project.DefaultBranch
	}

	ctx := c.Request.Context()
	graphMapper := services.NewGraphMapper()

	// Get cached catalog
	catalog, err := graphMapper.GetCachedCatalog(ctx, projectID, branch)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get cached catalog: " + err.Error()})
		return
	}

	if catalog == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "No knowledge graph found for this project/branch. Generate one first."})
		return
	}

	// Generate coverage report from cached data
	// We need the route map - fetch from GitLab
	files, err := graphMapper.FetchFileTree(gitlabClient, projectID, branch)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch file tree: " + err.Error()})
		return
	}

	routeMapper := services.NewRouteMapper()
	routeMap := routeMapper.BuildRouteMapFromFileList(files)

	// Generate coverage report
	coverage := graphMapper.GenerateCoverageReport(routeMap, catalog, catalog.Selectors, 1, 0)

	c.JSON(http.StatusOK, gin.H{
		"coverage": coverage,
	})
}

// ListKnowledgeGraphs returns all cached knowledge graphs for a project
func ListKnowledgeGraphs(c *gin.Context) {
	projectID := c.Param("id")

	ctx := c.Request.Context()
	graphMapper := services.NewGraphMapper()

	catalogs, err := graphMapper.ListCachedCatalogs(ctx, projectID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list knowledge graphs: " + err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"catalogs": catalogs,
		"count":    len(catalogs),
	})
}

// InvalidateKnowledgeGraph removes the cached knowledge graph for a project/branch
func InvalidateKnowledgeGraph(c *gin.Context) {
	projectID := c.Param("id")
	branch := c.Query("branch")

	if branch == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "branch query parameter is required"})
		return
	}

	ctx := c.Request.Context()
	graphMapper := services.NewGraphMapper()

	err := graphMapper.InvalidateCatalog(ctx, projectID, branch)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to invalidate cache: " + err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message":    "Knowledge graph cache invalidated",
		"project_id": projectID,
		"branch":     branch,
	})
}