package authHandler

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"qa-extension-backend/database"
	"time"

	"github.com/google/uuid"
	"github.com/joho/godotenv"
	"gitlab.com/gitlab-org/api/client-go/gitlaboauth2"
	"golang.org/x/oauth2"
)

func getOAuthConfig() *oauth2.Config {
	_ = godotenv.Load()

	clientID := os.Getenv("GITLAB_APPLICATION_ID")
	clientSecret := os.Getenv("GITLAB_SECRET")
	// Updated to point to the main API server port
	redirectURL := "http://localhost:3000/auth/callback"
	// explicit scopes are better than empty string
	scopes := []string{"api", "read_user"}

	config := gitlaboauth2.NewOAuth2Config("", clientID, redirectURL, scopes)
	config.ClientSecret = clientSecret
	return config
}

func GetAuthURL() string {
	config := getOAuthConfig()
	// "state" should ideally be random to prevent CSRF, but static is fine for now
	return config.AuthCodeURL("state", oauth2.AccessTypeOffline)
}

func ExchangeToken(ctx context.Context, code string) (*oauth2.Token, error) {
	config := getOAuthConfig()
	return config.Exchange(ctx, code)
}

func CreateSession(token *oauth2.Token) (string, error) {
	sessionID := uuid.New().String()
	tokenBytes, err := json.Marshal(token)
	if err != nil {
		return "", err
	}

	// Store in Redis with expiration (24 hours)
	err = database.RedisClient.Set(context.Background(), "session:"+sessionID, tokenBytes, 24*time.Hour).Err()
	if err != nil {
		return "", err
	}
	return sessionID, nil
}

func GetSession(ctx context.Context, sessionID string) (*oauth2.Token, error) {
	sessionKey := "session:" + sessionID
	sessionData, err := database.RedisClient.Get(ctx, sessionKey).Result()
	if err != nil {
		return nil, err
	}

	var token *oauth2.Token
	if err := json.Unmarshal([]byte(sessionData), &token); err != nil {
		return nil, err
	}

	// Check if token needs refresh
	if !token.Valid() {
		// Token is expired or invalid, try to refresh
		config := getOAuthConfig()
		// Re-use the token source to get a refreshed token
		tokenSource := config.TokenSource(ctx, token)
		newToken, err := tokenSource.Token()
		if err != nil {
			return nil, fmt.Errorf("failed to refresh token: %w", err)
		}

		// Update session with new token
		if err := UpdateSession(ctx, sessionID, newToken); err != nil {
			return nil, fmt.Errorf("failed to update session in redis: %w", err)
		}

		return newToken, nil
	}

	return token, nil
}

func UpdateSession(ctx context.Context, sessionID string, token *oauth2.Token) error {
	tokenBytes, err := json.Marshal(token)
	if err != nil {
		return err
	}
	return database.RedisClient.Set(ctx, "session:"+sessionID, tokenBytes, 24*time.Hour).Err()
}
