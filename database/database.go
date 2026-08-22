package database

import (
	"context"
	"encoding/json"
	"os"
	"time"

	"github.com/redis/go-redis/v9"
)

var RedisClient *redis.Client

// InitRedis opens the shared Redis connection used by every component that
// needs Redis (sessions, SSE pub/sub, scenarios, boards, etc.).
func InitRedis() error {
	redisAddr := os.Getenv("REDIS_ADDR")
	if redisAddr == "" {
		redisAddr = "localhost:6379"
	}

	RedisClient = redis.NewClient(&redis.Options{
		Addr:     redisAddr,
		Password: "",
		DB:       0,
		PoolSize: 100,
	})

	return RedisClient.Ping(context.Background()).Err()
}

// BoardResponseCacheTTL is the TTL for cached board responses.
const BoardResponseCacheTTL = 15 * time.Minute

// GetCachedBoardResponse retrieves a cached board response.
func GetCachedBoardResponse(ctx context.Context, projectID string) ([]byte, bool) {
	if RedisClient == nil {
		return nil, false
	}
	key := "boards:response:" + projectID
	data, err := RedisClient.Get(ctx, key).Bytes()
	if err != nil {
		return nil, false
	}
	return data, true
}

// SetCachedBoardResponse stores a board response in cache.
func SetCachedBoardResponse(ctx context.Context, projectID string, data []byte) {
	if RedisClient == nil {
		return
	}
	key := "boards:response:" + projectID
	RedisClient.Set(ctx, key, data, BoardResponseCacheTTL)
}

// InvalidateBoardCache invalidates board cache for a project.
func InvalidateBoardCache(ctx context.Context, projectID string) {
	if RedisClient == nil {
		return
	}
	key := "boards:response:" + projectID
	RedisClient.Del(ctx, key)
}

// StreamEvent represents a unified SSE event for all long-running operations.
// Follows AG-UI-inspired event patterns for agent-to-frontend real-time communication.
type StreamEvent struct {
	Type          string           `json:"type"`                    // "generation" | "execution" | "agent"
	ResourceType  string           `json:"resourceType,omitempty"`  // "scenario" | "recording" | "session"
	ResourceID    string           `json:"resourceId,omitempty"`    // ID of the resource being operated on
	Stage         string           `json:"stage"`                   // "start", "progress", "done", "error"
	Message       string           `json:"message"`                 // Human-readable contextual message
	StepInfo      *StreamStepInfo  `json:"stepInfo,omitempty"`      // For execution step progress
	ImportStatus  any              `json:"importStatus,omitempty"`  // Structured project scenario import status
	ErrorInfo     *StreamErrorInfo `json:"errorInfo,omitempty"`     // Structured error details
	CorrelationID string           `json:"correlationId,omitempty"` // Links all events in a single operation
	Timestamp     string           `json:"timestamp"`               // RFC3339 timestamp
}

// StreamStepInfo describes progress within a multi-step operation (e.g. test execution)
type StreamStepInfo struct {
	CurrentStep int    `json:"currentStep"`        // 1-indexed
	TotalSteps  int    `json:"totalSteps"`         // Total steps in the operation
	StepName    string `json:"stepName"`           // Short description of current step
	Action      string `json:"action,omitempty"`   // e.g. "navigate", "click", "type"
	Progress    int    `json:"progress,omitempty"` // 0-100 percentage
}

// StreamErrorInfo provides structured error details
type StreamErrorInfo struct {
	Code    string `json:"code,omitempty"`    // Machine-readable error code
	Details string `json:"details,omitempty"` // Additional error context
}

// StreamChannel is the unified Redis pub/sub channel for all stream events.
const StreamChannel = "stream:events"

// PublishStreamEvent publishes a unified event to the shared Redis pub/sub
// channel. All SSE subscribers receive all events; the frontend filters by
// resourceId client-side.
func PublishStreamEvent(ctx context.Context, event StreamEvent) error {
	event.Timestamp = time.Now().Format(time.RFC3339)
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	return RedisClient.Publish(ctx, StreamChannel, string(data)).Err()
}

// SubscribeAllStreamEvents subscribes to the unified stream channel.
// Returns a Redis pub/sub subscription. Caller MUST call sub.Close() when done.
func SubscribeAllStreamEvents(ctx context.Context) *redis.PubSub {
	return RedisClient.Subscribe(ctx, StreamChannel)
}
