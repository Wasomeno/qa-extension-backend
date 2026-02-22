package routes

import (
	"context"
	"net/http"
	"qa-extension-backend/client"
	authHandler "qa-extension-backend/handlers"

	"github.com/gin-gonic/gin"
	"golang.org/x/oauth2"
)

func LoginEndpoint(ginContext *gin.Context) {
	url := authHandler.GetAuthURL()
	ginContext.JSON(http.StatusOK, gin.H{"url": url})
}

func AuthCallbackEndpoint(ginContext *gin.Context) {
	defer func() {
		// Clear query params to prevent code reuse or history pollution optional
	}()
	code := ginContext.Query("code")
	if code == "" {
		ginContext.String(http.StatusBadRequest, "Missing code parameter")
		return
	}

	token, err := authHandler.ExchangeToken(ginContext, code)
	if err != nil {
		ginContext.String(http.StatusUnauthorized, "Failed to exchange token: "+err.Error())
		return
	}

	sessionID, err := authHandler.CreateSession(token)
	if err != nil {
		ginContext.String(http.StatusInternalServerError, "Failed to create session: "+err.Error())
		return
	}

	tokenSaver := func(ctx context.Context, t *oauth2.Token) error {
		return authHandler.UpdateSession(ctx, sessionID, t)
	}

	gitlabClient, err := client.GetClient(ginContext, token, tokenSaver)
	if err != nil {
		ginContext.String(http.StatusInternalServerError, "Failed to create GitLab client: "+err.Error())
		return
	}

	user, _, err := gitlabClient.Users.CurrentUser()
	if err != nil {
		ginContext.String(http.StatusInternalServerError, "Failed to fetch user: "+err.Error())
		return
	}

	// Set HttpOnly cookie
	// MaxAge: 3600*24 (24 hours) as per handler logic
	ginContext.SetCookie("session_id", sessionID, 3600*24, "/", "localhost", false, true)

	ginContext.JSON(http.StatusOK, gin.H{
		"message": "Login successful",
		"user":    user,
	})
}

func GetSessionEndpoint(ginContext *gin.Context) {
	sessionID, err := ginContext.Cookie("session_id")
	if err != nil || sessionID == "" {
		ginContext.JSON(http.StatusUnauthorized, gin.H{"error": "No session cookie found"})
		return
	}

	token, err := authHandler.GetSession(ginContext, sessionID)
	if err != nil {
		// Maybe clear cookie if session invalid
		ginContext.SetCookie("session_id", "", -1, "/", "localhost", false, true)
		ginContext.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid session"})
		return
	}

	ginContext.JSON(http.StatusOK, gin.H{
		"session": token,
	})
}

func LogoutEndpoint(ginContext *gin.Context) {
	sessionID, exists := ginContext.Get("session_id")
	if !exists {
		ginContext.JSON(http.StatusBadRequest, gin.H{"error": "No session ID found in context"})
		return
	}

	sid, ok := sessionID.(string)
	if !ok {
		ginContext.JSON(http.StatusInternalServerError, gin.H{"error": "Invalid session ID format"})
		return
	}

	// Delete from Redis
	if err := authHandler.DeleteSession(ginContext, sid); err != nil {
		ginContext.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete session: " + err.Error()})
		return
	}

	// Clear HttpOnly cookie
	ginContext.SetCookie("session_id", "", -1, "/", "localhost", false, true)

	ginContext.JSON(http.StatusOK, gin.H{
		"message": "Logout successful",
	})
}
