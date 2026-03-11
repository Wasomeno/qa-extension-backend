package routes

import (
	"context"
	"fmt"
	authHandler "qa-extension-backend/handlers"
	"qa-extension-backend/client"

	"github.com/gin-gonic/gin"
	"golang.org/x/oauth2"
)

// GetCurrentUserID fetches the current user's GitLab ID from the context
func GetCurrentUserID(ginContext *gin.Context) (int, error) {
	token, ok := ginContext.MustGet("token").(*oauth2.Token)
	if !ok {
		return 0, fmt.Errorf("missing token")
	}
	sessionID, ok := ginContext.MustGet("session_id").(string)
	if !ok {
		return 0, fmt.Errorf("missing session id")
	}

	return GetCurrentUserIDFromCtx(ginContext.Request.Context(), token, sessionID)
}

// GetCurrentUserIDFromCtx fetches the current user's GitLab ID using a standard context
func GetCurrentUserIDFromCtx(ctx context.Context, token *oauth2.Token, sessionID string) (int, error) {
	tokenSaver := func(ctx context.Context, t *oauth2.Token) error {
		return authHandler.UpdateSession(ctx, sessionID, t)
	}

	gitlabClient, err := client.GetClient(ctx, token, tokenSaver)
	if err != nil {
		return 0, err
	}

	user, _, err := gitlabClient.Users.CurrentUser()
	if err != nil {
		return 0, err
	}

	return user.ID, nil
}
