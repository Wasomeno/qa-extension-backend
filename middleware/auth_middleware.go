package middleware

import (
	"net/http"
	"qa-extension-backend/auth"
	"qa-extension-backend/config"

	"github.com/gin-gonic/gin"
)

func AuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		// Try cookie first, then fallback to X-Session-ID header (for web app)
		sessionID, err := c.Cookie("session_id")
		if err != nil || sessionID == "" {
			sessionID = c.GetHeader("X-Session-ID")
		}

		if sessionID == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error": "Unauthorized: No session found",
				"code": "MISSING_SESSION",
			})
			return
		}

		token, err := auth.GetSession(c, sessionID)
		if err != nil {
			// If session is invalid in Redis (expired or doesn't exist), return 401
			// and attempt to clear the invalid cookie
			isSecure := config.GetEnvOrDefault("APP_ENV", "development") == "production"
			cookieDomain := config.GetEnv("COOKIE_DOMAIN")
			c.SetCookie("session_id", "", -1, "/", cookieDomain, isSecure, true)
			
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error": "Unauthorized: Invalid or expired session",
				"details": err.Error(),
				"code": "INVALID_SESSION",
			})
			return
		}

		// Store token and sessionID in context for handlers
		c.Set("token", token)
		c.Set("session_id", sessionID)
		c.Next()
	}
}
