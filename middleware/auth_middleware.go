package middleware

import (
	"log"
	"net/http"
	"qa-extension-backend/auth"
	"qa-extension-backend/config"

	"github.com/gin-gonic/gin"
)

func AuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		// Try cookie first, then fallback to headers or query params
		sessionID, err := c.Cookie("session_id")
		if err != nil || sessionID == "" {
			// Fallback 1: Custom Header
			sessionID = c.GetHeader("X-Session-ID")
			
			// Fallback 2: Standard Authorization Header (Bearer <session_id>)
			if sessionID == "" {
				authHeader := c.GetHeader("Authorization")
				if len(authHeader) > 7 && authHeader[:7] == "Bearer " {
					sessionID = authHeader[7:]
				}
			}
			
			// Fallback 3: Query Parameter (useful for SSE/Streams)
			if sessionID == "" {
				sessionID = c.Query("session_id")
			}
		} else {
			log.Printf("[AuthMiddleware] Found session_id in cookie")
		}

		if sessionID != "" && err != nil {
			log.Printf("[AuthMiddleware] Found session_id in header: %s", sessionID)
		}

		if sessionID == "" {
			log.Printf("[AuthMiddleware] No session ID found in cookie or X-Session-ID header")
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error": "Unauthorized: No session found",
				"code": "MISSING_SESSION",
			})
			return
		}

		token, err := auth.GetSession(c, sessionID)
		if err != nil {
			log.Printf("[AuthMiddleware] Invalid session %s: %v", sessionID, err)
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
