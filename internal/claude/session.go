package claude

import (
	"sync"

	"github.com/anthropics/anthropic-sdk-go"
)

// session maintains per-thread conversation history for the Claude API.
type session struct {
	mu       sync.Mutex
	cwd      string
	messages []anthropic.MessageParam
	// loggedOut tracks whether this session's auth has been cleared.
	loggedOut bool
}

// Messages returns a snapshot of the conversation history.
func (s *session) Messages() []anthropic.MessageParam {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]anthropic.MessageParam, len(s.messages))
	copy(out, s.messages)
	return out
}

// AppendUser appends a user-role message to the history.
func (s *session) AppendUser(content []anthropic.ContentBlockParamUnion) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages = append(s.messages, anthropic.MessageParam{
		Role:    anthropic.MessageParamRoleUser,
		Content: content,
	})
}

// AppendAssistant appends a completed assistant message to the history.
func (s *session) AppendAssistant(msg anthropic.Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages = append(s.messages, msg.ToParam())
}

// Replace replaces the full history with the given messages.
// Used by /compact to substitute history with a summary.
func (s *session) Replace(messages []anthropic.MessageParam) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages = messages
}

// sessionStore manages per-thread sessions.
type sessionStore struct {
	mu       sync.Mutex
	sessions map[string]*session
}

func newSessionStore() *sessionStore {
	return &sessionStore{sessions: make(map[string]*session)}
}

func (s *sessionStore) Create(threadID, cwd string) *session {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess := &session{cwd: cwd}
	s.sessions[threadID] = sess
	return sess
}

func (s *sessionStore) Get(threadID string) (*session, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[threadID]
	return sess, ok
}
