package tracker

import (
	"context"
	"fmt"
	"log"
	"time"

	"qa-extension-backend/database"

	"github.com/redis/go-redis/v9"
)

// contextKey is a private type for context keys to avoid collisions.
type contextKey string

const (
	// ContextKeyUserID is the context key for the authenticated user ID.
	ContextKeyUserID contextKey = "tracker_user_id"
	// ContextKeySessionID is the context key for the session ID.
	ContextKeySessionID contextKey = "tracker_session_id"
	// ContextKeyRequestID is the context key for a unique request ID.
	ContextKeyRequestID contextKey = "tracker_request_id"
)

// TokenUsage represents a single LLM API call's token consumption.
type TokenUsage struct {
	// Identity
	RequestID string // unique per LLM call (auto-generated if empty)
	SessionID string // agent session or request session
	UserID    int    // who triggered it (0 if unknown)

	// What
	Feature string // "qa_chat", "test_generation", "graph_mapper", "issue_autocomplete", "maas", "glm"
	Model   string // "gemini-3-flash-preview", "gemini-3.1-flash-lite-preview", etc.

	// Tokens
	InputTokens  int32
	OutputTokens int32
	TotalTokens  int32

	// Cost (auto-calculated by Log)
	InputCostUSD  float64
	OutputCostUSD float64
	TotalCostUSD  float64

	// Metadata
	Timestamp  time.Time
	Duration   time.Duration // how long the LLM call took
	BatchIndex int           // for batched calls (-1 if not applicable)
}

// ModelPrice defines the cost per 1 million tokens for a model.
type ModelPrice struct {
	InputPer1M  float64 // USD per 1M input tokens
	OutputPer1M float64 // USD per 1M output tokens
}

// ModelPricing maps model names to their pricing.
// Update this table when pricing changes.
// Source: https://ai.google.dev/pricing (as of 2025)
var ModelPricing = map[string]ModelPrice{
	"gemini-3.1-flash-lite-preview": {
		InputPer1M:  0.075,
		OutputPer1M: 0.30,
	},
	"gemini-3-flash-preview": {
		InputPer1M:  0.15,
		OutputPer1M: 0.60,
	},
	"gemini-2.0-flash-exp": {
		InputPer1M:  0.075,
		OutputPer1M: 0.30,
	},
	"gemini-3.1-pro-preview": {
		InputPer1M:  1.25,
		OutputPer1M: 10.00,
	},
	// MaaS models (adjust based on your Vertex AI MaaS pricing)
	"meta/llama3-405b-instruct-maas": {
		InputPer1M:  1.00,
		OutputPer1M: 1.00,
	},
	"zai-org/glm-5": {
		InputPer1M:  1.00,
		OutputPer1M: 1.00,
	},
	"moonshotai/kimi-k2-5": {
		InputPer1M:  1.00,
		OutputPer1M: 1.00,
	},
}

// GetUserIDFromCtx extracts the user ID from context.
// Returns 0 if not found.
func GetUserIDFromCtx(ctx context.Context) int {
	if v := ctx.Value(ContextKeyUserID); v != nil {
		if id, ok := v.(int); ok {
			return id
		}
	}
	return 0
}

// GetSessionIDFromCtx extracts the session ID from context.
// Returns "" if not found.
func GetSessionIDFromCtx(ctx context.Context) string {
	if v := ctx.Value(ContextKeySessionID); v != nil {
		if s, ok := v.(string); ok {
			return s
		}
	}
	// Fallback: try legacy keys used in the codebase
	if v := ctx.Value("session_id"); v != nil {
		if s, ok := v.(string); ok {
			return s
		}
	}
	if v := ctx.Value("agent_session_id"); v != nil {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// WithUserID returns a context with the user ID set.
func WithUserID(ctx context.Context, userID int) context.Context {
	return context.WithValue(ctx, ContextKeyUserID, userID)
}

// WithSessionID returns a context with the session ID set.
func WithSessionID(ctx context.Context, sessionID string) context.Context {
	return context.WithValue(ctx, ContextKeySessionID, sessionID)
}

// calculateCost computes the USD cost for a token usage.
func calculateCost(usage *TokenUsage) {
	price, ok := ModelPricing[usage.Model]
	if !ok {
		// Unknown model — log warning but don't block
		log.Printf("[TokenTracker] WARNING: No pricing data for model %q, cost will be $0", usage.Model)
		return
	}

	usage.InputCostUSD = float64(usage.InputTokens) / 1_000_000 * price.InputPer1M
	usage.OutputCostUSD = float64(usage.OutputTokens) / 1_000_000 * price.OutputPer1M
	usage.TotalCostUSD = usage.InputCostUSD + usage.OutputCostUSD
}

// Log records a token usage event. It:
// 1. Calculates USD cost from the pricing table
// 2. Writes structured log
// 3. Aggregates counters in Redis (if available)
//
// This function is safe to call from any goroutine.
func Log(ctx context.Context, usage TokenUsage) {
	// Fill defaults
	if usage.RequestID == "" {
		usage.RequestID = fmt.Sprintf("req_%d", time.Now().UnixNano())
	}
	if usage.Timestamp.IsZero() {
		usage.Timestamp = time.Now()
	}
	if usage.BatchIndex == 0 {
		usage.BatchIndex = -1 // -1 means "not a batch call"
	}

	// Enrich from context if not set
	if usage.UserID == 0 {
		usage.UserID = GetUserIDFromCtx(ctx)
	}
	if usage.SessionID == "" {
		usage.SessionID = GetSessionIDFromCtx(ctx)
	}

	// Calculate cost
	calculateCost(&usage)

	// 1. Structured log (always)
	batchStr := ""
	if usage.BatchIndex >= 0 {
		batchStr = fmt.Sprintf(" batch=%d", usage.BatchIndex)
	}
	log.Printf("[TokenTracker] feature=%s model=%s input=%d output=%d total=%d cost=$%.6f user=%d session=%s duration=%v%s",
		usage.Feature,
		usage.Model,
		usage.InputTokens,
		usage.OutputTokens,
		usage.TotalTokens,
		usage.TotalCostUSD,
		usage.UserID,
		usage.SessionID,
		usage.Duration.Round(time.Millisecond),
		batchStr,
	)

	// 2. Redis aggregation (best-effort, don't fail the caller)
	if database.RedisClient == nil {
		return
	}

	rdb := database.RedisClient
	pipe := rdb.Pipeline()

	today := usage.Timestamp.Format("2006-01-02")

	// Global counters
	pipe.IncrBy(ctx, "token:total:input", int64(usage.InputTokens))
	pipe.IncrBy(ctx, "token:total:output", int64(usage.OutputTokens))
	pipe.IncrBy(ctx, "token:total:all", int64(usage.TotalTokens))
	pipe.IncrByFloat(ctx, "token:total:cost", usage.TotalCostUSD)
	pipe.Incr(ctx, "token:total:calls")

	// Per-feature
	pipe.IncrBy(ctx, fmt.Sprintf("token:feature:%s:input", usage.Feature), int64(usage.InputTokens))
	pipe.IncrBy(ctx, fmt.Sprintf("token:feature:%s:output", usage.Feature), int64(usage.OutputTokens))
	pipe.IncrBy(ctx, fmt.Sprintf("token:feature:%s:all", usage.Feature), int64(usage.TotalTokens))
	pipe.IncrByFloat(ctx, fmt.Sprintf("token:feature:%s:cost", usage.Feature), usage.TotalCostUSD)
	pipe.Incr(ctx, fmt.Sprintf("token:feature:%s:calls", usage.Feature))

	// Per-model
	pipe.IncrBy(ctx, fmt.Sprintf("token:model:%s:input", usage.Model), int64(usage.InputTokens))
	pipe.IncrBy(ctx, fmt.Sprintf("token:model:%s:output", usage.Model), int64(usage.OutputTokens))
	pipe.IncrBy(ctx, fmt.Sprintf("token:model:%s:all", usage.Model), int64(usage.TotalTokens))
	pipe.IncrByFloat(ctx, fmt.Sprintf("token:model:%s:cost", usage.Model), usage.TotalCostUSD)

	// Per-user (skip if unknown)
	if usage.UserID > 0 {
		pipe.IncrBy(ctx, fmt.Sprintf("token:user:%d:input", usage.UserID), int64(usage.InputTokens))
		pipe.IncrBy(ctx, fmt.Sprintf("token:user:%d:output", usage.UserID), int64(usage.OutputTokens))
		pipe.IncrBy(ctx, fmt.Sprintf("token:user:%d:all", usage.UserID), int64(usage.TotalTokens))
		pipe.IncrByFloat(ctx, fmt.Sprintf("token:user:%d:cost", usage.UserID), usage.TotalCostUSD)
		pipe.Incr(ctx, fmt.Sprintf("token:user:%d:calls", usage.UserID))
	}

	// Per-session (skip if empty)
	if usage.SessionID != "" {
		pipe.IncrBy(ctx, fmt.Sprintf("token:session:%s:input", usage.SessionID), int64(usage.InputTokens))
		pipe.IncrBy(ctx, fmt.Sprintf("token:session:%s:output", usage.SessionID), int64(usage.OutputTokens))
		pipe.IncrBy(ctx, fmt.Sprintf("token:session:%s:all", usage.SessionID), int64(usage.TotalTokens))
		pipe.IncrByFloat(ctx, fmt.Sprintf("token:session:%s:cost", usage.SessionID), usage.TotalCostUSD)
	}

	// Daily aggregates
	pipe.IncrBy(ctx, fmt.Sprintf("token:daily:%s:input", today), int64(usage.InputTokens))
	pipe.IncrBy(ctx, fmt.Sprintf("token:daily:%s:output", today), int64(usage.OutputTokens))
	pipe.IncrBy(ctx, fmt.Sprintf("token:daily:%s:all", today), int64(usage.TotalTokens))
	pipe.IncrByFloat(ctx, fmt.Sprintf("token:daily:%s:cost", today), usage.TotalCostUSD)
	pipe.Incr(ctx, fmt.Sprintf("token:daily:%s:calls", today))

	// Daily per-user
	if usage.UserID > 0 {
		pipe.IncrBy(ctx, fmt.Sprintf("token:daily:%s:user:%d:input", today, usage.UserID), int64(usage.InputTokens))
		pipe.IncrBy(ctx, fmt.Sprintf("token:daily:%s:user:%d:output", today, usage.UserID), int64(usage.OutputTokens))
		pipe.IncrByFloat(ctx, fmt.Sprintf("token:daily:%s:user:%d:cost", today, usage.UserID), usage.TotalCostUSD)
	}

	// Daily per-feature
	pipe.IncrBy(ctx, fmt.Sprintf("token:daily:%s:feature:%s:input", today, usage.Feature), int64(usage.InputTokens))
	pipe.IncrBy(ctx, fmt.Sprintf("token:daily:%s:feature:%s:output", today, usage.Feature), int64(usage.OutputTokens))
	pipe.IncrByFloat(ctx, fmt.Sprintf("token:daily:%s:feature:%s:cost", today, usage.Feature), usage.TotalCostUSD)

	// Per-call detail (for recent history / debugging)
	detailKey := fmt.Sprintf("token:call:%s", usage.RequestID)
	pipe.HSet(ctx, detailKey, map[string]interface{}{
		"feature":         usage.Feature,
		"model":           usage.Model,
		"input_tokens":    usage.InputTokens,
		"output_tokens":   usage.OutputTokens,
		"total_tokens":    usage.TotalTokens,
		"input_cost_usd":  fmt.Sprintf("%.8f", usage.InputCostUSD),
		"output_cost_usd": fmt.Sprintf("%.8f", usage.OutputCostUSD),
		"total_cost_usd":  fmt.Sprintf("%.8f", usage.TotalCostUSD),
		"user_id":         usage.UserID,
		"session_id":      usage.SessionID,
		"timestamp":       usage.Timestamp.Format(time.RFC3339),
		"duration_ms":     usage.Duration.Milliseconds(),
		"batch_index":     usage.BatchIndex,
	})
	pipe.Expire(ctx, detailKey, 7*24*time.Hour) // Keep per-call details for 7 days

	// Execute pipeline (best-effort)
	if _, err := pipe.Exec(ctx); err != nil && err != redis.Nil {
		log.Printf("[TokenTracker] Redis write failed: %v", err)
	}
}


