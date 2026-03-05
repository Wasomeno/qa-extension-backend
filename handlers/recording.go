package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"qa-extension-backend/database"
	"qa-extension-backend/models"

	"github.com/gin-gonic/gin"
)

func SaveRecording(c *gin.Context) {
	var recording models.TestRecording
	if err := c.ShouldBindJSON(&recording); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if recording.ID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "recording id is required"})
		return
	}

	// Save to Redis
	ctx := context.Background()
	key := fmt.Sprintf("recording:%s", recording.ID)
	
	val, err := json.Marshal(recording)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to marshal recording"})
		return
	}

	err = database.RedisClient.Set(ctx, key, val, 0).Err()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save to redis"})
		return
	}

	// Also add to a set of all recording IDs for easy listing
	err = database.RedisClient.SAdd(ctx, "recordings", recording.ID).Err()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to index recording"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "recording saved successfully",
		"id":      recording.ID,
	})
}

func ListRecordings(c *gin.Context) {
	ctx := context.Background()
	ids, err := database.RedisClient.SMembers(ctx, "recordings").Result()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list recordings"})
		return
	}

	var recordings []models.TestRecording
	for _, id := range ids {
		val, err := database.RedisClient.Get(ctx, fmt.Sprintf("recording:%s", id)).Result()
		if err == nil {
			var r models.TestRecording
			if json.Unmarshal([]byte(val), &r) == nil {
				recordings = append(recordings, r)
			}
		}
	}

	c.JSON(http.StatusOK, recordings)
}

func UpdateRecording(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "recording id is required"})
		return
	}

	ctx := context.Background()
	key := fmt.Sprintf("recording:%s", id)

	// Check if exists
	exists, err := database.RedisClient.Exists(ctx, key).Result()
	if err != nil || exists == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "recording not found"})
		return
	}

	// Fetch existing for partial updates
	val, err := database.RedisClient.Get(ctx, key).Result()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to fetch existing recording"})
		return
	}

	var existing models.TestRecording
	if err := json.Unmarshal([]byte(val), &existing); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to unmarshal existing recording"})
		return
	}

	// Bind update data
	var updateData map[string]interface{}
	if err := c.ShouldBindJSON(&updateData); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Simple partial update logic
	if name, ok := updateData["name"].(string); ok {
		existing.Name = name
	}
	if desc, ok := updateData["description"].(string); ok {
		existing.Description = desc
	}
	if status, ok := updateData["status"].(string); ok {
		existing.Status = status
	}
	// For full replacement via PUT, we could check the method
	if c.Request.Method == http.MethodPut {
		if steps, ok := updateData["steps"]; ok {
			stepsJSON, _ := json.Marshal(steps)
			json.Unmarshal(stepsJSON, &existing.Steps)
		}
		if params, ok := updateData["parameters"]; ok {
			paramsJSON, _ := json.Marshal(params)
			json.Unmarshal(paramsJSON, &existing.Parameters)
		}
	}

	// Save back
	newVal, err := json.Marshal(existing)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to marshal updated recording"})
		return
	}

	err = database.RedisClient.Set(ctx, key, newVal, 0).Err()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save updated recording"})
		return
	}

	c.JSON(http.StatusOK, existing)
}

func DeleteRecording(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "recording id is required"})
		return
	}

	ctx := context.Background()
	key := fmt.Sprintf("recording:%s", id)

	// Check if exists
	exists, err := database.RedisClient.Exists(ctx, key).Result()
	if err != nil || exists == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "recording not found"})
		return
	}

	// Delete from Redis
	err = database.RedisClient.Del(ctx, key).Err()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete recording from redis"})
		return
	}

	// Remove from index set
	err = database.RedisClient.SRem(ctx, "recordings", id).Err()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to remove from recordings index"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "recording deleted successfully", "id": id})
}
