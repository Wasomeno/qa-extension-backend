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

// Project name cache helpers with TTL
var projectCacheTTL = 30 * time.Minute

// GetCachedProjectName retrieves a cached project name by project ID
func GetCachedProjectName(ctx context.Context, projectID interface{}) (string, bool) {
	key := fmt.Sprintf("project:name:%v", projectID)
	name, err := RedisClient.Get(ctx, key).Result()
	if err != nil {
		return "", false
	}
	return name, true
}

// SetCachedProjectName stores a project name in cache
func SetCachedProjectName(ctx context.Context, projectID interface{}, name string) {
	key := fmt.Sprintf("project:name:%v", projectID)
	RedisClient.Set(ctx, key, name, projectCacheTTL)
}

// GetCachedWorkItemData retrieves cached work item data (child count, child items) by issue ID
func GetCachedWorkItemData(ctx context.Context, issueID int64) (childCount int, childItems []string, found bool) {
	key := fmt.Sprintf("workitem:children:%d", issueID)
	data, err := RedisClient.Get(ctx, key).Result()
	if err != nil {
		return 0, nil, false
	}
	
	var cached struct {
		ChildCount int      `json:"childCount"`
		ChildItems []string `json:"childItems"`
	}
	if err := json.Unmarshal([]byte(data), &cached); err != nil {
		return 0, nil, false
	}
	return cached.ChildCount, cached.ChildItems, true
}

// SetCachedWorkItemData stores work item child data in cache
func SetCachedWorkItemData(ctx context.Context, issueID int64, childCount int, childItemsJSON string) {
	key := fmt.Sprintf("workitem:children:%d", issueID)
	cachedData := struct {
		ChildCount int    `json:"childCount"`
		ChildJSON string `json:"childJSON"`
	}{
		ChildCount: childCount,
		ChildJSON:  childItemsJSON,
	}
	
	data, err := json.Marshal(cachedData)
	if err != nil {
		return
	}
	// Short TTL for work items since they change more frequently
	RedisClient.Set(ctx, key, data, 10*time.Minute)
}
