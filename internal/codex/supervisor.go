package codex

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"
)

const defaultInitializeTimeout = 5 * time.Second

var errSupervisorClosed = errors.New("app-server supervisor is closed")

// SupervisorConfig controls app-server lifecycle management.
type SupervisorConfig struct {
	Process           ProcessConfig
	Logger            *slog.Logger
	InitializeTimeout time.Duration
}

// Supervisor keeps app-server available and recovers after child exits.
type Supervisor struct {
	mu     sync.Mutex
	cfg    SupervisorConfig
	client *Client
	closed bool
}

// NewSupervisor creates and initializes one app-server supervisor.
func NewSupervisor(ctx context.Context, cfg SupervisorConfig) (*Supervisor, error) {
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.NewJSONHandler(io.Discard, nil))
	}
	if cfg.InitializeTimeout <= 0 {
		cfg.InitializeTimeout = defaultInitializeTimeout
	}

	s := &Supervisor{cfg: cfg}
	if err := s.restartLocked(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

// Close stops the active app-server client.
func (s *Supervisor) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	client := s.client
	s.client = nil
	s.mu.Unlock()

	if client == nil {
		return nil
	}
	return client.Close()
}

// ThreadStart creates one thread in app-server.
func (s *Supervisor) ThreadStart(ctx context.Context, cwd string, options RunOptions) (string, error) {
	var threadID string
	err := s.call(ctx, methodThreadStart, func(client *Client) error {
		var callErr error
		threadID, callErr = client.ThreadStart(ctx, cwd, options)
		return callErr
	})
	if err != nil {
		return "", err
	}
	return threadID, nil
}

// ThreadList returns one page of app-server thread history.
func (s *Supervisor) ThreadList(ctx context.Context, params ThreadListParams) (ThreadListResult, error) {
	var result ThreadListResult
	err := s.call(ctx, methodThreadList, func(client *Client) error {
		var callErr error
		result, callErr = client.ThreadsList(ctx, params)
		return callErr
	})
	if err != nil {
		return ThreadListResult{}, err
	}
	return result, nil
}

// TurnStart starts one prompt turn and returns event stream.
func (s *Supervisor) TurnStart(
	ctx context.Context,
	threadID string,
	input []UserInput,
	options RunOptions,
) (string, <-chan TurnEvent, error) {
	var turnID string
	var events <-chan TurnEvent
	err := s.call(ctx, methodTurnStart, func(client *Client) error {
		var callErr error
		turnID, events, callErr = client.TurnStart(ctx, threadID, input, options)
		return callErr
	})
	if err != nil {
		return "", nil, err
	}
	return turnID, events, nil
}

// ReviewStart starts one review workflow turn and returns event stream.
func (s *Supervisor) ReviewStart(
	ctx context.Context,
	threadID string,
	instructions string,
	options RunOptions,
) (string, <-chan TurnEvent, error) {
	var turnID string
	var events <-chan TurnEvent
	err := s.call(ctx, methodReviewStart, func(client *Client) error {
		var callErr error
		turnID, events, callErr = client.ReviewStart(ctx, threadID, instructions, options)
		return callErr
	})
	if err != nil {
		return "", nil, err
	}
	return turnID, events, nil
}

// CompactStart starts one compact turn and returns event stream.
func (s *Supervisor) CompactStart(ctx context.Context, threadID string) (string, <-chan TurnEvent, error) {
	var turnID string
	var events <-chan TurnEvent
	err := s.call(ctx, methodThreadCompact, func(client *Client) error {
		var callErr error
		turnID, events, callErr = client.CompactStart(ctx, threadID)
		return callErr
	})
	if err != nil {
		return "", nil, err
	}
	return turnID, events, nil
}

// TurnInterrupt interrupts one running turn.
func (s *Supervisor) TurnInterrupt(ctx context.Context, threadID, turnID string) error {
	return s.call(ctx, methodTurnInterrupt, func(client *Client) error {
		return client.TurnInterrupt(ctx, threadID, turnID)
	})
}

// ModelsList fetches selectable models from app-server.
func (s *Supervisor) ModelsList(ctx context.Context) ([]ModelOption, error) {
	var models []ModelOption
	err := s.call(ctx, methodModelList, func(client *Client) error {
		var callErr error
		models, callErr = client.ModelsList(ctx)
		return callErr
	})
	if err != nil {
		return nil, err
	}
	return models, nil
}

// ApprovalRespond forwards one user decision back to app-server.
func (s *Supervisor) ApprovalRespond(ctx context.Context, approvalID string, decision ApprovalDecision) error {
	return s.call(ctx, "approval/respond", func(client *Client) error {
		return client.ApprovalRespond(ctx, approvalID, decision)
	})
}

// MCPServersList lists available MCP servers.
func (s *Supervisor) MCPServersList(ctx context.Context) ([]MCPServer, error) {
	var servers []MCPServer
	err := s.call(ctx, methodMCPServerList, func(client *Client) error {
		var callErr error
		servers, callErr = client.MCPServersList(ctx)
		return callErr
	})
	if err != nil {
		return nil, err
	}
	return servers, nil
}

// MCPToolCall invokes one MCP tool.
func (s *Supervisor) MCPToolCall(ctx context.Context, params MCPToolCallParams) (MCPToolCallResult, error) {
	var result MCPToolCallResult
	err := s.call(ctx, methodMCPServerCall, func(client *Client) error {
		var callErr error
		result, callErr = client.MCPToolCall(ctx, params)
		return callErr
	})
	if err != nil {
		return MCPToolCallResult{}, err
	}
	return result, nil
}

// MCPOAuthLogin starts MCP OAuth flow.
func (s *Supervisor) MCPOAuthLogin(ctx context.Context, server string) (MCPOAuthLoginResult, error) {
	var result MCPOAuthLoginResult
	err := s.call(ctx, methodMCPOAuthLogin, func(client *Client) error {
		var callErr error
		result, callErr = client.MCPOAuthLogin(ctx, server)
		return callErr
	})
	if err != nil {
		return MCPOAuthLoginResult{}, err
	}
	return result, nil
}

// Logout clears auth state in app-server when supported.
func (s *Supervisor) Logout(ctx context.Context) error {
	return s.call(ctx, methodAuthLogout, func(client *Client) error {
		return client.Logout(ctx)
	})
}

func (s *Supervisor) call(ctx context.Context, method string, fn func(*Client) error) error {
	client, err := s.ensureClient(ctx)
	if err != nil {
		return fmt.Errorf("%s: %w", method, err)
	}

	if err := fn(client); err != nil {
		if !isRecoverableClientError(err) || ctx.Err() != nil {
			return fmt.Errorf("%s: %w", method, err)
		}

		s.cfg.Logger.Warn(
			"codex app-server call failed; restarting",
			slog.String("method", method),
			slog.String("error", err.Error()),
		)
		restartErr := s.restart(ctx)
		if restartErr != nil {
			return fmt.Errorf("%s: codex app-server unavailable (%w); restart failed: %v", method, err, restartErr)
		}
		return fmt.Errorf("%s: codex app-server unavailable (%w); app-server restarted, retry request", method, err)
	}
	return nil
}

func (s *Supervisor) ensureClient(ctx context.Context) (*Client, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return nil, errSupervisorClosed
	}

	if s.client != nil {
		exited, _ := s.client.process.HasExited()
		if !exited {
			return s.client, nil
		}
		_ = s.client.Close()
		s.client = nil
	}

	if err := s.restartLocked(ctx); err != nil {
		return nil, err
	}
	return s.client, nil
}

func (s *Supervisor) restart(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.restartLocked(ctx)
}

func (s *Supervisor) restartLocked(ctx context.Context) error {
	if s.closed {
		return errSupervisorClosed
	}

	if s.client != nil {
		_ = s.client.Close()
		s.client = nil
	}

	process, err := StartProcess(ctx, s.cfg.Process)
	if err != nil {
		return fmt.Errorf("start codex app-server: %w", err)
	}

	client := NewClient(process, s.cfg.Logger)

	initCtx, cancel := context.WithTimeout(ctx, s.cfg.InitializeTimeout)
	defer cancel()

	if err := client.Initialize(initCtx); err != nil {
		_ = client.Close()
		return fmt.Errorf("initialize codex app-server: %w", err)
	}
	if err := client.Initialized(); err != nil {
		_ = client.Close()
		return fmt.Errorf("send initialized to codex app-server: %w", err)
	}

	s.client = client
	return nil
}

func isRecoverableClientError(err error) bool {
	if errors.Is(err, errClientClosed) {
		return true
	}

	lower := strings.ToLower(err.Error())
	recoverableTokens := []string{
		"app-server read loop",
		"broken pipe",
		"connection reset",
		"eof",
		"client is closed",
	}
	for _, token := range recoverableTokens {
		if strings.Contains(lower, token) {
			return true
		}
	}
	return false
}
