package routes

import (
	"net/http"
	"strings"

	"qa-extension-backend/database"

	"github.com/gin-gonic/gin"
)

// GetTokenUsage returns aggregated token usage statistics.
// Query params:
//   - scope: "global" (default), "user", "session", "feature", "model", "daily"
//   - key:   required when scope != "global" (e.g. user ID, session ID, feature name, model name, date)
//   - date:  for daily scope, format "YYYY-MM-DD" (defaults to today)
func GetTokenUsage(c *gin.Context) {
	ctx := c.Request.Context()
	rdb := database.RedisClient
	if rdb == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Redis not available"})
		return
	}

	scope := c.DefaultQuery("scope", "global")
	key := c.Query("key")

	var inputKey, outputKey, allKey, costKey, callsKey string

	switch scope {
	case "global":
		inputKey = "token:total:input"
		outputKey = "token:total:output"
		allKey = "token:total:all"
		costKey = "token:total:cost"
		callsKey = "token:total:calls"

	case "user":
		if key == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "key (user ID) is required for scope=user"})
			return
		}
		inputKey = "token:user:" + key + ":input"
		outputKey = "token:user:" + key + ":output"
		allKey = "token:user:" + key + ":all"
		costKey = "token:user:" + key + ":cost"
		callsKey = "token:user:" + key + ":calls"

	case "session":
		if key == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "key (session ID) is required for scope=session"})
			return
		}
		inputKey = "token:session:" + key + ":input"
		outputKey = "token:session:" + key + ":output"
		allKey = "token:session:" + key + ":all"
		costKey = "token:session:" + key + ":cost"
		callsKey = ""

	case "feature":
		if key == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "key (feature name) is required for scope=feature"})
			return
		}
		inputKey = "token:feature:" + key + ":input"
		outputKey = "token:feature:" + key + ":output"
		allKey = "token:feature:" + key + ":all"
		costKey = "token:feature:" + key + ":cost"
		callsKey = "token:feature:" + key + ":calls"

	case "model":
		if key == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "key (model name) is required for scope=model"})
			return
		}
		inputKey = "token:model:" + key + ":input"
		outputKey = "token:model:" + key + ":output"
		allKey = "token:model:" + key + ":all"
		costKey = "token:model:" + key + ":cost"
		callsKey = ""

	case "daily":
		if key == "" {
			// Default to today — the caller can pass date param
			key = c.DefaultQuery("date", "")
		}
		if key == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "key or date param (YYYY-MM-DD) is required for scope=daily"})
			return
		}
		inputKey = "token:daily:" + key + ":input"
		outputKey = "token:daily:" + key + ":output"
		allKey = "token:daily:" + key + ":all"
		costKey = "token:daily:" + key + ":cost"
		callsKey = "token:daily:" + key + ":calls"

	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid scope, must be one of: global, user, session, feature, model, daily"})
		return
	}

	// Batch read all keys
	keys := []string{inputKey, outputKey, allKey, costKey}
	if callsKey != "" {
		keys = append(keys, callsKey)
	}

	vals := make([]string, len(keys))
	pipe := rdb.Pipeline()
	cmds := make([]interface{}, len(keys))
	for i, k := range keys {
		cmds[i] = pipe.Get(ctx, k)
	}
	pipe.Exec(ctx)

	for i, cmd := range cmds {
		switch c := cmd.(type) {
		case interface{ Val() string }:
			vals[i] = c.Val()
		}
	}

	// Parse results (missing keys default to "0")
	getVal := func(idx int) string {
		if idx < len(vals) {
			v := vals[idx]
			if v == "" {
				return "0"
			}
			return v
		}
		return "0"
	}

	result := gin.H{
		"scope":        scope,
		"key":          key,
		"input_tokens": getVal(0),
		"output_tokens": getVal(1),
		"total_tokens": getVal(2),
		"total_cost_usd": getVal(3),
	}
	if callsKey != "" {
		result["total_calls"] = getVal(4)
	}

	c.JSON(http.StatusOK, result)
}

// GetTokenUsageSummary returns a summary across all features and models.
func GetTokenUsageSummary(c *gin.Context) {
	ctx := c.Request.Context()
	rdb := database.RedisClient
	if rdb == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Redis not available"})
		return
	}

	// Get global totals
	totalInput, _ := rdb.Get(ctx, "token:total:input").Result()
	totalOutput, _ := rdb.Get(ctx, "token:total:output").Result()
	totalAll, _ := rdb.Get(ctx, "token:total:all").Result()
	totalCost, _ := rdb.Get(ctx, "token:total:cost").Result()
	totalCalls, _ := rdb.Get(ctx, "token:total:calls").Result()

	// Get per-feature breakdown
	featureKeys, _ := rdb.Keys(ctx, "token:feature:*:cost").Result()
	features := make(map[string]gin.H)
	for _, fk := range featureKeys {
		// Extract feature name from key: "token:feature:FEATURE:cost"
		parts := strings.Split(fk, ":")
		if len(parts) < 4 {
			continue
		}
		featureName := parts[2]
		cost, _ := rdb.Get(ctx, fk).Result()
		input, _ := rdb.Get(ctx, "token:feature:"+featureName+":input").Result()
		output, _ := rdb.Get(ctx, "token:feature:"+featureName+":output").Result()
		calls, _ := rdb.Get(ctx, "token:feature:"+featureName+":calls").Result()
		features[featureName] = gin.H{
			"input_tokens":  input,
			"output_tokens": output,
			"total_cost_usd": cost,
			"total_calls":   calls,
		}
	}

	// Get per-model breakdown
	modelKeys, _ := rdb.Keys(ctx, "token:model:*:cost").Result()
	models := make(map[string]gin.H)
	for _, mk := range modelKeys {
		parts := strings.Split(mk, ":")
		if len(parts) < 4 {
			continue
		}
		modelName := parts[2]
		cost, _ := rdb.Get(ctx, mk).Result()
		input, _ := rdb.Get(ctx, "token:model:"+modelName+":input").Result()
		output, _ := rdb.Get(ctx, "token:model:"+modelName+":output").Result()
		models[modelName] = gin.H{
			"input_tokens":  input,
			"output_tokens": output,
			"total_cost_usd": cost,
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"global": gin.H{
			"input_tokens":  totalInput,
			"output_tokens": totalOutput,
			"total_tokens":  totalAll,
			"total_cost_usd": totalCost,
			"total_calls":   totalCalls,
		},
		"by_feature": features,
		"by_model":   models,
	})
}

// GetTokenCallDetail returns details for a specific LLM call by request ID.
func GetTokenCallDetail(c *gin.Context) {
	ctx := c.Request.Context()
	rdb := database.RedisClient
	if rdb == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Redis not available"})
		return
	}

	requestID := c.Param("request_id")
	if requestID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "request_id is required"})
		return
	}

	key := "token:call:" + requestID
	detail, err := rdb.HGetAll(ctx, key).Result()
	if err != nil || len(detail) == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "call detail not found"})
		return
	}

	c.JSON(http.StatusOK, detail)
}
