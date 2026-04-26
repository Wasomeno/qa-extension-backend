package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"qa-extension-backend/config"
	"qa-extension-backend/database"
	"time"

	"github.com/google/uuid"
	"gitlab.com/gitlab-org/api/client-go/gitlaboauth2"
	"golang.org/x/oauth2"
)

func getOAuthConfig() *oauth2.Config {
	baseURL := config.GetEnv("GITLAB_BASE_URL")
	clientID := config.GetEnv("GITLAB_APPLICATION_ID")
	clientSecret := config.GetEnv("GITLAB_SECRET")
	redirectURL := config.GetEnv("GITLAB_REDIRECT_URI")
	scopes := []string{"api", "read_user"}

	configMap := gitlaboauth2.NewOAuth2Config(baseURL, clientID, redirectURL, scopes)
	configMap.ClientSecret = clientSecret
	return configMap
}

func GetAuthURL(state string) string {
	cfg := getOAuthConfig()
	return cfg.AuthCodeURL(state, oauth2.AccessTypeOffline)
}

func ExchangeToken(ctx context.Context, code string) (*oauth2.Token, error) {
	cfg := getOAuthConfig()
	return cfg.Exchange(ctx, code)
}

func CreateSession(token *oauth2.Token) (string, error) {
	sessionID := uuid.New().String()
	tokenBytes, err := json.Marshal(token)
	if err != nil {
		return "", err
	}

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

	if !token.Valid() {
		cfg := getOAuthConfig()
		tokenSource := cfg.TokenSource(ctx, token)
		newToken, err := tokenSource.Token()
		if err != nil {
			return nil, fmt.Errorf("failed to refresh token: %w", err)
		}

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

func DeleteSession(ctx context.Context, sessionID string) error {
	return database.RedisClient.Del(ctx, "session:"+sessionID).Err()
}
