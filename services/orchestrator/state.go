package main

import (
	"context"
	"sync"
	"time"

	assistantpb "github.com/Julzz10110/nlp-assistant/api/proto/assistantpb"
)

type DialogStatus string

const (
	StatusNew        DialogStatus = "NEW"
	StatusProcessing DialogStatus = "PROCESSING"
	StatusCollecting DialogStatus = "COLLECTING"
	StatusConfirming DialogStatus = "CONFIRMING"
	StatusCompleted  DialogStatus = "COMPLETED"
)

type SessionState struct {
	SessionID       string
	UserID          string
	Status          DialogStatus
	CurrentIntent   string
	Entities        map[string]*assistantpb.EntityValue
	MissingEntities []string
	Context         map[string]string

	History []*assistantpb.ClientMessage

	CreatedAt time.Time
	UpdatedAt time.Time
}

type StateStore interface {
	GetSession(ctx context.Context, sessionID string) (*SessionState, error)
	SaveSession(ctx context.Context, state *SessionState) error
}

type InMemoryStateStore struct {
	mu       sync.RWMutex
	sessions map[string]*SessionState
}

func NewInMemoryStateStore() *InMemoryStateStore {
	return &InMemoryStateStore{
		sessions: make(map[string]*SessionState),
	}
}

func (s *InMemoryStateStore) GetSession(_ context.Context, sessionID string) (*SessionState, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if st, ok := s.sessions[sessionID]; ok {
		return st, nil
	}
	return nil, nil
}

func (s *InMemoryStateStore) SaveSession(_ context.Context, state *SessionState) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	state.UpdatedAt = time.Now().UTC()
	if state.CreatedAt.IsZero() {
		state.CreatedAt = state.UpdatedAt
	}
	s.sessions[state.SessionID] = state
	return nil
}

