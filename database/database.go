package database

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
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
		PoolSize: 100, // Connection pool - prevents connection exhaustion
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

// IssueResponseCacheTTL is the TTL for cached issue responses
const IssueResponseCacheTTL = 5 * time.Minute

// GetCachedIssueResponse retrieves a cached issue response by cache key
// Returns the cached JSON bytes and true if found, or nil and false if not found
func GetCachedIssueResponse(ctx context.Context, cacheKey string) ([]byte, bool) {
	key := fmt.Sprintf("issues:response:%s", cacheKey)
	data, err := RedisClient.Get(context.Background(), key).Bytes()
	if err != nil {
		log.Printf("[CACHE DEBUG] Get key=%s, err=%v", key, err)
		return nil, false
	}
	log.Printf("[CACHE DEBUG] Get key=%s, len=%d, SUCCESS", key, len(data))
	return data, true
}

// SetCachedIssueResponse stores an issue response in cache
func SetCachedIssueResponse(ctx context.Context, cacheKey string, data []byte) error {
	key := fmt.Sprintf("issues:response:%s", cacheKey)
	err := RedisClient.Set(context.Background(), key, data, IssueResponseCacheTTL).Err()
	log.Printf("[CACHE DEBUG] Set key=%s, len=%d, err=%v", key, len(data), err)
	return err
}

// GetRedisKeyForDebug returns the actual Redis key for a cache key
func GetRedisKeyForDebug(cacheKey string) string {
	return fmt.Sprintf("issues:response:%s", cacheKey)
}

// InvalidateIssueResponseCache invalidates cached issue responses
// Call this when issues are created, updated, or deleted
func InvalidateIssueResponseCache(ctx context.Context) {
	// Use pattern matching to find and delete all issue response caches
	// Redis SCAN is better than KEYS for production to avoid blocking
	iter := RedisClient.Scan(ctx, 0, "issues:response:*", 100).Iterator()
	for iter.Next(ctx) {
		RedisClient.Del(ctx, iter.Val())
	}
}

// GenerateIssueCacheKey creates a deterministic cache key based on query parameters
func GenerateIssueCacheKey(labels, search, issueIds, assigneeId, assigneeIds, authorId, state, projectIds string, limit int) string {
	// Create a simple hash from the parameters
	key := fmt.Sprintf("l:%s|s:%s|i:%s|ai:%s|ais:%s|au:%s|st:%s|p:%s|lim:%d",
		labels, search, issueIds, assigneeId, assigneeIds, authorId, state, projectIds, limit)
	return key
}

// BoardResponseCacheTTL is the TTL for cached board responses
const BoardResponseCacheTTL = 2 * time.Minute

// GetCachedBoardResponse retrieves a cached board response
func GetCachedBoardResponse(ctx context.Context, projectID string) ([]byte, bool) {
	key := fmt.Sprintf("boards:response:%s", projectID)
	data, err := RedisClient.Get(ctx, key).Bytes()
	if err != nil {
		return nil, false
	}
	return data, true
}

// SetCachedBoardResponse stores a board response in cache
func SetCachedBoardResponse(ctx context.Context, projectID string, data []byte) {
	key := fmt.Sprintf("boards:response:%s", projectID)
	RedisClient.Set(ctx, key, data, BoardResponseCacheTTL)
}

// InvalidateBoardCache invalidates board cache for a project
func InvalidateBoardCache(ctx context.Context, projectID string) {
	key := fmt.Sprintf("boards:response:%s", projectID)
	RedisClient.Del(ctx, key)
}

// GenerationEvent represents a SSE event published during test generation
type GenerationEvent struct {
	ScenarioID string `json:"scenarioId"`
	Stage      string `json:"stage"`      // "start", "fetching_codebase", "reading_files", "sending_to_ai", "processing", "saving", "done", "error"
	Message    string `json:"message"`     // Human-readable contextual message
	Data       string `json:"data,omitempty"` // Optional payload (final recordings JSON on "done")
}

// PublishGenerationEvent publishes a generation event to a Redis pub/sub channel
func PublishGenerationEvent(ctx context.Context, scenarioID string, event GenerationEvent) error {
	event.ScenarioID = scenarioID
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	channel := fmt.Sprintf("gen:%s", scenarioID)
	return RedisClient.Publish(ctx, channel, string(data)).Err()
}

// SubscribeGenerationEvents subscribes to generation events for a scenario
// Returns a Redis pub/sub subscription. Caller should call subscription.Close() when done.
func SubscribeGenerationEvents(ctx context.Context, scenarioID string) *redis.PubSub {
	channel := fmt.Sprintf("gen:%s", scenarioID)
	return RedisClient.Subscribe(ctx, channel)
}
