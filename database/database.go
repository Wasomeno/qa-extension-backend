package database

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"
	"qa-extension-backend/internal/models"

	"github.com/redis/go-redis/v9"
)

var RedisClient *redis.Client

// 2. Create an Init function to set up the connection.
func InitRedis() error {
	redisAddr := os.Getenv("REDIS_ADDR")
	if redisAddr == "" {
		redisAddr = "localhost:6379"
	}

	RedisClient = redis.NewClient(&redis.Options{
		Addr:     redisAddr,
		Password: "", // no password set
		DB:       0,  // use default DB
	})

	// Verify the connection
	return RedisClient.Ping(context.Background()).Err()
}

func SaveTestResult(ctx context.Context, result *models.TestResult) error {
	data, err := json.Marshal(result)
	if err != nil {
		return err
	}
	
	resultID := fmt.Sprintf("result:%s:%d", result.TestID, time.Now().Unix())
	if err := RedisClient.Set(ctx, resultID, data, 0).Err(); err != nil {
		return err
	}
	
	// Add to results list for the test
	return RedisClient.LPush(ctx, fmt.Sprintf("results:%s", result.TestID), resultID).Err()
}
