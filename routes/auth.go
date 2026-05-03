package routes

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/url"
	"qa-extension-backend/client"
	"qa-extension-backend/config"
	"qa-extension-backend/auth"
	"strings"

	"github.com/gin-gonic/gin"
	"golang.org/x/oauth2"
)

func LoginEndpoint(ginContext *gin.Context) {
	redirectURL := ginContext.Query("redirect_url")
	
	// Encode redirect URL into state parameter for callback retrieval
	state := "default"
	if redirectURL != "" {
		state = base64.URLEncoding.EncodeToString([]byte(redirectURL))
	}
	
	url := auth.GetAuthURL(state)
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

	token, err := auth.ExchangeToken(ginContext, code)
	if err != nil {
		ginContext.String(http.StatusUnauthorized, "Failed to exchange token: "+err.Error())
		return
	}

	sessionID, err := auth.CreateSession(token)
	if err != nil {
		ginContext.String(http.StatusInternalServerError, "Failed to create session: "+err.Error())
		return
	}

	tokenSaver := func(ctx context.Context, t *oauth2.Token) error {
		return auth.UpdateSession(ctx, sessionID, t)
	}

	gitlabClient, err := client.GetClient(ginContext, token, tokenSaver)
	if err != nil {
		ginContext.String(http.StatusInternalServerError, "Failed to create GitLab client: "+err.Error())
		return
	}

	_, _, err = gitlabClient.Users.CurrentUser()
	if err != nil {
		ginContext.String(http.StatusInternalServerError, "Failed to fetch user: "+err.Error())
		return
	}

	// Determine if we need Secure true/false based on environment
	isSecure := config.GetEnvOrDefault("APP_ENV", "development") == "production"
	cookieDomain := config.GetEnv("COOKIE_DOMAIN")

	// Set HttpOnly cookie
	// MaxAge: 3600*24 (24 hours) as per handler logic
	ginContext.SetCookie("session_id", sessionID, 3600*24, "/", cookieDomain, isSecure, true)

	// Check if a custom redirect was encoded in the state
	oauthState := ginContext.Query("state")
	redirectTarget := "/static/auth_success.html"
	
	if oauthState != "" && oauthState != "default" {
		decodedBytes, decodeErr := base64.URLEncoding.DecodeString(oauthState)
		if decodeErr == nil {
			decodedURL := string(decodedBytes)
			// Security: only allow localhost and known domains
			if isValidRedirectURL(decodedURL) {
				redirectTarget = decodedURL
			}
		}
	}

	// If redirecting to web app (not static page), append session_id to URL
	// so the web app can store it in localStorage (cookies won't work cross-domain via proxy)
	if redirectTarget != "/static/auth_success.html" {
		sep := "?"
		if strings.Contains(redirectTarget, "?") {
			sep = "&"
		}
		redirectTarget = redirectTarget + sep + "session_id=" + sessionID
	}

	ginContext.Redirect(http.StatusFound, redirectTarget)
}

// isValidRedirectURL prevents open redirect vulnerabilities
func isValidRedirectURL(target string) bool {
	u, err := url.Parse(target)
	if err != nil {
		return false
	}
	
	// Allow localhost for development
	if strings.HasPrefix(u.Host, "localhost:") || u.Host == "localhost" {
		return true
	}
	
	// Add your production domain here
	if u.Host == "playground-qa-extension.online" || strings.HasSuffix(u.Host, ".playground-qa-extension.online") {
		return true
	}

	// Allow arndev domains
	if u.Host == "flowg.arndev.nl" || strings.HasSuffix(u.Host, ".arndev.nl") {
		return true
	}
	
	return false
}

func GetSessionEndpoint(ginContext *gin.Context) {
	// Try cookie first, then fallback to X-Session-ID header
	sessionID, err := ginContext.Cookie("session_id")
	if err != nil || sessionID == "" {
		sessionID = ginContext.GetHeader("X-Session-ID")
	}

	if sessionID == "" {
		ginContext.JSON(http.StatusUnauthorized, gin.H{"error": "No session cookie found"})
		return
	}

	// Determine if we need Secure true/false based on environment
	isSecure := config.GetEnvOrDefault("APP_ENV", "development") == "production"
	cookieDomain := config.GetEnv("COOKIE_DOMAIN")

	token, err := auth.GetSession(ginContext, sessionID)
	if err != nil {
		// Maybe clear cookie if session invalid
		ginContext.SetCookie("session_id", "", -1, "/", cookieDomain, isSecure, true)
		ginContext.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid session"})
		return
	}

	ginContext.JSON(http.StatusOK, gin.H{
		"session": token,
		"session_id": sessionID,
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
	if err := auth.DeleteSession(ginContext, sid); err != nil {
		ginContext.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete session: " + err.Error()})
		return
	}

	// Determine if we need Secure true/false based on environment
	isSecure := config.GetEnvOrDefault("APP_ENV", "development") == "production"
	cookieDomain := config.GetEnv("COOKIE_DOMAIN")

	// Clear HttpOnly cookie
	ginContext.SetCookie("session_id", "", -1, "/", cookieDomain, isSecure, true)

	ginContext.JSON(http.StatusOK, gin.H{
		"message": "Logout successful",
	})
}
