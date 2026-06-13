package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"qa-extension-backend/database"
)

type generationJobContextKey struct{}

func waitForGenerationOpenAIQuota(ctx context.Context, messages []openAIChatMessage) error {
	if ctx.Value(generationJobContextKey{}) == nil {
		return nil
	}
	if err := waitFixedWindowQuota(ctx, "generation:openai:rpm", envInt("GENERATION_OPENAI_RPM", 30), 1); err != nil {
		return err
	}
	estimatedTokens := estimateOpenAITokens(messages)
	return waitFixedWindowQuota(ctx, "generation:openai:tpm", envInt("GENERATION_OPENAI_TPM", 120000), estimatedTokens)
}

func waitFixedWindowQuota(ctx context.Context, prefix string, limit int, cost int) error {
	if limit <= 0 || cost <= 0 {
		return nil
	}
	for {
		now := time.Now()
		window := now.Unix() / 60
		key := fmt.Sprintf("%s:%d", prefix, window)
		count, err := database.RedisClient.IncrBy(ctx, key, int64(cost)).Result()
		if err != nil {
			return err
		}
		_ = database.RedisClient.Expire(ctx, key, 2*time.Minute).Err()
		if count <= int64(limit) {
			return nil
		}
		wait := time.Duration(60-now.Second()) * time.Second
		if wait < time.Second {
			wait = time.Second
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
}

func estimateOpenAITokens(messages []openAIChatMessage) int {
	data, err := json.Marshal(messages)
	if err != nil {
		return 4096
	}
	estimate := int(math.Ceil(float64(len(data))/4.0)) + 2048
	if estimate < 4096 {
		return 4096
	}
	return estimate
}

func retryOpenAIChatCompletion(ctx context.Context, fn func() (*openAIChatResponse, error)) (*openAIChatResponse, error) {
	maxAttempts := envInt("GENERATION_OPENAI_MAX_ATTEMPTS", 3)
	if ctx.Value(generationJobContextKey{}) == nil || maxAttempts <= 1 {
		return fn()
	}
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		resp, err := fn()
		if err == nil {
			return resp, nil
		}
		lastErr = err
		if attempt == maxAttempts || !isTransientOpenAIError(err) {
			break
		}
		delay := retryAfterDelay(err)
		if delay <= 0 {
			delay = time.Duration(attempt*attempt)*time.Second + time.Duration(time.Now().UnixNano()%500)*time.Millisecond
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(delay):
		}
	}
	return nil, lastErr
}

func isTransientOpenAIError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "429") ||
		strings.Contains(msg, "rate limit") ||
		strings.Contains(msg, "too many requests") ||
		strings.Contains(msg, "500") ||
		strings.Contains(msg, "502") ||
		strings.Contains(msg, "503") ||
		strings.Contains(msg, "504") ||
		strings.Contains(msg, "timeout") ||
		strings.Contains(msg, "connection reset") ||
		errors.Is(err, context.DeadlineExceeded)
}

func retryAfterDelay(err error) time.Duration {
	msg := strings.ToLower(err.Error())
	idx := strings.Index(msg, "retry-after")
	if idx < 0 {
		return 0
	}
	fragment := msg[idx:]
	fields := strings.FieldsFunc(fragment, func(r rune) bool {
		return r == ':' || r == '=' || r == ',' || r == ';' || r == ' ' || r == '\n' || r == '\t'
	})
	for _, field := range fields {
		if n, parseErr := strconv.Atoi(strings.TrimSpace(field)); parseErr == nil && n > 0 {
			return time.Duration(n) * time.Second
		}
	}
	return 0
}
