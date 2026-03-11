package bridge

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
)

var (
	// ErrSessionNotFound indicates sessionId is unknown.
	ErrSessionNotFound = errors.New("session not found")
	// ErrActiveTurnExists indicates this session already has an active turn.
	ErrActiveTurnExists = errors.New("active turn already exists")
)

type turnState struct {
	threadID string
	turnID   string
	cancel   context.CancelFunc
}

type sessionState struct {
	threadID string
	active   *turnState
}

// Store tracks session/thread/turn bindings for ACP lifecycle.
type Store struct {
	mu        sync.Mutex
	sessions  map[string]*sessionState
	threadIDs map[string]string
	nextID    uint64
}

// NewStore creates an empty session store.
func NewStore() *Store {
	return &Store{
		sessions:  make(map[string]*sessionState),
		threadIDs: make(map[string]string),
	}
}

// Create creates a session bound to an app-server thread.
func (s *Store) Create(threadID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.createLocked(threadID)
}

// Bind attaches one caller-provided session id to an existing thread id.
func (s *Store) Bind(sessionID, threadID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if sessionID == "" {
		return fmt.Errorf("session id is required")
	}
	if threadID == "" {
		return fmt.Errorf("thread id is required")
	}

	if existingSessionID, ok := s.threadIDs[threadID]; ok {
		if existingSessionID != sessionID {
			return fmt.Errorf("thread %q already bound to session %q", threadID, existingSessionID)
		}
	}

	if session, ok := s.sessions[sessionID]; ok {
		if session.threadID != threadID {
			return fmt.Errorf("session %q already bound to thread %q", sessionID, session.threadID)
		}
		return nil
	}

	s.sessions[sessionID] = &sessionState{threadID: threadID}
	s.threadIDs[threadID] = sessionID
	return nil
}

func (s *Store) createLocked(threadID string) string {
	if sessionID, ok := s.threadIDs[threadID]; ok {
		return sessionID
	}

	id := fmt.Sprintf("session-%d", atomic.AddUint64(&s.nextID, 1))
	s.sessions[id] = &sessionState{threadID: threadID}
	s.threadIDs[threadID] = id
	return id
}

// ThreadID returns the mapped thread id for a session.
func (s *Store) ThreadID(sessionID string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	session, ok := s.sessions[sessionID]
	if !ok {
		return "", ErrSessionNotFound
	}
	return session.threadID, nil
}

// BeginTurn marks a turn as active for a session.
func (s *Store) BeginTurn(sessionID, turnID string, cancel context.CancelFunc) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	session, ok := s.sessions[sessionID]
	if !ok {
		return "", ErrSessionNotFound
	}
	if session.active != nil {
		return "", ErrActiveTurnExists
	}

	session.active = &turnState{
		threadID: session.threadID,
		turnID:   turnID,
		cancel:   cancel,
	}
	return session.threadID, nil
}

// ReplaceTurn swaps the active turn id for one session during in-flight retries.
func (s *Store) ReplaceTurn(
	sessionID string,
	oldTurnID string,
	newTurnID string,
	cancel context.CancelFunc,
) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	session, ok := s.sessions[sessionID]
	if !ok {
		return "", ErrSessionNotFound
	}
	if session.active == nil {
		return "", ErrActiveTurnExists
	}
	if session.active.turnID != oldTurnID {
		return "", ErrActiveTurnExists
	}

	session.active.turnID = newTurnID
	session.active.cancel = cancel
	return session.threadID, nil
}

// EndTurn clears active turn marker if it matches the same turn id.
func (s *Store) EndTurn(sessionID, turnID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	session, ok := s.sessions[sessionID]
	if !ok || session.active == nil {
		return
	}
	if session.active.turnID == turnID {
		session.active = nil
	}
}

// Cancel returns active turn details and leaves the state active until EndTurn.
func (s *Store) Cancel(sessionID string) (string, string, context.CancelFunc, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	session, ok := s.sessions[sessionID]
	if !ok {
		return "", "", nil, false, ErrSessionNotFound
	}
	if session.active == nil {
		return "", "", nil, false, nil
	}

	return session.active.threadID, session.active.turnID, session.active.cancel, true, nil
}
