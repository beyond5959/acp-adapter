package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"codex-acp/internal/appserver"
	"codex-acp/internal/bridge"
)

const (
	methodInitialize    = "initialize"
	methodSessionNew    = "session/new"
	methodSessionPrompt = "session/prompt"
	methodSessionCancel = "session/cancel"
	methodSessionUpdate = "session/update"

	rpcErrMethodNotFound = -32601
	rpcErrInvalidParams  = -32602
	rpcErrInternal       = -32000
)

type turnPhase string

const (
	turnPhaseStarted   turnPhase = "started"
	turnPhaseStreaming turnPhase = "streaming"
	turnPhaseCompleted turnPhase = "completed"
	turnPhaseCancelled turnPhase = "cancelled"
	turnPhaseError     turnPhase = "error"
)

type turnLifecycle struct {
	sessionID       string
	turnID          string
	phase           turnPhase
	cancelRequested bool
}

type appClient interface {
	ThreadStart(ctx context.Context, cwd string) (string, error)
	TurnStart(ctx context.Context, threadID, input string) (string, <-chan appserver.TurnEvent, error)
	TurnInterrupt(ctx context.Context, threadID, turnID string) error
}

// Server handles ACP JSON-RPC requests over stdio.
type Server struct {
	codec    *StdioCodec
	app      appClient
	sessions *bridge.Store
	logger   *slog.Logger
}

// NewServer creates an ACP request router.
func NewServer(codec *StdioCodec, app appClient, sessions *bridge.Store, logger *slog.Logger) *Server {
	return &Server{
		codec:    codec,
		app:      app,
		sessions: sessions,
		logger:   logger,
	}
}

// Serve reads ACP requests and writes responses/notifications.
func (s *Server) Serve(ctx context.Context) error {
	for {
		msg, err := s.codec.ReadMessage()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}

		if msg.Method == "" || msg.ID == nil {
			continue
		}

		go s.handleRequest(ctx, msg)
	}
}

func (s *Server) handleRequest(ctx context.Context, msg RPCMessage) {
	rawID := *msg.ID

	switch msg.Method {
	case methodInitialize:
		s.handleInitialize(rawID)
	case methodSessionNew:
		s.handleSessionNew(ctx, rawID, msg.Params)
	case methodSessionPrompt:
		s.handleSessionPrompt(ctx, rawID, msg.Params)
	case methodSessionCancel:
		s.handleSessionCancel(ctx, rawID, msg.Params)
	default:
		s.writeError(rawID, rpcErrMethodNotFound, "method not found", map[string]any{
			"method": msg.Method,
		})
	}
}

func (s *Server) handleInitialize(id json.RawMessage) {
	result := InitializeResult{
		AgentCapabilities: AgentCapabilities{
			Sessions:      true,
			Images:        true,
			ToolCalls:     true,
			SlashCommands: true,
			Permissions:   true,
		},
		AuthMethods: []AuthMethod{
			{Type: "codex_api_key", Label: "CODEX_API_KEY"},
			{Type: "openai_api_key", Label: "OPENAI_API_KEY"},
			{Type: "chatgpt_subscription", Label: "ChatGPT subscription"},
		},
	}
	_ = s.codec.WriteResult(id, result)
}

func (s *Server) handleSessionNew(ctx context.Context, id json.RawMessage, paramsRaw json.RawMessage) {
	var params SessionNewParams
	if err := decodeParams(paramsRaw, &params); err != nil {
		s.writeInvalidParams(id, map[string]any{"error": err.Error()})
		return
	}

	threadID, err := s.app.ThreadStart(ctx, params.CWD)
	if err != nil {
		s.writeInternalError(id, "thread/start failed", map[string]any{"error": err.Error()})
		return
	}

	sessionID := s.sessions.Create(threadID)
	_ = s.codec.WriteResult(id, SessionNewResult{SessionID: sessionID})
}

func (s *Server) handleSessionPrompt(ctx context.Context, id json.RawMessage, paramsRaw json.RawMessage) {
	var params SessionPromptParams
	if err := decodeParams(paramsRaw, &params); err != nil {
		s.writeInvalidParams(id, map[string]any{"error": err.Error()})
		return
	}
	if params.SessionID == "" {
		s.writeInvalidParams(id, map[string]any{"sessionId": "required"})
		return
	}

	threadID, err := s.sessions.ThreadID(params.SessionID)
	if err != nil {
		s.writeInternalError(id, "unknown session", map[string]any{
			"error":     err.Error(),
			"sessionId": params.SessionID,
		})
		return
	}

	prompt := strings.TrimSpace(params.Prompt)
	if prompt == "" {
		prompt = fallbackPrompt(paramsRaw)
	}

	turnCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	turnID, events, err := s.app.TurnStart(turnCtx, threadID, prompt)
	if err != nil {
		s.writeInternalError(id, "turn/start failed", map[string]any{
			"error":     err.Error(),
			"sessionId": params.SessionID,
			"threadId":  threadID,
		})
		return
	}

	if _, err := s.sessions.BeginTurn(params.SessionID, turnID, cancel); err != nil {
		_ = s.app.TurnInterrupt(ctx, threadID, turnID)
		s.writeInternalError(id, "begin turn failed", map[string]any{
			"error":     err.Error(),
			"sessionId": params.SessionID,
			"threadId":  threadID,
			"turnId":    turnID,
		})
		return
	}
	defer s.sessions.EndTurn(params.SessionID, turnID)

	lifecycle := newTurnLifecycle(params.SessionID, turnID)
	s.emitUpdates(lifecycle.startedUpdate())

	for {
		select {
		case <-turnCtx.Done():
			lifecycle.markCancelRequested()
			s.emitUpdates(lifecycle.cancelledUpdate())
			s.writePromptResult(id, "cancelled")
			return
		case event, ok := <-events:
			if !ok {
				s.writePromptResult(id, lifecycle.fallbackStopReason())
				return
			}

			updates, done, stopReason := lifecycle.apply(event)
			s.emitUpdates(updates)
			if done {
				s.writePromptResult(id, stopReason)
				return
			}
		}
	}
}

func (s *Server) handleSessionCancel(ctx context.Context, id json.RawMessage, paramsRaw json.RawMessage) {
	var params SessionCancelParams
	if err := decodeParams(paramsRaw, &params); err != nil {
		s.writeInvalidParams(id, map[string]any{"error": err.Error()})
		return
	}
	if params.SessionID == "" {
		s.writeInvalidParams(id, map[string]any{"sessionId": "required"})
		return
	}

	threadID, turnID, cancelTurn, active, err := s.sessions.Cancel(params.SessionID)
	if err != nil {
		s.writeInternalError(id, "unknown session", map[string]any{
			"error":     err.Error(),
			"sessionId": params.SessionID,
		})
		return
	}
	if !active {
		_ = s.codec.WriteResult(id, SessionCancelResult{Cancelled: false})
		return
	}

	cancelTurn()

	interruptCtx, interruptCancel := context.WithTimeout(ctx, 2*time.Second)
	defer interruptCancel()
	if err := s.app.TurnInterrupt(interruptCtx, threadID, turnID); err != nil {
		s.writeInternalError(id, "turn/interrupt failed", map[string]any{
			"error":     err.Error(),
			"sessionId": params.SessionID,
			"threadId":  threadID,
			"turnId":    turnID,
		})
		return
	}

	_ = s.codec.WriteResult(id, SessionCancelResult{Cancelled: true})
}

func (s *Server) writePromptResult(id json.RawMessage, stopReason string) {
	_ = s.codec.WriteResult(id, SessionPromptResult{
		StopReason: normalizeStopReason(stopReason),
	})
}

func (s *Server) writeInvalidParams(id json.RawMessage, data map[string]any) {
	s.writeError(id, rpcErrInvalidParams, "invalid params", data)
}

func (s *Server) writeInternalError(id json.RawMessage, message string, data map[string]any) {
	s.writeError(id, rpcErrInternal, message, data)
}

func (s *Server) writeError(id json.RawMessage, code int, message string, data map[string]any) {
	_ = s.codec.WriteError(id, code, message, data)
}

func (s *Server) emitUpdates(updates []SessionUpdateParams) {
	for _, update := range updates {
		if err := s.codec.WriteNotification(methodSessionUpdate, update); err != nil {
			s.logger.Warn("failed to write session/update", slog.String("error", err.Error()))
			return
		}
	}
}

func decodeParams(raw json.RawMessage, out any) error {
	if len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("decode params: %w", err)
	}
	return nil
}

func fallbackPrompt(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}

	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return ""
	}

	for _, key := range []string{"prompt", "input", "text"} {
		if value, ok := payload[key].(string); ok {
			return value
		}
	}
	return ""
}

func normalizeStopReason(reason string) string {
	switch reason {
	case "cancelled":
		return "cancelled"
	case "error":
		return "error"
	default:
		return "end_turn"
	}
}

func newTurnLifecycle(sessionID, turnID string) *turnLifecycle {
	return &turnLifecycle{
		sessionID: sessionID,
		turnID:    turnID,
		phase:     turnPhaseStarted,
	}
}

func (t *turnLifecycle) markCancelRequested() {
	t.cancelRequested = true
}

func (t *turnLifecycle) startedUpdate() []SessionUpdateParams {
	return []SessionUpdateParams{
		{
			SessionID: t.sessionID,
			TurnID:    t.turnID,
			Type:      "status",
			Phase:     string(t.phase),
			Status:    "turn_started",
		},
	}
}

func (t *turnLifecycle) cancelledUpdate() []SessionUpdateParams {
	t.phase = turnPhaseCancelled
	return []SessionUpdateParams{
		{
			SessionID: t.sessionID,
			TurnID:    t.turnID,
			Type:      "status",
			Phase:     string(t.phase),
			Status:    "turn_cancelled",
		},
	}
}

func (t *turnLifecycle) apply(event appserver.TurnEvent) ([]SessionUpdateParams, bool, string) {
	switch event.Type {
	case appserver.TurnEventTypeStarted:
		t.phase = turnPhaseStarted
		return []SessionUpdateParams{
			{
				SessionID: t.sessionID,
				TurnID:    t.turnID,
				Type:      "status",
				Phase:     string(t.phase),
				Status:    "turn_started",
			},
		}, false, ""
	case appserver.TurnEventTypeUpdate, appserver.TurnEventTypeAgentMessageDelta:
		t.phase = turnPhaseStreaming
		return []SessionUpdateParams{
			{
				SessionID: t.sessionID,
				TurnID:    event.TurnID,
				Type:      "message",
				Phase:     string(t.phase),
				ItemID:    event.ItemID,
				Delta:     event.Delta,
			},
		}, false, ""
	case appserver.TurnEventTypeItemStarted:
		t.phase = turnPhaseStreaming
		return []SessionUpdateParams{
			{
				SessionID: t.sessionID,
				TurnID:    event.TurnID,
				Type:      "status",
				Phase:     string(t.phase),
				ItemID:    event.ItemID,
				ItemType:  event.ItemType,
				Status:    "item_started",
			},
		}, false, ""
	case appserver.TurnEventTypeItemCompleted:
		t.phase = turnPhaseStreaming
		return []SessionUpdateParams{
			{
				SessionID: t.sessionID,
				TurnID:    event.TurnID,
				Type:      "status",
				Phase:     string(t.phase),
				ItemID:    event.ItemID,
				ItemType:  event.ItemType,
				Status:    "item_completed",
			},
		}, false, ""
	case appserver.TurnEventTypeCompleted:
		stopReason := normalizeStopReason(event.StopReason)
		if t.cancelRequested {
			stopReason = "cancelled"
		}
		switch stopReason {
		case "cancelled":
			t.phase = turnPhaseCancelled
		case "error":
			t.phase = turnPhaseError
		default:
			t.phase = turnPhaseCompleted
		}
		return []SessionUpdateParams{
			{
				SessionID: t.sessionID,
				TurnID:    event.TurnID,
				Type:      "status",
				Phase:     string(t.phase),
				Status:    "turn_completed",
			},
		}, true, stopReason
	case appserver.TurnEventTypeError:
		t.phase = turnPhaseError
		return []SessionUpdateParams{
			{
				SessionID: t.sessionID,
				TurnID:    t.turnID,
				Type:      "status",
				Phase:     string(t.phase),
				Status:    "turn_error",
				Message:   event.Message,
			},
		}, true, "error"
	default:
		return nil, false, ""
	}
}

func (t *turnLifecycle) fallbackStopReason() string {
	if t.cancelRequested {
		return "cancelled"
	}
	switch t.phase {
	case turnPhaseCompleted:
		return "end_turn"
	case turnPhaseCancelled:
		return "cancelled"
	case turnPhaseError:
		return "error"
	default:
		return "error"
	}
}
