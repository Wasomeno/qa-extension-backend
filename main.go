package main

import (
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

	// Cleanup Playwright on exit
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-c
		fmt.Println("\nShutting down...")
		agent.StopPlaywright()
		os.Exit(0)
	}()

	router := gin.Default()
	router.Static("/static", "./static")

	api := router.Group("/api")

	// Public Routes
	api.POST("/auth/login", routes.LoginEndpoint)
	api.GET("/auth/gitlab/callback", routes.AuthCallbackEndpoint)
	api.GET("/auth/session", routes.GetSessionEndpoint)

	protected := api.Group("/")
	protected.Use(middleware.AuthMiddleware())
	{
		protected.POST("/recordings", handlers.SaveRecording)
		protected.GET("/recordings", handlers.ListRecordings)
		protected.GET("/recordings/:id", handlers.GetRecording)
		protected.PUT("/recordings/:id", handlers.UpdateRecording)
		protected.PATCH("/recordings/:id", handlers.UpdateRecording)
		protected.DELETE("/recordings/:id", handlers.DeleteRecording)
		protected.POST("/recordings/bulk-delete", handlers.BulkDeleteRecordings)

		protected.POST("/test-scenarios/upload", handlers.UploadScenario)
		protected.GET("/test-scenarios", handlers.ListScenarios)
		protected.GET("/test-scenarios/:id", handlers.GetScenario)
		protected.DELETE("/test-scenarios/:id", handlers.DeleteScenario)
		protected.POST("/test-scenarios/:id/generate", handlers.GenerateTests)
		protected.GET("/test-scenarios/:id/stream", handlers.StreamEvents)
		protected.POST("/test-scenarios/bulk-delete", handlers.BulkDeleteScenarios)

		protected.POST("/recordings/:id/run", handlers.RunRecording)

		// Public SSE stream - no auth required, the connection will be authenticated via session_id cookie
		api.GET("/stream", handlers.StreamEvents)

		protected.POST("/auth/logout", routes.LogoutEndpoint)
		protected.GET("/current-user", routes.GetUser)
		protected.GET("/projects", routes.GetProjects)
		protected.GET("/projects/:id", routes.GetProject)
		protected.GET("/projects/:id/labels", routes.GetProjectLabels)
		protected.GET("/projects/:id/issues", routes.GetProjectIssues)
		protected.POST("/projects/:id/issues", routes.CreateIssue)
		protected.POST("/projects/:id/issues-with-child", routes.CreateIssueWithChild)
		protected.PUT("/projects/:id/issues/:issue_id", routes.UpdateIssue)
		protected.GET("/projects/:id/issues/:issue_id", routes.GetIssue)
		protected.GET("/projects/:id/issues/:issue_id/comments", routes.GetIssueComments)
		protected.POST("/projects/:id/issues/:issue_id/comments", routes.CreateIssueComment)
		protected.POST("/projects/:id/issues/:issue_id/evidence", routes.CreateIssueEvidence)
		protected.PUT("/projects/:id/issues/:issue_id/comments/:note_id", routes.UpdateIssueComment)
		protected.DELETE("/projects/:id/issues/:issue_id/comments/:note_id", routes.DeleteIssueComment)
		protected.GET("/projects/:id/issues/:issue_id/links", routes.GetIssueLinks)
		protected.POST("/projects/:id/issues/:issue_id/links", routes.CreateIssueLink)
		protected.DELETE("/projects/:id/issues/:issue_id/links/:link_id", routes.DeleteIssueLink)
		protected.POST("/projects/:id/issues/:issue_id/children", routes.CreateChildIssue)
		protected.DELETE("/projects/:id/issues/:issue_id/children/:child_id", routes.UnlinkChildIssue)
		protected.GET("/projects/:id/members", routes.GetProjectMembers)
		protected.GET("/projects/:id/boards", routes.GetProjectBoards)
		protected.GET("/projects/:id/knowledge-graphs", routes.ListKnowledgeGraphs)
		protected.GET("/projects/:id/knowledge-graph", routes.GetKnowledgeGraph)
		protected.GET("/projects/:id/knowledge-graph/coverage", routes.GetKnowledgeGraphCoverage)
		protected.DELETE("/projects/:id/knowledge-graph", routes.InvalidateKnowledgeGraph)

		protected.GET("/issues", routes.GetIssues)
		protected.GET("/issues/:id", routes.GetIssue)
		protected.GET("/issues/open-ai-test", routes.SmartAutoCompleteIssueDescription)
		protected.POST("/agent/chat", routes.ChatWithAgent)
		protected.POST("/agent/fix-issue", routes.FixIssueWithAgent)
		protected.GET("/agent/fix-status/:session_id", routes.GetFixStatus)
		protected.POST("/agent/commands", routes.CreateCustomCommand)
		protected.GET("/agent/commands", routes.ListCustomCommands)
		protected.DELETE("/agent/commands/:id", routes.DeleteCustomCommand)
		protected.GET("/dashboard", routes.GetDashboardStats)
		protected.GET("/debug/notes/:project_id/:issue_iid", routes.DebugIssueNotes)
	}

	router.Run("0.0.0.0:3000")
}
