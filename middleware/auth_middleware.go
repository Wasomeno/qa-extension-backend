package middleware

import (
	"net/http"
	authHandler "qa-extension-backend/handlers"

	"github.com/gin-gonic/gin"
)

func AuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		sessionID, err := c.Cookie("session_id")
		if err != nil || sessionID == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized: No session found"})
			return
		}

		token, err := authHandler.GetSession(c, sessionID)
		if err != nil {
			// If session is invalid in Redis (expired or doesn't exist), return 401
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized: Invalid session"})
			return
		}

		// Store token and sessionID in context for handlers
		c.Set("token", token)
		c.Set("session_id", sessionID)
		c.Next()
	}
}
