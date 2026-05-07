package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"qa-extension-backend/database"
	"time"

	"google.golang.org/adk/session"
)

type RedisSessionService struct {
	AppName string
}

func NewRedisSessionService(appName string) *RedisSessionService {
	return &RedisSessionService{AppName: appName}
}

type SessionData struct {
	ID             string           `json:"id"`
	AppName        string           `json:"appName"`
	UserID         string           `json:"userId"`
	State          map[string]any   `json:"state"`
	Events         []*session.Event `json:"events"`
	LastUpdateTime time.Time        `json:"lastUpdateTime"`
}

func (s *RedisSessionService) Create(ctx context.Context, req *session.CreateRequest) (*session.CreateResponse, error) {
	sessionID := req.SessionID
	if sessionID == "" {
		return nil, fmt.Errorf("session_id is required for stateful sessions")
	}

	data := &SessionData{
		ID:             sessionID,
		AppName:        req.AppName,
		UserID:         req.UserID,
		State:          req.State,
		Events:         []*session.Event{},
		LastUpdateTime: time.Now(),
	}
	if data.State == nil {
		data.State = make(map[string]any)
	}

	err := s.save(ctx, data)
	if err != nil {
		return nil, err
	}

	return &session.CreateResponse{
		Session: &RedisSession{Data: data, Service: s},
	}, nil
}

func (s *RedisSessionService) Get(ctx context.Context, req *session.GetRequest) (*session.GetResponse, error) {
	data, err := s.load(ctx, req.SessionID)
	if err != nil {
		return nil, err
	}

	return &session.GetResponse{
		Session: &RedisSession{Data: data, Service: s},
	}, nil
}

func (s *RedisSessionService) List(ctx context.Context, req *session.ListRequest) (*session.ListResponse, error) {
	prefix := "agent:session:*"
	iter := database.RedisClient.Scan(ctx, 0, prefix, 0).Iterator()

	var sessions []session.Session
	for iter.Next(ctx) {
		key := iter.Val()
		sessionID := key[len("agent:session:"):]

		data, err := s.load(ctx, sessionID)
		if err != nil {
			continue
		}

		// Filter by AppName and UserID if provided
		if req.AppName != "" && data.AppName != req.AppName {
			continue
		}
		if req.UserID != "" && data.UserID != req.UserID {
			continue
		}

		sessions = append(sessions, &RedisSession{Data: data, Service: s})
	}

	if err := iter.Err(); err != nil {
		return nil, err
	}

	return &session.ListResponse{
		Sessions: sessions,
	}, nil
}

func (s *RedisSessionService) Delete(ctx context.Context, req *session.DeleteRequest) error {
	key := fmt.Sprintf("agent:session:%s", req.SessionID)
	return database.RedisClient.Del(ctx, key).Err()
}

func (s *RedisSessionService) AppendEvent(ctx context.Context, sess session.Session, event *session.Event) error {
	rs, ok := sess.(*RedisSession)
	if !ok {
		return fmt.Errorf("invalid session type")
	}

	rs.Data.Events = append(rs.Data.Events, event)
	rs.Data.LastUpdateTime = event.Timestamp

	if event.Actions.StateDelta != nil {
		for k, v := range event.Actions.StateDelta {
			rs.Data.State[k] = v
		}
	}

	return s.save(ctx, rs.Data)
}

func (s *RedisSessionService) save(ctx context.Context, data *SessionData) error {
	key := fmt.Sprintf("agent:session:%s", data.ID)
	val, err := json.Marshal(data)
	if err != nil {
		return err
	}
	return database.RedisClient.Set(ctx, key, val, 0).Err()
}

func (s *RedisSessionService) load(ctx context.Context, sessionID string) (*SessionData, error) {
	key := fmt.Sprintf("agent:session:%s", sessionID)
	val, err := database.RedisClient.Get(ctx, key).Result()
	if err != nil {
		return nil, err
	}

	var data SessionData
	err = json.Unmarshal([]byte(val), &data)
	if err != nil {
		return nil, err
	}
	return &data, nil
}

type RedisSession struct {
	Data    *SessionData
	Service *RedisSessionService
}

func (s *RedisSession) ID() string             { return s.Data.ID }
func (s *RedisSession) AppName() string        { return s.Data.AppName }
func (s *RedisSession) UserID() string         { return s.Data.UserID }
func (s *RedisSession) State() session.State   { return &redisState{data: s.Data} }
func (s *RedisSession) Events() session.Events { return redisEvents(s.Data.Events) }
func (s *RedisSession) LastUpdateTime() time.Time { return s.Data.LastUpdateTime }

type redisState struct {
	data *SessionData
}

func (s *redisState) Get(key string) (any, error) {
	val, ok := s.data.State[key]
	if !ok {
		return nil, session.ErrStateKeyNotExist
	}
	return val, nil
}

func (s *redisState) Set(key string, val any) error {
	s.data.State[key] = val
	return nil
}

func (s *redisState) All() iter.Seq2[string, any] {
	return func(yield func(string, any) bool) {
		for k, v := range s.data.State {
			if !yield(k, v) {
				return
			}
		}
	}
}

type redisEvents []*session.Event

func (e redisEvents) All() iter.Seq[*session.Event] {
	return func(yield func(*session.Event) bool) {
		for _, event := range e {
			if !yield(event) {
				return
			}
		}
	}
}

func (e redisEvents) Len() int {
	return len(e)
}

func (e redisEvents) At(i int) *session.Event {
	if i >= 0 && i < len(e) {
		return e[i]
	}
	return nil
}
