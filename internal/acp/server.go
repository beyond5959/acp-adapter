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
	methodFSWriteTextFile          = "fs/write_text_file"

	defaultPermissionTimeout = 30 * time.Second
	defaultFSWriteTimeout    = 10 * time.Second

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

type patchApplyMode string

const (
	patchApplyModeAppServer patchApplyMode = "appserver"
	patchApplyModeACPFS     patchApplyMode = "acp_fs"
)

type fsWriteTextFileResult struct {
	OK       bool   `json:"ok,omitempty"`
	Conflict bool   `json:"conflict,omitempty"`
	Message  string `json:"message,omitempty"`
}

type turnLifecycle struct {
	sessionID       string
	turnID          string
	phase           turnPhase
	cancelRequested bool
}

type runtimeOptions struct {
	Profile            string
	Model              string
	ApprovalPolicy     string
	Sandbox            string
	Personality        string
	SystemInstructions string
}

type slashCommandKind string

const (
	slashCommandNone         slashCommandKind = ""
	slashCommandReview       slashCommandKind = "review"
	slashCommandReviewBranch slashCommandKind = "review_branch"
	slashCommandReviewCommit slashCommandKind = "review_commit"
	slashCommandInit         slashCommandKind = "init"
	slashCommandCompact      slashCommandKind = "compact"
	slashCommandLogout       slashCommandKind = "logout"
	slashCommandMCPList      slashCommandKind = "mcp_list"
	slashCommandMCPCall      slashCommandKind = "mcp_call"
	slashCommandMCPOAuth     slashCommandKind = "mcp_oauth"
)

type slashCommand struct {
	kind               slashCommandKind
	argOne             string
	argTwo             string
	argTail            string
	turnInput          string
	reviewInstructions string
}

type appClient interface {
	ThreadStart(ctx context.Context, cwd string, options appserver.RunOptions) (string, error)
	TurnStart(
		ctx context.Context,
		threadID string,
		input string,
		options appserver.RunOptions,
	) (string, <-chan appserver.TurnEvent, error)
	ReviewStart(
		ctx context.Context,
		threadID string,
		instructions string,
		options appserver.RunOptions,
	) (string, <-chan appserver.TurnEvent, error)
	CompactStart(ctx context.Context, threadID string) (string, <-chan appserver.TurnEvent, error)
	TurnInterrupt(ctx context.Context, threadID, turnID string) error
	ApprovalRespond(ctx context.Context, approvalID string, decision appserver.ApprovalDecision) error
	MCPServersList(ctx context.Context) ([]appserver.MCPServer, error)
	MCPToolCall(ctx context.Context, params appserver.MCPToolCallParams) (appserver.MCPToolCallResult, error)
	MCPOAuthLogin(ctx context.Context, server string) (appserver.MCPOAuthLoginResult, error)
	Logout(ctx context.Context) error
}

// ServerOptions configures optional ACP server behaviors.
type ServerOptions struct {
	PatchApplyMode  string
	Profiles        map[string]ProfileConfig
	DefaultProfile  string
	InitialAuthMode string
}

// Server handles ACP JSON-RPC requests over stdio.
type Server struct {
	codec    *StdioCodec
	app      appClient
	sessions *bridge.Store
	logger   *slog.Logger
	options  ServerOptions

	pendingMu     sync.Mutex
	pendingClient map[string]chan RPCMessage
	nextClientID  uint64
	nextInlineID  uint64

	sessionConfigMu sync.Mutex
	sessionConfigs  map[string]runtimeOptions

	authMu       sync.Mutex
	authMode     string
	authLoggedIn bool
}

// NewServer creates an ACP request router.
func NewServer(
	codec *StdioCodec,
	app appClient,
	sessions *bridge.Store,
	logger *slog.Logger,
	options ServerOptions,
) *Server {
	if normalizePatchApplyMode(options.PatchApplyMode) == "" {
		options.PatchApplyMode = string(patchApplyModeAppServer)
	}
	options.DefaultProfile = strings.TrimSpace(options.DefaultProfile)

	return &Server{
		codec:          codec,
		app:            app,
		sessions:       sessions,
		logger:         logger,
		options:        options,
		pendingClient:  make(map[string]chan RPCMessage),
		nextClientID:   0,
		sessionConfigs: make(map[string]runtimeOptions),
		authMode:       strings.TrimSpace(options.InitialAuthMode),
		authLoggedIn:   strings.TrimSpace(options.InitialAuthMode) != "",
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
		ActiveAuthMethod: s.currentAuthMode(),
	}
	_ = s.codec.WriteResult(id, result)
}

func (s *Server) handleSessionNew(ctx context.Context, id json.RawMessage, paramsRaw json.RawMessage) {
	var params SessionNewParams
	if err := decodeParams(paramsRaw, &params); err != nil {
		s.writeInvalidParams(id, map[string]any{"error": err.Error()})
		return
	}
	if !s.requireAuth(id, "session/new") {
		return
	}

	options, err := s.resolveRuntimeOptions(runtimeOptions{
		Profile:            params.Profile,
		Model:              params.Model,
		ApprovalPolicy:     params.ApprovalPolicy,
		Sandbox:            params.Sandbox,
		Personality:        params.Personality,
		SystemInstructions: params.SystemInstructions,
	}, runtimeOptions{})
	if err != nil {
		s.writeInvalidParams(id, map[string]any{"error": err.Error()})
		return
	}

	threadID, err := s.app.ThreadStart(ctx, params.CWD, toRunOptions(options))
	if err != nil {
		s.writeInternalError(id, "thread/start failed", map[string]any{"error": err.Error()})
		return
	}

	sessionID := s.sessions.Create(threadID)
	s.setSessionConfig(sessionID, options)
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

	command, err := parseSlashCommand(prompt)
	if err != nil {
		s.writeInvalidParams(id, map[string]any{"error": err.Error()})
		return
	}
	if command.kind != slashCommandLogout && !s.requireAuth(id, "session/prompt") {
		return
	}

	sessionOptions := s.getSessionConfig(params.SessionID)
	resolvedOptions, err := s.resolveRuntimeOptions(runtimeOptions{
		Profile:            params.Profile,
		Model:              params.Model,
		ApprovalPolicy:     params.ApprovalPolicy,
		Sandbox:            params.Sandbox,
		Personality:        params.Personality,
		SystemInstructions: params.SystemInstructions,
	}, sessionOptions)
	if err != nil {
		s.writeInvalidParams(id, map[string]any{"error": err.Error()})
		return
	}

	switch command.kind {
	case slashCommandLogout:
		s.handleLogoutSlash(ctx, id, params.SessionID)
		return
	case slashCommandMCPList:
		s.handleMCPListSlash(ctx, id, params.SessionID)
		return
	case slashCommandMCPCall:
		s.handleMCPCallSlash(ctx, id, params.SessionID, command)
		return
	case slashCommandMCPOAuth:
		s.handleMCPOAuthSlash(ctx, id, params.SessionID, command)
		return
	default:
	}

	turnCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var turnID string
	var events <-chan appserver.TurnEvent
	method := "turn/start"
	switch command.kind {
	case slashCommandReview:
		method = "review/start"
		turnID, events, err = s.app.ReviewStart(turnCtx, threadID, command.reviewInstructions, toRunOptions(resolvedOptions))
	case slashCommandReviewBranch:
		method = "review/start"
		turnID, events, err = s.app.ReviewStart(
			turnCtx,
			threadID,
			fmt.Sprintf("review branch %s", command.argOne),
			toRunOptions(resolvedOptions),
		)
	case slashCommandReviewCommit:
		method = "review/start"
		turnID, events, err = s.app.ReviewStart(
			turnCtx,
			threadID,
			fmt.Sprintf("review commit %s", command.argOne),
			toRunOptions(resolvedOptions),
		)
	case slashCommandInit:
		turnID, events, err = s.app.TurnStart(turnCtx, threadID, command.turnInput, toRunOptions(resolvedOptions))
	case slashCommandCompact:
		method = "thread/compact/start"
		turnID, events, err = s.app.CompactStart(turnCtx, threadID)
	default:
		turnID, events, err = s.app.TurnStart(turnCtx, threadID, prompt, toRunOptions(resolvedOptions))
	}
	if err != nil {
		s.writeInternalError(id, method+" failed", map[string]any{
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

	toolStatus := "failed"
	toolMessage := fmt.Sprintf("permission %s", decision)
	respondDecision := mapDecisionToAppServer(decision)
	if decision == permissionOutcomeApproved {
		toolStatus = "completed"
	}

	mode := normalizePatchApplyMode(s.options.PatchApplyMode)
	if mode == patchApplyModeACPFS && event.Approval.Kind == appserver.ApprovalKindFile && decision == permissionOutcomeApproved {
		if err := s.applyPatchViaACPFS(ctx, lifecycle.sessionID, lifecycle.turnID, event.Approval); err != nil {
			toolStatus = "failed"
			toolMessage = fmt.Sprintf("permission approved but ACP fs apply failed: %v", err)
			respondDecision = appserver.ApprovalDecisionDeclined
			updates = append(updates, SessionUpdateParams{
				SessionID: lifecycle.sessionID,
				TurnID:    lifecycle.turnID,
				Type:      "status",
				Phase:     string(turnPhaseStreaming),
				Status:    "review_apply_failed",
				Message:   toolMessage,
			})
		} else {
			updates = append(updates, SessionUpdateParams{
				SessionID: lifecycle.sessionID,
				TurnID:    lifecycle.turnID,
				Type:      "status",
				Phase:     string(turnPhaseStreaming),
				Status:    "review_apply_applied",
				Message:   "patch applied via ACP fs",
			})
		}
	}

	updates = append(
		updates,
		lifecycle.toolCallOutcomeUpdate(
			event,
			decision,
			toolStatus,
			toolMessage,
		),
	)

	respondCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if respondErr := s.app.ApprovalRespond(respondCtx, event.Approval.ApprovalID, respondDecision); respondErr != nil {
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

func (s *Server) applyPatchViaACPFS(
	ctx context.Context,
	sessionID string,
	turnID string,
	approval appserver.ApprovalRequest,
) error {
	path := approval.WritePath
	if path == "" && len(approval.Files) > 0 {
		path = approval.Files[0]
	}
	if path == "" {
		return fmt.Errorf("missing file path for ACP fs apply")
	}
	if strings.TrimSpace(approval.WriteText) == "" {
		return fmt.Errorf("missing write text for ACP fs apply")
	}

	callCtx, cancel := context.WithTimeout(ctx, defaultFSWriteTimeout)
	defer cancel()

	params := map[string]any{
		"sessionId": sessionID,
		"turnId":    turnID,
		"path":      path,
		"text":      approval.WriteText,
		"patch":     approval.Patch,
	}

	var result fsWriteTextFileResult
	if err := s.callClient(callCtx, methodFSWriteTextFile, params, &result); err != nil {
		return err
	}
	if result.Conflict {
		if result.Message != "" {
			return fmt.Errorf("patch conflict: %s", result.Message)
		}
		return fmt.Errorf("patch conflict")
	}
	if result.OK {
		return nil
	}
	if result.Message != "" {
		return fmt.Errorf("fs write rejected: %s", result.Message)
	}
	return nil
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

func (s *Server) currentAuthMode() string {
	s.authMu.Lock()
	defer s.authMu.Unlock()
	if !s.authLoggedIn {
		return ""
	}
	return s.authMode
}

func (s *Server) requireAuth(id json.RawMessage, method string) bool {
	s.authMu.Lock()
	authenticated := s.authLoggedIn
	mode := s.authMode
	s.authMu.Unlock()
	if authenticated {
		return true
	}

	s.writeInternalError(id, method+" requires authentication", map[string]any{
		"hint": "set CODEX_API_KEY or OPENAI_API_KEY, or enable ChatGPT subscription login",
		"mode": mode,
	})
	return false
}

func (s *Server) markLoggedOut() {
	s.authMu.Lock()
	s.authLoggedIn = false
	s.authMode = ""
	s.authMu.Unlock()
}

func (s *Server) setSessionConfig(sessionID string, options runtimeOptions) {
	s.sessionConfigMu.Lock()
	s.sessionConfigs[sessionID] = options
	s.sessionConfigMu.Unlock()
}

func (s *Server) getSessionConfig(sessionID string) runtimeOptions {
	s.sessionConfigMu.Lock()
	defer s.sessionConfigMu.Unlock()
	return s.sessionConfigs[sessionID]
}

func (s *Server) resolveRuntimeOptions(requested runtimeOptions, base runtimeOptions) (runtimeOptions, error) {
	resolved := base
	if isRuntimeOptionsEmpty(resolved) && strings.TrimSpace(s.options.DefaultProfile) != "" {
		profile, ok := s.options.Profiles[s.options.DefaultProfile]
		if !ok {
			return runtimeOptions{}, fmt.Errorf("default profile not found: %s", s.options.DefaultProfile)
		}
		resolved = runtimeOptions{
			Profile:            s.options.DefaultProfile,
			Model:              profile.Model,
			ApprovalPolicy:     profile.ApprovalPolicy,
			Sandbox:            profile.Sandbox,
			Personality:        profile.Personality,
			SystemInstructions: profile.SystemInstructions,
		}
	}

	if profileName := strings.TrimSpace(requested.Profile); profileName != "" {
		profile, ok := s.options.Profiles[profileName]
		if !ok {
			return runtimeOptions{}, fmt.Errorf("profile not found: %s", profileName)
		}
		resolved = runtimeOptions{
			Profile:            profileName,
			Model:              profile.Model,
			ApprovalPolicy:     profile.ApprovalPolicy,
			Sandbox:            profile.Sandbox,
			Personality:        profile.Personality,
			SystemInstructions: profile.SystemInstructions,
		}
	}

	if value := strings.TrimSpace(requested.Model); value != "" {
		resolved.Model = value
	}
	if value := strings.TrimSpace(requested.ApprovalPolicy); value != "" {
		resolved.ApprovalPolicy = value
	}
	if value := strings.TrimSpace(requested.Sandbox); value != "" {
		resolved.Sandbox = value
	}
	if value := strings.TrimSpace(requested.Personality); value != "" {
		resolved.Personality = value
	}
	if value := strings.TrimSpace(requested.SystemInstructions); value != "" {
		resolved.SystemInstructions = value
	}
	return resolved, nil
}

func isRuntimeOptionsEmpty(options runtimeOptions) bool {
	return strings.TrimSpace(options.Profile) == "" &&
		strings.TrimSpace(options.Model) == "" &&
		strings.TrimSpace(options.ApprovalPolicy) == "" &&
		strings.TrimSpace(options.Sandbox) == "" &&
		strings.TrimSpace(options.Personality) == "" &&
		strings.TrimSpace(options.SystemInstructions) == ""
}

func toRunOptions(options runtimeOptions) appserver.RunOptions {
	return appserver.RunOptions{
		Model:              options.Model,
		ApprovalPolicy:     options.ApprovalPolicy,
		Sandbox:            options.Sandbox,
		Personality:        options.Personality,
		SystemInstructions: options.SystemInstructions,
	}
}

func parseSlashCommand(prompt string) (slashCommand, error) {
	trimmed := strings.TrimSpace(prompt)
	if trimmed == "" || !strings.HasPrefix(trimmed, "/") {
		return slashCommand{kind: slashCommandNone}, nil
	}

	switch {
	case trimmed == "/review" || strings.HasPrefix(trimmed, "/review "):
		instructions := strings.TrimSpace(strings.TrimPrefix(trimmed, "/review"))
		if instructions == "" {
			instructions = "review workspace changes"
		}
		return slashCommand{
			kind:               slashCommandReview,
			reviewInstructions: instructions,
		}, nil
	case strings.HasPrefix(trimmed, "/review-branch"):
		fields := strings.Fields(trimmed)
		if len(fields) < 2 {
			return slashCommand{}, fmt.Errorf("/review-branch requires <branch>")
		}
		return slashCommand{
			kind:   slashCommandReviewBranch,
			argOne: fields[1],
		}, nil
	case strings.HasPrefix(trimmed, "/review-commit"):
		fields := strings.Fields(trimmed)
		if len(fields) < 2 {
			return slashCommand{}, fmt.Errorf("/review-commit requires <sha>")
		}
		return slashCommand{
			kind:   slashCommandReviewCommit,
			argOne: fields[1],
		}, nil
	case strings.HasPrefix(trimmed, "/init"):
		tail := strings.TrimSpace(strings.TrimPrefix(trimmed, "/init"))
		input := "approval file initialize workspace scaffold"
		if tail != "" {
			input = "approval file initialize workspace scaffold: " + tail
		}
		return slashCommand{
			kind:      slashCommandInit,
			turnInput: input,
		}, nil
	case trimmed == "/compact":
		return slashCommand{kind: slashCommandCompact}, nil
	case trimmed == "/logout":
		return slashCommand{kind: slashCommandLogout}, nil
	case strings.HasPrefix(trimmed, "/mcp"):
		fields := strings.Fields(trimmed)
		if len(fields) < 2 {
			return slashCommand{}, fmt.Errorf("/mcp requires subcommand: list|call|oauth")
		}
		switch fields[1] {
		case "list":
			return slashCommand{kind: slashCommandMCPList}, nil
		case "call":
			if len(fields) < 4 {
				return slashCommand{}, fmt.Errorf("/mcp call requires <server> <tool> [arguments]")
			}
			command := slashCommand{
				kind:   slashCommandMCPCall,
				argOne: fields[2],
				argTwo: fields[3],
			}
			if len(fields) > 4 {
				command.argTail = strings.Join(fields[4:], " ")
			}
			return command, nil
		case "oauth", "login":
			if len(fields) < 3 {
				return slashCommand{}, fmt.Errorf("/mcp oauth requires <server>")
			}
			return slashCommand{
				kind:   slashCommandMCPOAuth,
				argOne: fields[2],
			}, nil
		default:
			return slashCommand{}, fmt.Errorf("unsupported /mcp subcommand: %s", fields[1])
		}
	default:
		// Unknown slash command is treated as normal prompt text.
		return slashCommand{kind: slashCommandNone}, nil
	}
}

func (s *Server) runInlineCommand(
	ctx context.Context,
	id json.RawMessage,
	sessionID string,
	turnPrefix string,
	fn func(context.Context, *turnLifecycle) (string, error),
) {
	turnID := fmt.Sprintf("%s-%d", turnPrefix, atomic.AddUint64(&s.nextInlineID, 1))
	turnCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	if _, err := s.sessions.BeginTurn(sessionID, turnID, cancel); err != nil {
		s.writeInternalError(id, "begin turn failed", map[string]any{
			"error":     err.Error(),
			"sessionId": sessionID,
			"turnId":    turnID,
		})
		return
	}
	defer s.sessions.EndTurn(sessionID, turnID)

	lifecycle := newTurnLifecycle(sessionID, turnID)
	s.emitUpdates(lifecycle.startedUpdate())

	stopReason, err := fn(turnCtx, lifecycle)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			lifecycle.phase = turnPhaseCancelled
			s.emitUpdates(lifecycle.cancelledUpdate())
			s.writePromptResult(id, "cancelled")
			return
		}
		lifecycle.phase = turnPhaseError
		s.emitUpdates([]SessionUpdateParams{
			{
				SessionID: sessionID,
				TurnID:    turnID,
				Type:      "status",
				Phase:     string(lifecycle.phase),
				Status:    "turn_error",
				Message:   err.Error(),
			},
		})
		s.writePromptResult(id, "error")
		return
	}

	normalized := normalizeStopReason(stopReason)
	switch normalized {
	case "cancelled":
		lifecycle.phase = turnPhaseCancelled
		s.emitUpdates([]SessionUpdateParams{
			{
				SessionID: sessionID,
				TurnID:    turnID,
				Type:      "status",
				Phase:     string(lifecycle.phase),
				Status:    "turn_cancelled",
			},
		})
	case "error":
		lifecycle.phase = turnPhaseError
		s.emitUpdates([]SessionUpdateParams{
			{
				SessionID: sessionID,
				TurnID:    turnID,
				Type:      "status",
				Phase:     string(lifecycle.phase),
				Status:    "turn_error",
			},
		})
	default:
		lifecycle.phase = turnPhaseCompleted
		s.emitUpdates([]SessionUpdateParams{
			{
				SessionID: sessionID,
				TurnID:    turnID,
				Type:      "status",
				Phase:     string(lifecycle.phase),
				Status:    "turn_completed",
			},
		})
	}
	s.writePromptResult(id, normalized)
}

func (s *Server) handleLogoutSlash(ctx context.Context, id json.RawMessage, sessionID string) {
	s.runInlineCommand(ctx, id, sessionID, "logout", func(turnCtx context.Context, lifecycle *turnLifecycle) (string, error) {
		s.markLoggedOut()

		logoutCtx, cancel := context.WithTimeout(turnCtx, 2*time.Second)
		defer cancel()
		if err := s.app.Logout(logoutCtx); err != nil {
			s.logger.Warn("app-server logout failed; local auth still cleared", slog.String("error", err.Error()))
		}

		lifecycle.phase = turnPhaseStreaming
		s.emitUpdates([]SessionUpdateParams{
			{
				SessionID: sessionID,
				TurnID:    lifecycle.turnID,
				Type:      "status",
				Phase:     string(lifecycle.phase),
				Status:    "auth_logged_out",
				Message:   "logout completed; re-authentication required",
			},
		})
		return "end_turn", nil
	})
}

func (s *Server) handleMCPListSlash(ctx context.Context, id json.RawMessage, sessionID string) {
	s.runInlineCommand(ctx, id, sessionID, "mcp-list", func(turnCtx context.Context, lifecycle *turnLifecycle) (string, error) {
		servers, err := s.app.MCPServersList(turnCtx)
		if err != nil {
			return "error", fmt.Errorf("mcpServer/list failed: %w", err)
		}

		message := "no MCP servers reported"
		if len(servers) > 0 {
			parts := make([]string, 0, len(servers))
			for _, server := range servers {
				parts = append(parts, fmt.Sprintf("%s(oauth=%t tools=%s)", server.Name, server.OAuthRequired, strings.Join(server.Tools, ",")))
			}
			message = "mcp servers: " + strings.Join(parts, "; ")
		}

		lifecycle.phase = turnPhaseStreaming
		s.emitUpdates([]SessionUpdateParams{
			{
				SessionID: sessionID,
				TurnID:    lifecycle.turnID,
				Type:      "message",
				Phase:     string(lifecycle.phase),
				Delta:     message,
			},
		})
		return "end_turn", nil
	})
}

func (s *Server) handleMCPOAuthSlash(
	ctx context.Context,
	id json.RawMessage,
	sessionID string,
	command slashCommand,
) {
	s.runInlineCommand(ctx, id, sessionID, "mcp-oauth", func(turnCtx context.Context, lifecycle *turnLifecycle) (string, error) {
		result, err := s.app.MCPOAuthLogin(turnCtx, command.argOne)
		if err != nil {
			return "error", fmt.Errorf("mcpServer/oauth/login failed: %w", err)
		}

		message := fmt.Sprintf(
			"mcp oauth server=%s status=%s url=%s %s",
			command.argOne,
			result.Status,
			result.URL,
			result.Message,
		)

		lifecycle.phase = turnPhaseStreaming
		s.emitUpdates([]SessionUpdateParams{
			{
				SessionID: sessionID,
				TurnID:    lifecycle.turnID,
				Type:      "message",
				Phase:     string(lifecycle.phase),
				Delta:     strings.TrimSpace(message),
			},
		})
		return "end_turn", nil
	})
}

func (s *Server) handleMCPCallSlash(
	ctx context.Context,
	id json.RawMessage,
	sessionID string,
	command slashCommand,
) {
	s.runInlineCommand(ctx, id, sessionID, "mcp-call", func(turnCtx context.Context, lifecycle *turnLifecycle) (string, error) {
		toolCallID := fmt.Sprintf("mcp-tool-%d", atomic.AddUint64(&s.nextInlineID, 1))
		approval := appserver.ApprovalRequest{
			TurnID:     lifecycle.turnID,
			ToolCallID: toolCallID,
			Kind:       appserver.ApprovalKindMCP,
			MCPServer:  command.argOne,
			MCPTool:    command.argTwo,
			Message:    "permission required before MCP side effect call",
		}
		event := appserver.TurnEvent{Approval: approval}
		s.emitUpdates(lifecycle.toolCallInProgressUpdates(event))

		decision, err := s.requestPermission(turnCtx, sessionID, lifecycle.turnID, approval)
		if err != nil {
			s.logger.Warn(
				"session/request_permission failed for mcp call; default deny",
				slog.String("sessionId", sessionID),
				slog.String("turnId", lifecycle.turnID),
				slog.String("error", err.Error()),
			)
			decision = permissionOutcomeCancelled
		}

		toolStatus := "failed"
		toolMessage := fmt.Sprintf("permission %s", decision)
		if decision == permissionOutcomeApproved {
			result, callErr := s.app.MCPToolCall(turnCtx, appserver.MCPToolCallParams{
				Server:    command.argOne,
				Tool:      command.argTwo,
				Arguments: command.argTail,
			})
			if callErr != nil {
				toolMessage = fmt.Sprintf("mcp call failed: %v", callErr)
			} else {
				toolStatus = "completed"
				toolMessage = "mcp call completed"
				lifecycle.phase = turnPhaseStreaming
				s.emitUpdates([]SessionUpdateParams{
					{
						SessionID: sessionID,
						TurnID:    lifecycle.turnID,
						Type:      "message",
						Phase:     string(lifecycle.phase),
						Delta:     strings.TrimSpace(result.Output),
					},
				})
			}
		}

		s.emitUpdates([]SessionUpdateParams{
			lifecycle.toolCallOutcomeUpdate(
				event,
				decision,
				toolStatus,
				toolMessage,
			),
		})
		return "end_turn", nil
	})
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

func normalizePatchApplyMode(raw string) patchApplyMode {
	switch strings.TrimSpace(strings.ToLower(raw)) {
	case string(patchApplyModeACPFS):
		return patchApplyModeACPFS
	case string(patchApplyModeAppServer):
		return patchApplyModeAppServer
	default:
		return ""
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

func (t *turnLifecycle) toolCallOutcomeUpdate(
	event appserver.TurnEvent,
	outcome permissionOutcome,
	status string,
	message string,
) SessionUpdateParams {
	return SessionUpdateParams{
		SessionID:          t.sessionID,
		TurnID:             t.turnID,
		Type:               "tool_call_update",
		Phase:              string(t.phase),
		Status:             status,
		ToolCallID:         event.Approval.ToolCallID,
		Approval:           string(event.Approval.Kind),
		PermissionDecision: string(outcome),
		Message:            message,
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
	case appserver.TurnEventTypeReviewModeEntered:
		t.phase = turnPhaseStreaming
		return []SessionUpdateParams{
			{
				SessionID: t.sessionID,
				TurnID:    event.TurnID,
				Type:      "status",
				Phase:     string(t.phase),
				Status:    "review_mode_entered",
			},
		}, false, ""
	case appserver.TurnEventTypeReviewModeExited:
		t.phase = turnPhaseStreaming
		return []SessionUpdateParams{
			{
				SessionID: t.sessionID,
				TurnID:    event.TurnID,
				Type:      "status",
				Phase:     string(t.phase),
				Status:    "review_mode_exited",
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
