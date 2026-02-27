package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"codex-acp/internal/appserver"
	"codex-acp/internal/bridge"
)

const (
	methodInitialize               = "initialize"
	methodSessionNew               = "session/new"
	methodSessionPrompt            = "session/prompt"
	methodSessionCancel            = "session/cancel"
	methodSessionUpdate            = "session/update"
	methodSessionRequestPermission = "session/request_permission"

	defaultPermissionTimeout = 30 * time.Second

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

type permissionOutcome string

const (
	permissionOutcomeApproved  permissionOutcome = "approved"
	permissionOutcomeDeclined  permissionOutcome = "declined"
	permissionOutcomeCancelled permissionOutcome = "cancelled"
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
	ApprovalRespond(ctx context.Context, approvalID string, decision appserver.ApprovalDecision) error
}

// Server handles ACP JSON-RPC requests over stdio.
type Server struct {
	codec    *StdioCodec
	app      appClient
	sessions *bridge.Store
	logger   *slog.Logger

	pendingMu     sync.Mutex
	pendingClient map[string]chan RPCMessage
	nextClientID  uint64
}

// NewServer creates an ACP request router.
func NewServer(codec *StdioCodec, app appClient, sessions *bridge.Store, logger *slog.Logger) *Server {
	return &Server{
		codec:         codec,
		app:           app,
		sessions:      sessions,
		logger:        logger,
		pendingClient: make(map[string]chan RPCMessage),
		nextClientID:  0,
	}
}

// Serve reads ACP requests and writes responses/notifications.
func (s *Server) Serve(ctx context.Context) error {
	for {
		msg, err := s.codec.ReadMessage()
		if err != nil {
			s.failPendingClientRequests(err)
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}

		switch {
		case msg.Method != "" && msg.ID != nil:
			go s.handleRequest(ctx, msg)
		case msg.Method == "" && msg.ID != nil:
			s.handleClientResponse(msg)
		default:
			continue
		}
	}
}

func (s *Server) handleClientResponse(msg RPCMessage) {
	if msg.ID == nil {
		return
	}
	id := normalizeMessageID(*msg.ID)

	s.pendingMu.Lock()
	ch, ok := s.pendingClient[id]
	if ok {
		delete(s.pendingClient, id)
	}
	s.pendingMu.Unlock()
	if !ok {
		return
	}

	ch <- msg
	close(ch)
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
			if event.Type == appserver.TurnEventTypeApprovalRequired {
				updates, done, stopReason := s.handleApprovalEvent(turnCtx, lifecycle, event)
				s.emitUpdates(updates)
				if done {
					s.writePromptResult(id, stopReason)
					return
				}
				continue
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

func (s *Server) handleApprovalEvent(
	ctx context.Context,
	lifecycle *turnLifecycle,
	event appserver.TurnEvent,
) ([]SessionUpdateParams, bool, string) {
	if event.Approval.ApprovalID == "" {
		return []SessionUpdateParams{
			{
				SessionID: lifecycle.sessionID,
				TurnID:    lifecycle.turnID,
				Type:      "status",
				Phase:     string(turnPhaseError),
				Status:    "turn_error",
				Message:   "approval event missing approvalId",
			},
		}, true, "error"
	}

	updates := lifecycle.toolCallInProgressUpdates(event)
	decision, err := s.requestPermission(ctx, lifecycle.sessionID, lifecycle.turnID, event.Approval)
	if err != nil {
		s.logger.Warn(
			"session/request_permission failed; default deny",
			slog.String("sessionId", lifecycle.sessionID),
			slog.String("turnId", lifecycle.turnID),
			slog.String("approvalId", event.Approval.ApprovalID),
			slog.String("error", err.Error()),
		)
		decision = permissionOutcomeCancelled
	}

	outcomeUpdates := lifecycle.toolCallOutcomeUpdates(event, decision)
	updates = append(updates, outcomeUpdates...)

	respondCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if respondErr := s.app.ApprovalRespond(respondCtx, event.Approval.ApprovalID, mapDecisionToAppServer(decision)); respondErr != nil {
		updates = append(updates, SessionUpdateParams{
			SessionID: lifecycle.sessionID,
			TurnID:    lifecycle.turnID,
			Type:      "status",
			Phase:     string(turnPhaseError),
			Status:    "turn_error",
			Message:   fmt.Sprintf("approval respond failed: %v", respondErr),
		})
		return updates, true, "error"
	}

	return updates, false, ""
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

func (s *Server) requestPermission(
	ctx context.Context,
	sessionID string,
	turnID string,
	approval appserver.ApprovalRequest,
) (permissionOutcome, error) {
	callCtx, cancel := context.WithTimeout(ctx, defaultPermissionTimeout)
	defer cancel()

	params := SessionRequestPermissionParams{
		SessionID:  sessionID,
		TurnID:     turnID,
		Approval:   string(approval.Kind),
		ToolCallID: approval.ToolCallID,
		Command:    approval.Command,
		Files:      approval.Files,
		Host:       approval.Host,
		Protocol:   approval.Protocol,
		Port:       approval.Port,
		MCPServer:  approval.MCPServer,
		MCPTool:    approval.MCPTool,
		Message:    approval.Message,
	}

	var result SessionRequestPermissionResult
	if err := s.callClient(callCtx, methodSessionRequestPermission, params, &result); err != nil {
		return permissionOutcomeCancelled, err
	}
	return normalizePermissionOutcome(result), nil
}

func (s *Server) callClient(ctx context.Context, method string, params any, out any) error {
	id := strconv.FormatUint(atomic.AddUint64(&s.nextClientID, 1), 10)
	rawID := json.RawMessage(strconv.Quote("server-" + id))

	msg, err := buildClientRequest(rawID, method, params)
	if err != nil {
		return err
	}

	respCh := make(chan RPCMessage, 1)
	s.pendingMu.Lock()
	s.pendingClient["server-"+id] = respCh
	s.pendingMu.Unlock()

	if err := s.codec.WriteMessage(msg); err != nil {
		s.removePendingClientRequest("server-" + id)
		return fmt.Errorf("%s write request: %w", method, err)
	}

	var resp RPCMessage
	select {
	case <-ctx.Done():
		s.removePendingClientRequest("server-" + id)
		return fmt.Errorf("%s wait response: %w", method, ctx.Err())
	case resp = <-respCh:
	}

	if resp.Error != nil {
		return fmt.Errorf("%s rpc error code=%d message=%s", method, resp.Error.Code, resp.Error.Message)
	}
	if out != nil && len(resp.Result) > 0 {
		if err := json.Unmarshal(resp.Result, out); err != nil {
			return fmt.Errorf("%s decode result: %w", method, err)
		}
	}
	return nil
}

func (s *Server) removePendingClientRequest(id string) {
	s.pendingMu.Lock()
	delete(s.pendingClient, id)
	s.pendingMu.Unlock()
}

func (s *Server) failPendingClientRequests(err error) {
	s.pendingMu.Lock()
	pending := s.pendingClient
	s.pendingClient = make(map[string]chan RPCMessage)
	s.pendingMu.Unlock()

	for _, ch := range pending {
		ch <- RPCMessage{
			Error: &RPCError{
				Code:    rpcErrInternal,
				Message: err.Error(),
			},
		}
		close(ch)
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

func (t *turnLifecycle) toolCallInProgressUpdates(event appserver.TurnEvent) []SessionUpdateParams {
	t.phase = turnPhaseStreaming
	return []SessionUpdateParams{
		{
			SessionID:  t.sessionID,
			TurnID:     t.turnID,
			Type:       "tool_call_update",
			Phase:      string(t.phase),
			Status:     "in_progress",
			ToolCallID: event.Approval.ToolCallID,
			Approval:   string(event.Approval.Kind),
			Message:    event.Approval.Message,
		},
	}
}

func (t *turnLifecycle) toolCallOutcomeUpdates(event appserver.TurnEvent, outcome permissionOutcome) []SessionUpdateParams {
	status := "failed"
	if outcome == permissionOutcomeApproved {
		status = "completed"
	}

	return []SessionUpdateParams{
		{
			SessionID:          t.sessionID,
			TurnID:             t.turnID,
			Type:               "tool_call_update",
			Phase:              string(t.phase),
			Status:             status,
			ToolCallID:         event.Approval.ToolCallID,
			Approval:           string(event.Approval.Kind),
			PermissionDecision: string(outcome),
			Message:            fmt.Sprintf("permission %s", outcome),
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

func normalizePermissionOutcome(result SessionRequestPermissionResult) permissionOutcome {
	outcome := strings.TrimSpace(strings.ToLower(result.Outcome))
	if outcome == "" {
		outcome = strings.TrimSpace(strings.ToLower(result.Decision))
	}

	switch outcome {
	case "approve", "approved", "allow", "allowed":
		return permissionOutcomeApproved
	case "decline", "declined", "deny", "denied":
		return permissionOutcomeDeclined
	case "cancel", "cancelled", "canceled":
		return permissionOutcomeCancelled
	}

	if result.Approved != nil {
		if *result.Approved {
			return permissionOutcomeApproved
		}
		return permissionOutcomeDeclined
	}
	return permissionOutcomeCancelled
}

func mapDecisionToAppServer(outcome permissionOutcome) appserver.ApprovalDecision {
	switch outcome {
	case permissionOutcomeApproved:
		return appserver.ApprovalDecisionApproved
	case permissionOutcomeDeclined:
		return appserver.ApprovalDecisionDeclined
	default:
		return appserver.ApprovalDecisionCancelled
	}
}

func buildClientRequest(rawID json.RawMessage, method string, params any) (RPCMessage, error) {
	msg := RPCMessage{
		JSONRPC: "2.0",
		Method:  method,
		ID:      cloneRawMessage(rawID),
	}
	if params == nil {
		return msg, nil
	}

	rawParams, err := json.Marshal(params)
	if err != nil {
		return RPCMessage{}, fmt.Errorf("%s encode params: %w", method, err)
	}
	msg.Params = rawParams
	return msg, nil
}

func normalizeMessageID(raw json.RawMessage) string {
	var idString string
	if err := json.Unmarshal(raw, &idString); err == nil {
		return idString
	}

	var idNumber int64
	if err := json.Unmarshal(raw, &idNumber); err == nil {
		return strconv.FormatInt(idNumber, 10)
	}
	return string(raw)
}
