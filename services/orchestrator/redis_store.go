package main

import (
	"context"
	"encoding/json"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/Julzz10110/nlp-assistant/internal/config"
)

// RedisStateStore is a Redis-backed implementation of StateStore.
// It serializes SessionState as JSON and stores it under keys like "session:<id>".
type RedisStateStore struct {
	client *redis.Client
	ttl    time.Duration
}

func NewRedisStateStore(cfg *config.OrchestratorConfig) *RedisStateStore {
	opts := &redis.Options{
		Addr:     cfg.Redis.Address,
		Password: cfg.Redis.Password,
		DB:       0,
	}

	return &RedisStateStore{
		client: redis.NewClient(opts),
		ttl:    cfg.Redis.TTL,
	}
}

func (s *RedisStateStore) Ping(ctx context.Context) error {
	return s.client.Ping(ctx).Err()
}

func (s *RedisStateStore) GetSession(ctx context.Context, sessionID string) (*SessionState, error) {
	key := "session:" + sessionID

	data, err := s.client.Get(ctx, key).Bytes()
	if err != nil {
		if err == redis.Nil {
			return nil, nil
		}
		return nil, err
	}

	var state SessionState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, err
	}
	return &state, nil
}

func (s *RedisStateStore) SaveSession(ctx context.Context, state *SessionState) error {
	state.UpdatedAt = time.Now().UTC()
	if state.CreatedAt.IsZero() {
		state.CreatedAt = state.UpdatedAt
	}

	data, err := json.Marshal(state)
	if err != nil {
		return err
	}

	key := "session:" + state.SessionID
	ttl := s.ttl
	if ttl < 0 {
		ttl = 0
	}

	return s.client.Set(ctx, key, data, ttl).Err()
}

