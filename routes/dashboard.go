package routes

import (
    "context"
    "net/http"

    "qa-extension-backend/client"
    authHandler "qa-extension-backend/handlers"

    "github.com/gin-gonic/gin"
    gitlab "gitlab.com/gitlab-org/api/client-go"
    "golang.org/x/oauth2"
)

func GetDashboardStats(ginContext *gin.Context) {
    token := ginContext.MustGet("token").(*oauth2.Token)
    sessionID := ginContext.MustGet("session_id").(string)

    tokenSaver := func(ctx context.Context, t *oauth2.Token) error {
        return authHandler.UpdateSession(ctx, sessionID, t)
    }

    gitlabClient, err := client.GetClient(ginContext, token, tokenSaver)
    if err != nil {
        ginContext.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create GitLab client: " + err.Error()})
        ginContext.Abort()
        return
    }

    scope := "assigned_to_me"

    // Fetch Recent Issues
    recentOpts := &gitlab.ListIssuesOptions{
        Scope: &scope,
        ListOptions: gitlab.ListOptions{
            PerPage: 5,
            Page:    1,
        },
        OrderBy: gitlab.Ptr("updated_at"),
        Sort:    gitlab.Ptr("desc"),
    }
    recentIssues, _, _ := gitlabClient.Issues.ListIssues(recentOpts)

    recentActivities := ConcurrentFeedAggregator(gitlabClient, recentIssues)

    ginContext.JSON(http.StatusOK, gin.H{
        "recent_issues":     recentIssues,
        "recent_activities": recentActivities,
    })
}
