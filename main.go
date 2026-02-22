package main

import (
	"fmt"
	"log"
	"qa-extension-backend/database"
	"qa-extension-backend/middleware"
	"qa-extension-backend/routes"

	"github.com/gin-gonic/gin"
)

func main() {
	if err := database.InitRedis(); err != nil {
		log.Fatalf("Could not connect to Redis: %v", err)
	}

	fmt.Println("Redis connected successfully")

	router := gin.Default()

	// Public Routes
	router.POST("/auth/login", routes.LoginEndpoint)
	router.GET("/auth/callback", routes.AuthCallbackEndpoint)
	router.GET("/auth/session", routes.GetSessionEndpoint)

	protected := router.Group("/")
	protected.Use(middleware.AuthMiddleware())
	{
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
		protected.PUT("/projects/:id/issues/:issue_id/comments/:note_id", routes.UpdateIssueComment)
		protected.DELETE("/projects/:id/issues/:issue_id/comments/:note_id", routes.DeleteIssueComment)
		protected.GET("/projects/:id/issues/:issue_id/links", routes.GetIssueLinks)
		protected.POST("/projects/:id/issues/:issue_id/links", routes.CreateIssueLink)
		protected.DELETE("/projects/:id/issues/:issue_id/links/:link_id", routes.DeleteIssueLink)
                protected.POST("/projects/:id/issues/:issue_id/children", routes.CreateChildIssue)
                protected.DELETE("/projects/:id/issues/:issue_id/children/:child_id", routes.UnlinkChildIssue)
		protected.GET("/projects/:id/members", routes.GetProjectMembers)
		protected.GET("/projects/:id/boards", routes.GetProjectBoards)

		protected.GET("/issues", routes.GetIssues)
		protected.GET("/issues/:id", routes.GetIssue)
		protected.GET("/issues/open-ai-test", routes.SmartAutoCompleteIssueDescription)
		protected.GET("/dashboard", routes.GetDashboardStats)
		protected.GET("/debug/notes/:project_id/:issue_iid", routes.DebugIssueNotes)
	}

	router.Run(":3000")
}
