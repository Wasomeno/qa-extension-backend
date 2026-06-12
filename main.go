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
		protected.POST("/recordings", handlers.SaveRecording)
		protected.GET("/recordings", handlers.ListRecordings)
		protected.GET("/recordings/:id", handlers.GetRecording)
		protected.PUT("/recordings/:id", handlers.UpdateRecording)
		protected.PATCH("/recordings/:id", handlers.UpdateRecording)
		protected.DELETE("/recordings/:id", handlers.DeleteRecording)
		protected.POST("/recordings/bulk-delete", handlers.BulkDeleteRecordings)

		protected.GET("/test-scenarios", handlers.ListScenarios)
		protected.GET("/test-scenarios/:id", handlers.GetScenario)
		protected.PATCH("/test-scenarios/:id", handlers.UpdateScenario)
		protected.DELETE("/test-scenarios/:id", handlers.DeleteScenario)
		protected.GET("/test-scenarios/:id/stream", handlers.StreamEvents)
		protected.POST("/test-scenarios/bulk-delete", handlers.BulkDeleteScenarios)

		// Project-scoped recordings and test scenarios.
		protected.POST("/projects/:id/recordings", handlers.SaveRecording)
		protected.GET("/projects/:id/recordings", handlers.ListRecordings)
		protected.GET("/projects/:id/recordings/:recording_id", handlers.GetRecording)
		protected.PUT("/projects/:id/recordings/:recording_id", handlers.UpdateRecording)
		protected.PATCH("/projects/:id/recordings/:recording_id", handlers.UpdateRecording)
		protected.DELETE("/projects/:id/recordings/:recording_id", handlers.DeleteRecording)
		protected.POST("/projects/:id/recordings/bulk-delete", handlers.BulkDeleteRecordings)
		protected.POST("/projects/:id/recordings/:recording_id/run", handlers.RunRecording)

		protected.POST("/projects/:id/test-scenarios/sync", routes.SyncAppProjectTestScenarios)
		protected.GET("/projects/:id/test-scenarios/import-status", routes.GetAppProjectTestScenarioImportStatus)
		protected.GET("/projects/:id/test-scenarios", handlers.ListScenarios)
		protected.GET("/projects/:id/test-scenarios/:scenario_id", handlers.GetScenario)
		protected.PATCH("/projects/:id/test-scenarios/:scenario_id", handlers.UpdateScenario)
		protected.DELETE("/projects/:id/test-scenarios/:scenario_id", handlers.DeleteScenario)
		protected.POST("/projects/:id/test-scenarios/:scenario_id/automations", handlers.GenerateTestCaseAutomations)
		protected.POST("/projects/:id/test-scenarios/:scenario_id/sync", handlers.SyncScenario)
		protected.GET("/projects/:id/test-scenarios/:scenario_id/stream", handlers.StreamEvents)
		protected.POST("/projects/:id/test-scenarios/bulk-delete", handlers.BulkDeleteScenarios)

		// Test case CRUD endpoints
		protected.POST("/test-scenarios/:id/sections/:sectionId/test-cases", handlers.AddTestCase)
		protected.PATCH("/test-scenarios/:id/sections/:sectionId/test-cases/reorder", handlers.ReorderTestCases)
		protected.PATCH("/test-scenarios/:id/sections/:sectionId/test-cases/:tcId", handlers.UpdateTestCase)
		protected.PATCH("/test-scenarios/:id/test-cases/:tcId/automation-category", handlers.UpdateTestCaseAutomationCategory)
		protected.PATCH("/test-scenarios/:id/sections/:sectionId/test-cases/:tcId/automation-category", handlers.UpdateTestCaseAutomationCategory)
		protected.POST("/test-scenarios/:id/sections/:sectionId/test-cases/:tcId/run", handlers.RunScenarioTestCase)
		protected.POST("/projects/:id/test-scenarios/:scenario_id/sections/:sectionId/test-cases", handlers.AddTestCase)
		protected.PATCH("/projects/:id/test-scenarios/:scenario_id/sections/:sectionId/test-cases/reorder", handlers.ReorderTestCases)
		protected.PATCH("/projects/:id/test-scenarios/:scenario_id/sections/:sectionId/test-cases/:tcId", handlers.UpdateTestCase)
		protected.PATCH("/projects/:id/test-scenarios/:scenario_id/test-cases/:tcId/automation-category", handlers.UpdateTestCaseAutomationCategory)
		protected.PATCH("/projects/:id/test-scenarios/:scenario_id/sections/:sectionId/test-cases/:tcId/automation-category", handlers.UpdateTestCaseAutomationCategory)
		protected.POST("/projects/:id/test-scenarios/:scenario_id/sections/:sectionId/test-cases/:tcId/run", handlers.RunScenarioTestCase)
		protected.GET("/projects/:id/test-scenarios/:scenario_id/test-cases/:tcId/manual-results", handlers.ListManualTestResults)
		protected.POST("/projects/:id/test-scenarios/:scenario_id/test-cases/:tcId/manual-results", handlers.CreateManualTestResult)

		protected.POST("/recordings/:id/run", handlers.RunRecording)

		// Public SSE stream - no auth required, the connection will be authenticated via session_id cookie
		api.GET("/stream", handlers.StreamEvents)

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

		// File uploads
		protected.POST("/projects/:id/uploads", routes.UploadFile)
		protected.GET("/files/proxy", routes.ProxyFile)

		// Project-scoped GitLab issue and board operations use the project's issueRepoId.
		protected.GET("/projects/:id/labels", routes.WithIssueRepo(routes.GetProjectLabels))
		protected.GET("/projects/:id/issues", routes.WithIssueRepo(routes.GetProjectIssues))
		protected.POST("/projects/:id/issues", routes.WithIssueRepo(routes.CreateIssue))
		protected.POST("/projects/:id/issues-with-child", routes.WithIssueRepo(routes.CreateIssueWithChild))
		protected.POST("/projects/:id/fsd-issues/preview", routes.PreviewFSDIssues)
		protected.POST("/projects/:id/fsd-issues", routes.CreateFSDIssues)
		protected.PUT("/projects/:id/issues/:issue_id", routes.WithIssueRepo(routes.UpdateIssue))
		protected.GET("/projects/:id/issues/:issue_id", routes.WithIssueRepo(routes.GetIssue))
		protected.GET("/projects/:id/issues/:issue_id/comments", routes.WithIssueRepo(routes.GetIssueComments))
		protected.POST("/projects/:id/issues/:issue_id/comments", routes.WithIssueRepo(routes.CreateIssueComment))
		protected.POST("/projects/:id/issues/:issue_id/evidence", routes.WithIssueRepo(routes.CreateIssueEvidence))
		protected.PUT("/projects/:id/issues/:issue_id/comments/:note_id", routes.WithIssueRepo(routes.UpdateIssueComment))
		protected.DELETE("/projects/:id/issues/:issue_id/comments/:note_id", routes.WithIssueRepo(routes.DeleteIssueComment))
		protected.GET("/projects/:id/issues/:issue_id/links", routes.WithIssueRepo(routes.GetIssueLinks))
		protected.POST("/projects/:id/issues/:issue_id/links", routes.WithIssueRepo(routes.CreateIssueLink))
		protected.DELETE("/projects/:id/issues/:issue_id/links/:link_id", routes.WithIssueRepo(routes.DeleteIssueLink))
		protected.POST("/projects/:id/issues/:issue_id/children", routes.WithIssueRepo(routes.CreateChildIssue))
		protected.DELETE("/projects/:id/issues/:issue_id/children/:child_id", routes.WithIssueRepo(routes.UnlinkChildIssue))
		protected.GET("/projects/:id/boards", routes.WithIssueRepo(routes.GetProjectBoards))

		// Project-scoped knowledge graph and specs use the project's specsRepoId.
		protected.GET("/projects/:id/knowledge-graphs", routes.WithSpecsRepo(routes.ListKnowledgeGraphs))
		protected.GET("/projects/:id/knowledge-graph", routes.WithSpecsRepo(routes.GetKnowledgeGraph))
		protected.GET("/projects/:id/knowledge-graph/coverage", routes.WithSpecsRepo(routes.GetKnowledgeGraphCoverage))
		protected.DELETE("/projects/:id/knowledge-graph", routes.WithSpecsRepo(routes.InvalidateKnowledgeGraph))
		protected.GET("/projects/:id/specs/tree", routes.WithSpecsRepo(routes.GetSpecsTree))
		protected.GET("/projects/:id/specs/file", routes.WithSpecsRepo(routes.GetSpecsFile))
		protected.PUT("/projects/:id/specs/file", routes.WithSpecsRepo(routes.SaveSpecsFile))
		protected.DELETE("/projects/:id/specs/file", routes.WithSpecsRepo(routes.DeleteSpecsFile))
		protected.POST("/projects/:id/specs/commit", routes.WithSpecsRepo(routes.CommitSpecsFiles))
		protected.GET("/projects/:id/specs/commits", routes.WithSpecsRepo(routes.GetSpecsCommits))
		protected.GET("/projects/:id/specs/commits/:sha", routes.WithSpecsRepo(routes.GetSpecsCommitDetail))
		protected.GET("/projects/:id/specs/search", routes.WithSpecsRepo(routes.SearchSpecs))
		protected.GET("/projects/:id/specs/blame", routes.WithSpecsRepo(routes.GetSpecsFileBlame))

		protected.GET("/issues", routes.GetIssues)
		protected.GET("/issues/:id", routes.GetIssue)
		protected.GET("/issues/open-ai-test", routes.SmartAutoCompleteIssueDescription)
		protected.GET("/agent/chat-sessions", routes.ListSessions)
		protected.GET("/agent/chat-sessions/:session_id", routes.GetSession)
		protected.DELETE("/agent/chat-sessions/:session_id", routes.DeleteSession)
		protected.POST("/agent/chat", routes.ChatWithAgent)
		protected.POST("/agent/fix-issue", routes.FixIssueWithAgent)
		protected.GET("/agent/fix-sessions", routes.ListFixSessions)
		protected.GET("/agent/fix-status/:session_id", routes.GetFixStatus)
		protected.DELETE("/agent/fix-sessions/:session_id", routes.DeleteFixSession)
		protected.POST("/agent/fix-sessions/:session_id/retry", routes.RetryFixSession)
		protected.POST("/projects/:id/fix-issue", routes.FixIssueWithAgent)
		protected.GET("/projects/:id/fix-sessions", routes.ListFixSessions)
		protected.GET("/projects/:id/fix-sessions/:session_id", routes.GetFixStatus)
		protected.DELETE("/projects/:id/fix-sessions/:session_id", routes.DeleteFixSession)
		protected.POST("/projects/:id/fix-sessions/:session_id/retry", routes.RetryFixSession)
		// Scenario agent — discussion agent for the test scenario page (GitLab tools only)
		protected.POST("/projects/:id/scenario-agent/chat", routes.ChatWithScenarioAgent)
		protected.GET("/projects/:id/scenario-agent/sessions", routes.ListScenarioSessions)
		protected.GET("/projects/:id/scenario-agent/sessions/:session_id", routes.GetScenarioSession)
		protected.DELETE("/projects/:id/scenario-agent/sessions/:session_id", routes.DeleteScenarioSession)
		protected.POST("/agent/commands", routes.CreateCustomCommand)
		protected.GET("/agent/commands", routes.ListCustomCommands)
		protected.DELETE("/agent/commands/:id", routes.DeleteCustomCommand)

		// Token Usage
		protected.GET("/token-usage", routes.GetTokenUsage)
		protected.GET("/token-usage/summary", routes.GetTokenUsageSummary)
		protected.GET("/token-usage/call/:request_id", routes.GetTokenCallDetail)
		protected.GET("/debug/notes/:project_id/:issue_iid", routes.DebugIssueNotes)
	}

	router.Run("0.0.0.0:3000")
}
