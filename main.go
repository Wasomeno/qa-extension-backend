package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"log"
	"os"
	"os/signal"
	"qa-extension-backend/agent"
	"qa-extension-backend/config"
	"qa-extension-backend/database"
	"qa-extension-backend/handlers"
	"qa-extension-backend/middleware"
	"qa-extension-backend/routes"
	"syscall"

	"github.com/gin-gonic/gin"
)

func main() {
	if b64Creds := os.Getenv("GCP_CREDS_BASE64"); b64Creds != "" {
		credsPath := "/tmp/gcp-key.json"
		if decoded, err := base64.StdEncoding.DecodeString(b64Creds); err == nil {
			if err := os.WriteFile(credsPath, decoded, 0600); err == nil {
				os.Setenv("GOOGLE_APPLICATION_CREDENTIALS", credsPath)
			}
		}
	} else if jsonCreds := os.Getenv("GCP_CREDS_JSON"); jsonCreds != "" {
		credsPath := "/tmp/gcp-key.json"
		if err := os.WriteFile(credsPath, []byte(jsonCreds), 0600); err == nil {
			os.Setenv("GOOGLE_APPLICATION_CREDENTIALS", credsPath)
		} else {
			log.Printf("Warning: Failed to write GCP credentials file: %v", err)
		}
	}

	config.Init()

	if err := database.InitRedis(); err != nil {
		log.Fatalf("Could not connect to Redis: %v", err)
	}

	fmt.Println("Redis connected successfully")
	workerCtx, workerCancel := context.WithCancel(context.Background())
	agent.StartE2EGenerationWorkers(workerCtx)

	// Cleanup Playwright on exit
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-c
		fmt.Println("\nShutting down...")
		workerCancel()
		os.Exit(0)
	}()

	router := gin.Default()
	router.Use(middleware.CORSMiddleware())
	router.Static("/static", "./static")

	api := router.Group("/api")

	// Public Routes
	api.POST("/auth/login", routes.LoginEndpoint)
	api.GET("/auth/gitlab/callback", routes.AuthCallbackEndpoint)
	api.GET("/auth/session", routes.GetSessionEndpoint)

	protected := api.Group("")
	protected.Use(middleware.AuthMiddleware())
	{
		// The browser EventSource API cannot send custom headers, so the
		// authenticated session may be supplied as a query parameter. Keep the
		// stream behind the same middleware as every other app endpoint.
		protected.GET("/stream", handlers.StreamEvents)

		protected.POST("/auth/logout", routes.LogoutEndpoint)
		protected.GET("/current-user", routes.GetUser)

		// GitLab repository selection and repository-specific helpers
		protected.GET("/gitlab/projects", routes.GetProjects)
		protected.GET("/gitlab/projects/:id", routes.GetProject)
		protected.GET("/gitlab/projects/:id/labels", routes.GetProjectLabels)
		protected.GET("/gitlab/projects/:id/members", routes.GetProjectMembers)
		protected.GET("/gitlab/projects/:id/branches", routes.GetProjectBranches)

		// Public QA projects
		protected.POST("/projects", routes.CreateAppProject)
		protected.GET("/projects", routes.ListAppProjects)
		protected.GET("/projects/:id", routes.GetAppProject)
		protected.PATCH("/projects/:id", routes.UpdateAppProject)
		protected.PUT("/projects/:id", routes.UpdateAppProject)
		protected.GET("/projects/:id/test-context", routes.GetProjectTestContext)
		protected.PUT("/projects/:id/test-context", routes.UpdateProjectTestContext)
		protected.PATCH("/projects/:id/test-context", routes.UpdateProjectTestContext)
		protected.DELETE("/projects/:id", routes.DeleteAppProject)
		protected.GET("/projects/:id/activity", routes.ListAppProjectActivity)
		protected.GET("/projects/:id/dashboard", routes.GetProjectDashboard)
		protected.GET("/projects/:id/boards", routes.WithIssueRepo(routes.GetProjectBoards))

		// File uploads
		protected.POST("/projects/:id/uploads", routes.UploadFile)
		protected.GET("/files/proxy", routes.ProxyFile)

		// Project-scoped FSD issues
		protected.POST("/projects/:id/fsd-issues/preview", routes.PreviewFSDIssues)
		protected.POST("/projects/:id/fsd-issues", routes.CreateFSDIssues)

		// Project-scoped specs use the project's specsRepoId.
		protected.GET("/projects/:id/specs/tree", routes.WithSpecsRepo(routes.GetSpecsTree))
		protected.GET("/projects/:id/specs/file", routes.WithSpecsRepo(routes.GetSpecsFile))
		protected.PUT("/projects/:id/specs/file", routes.WithSpecsRepo(routes.SaveSpecsFile))
		protected.DELETE("/projects/:id/specs/file", routes.WithSpecsRepo(routes.DeleteSpecsFile))
		protected.POST("/projects/:id/specs/commit", routes.WithSpecsRepo(routes.CommitSpecsFiles))
		protected.GET("/projects/:id/specs/commits", routes.WithSpecsRepo(routes.GetSpecsCommits))
		protected.GET("/projects/:id/specs/commits/:sha", routes.WithSpecsRepo(routes.GetSpecsCommitDetail))
		protected.GET("/projects/:id/specs/search", routes.WithSpecsRepo(routes.SearchSpecs))
		protected.GET("/projects/:id/specs/blame", routes.WithSpecsRepo(routes.GetSpecsFileBlame))

		protected.GET("/debug/notes/:project_id/:issue_iid", routes.DebugIssueNotes)
	}

	router.Run("0.0.0.0:3000")
}
