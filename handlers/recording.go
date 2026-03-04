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
