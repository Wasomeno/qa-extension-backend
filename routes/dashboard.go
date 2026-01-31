package routes

import (
    "context"
    "net/http"
    "time"
    "log"

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

    now := time.Now()
    startOfDay := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())

    offset := int(now.Weekday()) - 1
    if offset < 0 {
        offset = 6
    }
    startOfWeek := startOfDay.AddDate(0, 0, -offset)

    scope := "assigned_to_me"
    openedState := "opened"
    closedState := "closed"

    openOpts := &gitlab.ListIssuesOptions{
        State: &openedState,
        Scope: &scope,
        ListOptions: gitlab.ListOptions{
            PerPage: 1,
        },
    }

    closedOpts := &gitlab.ListIssuesOptions{
        State:        &closedState,
        UpdatedAfter: &startOfDay,
        Scope:        &scope,
        ListOptions: gitlab.ListOptions{
            PerPage: 1,
        },
    }

    weekOpts := &gitlab.ListIssuesOptions{
        CreatedAfter: &startOfWeek,
        Scope:        &scope,
        ListOptions: gitlab.ListOptions{
            PerPage: 1,
        },
    }

    getCount := func(opts *gitlab.ListIssuesOptions) int {
        _, resp, err := gitlabClient.Issues.ListIssues(opts)
        if err != nil {
            log.Printf("Error fetching stats: %v", err)
            return 0
        }
        return int(resp.TotalItems)
    }

    openCount := getCount(openOpts)
    closedCount := getCount(closedOpts)
    weekCount := getCount(weekOpts)

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

    ginContext.JSON(http.StatusOK, gin.H{
        "open_issues":   openCount,
        "closed_today":  closedCount,
        "this_week":     weekCount,
        "recent_issues": recentIssues,
    })
}
