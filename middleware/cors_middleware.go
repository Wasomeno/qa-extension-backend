package middleware

import (
	"log"
	"net/http"

	"github.com/gin-gonic/gin"
)

func CORSMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		origin := c.Request.Header.Get("Origin")
		if origin != "" {
			c.Header("Access-Control-Allow-Origin", origin)
		} else {
			// For credentials to work, Origin cannot be *
			// If no Origin header, we fallback to * but it might fail for credentialed requests
			c.Header("Access-Control-Allow-Origin", "*")
		}
		
		c.Header("Access-Control-Allow-Credentials", "true")
		c.Header("Access-Control-Allow-Headers", "Content-Type, Content-Length, Accept-Encoding, X-CSRF-Token, Authorization, accept, origin, Cache-Control, X-Requested-With, X-Session-ID, X-Agent-Session-ID")
		c.Header("Access-Control-Allow-Methods", "POST, OPTIONS, GET, PUT, DELETE, PATCH")
		c.Header("Access-Control-Expose-Headers", "Content-Length, Content-Type, X-Session-ID")

		if c.Request.Method == "OPTIONS" {
			log.Printf("[CORSMiddleware] Handling preflight request for %s from %s", c.Request.URL.Path, origin)
			c.AbortWithStatus(http.StatusNoContent)
			return
		}

		c.Next()
	}
}
