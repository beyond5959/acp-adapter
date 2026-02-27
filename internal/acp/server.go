package acp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/beyond5959/codex-acp/internal/appserver"
	"github.com/beyond5959/codex-acp/internal/bridge"
)

const (
	methodInitialize               = "initialize"
	methodAuthenticate             = "authenticate"
	methodSessionNew               = "session/new"
	methodSessionPrompt            = "session/prompt"
	methodSessionCancel            = "session/cancel"
	methodSessionUpdate            = "session/update"
	methodSessionRequestPermission = "session/request_permission"
	methodFSWriteTextFile          = "fs/write_text_file"
	methodFSReadTextFile           = "fs/read_text_file"

	defaultPermissionTimeout = 30 * time.Second
	defaultFSWriteTimeout    = 10 * time.Second
	defaultImageSizeLimit    = 4 * 1024 * 1024
	defaultMentionTextLimit  = 64 * 1024

	rpcErrMethodNotFound = -32601
	rpcErrInvalidParams  = -32602
	rpcErrInternal       = -32000
)

var (
	todoChecklistPattern = regexp.MustCompile(`(?m)^\s*(?:[-*]|\d+\.)\s+\[([ xX])\]\s+(.+?)\s*$`)
	allowedImageMimeType = map[string]struct{}{
		"image/png":  {},
		"image/jpeg": {},
		"image/webp": {},
		"image/gif":  {},
	}
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

type fsReadTextFileResult struct {
	Text    string `json:"text,omitempty"`
	Content string `json:"content,omitempty"`
	Message string `json:"message,omitempty"`
}

type adapterCapabilities struct {
	canReadTextFile bool
}

type turnLifecycle struct {
	sessionID       string
	turnID          string
	phase           turnPhase
	cancelRequested bool
	messageBuffer   strings.Builder
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
		input []appserver.UserInput,
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
	PatchApplyMode   string
	RetryTurnOnCrash bool
	Profiles         map[string]ProfileConfig
	DefaultProfile   string
	InitialAuthMode  string
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

	capabilitiesMu sync.RWMutex
	capabilities   adapterCapabilities

	sessionTodosMu sync.Mutex
	sessionTodos   map[string][]TodoItem

	authMu       sync.Mutex
	authMode     string
	lastAuthMode string
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
		sessionTodos:   make(map[string][]TodoItem),
		authMode:       strings.TrimSpace(options.InitialAuthMode),
		lastAuthMode:   strings.TrimSpace(options.InitialAuthMode),
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
		s.handleInitialize(rawID, msg.Params)
	case methodAuthenticate:
		s.handleAuthenticate(rawID, msg.Params)
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

func (s *Server) handleInitialize(id json.RawMessage, paramsRaw json.RawMessage) {
	s.captureClientCapabilities(paramsRaw)

	authMethods := []AuthMethod{
		{
			ID:          "codex_api_key",
			Name:        "CODEX_API_KEY",
			Description: "Authenticate with CODEX_API_KEY from environment.",
			Type:        "codex_api_key",
			Label:       "CODEX_API_KEY",
		},
		{
			ID:          "openai_api_key",
			Name:        "OPENAI_API_KEY",
			Description: "Authenticate with OPENAI_API_KEY from environment.",
			Type:        "openai_api_key",
			Label:       "OPENAI_API_KEY",
		},
		{
			ID:          "chatgpt_subscription",
			Name:        "ChatGPT subscription",
			Description: "Authenticate with existing Codex CLI subscription login state.",
			Type:        "chatgpt_subscription",
			Label:       "ChatGPT subscription",
		},
	}

	result := InitializeResult{
		ProtocolVersion: 1,
		AgentCapabilities: AgentCapabilities{
			LoadSession: false,
			PromptCapabilities: PromptCapabilities{
				Image:           true,
				Audio:           false,
				EmbeddedContext: true,
			},
			MCPCapabilities: MCPCapabilities{
				HTTP: false,
				SSE:  false,
			},
			SessionCapabilities: SessionCapabilities{},

			// Legacy capability fields for older ACP clients.
			Sessions:      true,
			Images:        true,
			ToolCalls:     true,
			SlashCommands: true,
			Permissions:   true,
		},
		AgentInfo: &ImplementationInfo{
			Name:    "codex-acp-go",
			Version: "dev",
			Title:   "Codex ACP Go",
		},
		AuthMethods:      authMethods,
		ActiveAuthMethod: s.currentAuthMode(),
	}
	_ = s.codec.WriteResult(id, result)
}

func (s *Server) handleAuthenticate(id json.RawMessage, paramsRaw json.RawMessage) {
	var params AuthenticateParams
	if err := decodeParams(paramsRaw, &params); err != nil {
		s.writeInvalidParams(id, map[string]any{"error": err.Error()})
		return
	}

	methodID := strings.TrimSpace(params.MethodID)
	if methodID == "" {
		methodID = strings.TrimSpace(params.Type)
	}
	switch methodID {
	case "codex_api_key", "openai_api_key", "chatgpt_subscription":
	default:
		s.writeInvalidParams(id, map[string]any{
			"methodId": "unsupported auth method",
			"allowed": []string{
				"codex_api_key",
				"openai_api_key",
				"chatgpt_subscription",
			},
		})
		return
	}

	s.authMu.Lock()
	s.authMode = methodID
	s.lastAuthMode = methodID
	s.authLoggedIn = true
	s.authMu.Unlock()

	_ = s.codec.WriteResult(id, AuthenticateResult{
		Authenticated:    true,
		ActiveAuthMethod: methodID,
	})
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
	s.sessionTodosMu.Lock()
	s.sessionTodos[sessionID] = nil
	s.sessionTodosMu.Unlock()
	_ = s.codec.WriteResult(id, SessionNewResult{SessionID: sessionID})
}

func (s *Server) handleSessionPrompt(ctx context.Context, id json.RawMessage, paramsRaw json.RawMessage) {
	params, err := decodeSessionPromptParams(paramsRaw)
	if err != nil {
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

	preparedInput, prepWarnings, promptText, err := s.prepareTurnInput(ctx, params.SessionID, params, paramsRaw)
	if err != nil {
		s.writeInvalidParams(id, map[string]any{"error": err.Error()})
		return
	}

	command, err := parseSlashCommand(promptText)
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

	method, turnID, events, err := s.startPromptTurn(turnCtx, threadID, command, preparedInput, resolvedOptions)
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

	activeTurnID := turnID
	defer func() {
		s.sessions.EndTurn(params.SessionID, activeTurnID)
	}()

	lifecycle := newTurnLifecycle(params.SessionID, turnID)
	s.emitUpdates(lifecycle.startedUpdate())
	s.emitUpdates(warningUpdates(params.SessionID, lifecycle.turnID, prepWarnings))

	retried := false
	retrySafe := true

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
			if event.Type == appserver.TurnEventTypeError {
				if s.options.RetryTurnOnCrash && !retried && retrySafe && isRetryableTurnError(event.Message) {
					retried = true
					lifecycle.resetForRetry()
					s.emitUpdates([]SessionUpdateParams{
						{
							SessionID: lifecycle.sessionID,
							TurnID:    lifecycle.turnID,
							Type:      "status",
							Phase:     string(turnPhaseStreaming),
							Status:    "backend_restarted_retrying",
							Message:   "codex app-server exited mid-turn; backend restarted, retrying once",
						},
					})

					_, retryTurnID, retryEvents, retryErr := s.startPromptTurn(
						turnCtx,
						threadID,
						command,
						preparedInput,
						resolvedOptions,
					)
					if retryErr != nil && shouldRetryAfterSupervisorRestart(retryErr) {
						_, retryTurnID, retryEvents, retryErr = s.startPromptTurn(
							turnCtx,
							threadID,
							command,
							preparedInput,
							resolvedOptions,
						)
					}
					if retryErr != nil {
						lifecycle.phase = turnPhaseError
						s.emitUpdates([]SessionUpdateParams{
							{
								SessionID: lifecycle.sessionID,
								TurnID:    lifecycle.turnID,
								Type:      "status",
								Phase:     string(turnPhaseError),
								Status:    "turn_error",
								Message: fmt.Sprintf(
									"backend restarted but internal retry failed: %v; please retry this prompt once",
									retryErr,
								),
							},
						})
						s.clearTurnTodosOnFailure(params.SessionID, "error")
						s.writePromptResult(id, "error")
						return
					}
					if _, replaceErr := s.sessions.ReplaceTurn(params.SessionID, activeTurnID, retryTurnID, cancel); replaceErr != nil {
						_ = s.app.TurnInterrupt(turnCtx, threadID, retryTurnID)
						lifecycle.phase = turnPhaseError
						s.emitUpdates([]SessionUpdateParams{
							{
								SessionID: lifecycle.sessionID,
								TurnID:    lifecycle.turnID,
								Type:      "status",
								Phase:     string(turnPhaseError),
								Status:    "turn_error",
								Message: fmt.Sprintf(
									"backend retry started but session state update failed: %v; please retry this prompt once",
									replaceErr,
								),
							},
						})
						s.clearTurnTodosOnFailure(params.SessionID, "error")
						s.writePromptResult(id, "error")
						return
					}

					activeTurnID = retryTurnID
					events = retryEvents
					continue
				}
				if retried && isRetryableTurnError(event.Message) {
					lifecycle.phase = turnPhaseError
					s.emitUpdates([]SessionUpdateParams{
						{
							SessionID: lifecycle.sessionID,
							TurnID:    lifecycle.turnID,
							Type:      "status",
							Phase:     string(turnPhaseError),
							Status:    "turn_error",
							Message:   "backend restarted but crashed again during retry; please retry this prompt once",
						},
					})
					s.clearTurnTodosOnFailure(params.SessionID, "error")
					s.writePromptResult(id, "error")
					return
				}
			}
			if event.Type == appserver.TurnEventTypeApprovalRequired {
				retrySafe = false
				updates, done, stopReason := s.handleApprovalEvent(turnCtx, lifecycle, event)
				s.emitUpdates(updates)
				if done {
					s.writePromptResult(id, stopReason)
					return
				}
				continue
			}
			if event.Type != appserver.TurnEventTypeStarted {
				retrySafe = false
			}

			updates, done, stopReason := lifecycle.apply(event)
			s.emitUpdates(updates)
			if done {
				s.clearTurnTodosOnFailure(params.SessionID, stopReason)
				s.writePromptResult(id, stopReason)
				return
			}
		}
	}
}

func (s *Server) startPromptTurn(
	ctx context.Context,
	threadID string,
	command slashCommand,
	preparedInput []appserver.UserInput,
	options runtimeOptions,
) (string, string, <-chan appserver.TurnEvent, error) {
	method := "turn/start"
	switch command.kind {
	case slashCommandReview:
		turnID, events, err := s.app.ReviewStart(
			ctx,
			threadID,
			command.reviewInstructions,
			toRunOptions(options),
		)
		return "review/start", turnID, events, err
	case slashCommandReviewBranch:
		turnID, events, err := s.app.ReviewStart(
			ctx,
			threadID,
			fmt.Sprintf("review branch %s", command.argOne),
			toRunOptions(options),
		)
		return "review/start", turnID, events, err
	case slashCommandReviewCommit:
		turnID, events, err := s.app.ReviewStart(
			ctx,
			threadID,
			fmt.Sprintf("review commit %s", command.argOne),
			toRunOptions(options),
		)
		return "review/start", turnID, events, err
	case slashCommandInit:
		turnID, events, err := s.app.TurnStart(
			ctx,
			threadID,
			textTurnInput(command.turnInput),
			toRunOptions(options),
		)
		return method, turnID, events, err
	case slashCommandCompact:
		turnID, events, err := s.app.CompactStart(ctx, threadID)
		return "thread/compact/start", turnID, events, err
	default:
		turnID, events, err := s.app.TurnStart(
			ctx,
			threadID,
			preparedInput,
			toRunOptions(options),
		)
		return method, turnID, events, err
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

func isRetryableTurnError(message string) bool {
	lower := strings.ToLower(strings.TrimSpace(message))
	if lower == "" {
		return false
	}
	tokens := []string{
		"app-server read loop",
		"broken pipe",
		"connection reset",
		"eof",
		"client is closed",
		"codex app-server unavailable",
	}
	for _, token := range tokens {
		if strings.Contains(lower, token) {
			return true
		}
	}
	return false
}

func shouldRetryAfterSupervisorRestart(err error) bool {
	if err == nil {
		return false
	}
	lower := strings.ToLower(err.Error())
	if strings.Contains(lower, "app-server restarted, retry request") {
		return true
	}
	restartWindowTokens := []string{
		"file already closed",
		"broken pipe",
		"connection reset",
		"eof",
	}
	for _, token := range restartWindowTokens {
		if strings.Contains(lower, token) {
			return true
		}
	}
	return false
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
		update = s.attachSessionTodos(update)
		payload := buildSessionUpdatePayload(update)
		if err := s.codec.WriteNotification(methodSessionUpdate, payload); err != nil {
			s.logger.Warn("failed to write session/update", slog.String("error", err.Error()))
			return
		}
	}
}

func buildSessionUpdatePayload(update SessionUpdateParams) map[string]any {
	payload := map[string]any{
		"sessionId": update.SessionID,
	}
	if update.TurnID != "" {
		payload["turnId"] = update.TurnID
	}
	if update.Type != "" {
		payload["type"] = update.Type
	}
	if update.Phase != "" {
		payload["phase"] = update.Phase
	}
	if update.ItemID != "" {
		payload["itemId"] = update.ItemID
	}
	if update.ItemType != "" {
		payload["itemType"] = update.ItemType
	}
	if update.Delta != "" {
		payload["delta"] = update.Delta
	}
	if update.Status != "" {
		payload["status"] = update.Status
	}
	if update.Message != "" {
		payload["message"] = update.Message
	}
	if update.ToolCallID != "" {
		payload["toolCallId"] = update.ToolCallID
	}
	if update.Approval != "" {
		payload["approval"] = update.Approval
	}
	if update.PermissionDecision != "" {
		payload["permissionDecision"] = update.PermissionDecision
	}
	if len(update.Todo) > 0 {
		payload["todo"] = update.Todo
	}

	if mapped := mapACPUpdateForClient(update); mapped != nil {
		payload["update"] = mapped
	}

	return payload
}

func mapACPUpdateForClient(update SessionUpdateParams) map[string]any {
	switch update.Type {
	case "message":
		return map[string]any{
			"sessionUpdate": "agent_message_chunk",
			"content": map[string]any{
				"type": "text",
				"text": update.Delta,
			},
		}
	case "tool_call_update":
		mapped := map[string]any{
			"sessionUpdate": "tool_call_update",
		}
		if update.ToolCallID != "" {
			mapped["toolCallId"] = update.ToolCallID
		}
		if update.Status != "" {
			mapped["status"] = update.Status
		}
		if update.Message != "" {
			mapped["title"] = update.Message
		}
		return mapped
	default:
		text := strings.TrimSpace(update.Delta)
		if text == "" {
			text = strings.TrimSpace(update.Message)
		}
		if text == "" {
			text = strings.TrimSpace(update.Status)
		}
		if text == "" {
			text = strings.TrimSpace(update.Type)
		}
		if text == "" {
			text = "status"
		}
		return map[string]any{
			"sessionUpdate": "agent_thought_chunk",
			"content": map[string]any{
				"type": "text",
				"text": text,
			},
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

func decodeSessionPromptParams(raw json.RawMessage) (SessionPromptParams, error) {
	type promptWire struct {
		SessionID string               `json:"sessionId"`
		Prompt    json.RawMessage      `json:"prompt,omitempty"`
		Content   []PromptContentBlock `json:"content,omitempty"`
		Resources []PromptResource     `json:"resources,omitempty"`
		PromptConfig
	}

	var wire promptWire
	if err := decodeParams(raw, &wire); err != nil {
		return SessionPromptParams{}, err
	}

	params := SessionPromptParams{
		SessionID:    wire.SessionID,
		Content:      wire.Content,
		Resources:    wire.Resources,
		PromptConfig: wire.PromptConfig,
	}

	promptRaw := strings.TrimSpace(string(wire.Prompt))
	if promptRaw == "" || promptRaw == "null" {
		return params, nil
	}

	var promptText string
	if err := json.Unmarshal(wire.Prompt, &promptText); err == nil {
		params.Prompt = promptText
		return params, nil
	}

	var promptBlocks []PromptContentBlock
	if err := json.Unmarshal(wire.Prompt, &promptBlocks); err == nil {
		if len(params.Content) == 0 {
			params.Content = promptBlocks
		} else {
			params.Content = append(promptBlocks, params.Content...)
		}
		return params, nil
	}

	var singleBlock PromptContentBlock
	if err := json.Unmarshal(wire.Prompt, &singleBlock); err == nil {
		params.Content = append([]PromptContentBlock{singleBlock}, params.Content...)
		return params, nil
	}

	return SessionPromptParams{}, fmt.Errorf("decode params: prompt must be string or content block(s)")
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

func warningUpdates(sessionID string, turnID string, warnings []string) []SessionUpdateParams {
	if len(warnings) == 0 {
		return nil
	}
	updates := make([]SessionUpdateParams, 0, len(warnings))
	for _, warning := range warnings {
		warning = strings.TrimSpace(warning)
		if warning == "" {
			continue
		}
		updates = append(updates, SessionUpdateParams{
			SessionID: sessionID,
			TurnID:    turnID,
			Type:      "message",
			Phase:     string(turnPhaseStreaming),
			Delta:     "[adapter warning] " + warning,
		})
	}
	return updates
}

func (s *Server) prepareTurnInput(
	ctx context.Context,
	sessionID string,
	params SessionPromptParams,
	paramsRaw json.RawMessage,
) ([]appserver.UserInput, []string, string, error) {
	promptText := strings.TrimSpace(params.Prompt)
	if promptText == "" {
		promptText = strings.TrimSpace(extractTextPrompt(params.Content))
	}
	if promptText == "" {
		promptText = strings.TrimSpace(fallbackPrompt(paramsRaw))
	}

	input := make([]appserver.UserInput, 0, len(params.Content)+len(params.Resources)+1)
	warnings := make([]string, 0, 4)
	hasPromptTextBlock := false

	for _, block := range params.Content {
		blockType := strings.ToLower(strings.TrimSpace(block.Type))
		switch blockType {
		case "", "text":
			text := strings.TrimSpace(block.Text)
			if text == "" {
				continue
			}
			hasPromptTextBlock = true
			input = append(input, appserver.UserInput{Type: "text", Text: text})
		case "image":
			imageInput, err := buildImageInput(block)
			if err != nil {
				return nil, nil, "", err
			}
			input = append(input, imageInput)
		case "resource", "mention":
			resource := resourceFromBlock(block)
			resourceInput, resourceWarnings := s.resourceToInputs(ctx, sessionID, resource)
			input = append(input, resourceInput...)
			warnings = append(warnings, resourceWarnings...)
		default:
			// Unknown block types degrade to text when text payload exists.
			if text := strings.TrimSpace(block.Text); text != "" {
				hasPromptTextBlock = true
				input = append(input, appserver.UserInput{Type: "text", Text: text})
			}
		}
	}

	for _, resource := range params.Resources {
		resourceInput, resourceWarnings := s.resourceToInputs(ctx, sessionID, resource)
		input = append(input, resourceInput...)
		warnings = append(warnings, resourceWarnings...)
	}

	if strings.TrimSpace(promptText) != "" && !hasPromptTextBlock {
		input = append([]appserver.UserInput{{Type: "text", Text: promptText}}, input...)
	}

	if len(input) == 0 {
		if strings.TrimSpace(promptText) != "" {
			return textTurnInput(promptText), warnings, promptText, nil
		}
		return nil, nil, "", fmt.Errorf("prompt or content is required")
	}

	if strings.TrimSpace(promptText) == "" {
		promptText = strings.TrimSpace(extractTextFromInput(input))
	}
	return input, warnings, promptText, nil
}

func extractTextPrompt(content []PromptContentBlock) string {
	parts := make([]string, 0, len(content))
	for _, block := range content {
		if strings.ToLower(strings.TrimSpace(block.Type)) != "text" {
			continue
		}
		if text := strings.TrimSpace(block.Text); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n")
}

func extractTextFromInput(input []appserver.UserInput) string {
	parts := make([]string, 0, len(input))
	for _, item := range input {
		if strings.ToLower(strings.TrimSpace(item.Type)) != "text" {
			continue
		}
		if text := strings.TrimSpace(item.Text); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n")
}

func resourceFromBlock(block PromptContentBlock) PromptResource {
	resource := PromptResource{
		Name:     block.Name,
		URI:      block.URI,
		Path:     block.Path,
		MimeType: block.MimeType,
		Text:     block.Text,
		Data:     block.Data,
		Range:    block.Range,
	}
	if block.Resource != nil {
		resource = *block.Resource
		if resource.Name == "" {
			resource.Name = block.Name
		}
		if resource.URI == "" {
			resource.URI = block.URI
		}
		if resource.Path == "" {
			resource.Path = block.Path
		}
		if resource.MimeType == "" {
			resource.MimeType = block.MimeType
		}
		if resource.Text == "" && strings.ToLower(strings.TrimSpace(block.Type)) == "resource" {
			resource.Text = block.Text
		}
		if resource.Data == "" {
			resource.Data = block.Data
		}
		if resource.Range == nil {
			resource.Range = block.Range
		}
	}
	return resource
}

func (s *Server) resourceToInputs(
	ctx context.Context,
	sessionID string,
	resource PromptResource,
) ([]appserver.UserInput, []string) {
	var warnings []string
	path := strings.TrimSpace(resource.Path)
	if path == "" {
		path = pathFromURI(resource.URI)
	}
	name := strings.TrimSpace(resource.Name)
	if name == "" {
		switch {
		case path != "":
			name = filepath.Base(path)
		case strings.TrimSpace(resource.URI) != "":
			name = strings.TrimSpace(resource.URI)
		default:
			name = "resource"
		}
	}

	input := make([]appserver.UserInput, 0, 2)
	if path != "" || strings.TrimSpace(resource.URI) != "" {
		mentionPath := path
		if mentionPath == "" {
			mentionPath = strings.TrimSpace(resource.URI)
		}
		input = append(input, appserver.UserInput{
			Type: "mention",
			Name: name,
			Path: mentionPath,
		})
	}

	text := strings.TrimSpace(resource.Text)
	if text == "" && strings.TrimSpace(resource.Data) != "" {
		if decoded, err := decodeBase64Payload(resource.Data); err == nil {
			text = string(decoded)
		} else {
			text = strings.TrimSpace(resource.Data)
		}
	}
	if text == "" && path != "" {
		if !s.canReadTextFile() {
			warnings = append(
				warnings,
				fmt.Sprintf("missing mention context for %s: client has no fs/read_text_file capability", name),
			)
		} else if readText, err := s.readTextFile(ctx, sessionID, path); err != nil {
			warnings = append(
				warnings,
				fmt.Sprintf("failed to read mention context for %s via fs/read_text_file: %v", name, err),
			)
		} else {
			text = readText
		}
	}

	if text != "" {
		truncatedText, truncated := truncateTextBytes(text, defaultMentionTextLimit)
		if truncated {
			warnings = append(
				warnings,
				fmt.Sprintf("mention %s text exceeded %d bytes and was truncated", name, defaultMentionTextLimit),
			)
		}
		input = append(input, appserver.UserInput{
			Type: "text",
			Text: formatMentionContext(resource, name, path, truncatedText, truncated),
		})
	} else if len(input) == 0 {
		warnings = append(warnings, "resource block had no usable uri/path/text")
	}

	return input, warnings
}

func formatMentionContext(
	resource PromptResource,
	name string,
	path string,
	text string,
	truncated bool,
) string {
	var builder strings.Builder
	builder.WriteString("[mention context]\n")
	builder.WriteString("name: " + name + "\n")
	if path != "" {
		builder.WriteString("path: " + path + "\n")
	}
	if uri := strings.TrimSpace(resource.URI); uri != "" {
		builder.WriteString("uri: " + uri + "\n")
	}
	if mime := strings.TrimSpace(resource.MimeType); mime != "" {
		builder.WriteString("mimeType: " + mime + "\n")
	}
	if resource.Range != nil {
		builder.WriteString(fmt.Sprintf("range: %d-%d\n", resource.Range.Start, resource.Range.End))
	}
	if truncated {
		builder.WriteString("truncated: true\n")
	}
	builder.WriteString("content:\n")
	builder.WriteString(text)
	return builder.String()
}

func buildImageInput(block PromptContentBlock) (appserver.UserInput, error) {
	if path := strings.TrimSpace(block.Path); path != "" {
		return appserver.UserInput{Type: "localImage", Path: path}, nil
	}

	if data := strings.TrimSpace(block.Data); data != "" {
		mime := normalizeImageMimeType(block.MimeType)
		if mime == "" {
			return appserver.UserInput{}, fmt.Errorf("image block requires mimeType")
		}
		if !isAllowedImageMimeType(mime) {
			return appserver.UserInput{}, fmt.Errorf("unsupported image mimeType: %s", mime)
		}
		decoded, err := decodeBase64Payload(data)
		if err != nil {
			return appserver.UserInput{}, fmt.Errorf("invalid image base64 payload: %w", err)
		}
		if len(decoded) > defaultImageSizeLimit {
			return appserver.UserInput{}, fmt.Errorf(
				"image payload exceeds %d bytes limit",
				defaultImageSizeLimit,
			)
		}
		return appserver.UserInput{
			Type: "image",
			URL:  fmt.Sprintf("data:%s;base64,%s", mime, sanitizeBase64(data)),
		}, nil
	}

	uri := strings.TrimSpace(block.URI)
	if uri == "" {
		return appserver.UserInput{}, fmt.Errorf("image block requires data, uri, or path")
	}
	if strings.HasPrefix(strings.ToLower(uri), "data:") {
		mime, payload, err := splitDataImageURI(uri)
		if err != nil {
			return appserver.UserInput{}, err
		}
		if !isAllowedImageMimeType(mime) {
			return appserver.UserInput{}, fmt.Errorf("unsupported image mimeType: %s", mime)
		}
		decoded, err := decodeBase64Payload(payload)
		if err != nil {
			return appserver.UserInput{}, fmt.Errorf("invalid image data URI payload: %w", err)
		}
		if len(decoded) > defaultImageSizeLimit {
			return appserver.UserInput{}, fmt.Errorf(
				"image payload exceeds %d bytes limit",
				defaultImageSizeLimit,
			)
		}
		return appserver.UserInput{
			Type: "image",
			URL:  uri,
		}, nil
	}
	if strings.HasPrefix(strings.ToLower(uri), "http://") || strings.HasPrefix(strings.ToLower(uri), "https://") {
		return appserver.UserInput{
			Type: "image",
			URL:  uri,
		}, nil
	}
	return appserver.UserInput{
		Type: "localImage",
		Path: uri,
	}, nil
}

func normalizeImageMimeType(mime string) string {
	mime = strings.TrimSpace(strings.ToLower(mime))
	if idx := strings.Index(mime, ";"); idx >= 0 {
		mime = strings.TrimSpace(mime[:idx])
	}
	return mime
}

func isAllowedImageMimeType(mime string) bool {
	_, ok := allowedImageMimeType[normalizeImageMimeType(mime)]
	return ok
}

func splitDataImageURI(uri string) (string, string, error) {
	parts := strings.SplitN(uri, ",", 2)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("invalid image data URI")
	}
	header := strings.TrimPrefix(strings.ToLower(parts[0]), "data:")
	if !strings.Contains(header, ";base64") {
		return "", "", fmt.Errorf("image data URI must be base64 encoded")
	}
	mime := strings.TrimSpace(strings.TrimSuffix(header, ";base64"))
	if mime == "" {
		return "", "", fmt.Errorf("image data URI missing mimeType")
	}
	return mime, parts[1], nil
}

func sanitizeBase64(payload string) string {
	payload = strings.TrimSpace(payload)
	return strings.TrimRight(payload, "\n\r\t ")
}

func decodeBase64Payload(payload string) ([]byte, error) {
	clean := sanitizeBase64(payload)
	decoded, err := base64.StdEncoding.DecodeString(clean)
	if err == nil {
		return decoded, nil
	}
	return base64.RawStdEncoding.DecodeString(clean)
}

func truncateTextBytes(input string, maxBytes int) (string, bool) {
	if maxBytes <= 0 || len(input) <= maxBytes {
		return input, false
	}

	cut := maxBytes
	for cut > 0 && (input[cut]&0xC0) == 0x80 {
		cut--
	}
	if cut <= 0 {
		cut = maxBytes
	}
	return input[:cut], true
}

func pathFromURI(uriRaw string) string {
	uriRaw = strings.TrimSpace(uriRaw)
	if uriRaw == "" {
		return ""
	}
	parsed, err := url.Parse(uriRaw)
	if err != nil {
		return ""
	}
	if strings.ToLower(parsed.Scheme) != "file" {
		return ""
	}
	if parsed.Path == "" {
		return ""
	}
	return parsed.Path
}

func (s *Server) canReadTextFile() bool {
	s.capabilitiesMu.RLock()
	defer s.capabilitiesMu.RUnlock()
	return s.capabilities.canReadTextFile
}

func (s *Server) readTextFile(ctx context.Context, sessionID string, path string) (string, error) {
	callCtx, cancel := context.WithTimeout(ctx, defaultFSWriteTimeout)
	defer cancel()

	var raw map[string]any
	if err := s.callClient(callCtx, methodFSReadTextFile, map[string]any{
		"sessionId": sessionID,
		"path":      path,
	}, &raw); err != nil {
		return "", err
	}

	for _, key := range []string{"text", "content"} {
		if text := strings.TrimSpace(valueAsString(raw[key])); text != "" {
			return text, nil
		}
	}
	if nested, ok := raw["result"].(map[string]any); ok {
		for _, key := range []string{"text", "content"} {
			if text := strings.TrimSpace(valueAsString(nested[key])); text != "" {
				return text, nil
			}
		}
	}
	return "", fmt.Errorf("empty fs/read_text_file result")
}

func valueAsString(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	default:
		return ""
	}
}

func (s *Server) captureClientCapabilities(paramsRaw json.RawMessage) {
	var payload map[string]any
	if err := decodeParams(paramsRaw, &payload); err != nil {
		return
	}
	enabled := detectReadTextCapability(payload)
	s.capabilitiesMu.Lock()
	s.capabilities.canReadTextFile = enabled
	s.capabilitiesMu.Unlock()
}

func detectReadTextCapability(payload map[string]any) bool {
	if payload == nil {
		return false
	}
	return detectReadTextCapabilityAny(payload)
}

func detectReadTextCapabilityAny(value any) bool {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			normalized := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(key, "-", "_"), ".", "_"))
			if strings.Contains(normalized, "read_text_file") || strings.Contains(normalized, "fs/read_text_file") {
				if boolish(child) {
					return true
				}
			}
			if detectReadTextCapabilityAny(child) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if detectReadTextCapabilityAny(child) {
				return true
			}
		}
	}
	return false
}

func boolish(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		switch strings.ToLower(strings.TrimSpace(typed)) {
		case "1", "true", "yes", "on", "enabled":
			return true
		}
	case float64:
		return typed != 0
	case map[string]any:
		for _, key := range []string{"enabled", "available", "supported"} {
			if boolish(typed[key]) {
				return true
			}
		}
	}
	return false
}

func parseMarkdownTodoItems(content string) []TodoItem {
	matches := todoChecklistPattern.FindAllStringSubmatch(content, -1)
	if len(matches) == 0 {
		return nil
	}
	items := make([]TodoItem, 0, len(matches))
	for _, match := range matches {
		if len(match) < 3 {
			continue
		}
		doneMark := strings.TrimSpace(match[1])
		text := strings.TrimSpace(match[2])
		if text == "" {
			continue
		}
		items = append(items, TodoItem{
			Text: text,
			Done: strings.EqualFold(doneMark, "x"),
		})
	}
	return items
}

func cloneTodoItems(items []TodoItem) []TodoItem {
	if len(items) == 0 {
		return nil
	}
	cp := make([]TodoItem, len(items))
	copy(cp, items)
	return cp
}

func (s *Server) attachSessionTodos(update SessionUpdateParams) SessionUpdateParams {
	if update.SessionID == "" {
		return update
	}
	if len(update.Todo) > 0 {
		s.sessionTodosMu.Lock()
		s.sessionTodos[update.SessionID] = cloneTodoItems(update.Todo)
		s.sessionTodosMu.Unlock()
		return update
	}
	return update
}

func (s *Server) clearTurnTodosOnFailure(sessionID string, stopReason string) {
	if sessionID == "" {
		return
	}
	if stopReason == "end_turn" {
		return
	}
	s.sessionTodosMu.Lock()
	delete(s.sessionTodos, sessionID)
	s.sessionTodosMu.Unlock()
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
	if mode == "" {
		mode = s.lastAuthMode
	}
	s.authMu.Unlock()
	if authenticated {
		return true
	}

	hint, command := authRecoveryHint(mode)
	s.writeInternalError(id, method+" requires authentication", map[string]any{
		"hint":            hint,
		"mode":            mode,
		"nextStepCommand": command,
	})
	return false
}

func (s *Server) markLoggedOut() string {
	s.authMu.Lock()
	previousMode := s.authMode
	if previousMode == "" {
		previousMode = s.lastAuthMode
	}
	if previousMode != "" {
		s.lastAuthMode = previousMode
	}
	s.authLoggedIn = false
	s.authMode = ""
	s.authMu.Unlock()
	return previousMode
}

func authRecoveryHint(mode string) (string, string) {
	switch strings.TrimSpace(strings.ToLower(mode)) {
	case "codex_api_key":
		return "set CODEX_API_KEY then restart the ACP agent process", `export CODEX_API_KEY="YOUR_CODEX_API_KEY" && unset OPENAI_API_KEY`
	case "openai_api_key":
		return "set OPENAI_API_KEY then restart the ACP agent process", `export OPENAI_API_KEY="YOUR_OPENAI_API_KEY" && unset CODEX_API_KEY`
	case "chatgpt_subscription":
		return "run codex login then restart the ACP agent process", "codex login"
	default:
		return "set CODEX_API_KEY or OPENAI_API_KEY, or run codex login; then restart the ACP agent process", `export CODEX_API_KEY="YOUR_CODEX_API_KEY"`
	}
}

func logoutRecoveryInstructions(mode string) string {
	switch strings.TrimSpace(strings.ToLower(mode)) {
	case "codex_api_key":
		return strings.Join([]string{
			"logout completed; re-authentication required.",
			"Next step (copy/paste):",
			`export CODEX_API_KEY="YOUR_CODEX_API_KEY" && unset OPENAI_API_KEY`,
			"Then restart the ACP agent process (or reopen the editor external agent session).",
		}, "\n")
	case "openai_api_key":
		return strings.Join([]string{
			"logout completed; re-authentication required.",
			"Next step (copy/paste):",
			`export OPENAI_API_KEY="YOUR_OPENAI_API_KEY" && unset CODEX_API_KEY`,
			"Then restart the ACP agent process (or reopen the editor external agent session).",
		}, "\n")
	case "chatgpt_subscription":
		return strings.Join([]string{
			"logout completed; re-authentication required.",
			"Next step (copy/paste):",
			"codex login",
			"Complete the browser login/local callback flow, then restart the ACP agent process.",
		}, "\n")
	default:
		return strings.Join([]string{
			"logout completed; re-authentication required.",
			"Choose one recovery path:",
			`1) export CODEX_API_KEY="YOUR_CODEX_API_KEY" && unset OPENAI_API_KEY`,
			`2) export OPENAI_API_KEY="YOUR_OPENAI_API_KEY" && unset CODEX_API_KEY`,
			"3) codex login",
			"After that, restart the ACP agent process.",
		}, "\n")
	}
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
		modeBeforeLogout := s.markLoggedOut()

		logoutCtx, cancel := context.WithTimeout(turnCtx, 2*time.Second)
		defer cancel()
		if err := s.app.Logout(logoutCtx); err != nil {
			s.logger.Warn("app-server logout failed; local auth still cleared", slog.String("error", err.Error()))
		}
		recovery := logoutRecoveryInstructions(modeBeforeLogout)

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
			{
				SessionID: sessionID,
				TurnID:    lifecycle.turnID,
				Type:      "message",
				Phase:     string(lifecycle.phase),
				Delta:     recovery,
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

func (t *turnLifecycle) resetForRetry() {
	t.phase = turnPhaseStarted
	t.messageBuffer.Reset()
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
		t.messageBuffer.WriteString(event.Delta)
		update := SessionUpdateParams{
			SessionID: t.sessionID,
			TurnID:    t.turnID,
			Type:      "message",
			Phase:     string(t.phase),
			ItemID:    event.ItemID,
			Delta:     event.Delta,
		}
		if todos := parseMarkdownTodoItems(t.messageBuffer.String()); len(todos) > 0 {
			update.Todo = todos
		}
		return []SessionUpdateParams{update}, false, ""
	case appserver.TurnEventTypeItemStarted:
		t.phase = turnPhaseStreaming
		return []SessionUpdateParams{
			{
				SessionID: t.sessionID,
				TurnID:    t.turnID,
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
				TurnID:    t.turnID,
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
				TurnID:    t.turnID,
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
				TurnID:    t.turnID,
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
				TurnID:    t.turnID,
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

func textTurnInput(text string) []appserver.UserInput {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	return []appserver.UserInput{
		{
			Type: "text",
			Text: text,
		},
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
