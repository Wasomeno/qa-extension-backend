package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"qa-extension-backend/database"
	"time"
)

const defaultAgentAppName = "qa_extension"

// AgentAttachment is an optional multimodal attachment for a user message.
type AgentAttachment struct {
	Name     string `json:"name"`
	MimeType string `json:"mimeType"`
	Data     []byte `json:"data"`
}

// AgentToolCall records a function/tool call requested by the model.
type AgentToolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// AgentMessage is the project-owned, ADK-free chat/session message format.
type AgentMessage struct {
	Role        string            `json:"role"` // system, user, assistant, tool
	Content     string            `json:"content,omitempty"`
	ToolCallID  string            `json:"toolCallId,omitempty"`
	ToolName    string            `json:"toolName,omitempty"`
	ToolCalls   []AgentToolCall   `json:"toolCalls,omitempty"`
	Attachments []AgentAttachment `json:"attachments,omitempty"`
	Timestamp   time.Time         `json:"timestamp"`
}

// AgentSession is persisted in Redis under agent:session:<id>.
type AgentSession struct {
	ID             string         `json:"id"`
	AppName        string         `json:"appName"`
	UserID         string         `json:"userId"`
	Messages       []AgentMessage `json:"messages"`
	State          map[string]any `json:"state,omitempty"`
	LastUpdateTime time.Time      `json:"lastUpdateTime"`
}

// RedisSessionService stores ADK-free agent sessions in Redis.
type RedisSessionService struct {
	AppName string
}

func NewRedisSessionService(appName string) *RedisSessionService {
	if appName == "" {
		appName = defaultAgentAppName
	}
	return &RedisSessionService{AppName: appName}
}

func GetSessionService() *RedisSessionService {
	return NewRedisSessionService(defaultAgentAppName)
}

func (s *RedisSessionService) Create(ctx context.Context, sessionID, userID string, state map[string]any) (*AgentSession, error) {
	if sessionID == "" {
		return nil, fmt.Errorf("session_id is required")
	}
	if userID == "" {
		userID = "user"
	}
	if state == nil {
		state = make(map[string]any)
	}
	sess := &AgentSession{
		ID:             sessionID,
		AppName:        s.AppName,
		UserID:         userID,
		Messages:       []AgentMessage{},
		State:          state,
		LastUpdateTime: time.Now(),
	}
	if err := s.save(ctx, sess); err != nil {
		return nil, err
	}
	return sess, nil
}

func (s *RedisSessionService) Get(ctx context.Context, sessionID string) (*AgentSession, error) {
	return s.load(ctx, sessionID)
}

func (s *RedisSessionService) GetOrCreate(ctx context.Context, sessionID, userID string) (*AgentSession, error) {
	sess, err := s.Get(ctx, sessionID)
	if err == nil {
		return sess, nil
	}
	return s.Create(ctx, sessionID, userID, nil)
}

func (s *RedisSessionService) List(ctx context.Context, userID string) ([]*AgentSession, error) {
	iter := database.RedisClient.Scan(ctx, 0, "agent:session:*", 0).Iterator()
	var sessions []*AgentSession
	for iter.Next(ctx) {
		key := iter.Val()
		sessionID := key[len("agent:session:"):]
		data, err := s.load(ctx, sessionID)
		if err != nil {
			continue
		}
		if data.AppName != "" && data.AppName != s.AppName {
			continue
		}
		if userID != "" && data.UserID != "" && data.UserID != userID {
			continue
		}
		sessions = append(sessions, data)
	}
	if err := iter.Err(); err != nil {
		return nil, err
	}
	return sessions, nil
}

func (s *RedisSessionService) Delete(ctx context.Context, sessionID string) error {
	key := fmt.Sprintf("agent:session:%s", sessionID)
	return database.RedisClient.Del(ctx, key).Err()
}

func (s *RedisSessionService) AppendMessage(ctx context.Context, sessionID string, msg AgentMessage) error {
	sess, err := s.load(ctx, sessionID)
	if err != nil {
		return err
	}
	if msg.Timestamp.IsZero() {
		msg.Timestamp = time.Now()
	}
	sess.Messages = append(sess.Messages, msg)
	sess.LastUpdateTime = msg.Timestamp
	return s.save(ctx, sess)
}

func (s *RedisSessionService) ReplaceMessages(ctx context.Context, sessionID string, messages []AgentMessage) error {
	sess, err := s.load(ctx, sessionID)
	if err != nil {
		return err
	}
	sess.Messages = messages
	sess.LastUpdateTime = time.Now()
	return s.save(ctx, sess)
}

func (s *RedisSessionService) save(ctx context.Context, data *AgentSession) error {
	if data.AppName == "" {
		data.AppName = s.AppName
	}
	if data.State == nil {
		data.State = make(map[string]any)
	}
	key := fmt.Sprintf("agent:session:%s", data.ID)
	val, err := json.Marshal(data)
	if err != nil {
		return err
	}
	return database.RedisClient.Set(ctx, key, val, 0).Err()
}

func (s *RedisSessionService) load(ctx context.Context, sessionID string) (*AgentSession, error) {
	key := fmt.Sprintf("agent:session:%s", sessionID)
	val, err := database.RedisClient.Get(ctx, key).Result()
	if err != nil {
		return nil, err
	}
	var data AgentSession
	if err := json.Unmarshal([]byte(val), &data); err != nil {
		return nil, err
	}
	if data.ID == "" {
		data.ID = sessionID
	}
	if data.AppName == "" {
		data.AppName = s.AppName
	}
	if data.State == nil {
		data.State = make(map[string]any)
	}
	if data.Messages == nil {
		data.Messages = []AgentMessage{}
	}
	return &data, nil
}
