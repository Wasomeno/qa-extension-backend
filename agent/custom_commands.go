package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"qa-extension-backend/database"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

type CustomCommand struct {
	ID             string `json:"id"`
	UserID         string `json:"userId"`
	Name           string `json:"name"`
	Description    string `json:"description"`
	ToolName       string `json:"toolName"`
	PromptTemplate string `json:"promptTemplate"`
}

func SaveCustomCommand(ctx context.Context, cmd *CustomCommand) error {
	if cmd.ID == "" {
		cmd.ID = uuid.New().String()
	}
	if cmd.Name != "" {
		cmd.Name = "/" + cmd.Name
	}

	data, err := json.Marshal(cmd)
	if err != nil {
		return fmt.Errorf("failed to marshal custom command: %w", err)
	}

	key := fmt.Sprintf("agent:custom_command:%s:%s", cmd.UserID, cmd.ID)
	if err := database.RedisClient.Set(ctx, key, data, 0).Err(); err != nil {
		return fmt.Errorf("failed to save custom command: %w", err)
	}

	if err := database.RedisClient.ZAdd(ctx, customCommandsIndexKey(cmd.UserID), redis.Z{
		Score:  float64(time.Now().Unix()),
		Member: cmd.ID,
	}).Err(); err != nil {
		return fmt.Errorf("failed to index custom command: %w", err)
	}

	return nil
}

func GetUserCustomCommands(ctx context.Context, userID string) ([]*CustomCommand, error) {
	cmdIDs, err := database.RedisClient.ZRevRange(ctx, customCommandsIndexKey(userID), 0, -1).Result()
	if err != nil {
		return nil, fmt.Errorf("failed to get custom command IDs: %w", err)
	}

	var commands []*CustomCommand
	for _, id := range cmdIDs {
		key := fmt.Sprintf("agent:custom_command:%s:%s", userID, id)
		data, err := database.RedisClient.Get(ctx, key).Result()
		if err != nil {
			continue
		}

		var cmd CustomCommand
		if err := json.Unmarshal([]byte(data), &cmd); err != nil {
			continue
		}
		commands = append(commands, &cmd)
	}

	return commands, nil
}

func GetCustomCommand(ctx context.Context, userID, commandID string) (*CustomCommand, error) {
	key := fmt.Sprintf("agent:custom_command:%s:%s", userID, commandID)
	data, err := database.RedisClient.Get(ctx, key).Result()
	if err != nil {
		return nil, fmt.Errorf("failed to get custom command: %w", err)
	}

	var cmd CustomCommand
	if err := json.Unmarshal([]byte(data), &cmd); err != nil {
		return nil, fmt.Errorf("failed to unmarshal custom command: %w", err)
	}

	return &cmd, nil
}

func DeleteCustomCommand(ctx context.Context, userID, commandID string) error {
	key := fmt.Sprintf("agent:custom_command:%s:%s", userID, commandID)

	if err := database.RedisClient.Del(ctx, key).Err(); err != nil {
		return fmt.Errorf("failed to delete custom command: %w", err)
	}

	database.RedisClient.ZRem(ctx, customCommandsIndexKey(userID), commandID)

	return nil
}

func customCommandsIndexKey(userID string) string {
	return fmt.Sprintf("agent:custom_commands_index:%s", userID)
}
