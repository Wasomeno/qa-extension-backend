package routes

import (
    "context"
    "net/http"
    "strconv"

    "qa-extension-backend/client"
    authHandler "qa-extension-backend/handlers"

    "github.com/gin-gonic/gin"
    "golang.org/x/oauth2"
)

func DebugIssueNotes(ginContext *gin.Context) {
    token := ginContext.MustGet("token").(*oauth2.Token)
    sessionID := ginContext.MustGet("session_id").(string)
    
    projectID, _ := strconv.Atoi(ginContext.Param("project_id"))
    issueIID, _ := strconv.Atoi(ginContext.Param("issue_iid"))

    tokenSaver := func(ctx context.Context, t *oauth2.Token) error {
        return authHandler.UpdateSession(ctx, sessionID, t)
    }

    gitlabClient, err := client.GetClient(ginContext, token, tokenSaver)
    if err != nil {
        ginContext.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
        return
    }

    // Fetch top 50 notes
    notes, err := client.FetchRecentIssueNotes(gitlabClient, projectID, issueIID, 50)
    if err != nil {
         ginContext.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
         return
    }

    ginContext.JSON(http.StatusOK, notes)
}
