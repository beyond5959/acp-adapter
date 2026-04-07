package pi

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/beyond5959/acp-adapter/internal/codex"
)

const (
	runKindPrompt  = "prompt"
	runKindReview  = "review"
	runKindCompact = "compact"

	defaultImageReadLimit = 4 * 1024 * 1024
)

type activeRun struct {
	mu           sync.Mutex
	turnID       string
	kind         string
	reviewMode   bool
	events       chan codex.TurnEvent
	closed       bool
	started      bool
	reviewOpened bool
	toolText     map[string]string
	toolCommands map[string]string
}

func newActiveRun(turnID string, kind string, reviewMode bool) *activeRun {
	return &activeRun{
		turnID:       turnID,
		kind:         kind,
		reviewMode:   reviewMode,
		events:       make(chan codex.TurnEvent, 256),
		toolText:     make(map[string]string),
		toolCommands: make(map[string]string),
	}
}

func (r *activeRun) send(event codex.TurnEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	r.events <- event
}

func (r *activeRun) ensureStarted(threadID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.started {
		return
	}
	r.started = true
	r.events <- codex.TurnEvent{
		Type:     codex.TurnEventTypeStarted,
		ThreadID: threadID,
		TurnID:   r.turnID,
	}
	if r.reviewMode && !r.reviewOpened {
		r.reviewOpened = true
		r.events <- codex.TurnEvent{
			Type:     codex.TurnEventTypeReviewModeEntered,
			ThreadID: threadID,
			TurnID:   r.turnID,
		}
	}
}

func (r *activeRun) reviewExit(threadID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || !r.reviewOpened {
		return
	}
	r.reviewOpened = false
	r.events <- codex.TurnEvent{
		Type:     codex.TurnEventTypeReviewModeExited,
		ThreadID: threadID,
		TurnID:   r.turnID,
	}
}

func (r *activeRun) toolDelta(toolCallID string, aggregated string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	prev := r.toolText[toolCallID]
	r.toolText[toolCallID] = aggregated
	if aggregated == "" {
		return ""
	}
	if prev == "" {
		return aggregated
	}
	if strings.HasPrefix(aggregated, prev) {
		return aggregated[len(prev):]
	}
	return aggregated
}

func (r *activeRun) rememberToolCommand(toolCallID string, command string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	toolCallID = strings.TrimSpace(toolCallID)
	command = strings.TrimSpace(command)
	if toolCallID == "" || command == "" {
		return
	}
	r.toolCommands[toolCallID] = command
}

func (r *activeRun) toolCommand(toolCallID string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.TrimSpace(r.toolCommands[toolCallID])
}

func (r *activeRun) close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	close(r.events)
	r.closed = true
}

type rpcSession struct {
	client   *Client
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	threadID string
	cwd      string

	writeMu  sync.Mutex
	mu       sync.Mutex
	pending  map[string]chan rpcEnvelope
	active   *activeRun
	closed   bool
	closeCh  chan struct{}
	closeErr error

	nextID atomic.Uint64
}

func (s *rpcSession) nextRequestID() string {
	return fmt.Sprintf("pi-rpc-%d", s.nextID.Add(1))
}

func (s *rpcSession) currentRun() *activeRun {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active
}

func (s *rpcSession) beginRun(turnID string, kind string, reviewMode bool) (<-chan codex.TurnEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, fmt.Errorf("pi rpc session closed")
	}
	if s.active != nil {
		return nil, fmt.Errorf("pi rpc session already has an active run")
	}
	run := newActiveRun(turnID, kind, reviewMode)
	s.active = run
	return run.events, nil
}

func (s *rpcSession) finishRun(run *activeRun) {
	s.mu.Lock()
	if s.active == run {
		s.active = nil
	}
	s.mu.Unlock()
	run.close()
}

func (s *rpcSession) registerPending(id string) (<-chan rpcEnvelope, error) {
	ch := make(chan rpcEnvelope, 1)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, fmt.Errorf("pi rpc session closed")
	}
	s.pending[id] = ch
	return ch, nil
}

func (s *rpcSession) removePending(id string) {
	s.mu.Lock()
	ch, ok := s.pending[id]
	if ok {
		delete(s.pending, id)
	}
	s.mu.Unlock()
	if ok {
		close(ch)
	}
}

func (s *rpcSession) resolvePending(id string, msg rpcEnvelope) {
	s.mu.Lock()
	ch, ok := s.pending[id]
	if ok {
		delete(s.pending, id)
	}
	s.mu.Unlock()
	if ok {
		ch <- msg
		close(ch)
	}
}

func (s *rpcSession) failPending(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	s.closeErr = err
	close(s.closeCh)
	for id, ch := range s.pending {
		delete(s.pending, id)
		close(ch)
	}
}

func (s *rpcSession) request(ctx context.Context, payload map[string]any, out any) error {
	id := s.nextRequestID()
	payload["id"] = id

	waitCh, err := s.registerPending(id)
	if err != nil {
		return err
	}

	if err := s.writeJSON(payload); err != nil {
		s.removePending(id)
		return err
	}

	select {
	case msg, ok := <-waitCh:
		if !ok {
			s.mu.Lock()
			closeErr := s.closeErr
			s.mu.Unlock()
			if closeErr != nil {
				return closeErr
			}
			return fmt.Errorf("pi rpc request %s closed without response", payload["type"])
		}
		if msg.Success != nil && !*msg.Success {
			command := strings.TrimSpace(msg.Command)
			if command == "" {
				command = fmt.Sprint(payload["type"])
			}
			return fmt.Errorf("%s: %s", command, strings.TrimSpace(msg.Error))
		}
		if out != nil && len(msg.Data) > 0 {
			if err := json.Unmarshal(msg.Data, out); err != nil {
				return fmt.Errorf("decode %v response: %w", payload["type"], err)
			}
		}
		return nil
	case <-ctx.Done():
		s.removePending(id)
		return ctx.Err()
	case <-s.closeCh:
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.closeErr != nil {
			return s.closeErr
		}
		return fmt.Errorf("pi rpc session closed")
	}
}

func (s *rpcSession) writeJSON(payload any) error {
	line, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if s.client.cfg.Trace != nil {
		s.client.cfg.Trace("send", line)
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.stdin.Write(append(line, '\n')); err != nil {
		return err
	}
	return nil
}

func (s *rpcSession) readLoop(stdout io.Reader) {
	reader := bufio.NewReader(stdout)
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			if errors.Is(err, io.EOF) {
				s.onReadLoopError(fmt.Errorf("pi rpc read loop: EOF"))
				return
			}
			s.onReadLoopError(fmt.Errorf("pi rpc read loop: %w", err))
			return
		}

		line = bytesTrimSpace(line)
		if len(line) == 0 {
			continue
		}
		if s.client.cfg.Trace != nil {
			s.client.cfg.Trace("recv", line)
		}

		var envelope rpcEnvelope
		if err := json.Unmarshal(line, &envelope); err != nil {
			continue
		}

		if envelope.Type == "response" && strings.TrimSpace(envelope.ID) != "" {
			s.resolvePending(strings.TrimSpace(envelope.ID), envelope)
			continue
		}

		s.handleEvent(line, envelope.Type)
	}
}

func (s *rpcSession) onReadLoopError(err error) {
	run := s.currentRun()
	if run != nil {
		run.send(codex.TurnEvent{
			Type:     codex.TurnEventTypeError,
			ThreadID: s.threadID,
			TurnID:   run.turnID,
			Message:  err.Error(),
		})
	}
	s.failPending(err)
	if run != nil {
		s.finishRun(run)
	}
}

func (s *rpcSession) handleEvent(line []byte, eventType string) {
	switch eventType {
	case "agent_start", "turn_start":
		if run := s.currentRun(); run != nil {
			run.ensureStarted(s.threadID)
		}
	case "message_update":
		s.handleMessageUpdate(line)
	case "tool_execution_start":
		s.handleToolExecutionStart(line)
	case "tool_execution_update":
		s.handleToolExecutionUpdate(line)
	case "tool_execution_end":
		s.handleToolExecutionEnd(line)
	case "agent_end":
		s.handleAgentEnd(line)
	case "extension_ui_request":
		s.handleExtensionUIRequest(line)
	case "auto_retry_start":
		s.handleAutoRetryStart(line)
	case "auto_retry_end":
		s.handleAutoRetryEnd(line)
	case "extension_error":
		s.handleExtensionError(line)
	default:
	}
}

func (s *rpcSession) handleMessageUpdate(line []byte) {
	run := s.currentRun()
	if run == nil {
		return
	}
	run.ensureStarted(s.threadID)

	var event rpcMessageUpdateEvent
	if err := json.Unmarshal(line, &event); err != nil {
		return
	}
	if event.AssistantMessageEvent == nil {
		return
	}

	itemID := run.turnID + "-msg"
	switch event.AssistantMessageEvent.Type {
	case "text_delta":
		if delta := strings.TrimSpace(event.AssistantMessageEvent.Delta); delta != "" {
			run.send(codex.TurnEvent{
				Type:     codex.TurnEventTypeAgentMessageDelta,
				ThreadID: s.threadID,
				TurnID:   run.turnID,
				ItemID:   itemID,
				ItemType: "agent_message",
				Delta:    event.AssistantMessageEvent.Delta,
			})
		}
	case "thinking_delta":
		if strings.TrimSpace(event.AssistantMessageEvent.Delta) != "" {
			run.send(codex.TurnEvent{
				Type:     codex.TurnEventTypeReasoningDelta,
				ThreadID: s.threadID,
				TurnID:   run.turnID,
				ItemID:   itemID,
				ItemType: "reasoning",
				Delta:    event.AssistantMessageEvent.Delta,
			})
		}
	}
}

func (s *rpcSession) handleToolExecutionStart(line []byte) {
	run := s.currentRun()
	if run == nil {
		return
	}
	run.ensureStarted(s.threadID)

	var event rpcToolExecutionStartEvent
	if err := json.Unmarshal(line, &event); err != nil {
		return
	}

	if strings.EqualFold(strings.TrimSpace(event.ToolName), "bash") {
		command := jsonStringField(event.Args, "command")
		run.rememberToolCommand(event.ToolCallID, command)
		run.send(codex.TurnEvent{
			Type:     codex.TurnEventTypeItemStarted,
			ThreadID: s.threadID,
			TurnID:   run.turnID,
			ItemID:   event.ToolCallID,
			ItemType: "commandExecution",
			Command: &codex.CommandExecution{
				ID:      event.ToolCallID,
				Command: command,
				Status:  "inProgress",
			},
		})
		return
	}

	toolName := strings.TrimSpace(event.ToolName)
	success := true
	run.send(codex.TurnEvent{
		Type:     codex.TurnEventTypeItemStarted,
		ThreadID: s.threadID,
		TurnID:   run.turnID,
		ItemID:   event.ToolCallID,
		ItemType: toolName,
		Tool: &codex.ToolExecution{
			ID:      event.ToolCallID,
			Kind:    toolName,
			Tool:    toolName,
			Status:  "inProgress",
			Success: &success,
		},
	})
}

func (s *rpcSession) handleToolExecutionUpdate(line []byte) {
	run := s.currentRun()
	if run == nil {
		return
	}

	var event rpcToolExecutionUpdateEvent
	if err := json.Unmarshal(line, &event); err != nil {
		return
	}

	if !strings.EqualFold(strings.TrimSpace(event.ToolName), "bash") || event.PartialResult == nil {
		return
	}
	text := textFromRPCBlocks(event.PartialResult.Content)
	delta := run.toolDelta(event.ToolCallID, text)
	if strings.TrimSpace(delta) == "" {
		return
	}
	run.send(codex.TurnEvent{
		Type:     codex.TurnEventTypeCommandExecutionDelta,
		ThreadID: s.threadID,
		TurnID:   run.turnID,
		ItemID:   event.ToolCallID,
		ItemType: "commandExecution",
		Delta:    delta,
	})
}

func (s *rpcSession) handleToolExecutionEnd(line []byte) {
	run := s.currentRun()
	if run == nil {
		return
	}

	var event rpcToolExecutionEndEvent
	if err := json.Unmarshal(line, &event); err != nil {
		return
	}

	toolName := strings.TrimSpace(event.ToolName)
	if strings.EqualFold(toolName, "bash") {
		content := ""
		if event.Result != nil {
			content = textFromRPCBlocks(event.Result.Content)
		}
		status := "completed"
		if event.IsError {
			status = "failed"
		}
		run.send(codex.TurnEvent{
			Type:     codex.TurnEventTypeItemCompleted,
			ThreadID: s.threadID,
			TurnID:   run.turnID,
			ItemID:   event.ToolCallID,
			ItemType: "commandExecution",
			Command: &codex.CommandExecution{
				ID:               event.ToolCallID,
				Command:          run.toolCommand(event.ToolCallID),
				AggregatedOutput: content,
				Status:           status,
			},
		})
		return
	}

	success := !event.IsError
	tool := &codex.ToolExecution{
		ID:           event.ToolCallID,
		Kind:         toolName,
		Tool:         toolName,
		Status:       ternaryString(event.IsError, "failed", "completed"),
		Success:      &success,
		ContentItems: toolOutputItems(event.Result),
	}
	run.send(codex.TurnEvent{
		Type:     codex.TurnEventTypeItemCompleted,
		ThreadID: s.threadID,
		TurnID:   run.turnID,
		ItemID:   event.ToolCallID,
		ItemType: toolName,
		Tool:     tool,
	})
}

func (s *rpcSession) handleAgentEnd(line []byte) {
	run := s.currentRun()
	if run == nil {
		return
	}

	var event rpcAgentEndEvent
	if err := json.Unmarshal(line, &event); err != nil {
		return
	}

	stopReason, message := stopReasonFromMessages(event.Messages)
	go s.completeRun(run, stopReason, message)
}

func (s *rpcSession) completeRun(run *activeRun, stopReason string, message string) {
	tokenUsageCh := make(chan *codex.ThreadTokenUsage, 1)
	go func() {
		statsCtx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()
		stats := s.fetchSessionStats(statsCtx)
		tokenUsageCh <- tokenUsageFromStats(stats)
	}()

	select {
	case tokenUsage := <-tokenUsageCh:
		if tokenUsage != nil {
			run.send(codex.TurnEvent{
				Type:       codex.TurnEventTypeTokenUsageUpdated,
				ThreadID:   s.threadID,
				TurnID:     run.turnID,
				TokenUsage: tokenUsage,
			})
		}
	case <-time.After(100 * time.Millisecond):
	}

	if run.reviewMode {
		run.reviewExit(s.threadID)
	}
	run.send(codex.TurnEvent{
		Type:       codex.TurnEventTypeCompleted,
		ThreadID:   s.threadID,
		TurnID:     run.turnID,
		StopReason: stopReason,
		Message:    message,
	})
	s.finishRun(run)
}

func (s *rpcSession) handleExtensionUIRequest(line []byte) {
	var request rpcExtensionUIRequest
	if err := json.Unmarshal(line, &request); err != nil {
		return
	}

	switch request.Method {
	case "notify", "setStatus", "setWidget", "setTitle", "set_editor_text":
		return
	}

	run := s.currentRun()
	if run == nil {
		_ = s.respondExtensionUI(request.ID, nil, true)
		return
	}

	if strings.TrimSpace(request.Title) == gateExtensionTitle {
		var payload gateRequestPayload
		if err := json.Unmarshal([]byte(request.Message), &payload); err == nil && payload.Gate == "acp-adapter" {
			approval := approvalFromGatePayload(payload)
			approval.ApprovalID = request.ID
			s.client.registerApproval(request.ID, s)
			run.send(codex.TurnEvent{
				Type:     codex.TurnEventTypeApprovalRequired,
				ThreadID: s.threadID,
				TurnID:   run.turnID,
				Approval: approval,
			})
			return
		}
	}

	_ = s.respondExtensionUI(request.ID, nil, true)
	run.send(codex.TurnEvent{
		Type:      codex.TurnEventTypeBackendError,
		ThreadID:  s.threadID,
		TurnID:    run.turnID,
		Message:   "pi extension_ui_request is not bridged by ACP adapter; request auto-cancelled",
		WillRetry: false,
	})
}

func (s *rpcSession) handleAutoRetryStart(line []byte) {
	run := s.currentRun()
	if run == nil {
		return
	}
	var event rpcAutoRetryStartEvent
	if err := json.Unmarshal(line, &event); err != nil {
		return
	}
	run.send(codex.TurnEvent{
		Type:      codex.TurnEventTypeBackendError,
		ThreadID:  s.threadID,
		TurnID:    run.turnID,
		Message:   strings.TrimSpace(event.ErrorMessage),
		WillRetry: true,
	})
}

func (s *rpcSession) handleAutoRetryEnd(line []byte) {
	run := s.currentRun()
	if run == nil {
		return
	}
	var event rpcAutoRetryEndEvent
	if err := json.Unmarshal(line, &event); err != nil {
		return
	}
	if event.Success {
		return
	}
	run.send(codex.TurnEvent{
		Type:      codex.TurnEventTypeBackendError,
		ThreadID:  s.threadID,
		TurnID:    run.turnID,
		Message:   strings.TrimSpace(event.FinalError),
		WillRetry: false,
	})
}

func (s *rpcSession) handleExtensionError(line []byte) {
	run := s.currentRun()
	if run == nil {
		return
	}
	var payload struct {
		Type          string `json:"type"`
		ExtensionPath string `json:"extensionPath,omitempty"`
		Event         string `json:"event,omitempty"`
		Error         string `json:"error,omitempty"`
	}
	if err := json.Unmarshal(line, &payload); err != nil {
		return
	}
	run.send(codex.TurnEvent{
		Type:      codex.TurnEventTypeBackendError,
		ThreadID:  s.threadID,
		TurnID:    run.turnID,
		Message:   strings.TrimSpace(payload.Error),
		WillRetry: false,
	})
}

func (s *rpcSession) respondExtensionUI(requestID string, confirmed *bool, cancelled bool) error {
	payload := rpcExtensionUIResponse{
		Type:      "extension_ui_response",
		ID:        requestID,
		Confirmed: confirmed,
		Cancelled: cancelled,
	}
	return s.writeJSON(payload)
}

func (s *rpcSession) close() {
	_ = s.stdin.Close()
	if s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}
}

func (s *rpcSession) getState(ctx context.Context) (rpcState, error) {
	var state rpcState
	err := s.request(ctx, map[string]any{"type": "get_state"}, &state)
	return state, err
}

func (s *rpcSession) getMessages(ctx context.Context) ([]rpcAgentMessage, error) {
	var data rpcGetMessagesResult
	if err := s.request(ctx, map[string]any{"type": "get_messages"}, &data); err != nil {
		return nil, err
	}
	return data.Messages, nil
}

func (s *rpcSession) getCommands(ctx context.Context) ([]rpcSlashCommand, error) {
	var data rpcGetCommandsResult
	if err := s.request(ctx, map[string]any{"type": "get_commands"}, &data); err != nil {
		return nil, err
	}
	return data.Commands, nil
}

func (s *rpcSession) getAvailableModels(ctx context.Context) ([]rpcModel, error) {
	var data struct {
		Models []rpcModel `json:"models,omitempty"`
	}
	if err := s.request(ctx, map[string]any{"type": "get_available_models"}, &data); err != nil {
		return nil, err
	}
	return data.Models, nil
}

func (s *rpcSession) newSession(ctx context.Context) error {
	var result rpcCommandResult
	if err := s.request(ctx, map[string]any{"type": "new_session"}, &result); err != nil {
		return err
	}
	if result.Cancelled {
		return fmt.Errorf("pi rpc new_session was cancelled")
	}
	return nil
}

func (s *rpcSession) switchSession(ctx context.Context, sessionPath string) error {
	var result rpcCommandResult
	if err := s.request(ctx, map[string]any{
		"type":        "switch_session",
		"sessionPath": sessionPath,
	}, &result); err != nil {
		return err
	}
	if result.Cancelled {
		return fmt.Errorf("pi rpc switch_session was cancelled")
	}
	return nil
}

func (s *rpcSession) setModel(ctx context.Context, provider, modelID string) error {
	return s.request(ctx, map[string]any{
		"type":     "set_model",
		"provider": provider,
		"modelId":  modelID,
	}, nil)
}

func (s *rpcSession) setThinkingLevel(ctx context.Context, level string) error {
	return s.request(ctx, map[string]any{
		"type":  "set_thinking_level",
		"level": level,
	}, nil)
}

func (s *rpcSession) fetchSessionStats(ctx context.Context) *rpcSessionStats {
	var stats rpcSessionStats
	if err := s.request(ctx, map[string]any{"type": "get_session_stats"}, &stats); err != nil {
		return nil
	}
	return &stats
}

func (s *rpcSession) prompt(ctx context.Context, message string, images []rpcImageContent) error {
	payload := map[string]any{
		"type":    "prompt",
		"message": message,
	}
	if len(images) > 0 {
		payload["images"] = images
	}
	return s.request(ctx, payload, nil)
}

func (s *rpcSession) abort(ctx context.Context) error {
	return s.request(ctx, map[string]any{"type": "abort"}, nil)
}

func (s *rpcSession) compact(ctx context.Context) (rpcCompactionResult, error) {
	var result rpcCompactionResult
	err := s.request(ctx, map[string]any{"type": "compact"}, &result)
	return result, err
}

// Client implements the shared appClient interface on top of Pi RPC mode.
type Client struct {
	cfg Config

	loggedOut atomic.Bool
	nextTurn  atomic.Uint64

	mu        sync.Mutex
	sessions  map[string]*rpcSession
	approvals map[string]*rpcSession

	modelsMu    sync.Mutex
	modelsCache []codex.ModelOption
}

// NewClient creates a Pi RPC client.
func NewClient(cfg Config) *Client {
	cfg = cfg.Normalize()
	return &Client{
		cfg:       cfg,
		sessions:  make(map[string]*rpcSession),
		approvals: make(map[string]*rpcSession),
	}
}

func (c *Client) genTurnID(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, c.nextTurn.Add(1))
}

func (c *Client) registerSession(threadID string, sess *rpcSession) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sessions[threadID] = sess
}

func (c *Client) lookupSession(threadID string) (*rpcSession, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	sess, ok := c.sessions[threadID]
	return sess, ok
}

func (c *Client) removeSession(threadID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.sessions, threadID)
}

func (c *Client) registerApproval(approvalID string, sess *rpcSession) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.approvals[approvalID] = sess
}

func (c *Client) takeApproval(approvalID string) (*rpcSession, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	sess, ok := c.approvals[approvalID]
	if ok {
		delete(c.approvals, approvalID)
	}
	return sess, ok
}

func (c *Client) sessionArgs() ([]string, error) {
	args := []string{"--mode", "rpc"}
	if strings.TrimSpace(c.cfg.SessionDir) != "" {
		args = append(args, "--session-dir", c.cfg.SessionDir)
	}
	if strings.TrimSpace(c.cfg.DefaultProvider) != "" {
		args = append(args, "--provider", c.cfg.DefaultProvider)
	}
	if strings.TrimSpace(c.cfg.DefaultModel) != "" {
		args = append(args, "--model", c.cfg.DefaultModel)
	}
	if c.cfg.EnableGate {
		gatePath, err := ensureGateExtensionFile()
		if err != nil {
			return nil, err
		}
		args = append(args, "--extension", gatePath)
	}
	args = append(args, c.cfg.ExtraArgs...)
	return args, nil
}

func (c *Client) startSessionProcess(cwd string) (*rpcSession, error) {
	args, err := c.sessionArgs()
	if err != nil {
		return nil, err
	}

	cmd := exec.Command(c.cfg.PiBin, args...)
	if strings.TrimSpace(cwd) != "" {
		cmd.Dir = cwd
	} else if strings.TrimSpace(c.cfg.WorkDir) != "" {
		cmd.Dir = c.cfg.WorkDir
	}
	cmd.Stderr = c.cfg.Stderr

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("pi rpc stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("pi rpc stdout: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start pi rpc process: %w", err)
	}

	sess := &rpcSession{
		client:  c,
		cmd:     cmd,
		stdin:   stdin,
		cwd:     cwd,
		pending: make(map[string]chan rpcEnvelope),
		closeCh: make(chan struct{}),
	}
	go sess.readLoop(stdout)
	return sess, nil
}

// ThreadStart starts a new persisted Pi session and returns its session file path.
func (c *Client) ThreadStart(ctx context.Context, cwd string, options codex.RunOptions) (string, error) {
	if c.loggedOut.Load() {
		return "", fmt.Errorf("pi rpc: logged out")
	}
	sess, err := c.startSessionProcess(cwd)
	if err != nil {
		return "", err
	}
	if err := sess.newSession(ctx); err != nil {
		sess.close()
		return "", err
	}
	if err := c.applyRunOptions(ctx, sess, options); err != nil {
		sess.close()
		return "", err
	}
	state, err := sess.getState(ctx)
	if err != nil {
		sess.close()
		return "", err
	}
	threadID := normalizeThreadID(state.SessionFile, state.SessionID)
	if threadID == "" {
		sess.close()
		return "", fmt.Errorf("pi rpc returned empty session identifier")
	}
	sess.threadID = threadID
	sess.cwd = chooseFirstNonEmpty(strings.TrimSpace(cwd), extractCWDFromSessionPath(state.SessionFile), sess.cwd)
	c.registerSession(threadID, sess)
	return threadID, nil
}

// ThreadList lists Pi sessions from disk.
func (c *Client) ThreadList(_ context.Context, params codex.ThreadListParams) (codex.ThreadListResult, error) {
	if params.Archived != nil && *params.Archived {
		return codex.ThreadListResult{Data: []codex.Thread{}}, nil
	}
	sessions, err := c.listDiskSessions(strings.TrimSpace(params.CWD))
	if err != nil {
		return codex.ThreadListResult{}, err
	}

	offset, err := parseOffsetCursor(params.Cursor)
	if err != nil {
		return codex.ThreadListResult{}, err
	}
	limit := 100
	if params.Limit != nil && *params.Limit > 0 {
		limit = int(*params.Limit)
	}
	if offset > len(sessions) {
		offset = len(sessions)
	}
	end := offset + limit
	if end > len(sessions) {
		end = len(sessions)
	}

	items := make([]codex.Thread, 0, end-offset)
	for _, session := range sessions[offset:end] {
		items = append(items, codex.Thread{
			ID:        session.Path,
			CWD:       session.CWD,
			Name:      session.Name,
			Preview:   session.FirstMessage,
			Path:      session.Path,
			CreatedAt: session.CreatedAt.Unix(),
			UpdatedAt: session.UpdatedAt.Unix(),
		})
	}
	nextCursor := ""
	if end < len(sessions) {
		nextCursor = strconv.Itoa(end)
	}
	return codex.ThreadListResult{
		Data:       items,
		NextCursor: nextCursor,
	}, nil
}

// ThreadResume resumes a known Pi session.
func (c *Client) ThreadResume(
	ctx context.Context,
	threadID string,
	cwd string,
	options codex.RunOptions,
) (codex.ThreadResumeResult, error) {
	if c.loggedOut.Load() {
		return codex.ThreadResumeResult{}, fmt.Errorf("pi rpc: logged out")
	}
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return codex.ThreadResumeResult{}, fmt.Errorf("pi rpc: thread id is required")
	}

	sess, ok := c.lookupSession(threadID)
	if !ok {
		var err error
		sess, err = c.startSessionProcess(cwd)
		if err != nil {
			return codex.ThreadResumeResult{}, err
		}
		if err := sess.switchSession(ctx, threadID); err != nil {
			sess.close()
			return codex.ThreadResumeResult{}, err
		}
		sess.threadID = threadID
		c.registerSession(threadID, sess)
	}

	if err := c.applyRunOptions(ctx, sess, options); err != nil {
		return codex.ThreadResumeResult{}, err
	}
	state, err := sess.getState(ctx)
	if err != nil {
		return codex.ThreadResumeResult{}, err
	}
	messages, err := sess.getMessages(ctx)
	if err != nil {
		return codex.ThreadResumeResult{}, err
	}
	sessionCWD := chooseFirstNonEmpty(
		strings.TrimSpace(cwd),
		extractCWDFromSessionPath(state.SessionFile),
		extractCWDFromSessionPath(threadID),
		sess.cwd,
	)
	sess.cwd = sessionCWD
	return codex.ThreadResumeResult{
		CWD:             sessionCWD,
		Model:           modelValue(state.Model),
		ReasoningEffort: strings.TrimSpace(state.ThinkingLevel),
		Thread:          historyThreadFromMessages(threadID, sessionCWD, messages),
	}, nil
}

func (c *Client) ensureSession(ctx context.Context, threadID string) (*rpcSession, error) {
	if c.loggedOut.Load() {
		return nil, fmt.Errorf("pi rpc: logged out")
	}
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return nil, fmt.Errorf("pi rpc: thread id is required")
	}
	if sess, ok := c.lookupSession(threadID); ok {
		return sess, nil
	}

	cwd := extractCWDFromSessionPath(threadID)
	sess, err := c.startSessionProcess(cwd)
	if err != nil {
		return nil, err
	}
	if err := sess.switchSession(ctx, threadID); err != nil {
		sess.close()
		return nil, err
	}
	sess.threadID = threadID
	sess.cwd = chooseFirstNonEmpty(cwd, sess.cwd)
	c.registerSession(threadID, sess)
	return sess, nil
}

// TurnStart starts a prompt run on one Pi session.
func (c *Client) TurnStart(
	ctx context.Context,
	threadID string,
	input []codex.UserInput,
	options codex.RunOptions,
) (string, <-chan codex.TurnEvent, error) {
	sess, err := c.ensureSession(ctx, threadID)
	if err != nil {
		return "", nil, err
	}
	if err := c.applyRunOptions(ctx, sess, options); err != nil {
		return "", nil, err
	}
	message, images, err := promptFromInputs(input, options)
	if err != nil {
		return "", nil, err
	}
	turnID := c.genTurnID("turn")
	events, err := sess.beginRun(turnID, runKindPrompt, false)
	if err != nil {
		return "", nil, err
	}
	if err := sess.prompt(ctx, message, images); err != nil {
		run := sess.currentRun()
		if run != nil {
			sess.finishRun(run)
		}
		return "", nil, err
	}
	return turnID, events, nil
}

// ReviewStart simulates review mode using a normal Pi prompt plus synthetic review lifecycle events.
func (c *Client) ReviewStart(
	ctx context.Context,
	threadID string,
	instructions string,
	options codex.RunOptions,
) (string, <-chan codex.TurnEvent, error) {
	input := []codex.UserInput{{
		Type: "text",
		Text: reviewPrompt(instructions),
	}}
	sess, err := c.ensureSession(ctx, threadID)
	if err != nil {
		return "", nil, err
	}
	if err := c.applyRunOptions(ctx, sess, options); err != nil {
		return "", nil, err
	}
	message, images, err := promptFromInputs(input, options)
	if err != nil {
		return "", nil, err
	}
	turnID := c.genTurnID("review")
	events, err := sess.beginRun(turnID, runKindReview, true)
	if err != nil {
		return "", nil, err
	}
	if err := sess.prompt(ctx, message, images); err != nil {
		run := sess.currentRun()
		if run != nil {
			sess.finishRun(run)
		}
		return "", nil, err
	}
	return turnID, events, nil
}

// CompactStart executes one synchronous compaction command.
func (c *Client) CompactStart(ctx context.Context, threadID string) (string, <-chan codex.TurnEvent, error) {
	sess, err := c.ensureSession(ctx, threadID)
	if err != nil {
		return "", nil, err
	}
	turnID := c.genTurnID("compact")
	run := newActiveRun(turnID, runKindCompact, false)
	go func() {
		defer run.close()
		run.ensureStarted(threadID)
		result, err := sess.compact(ctx)
		if err != nil {
			run.send(codex.TurnEvent{
				Type:     codex.TurnEventTypeError,
				ThreadID: threadID,
				TurnID:   turnID,
				Message:  err.Error(),
			})
			return
		}
		if text := strings.TrimSpace(result.Summary); text != "" {
			run.send(codex.TurnEvent{
				Type:     codex.TurnEventTypeAgentMessageDelta,
				ThreadID: threadID,
				TurnID:   turnID,
				ItemID:   turnID + "-compact",
				ItemType: "agent_message",
				Delta:    text,
			})
		}
		if stats := sess.fetchSessionStats(context.Background()); stats != nil {
			if tokenUsage := tokenUsageFromStats(stats); tokenUsage != nil {
				run.send(codex.TurnEvent{
					Type:       codex.TurnEventTypeTokenUsageUpdated,
					ThreadID:   threadID,
					TurnID:     turnID,
					TokenUsage: tokenUsage,
				})
			}
		}
		run.send(codex.TurnEvent{
			Type:       codex.TurnEventTypeCompleted,
			ThreadID:   threadID,
			TurnID:     turnID,
			StopReason: "end_turn",
		})
	}()
	return turnID, run.events, nil
}

// TurnInterrupt aborts the active Pi run.
func (c *Client) TurnInterrupt(ctx context.Context, threadID, _ string) error {
	sess, ok := c.lookupSession(threadID)
	if !ok {
		return fmt.Errorf("pi rpc: unknown thread %q", threadID)
	}
	return sess.abort(ctx)
}

// ModelsList returns selectable Pi models.
func (c *Client) ModelsList(ctx context.Context) ([]codex.ModelOption, error) {
	c.modelsMu.Lock()
	if len(c.modelsCache) > 0 {
		out := append([]codex.ModelOption(nil), c.modelsCache...)
		c.modelsMu.Unlock()
		return out, nil
	}
	c.modelsMu.Unlock()

	var models []rpcModel
	if sess, ok := c.anySession(); ok {
		list, err := sess.getAvailableModels(ctx)
		if err == nil {
			models = list
		}
	}
	if len(models) == 0 {
		sess, err := c.startSessionProcess("")
		if err != nil {
			return nil, err
		}
		defer sess.close()
		list, err := sess.getAvailableModels(ctx)
		if err != nil {
			return nil, err
		}
		models = list
	}

	options := modelOptionsFromRPC(models)
	c.modelsMu.Lock()
	c.modelsCache = append([]codex.ModelOption(nil), options...)
	c.modelsMu.Unlock()
	return options, nil
}

func (c *Client) anySession() (*rpcSession, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, sess := range c.sessions {
		return sess, true
	}
	return nil, false
}

// ApprovalRespond maps ACP permission responses back to Pi extension_ui_response.
func (c *Client) ApprovalRespond(ctx context.Context, approvalID string, decision codex.ApprovalDecision) error {
	sess, ok := c.takeApproval(approvalID)
	if !ok {
		return fmt.Errorf("pi rpc: unknown approval %q", approvalID)
	}
	switch decision {
	case codex.ApprovalDecisionApproved:
		confirmed := true
		return sess.respondExtensionUI(approvalID, &confirmed, false)
	case codex.ApprovalDecisionDeclined:
		confirmed := false
		return sess.respondExtensionUI(approvalID, &confirmed, false)
	default:
		return sess.respondExtensionUI(approvalID, nil, true)
	}
}

// MCPServersList is unsupported in Pi mode.
func (c *Client) MCPServersList(_ context.Context) ([]codex.MCPServer, error) {
	return nil, fmt.Errorf("pi rpc: MCP servers are not supported")
}

// MCPToolCall is unsupported in Pi mode.
func (c *Client) MCPToolCall(_ context.Context, _ codex.MCPToolCallParams) (codex.MCPToolCallResult, error) {
	return codex.MCPToolCallResult{}, fmt.Errorf("pi rpc: MCP tool routing is not supported")
}

// MCPOAuthLogin is unsupported in Pi mode.
func (c *Client) MCPOAuthLogin(_ context.Context, _ string) (codex.MCPOAuthLoginResult, error) {
	return codex.MCPOAuthLoginResult{
		Status:  "not_supported",
		Message: "Pi RPC mode does not support MCP OAuth routing",
	}, nil
}

// Authenticate restores prompt execution after a prior logout.
func (c *Client) Authenticate(_ context.Context, _ string) error {
	c.loggedOut.Store(false)
	return nil
}

// Logout marks the client as logged out and terminates live Pi sessions.
func (c *Client) Logout(_ context.Context) error {
	c.loggedOut.Store(true)
	c.mu.Lock()
	defer c.mu.Unlock()
	for threadID, sess := range c.sessions {
		sess.close()
		delete(c.sessions, threadID)
	}
	for approvalID := range c.approvals {
		delete(c.approvals, approvalID)
	}
	return nil
}

func (c *Client) applyRunOptions(ctx context.Context, sess *rpcSession, options codex.RunOptions) error {
	if strings.TrimSpace(options.Model) != "" {
		provider, modelID, err := c.resolveModelSelection(ctx, sess, options.Model)
		if err != nil {
			return err
		}
		if err := sess.setModel(ctx, provider, modelID); err != nil {
			return err
		}
	}
	if strings.TrimSpace(options.Effort) != "" {
		if err := sess.setThinkingLevel(ctx, options.Effort); err != nil {
			return err
		}
	}
	return nil
}

func (c *Client) resolveModelSelection(ctx context.Context, sess *rpcSession, value string) (string, string, error) {
	models, err := sess.getAvailableModels(ctx)
	if err != nil {
		return "", "", err
	}
	target := strings.TrimSpace(value)
	for _, model := range models {
		if strings.EqualFold(model.FullID(), target) {
			return model.Provider, model.ID, nil
		}
	}
	for _, model := range models {
		if strings.EqualFold(strings.TrimSpace(model.ID), target) {
			return model.Provider, model.ID, nil
		}
	}
	for _, model := range models {
		if strings.EqualFold(strings.TrimSpace(model.Name), target) {
			return model.Provider, model.ID, nil
		}
	}
	if provider, modelID, ok := splitProviderModel(target); ok {
		return provider, modelID, nil
	}
	return "", "", fmt.Errorf("pi rpc: model %q not found", value)
}

type listedSession struct {
	Path         string
	CWD          string
	Name         string
	FirstMessage string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

func (c *Client) listDiskSessions(cwd string) ([]listedSession, error) {
	files, err := c.sessionFilesForCWD(cwd)
	if err != nil {
		return nil, err
	}
	out := make([]listedSession, 0, len(files))
	for _, file := range files {
		session, err := parseDiskSession(file)
		if err != nil {
			continue
		}
		if strings.TrimSpace(cwd) != "" && strings.TrimSpace(session.CWD) != strings.TrimSpace(cwd) {
			continue
		}
		out = append(out, session)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})
	return out, nil
}

func (c *Client) sessionFilesForCWD(cwd string) ([]string, error) {
	if strings.TrimSpace(c.cfg.SessionDir) != "" {
		return sessionFilesInDir(c.cfg.SessionDir)
	}

	root := filepath.Join(piAgentDir(), "sessions")
	if strings.TrimSpace(cwd) != "" {
		return sessionFilesInDir(defaultSessionDir(root, cwd))
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var files []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		more, err := sessionFilesInDir(filepath.Join(root, entry.Name()))
		if err != nil {
			continue
		}
		files = append(files, more...)
	}
	return files, nil
}

func sessionFilesInDir(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	files := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		files = append(files, filepath.Join(dir, entry.Name()))
	}
	return files, nil
}

func parseDiskSession(path string) (listedSession, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return listedSession{}, err
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) == "" {
		return listedSession{}, fmt.Errorf("empty session file")
	}
	var header diskSessionHeader
	if err := json.Unmarshal([]byte(lines[0]), &header); err != nil {
		return listedSession{}, err
	}
	if header.Type != "session" {
		return listedSession{}, fmt.Errorf("invalid session header")
	}

	info := listedSession{
		Path: path,
		CWD:  strings.TrimSpace(header.CWD),
	}
	if createdAt, err := time.Parse(time.RFC3339, header.Timestamp); err == nil {
		info.CreatedAt = createdAt
	}
	for _, raw := range lines[1:] {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		var typed struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal([]byte(raw), &typed); err != nil {
			continue
		}
		switch typed.Type {
		case "session_info":
			var entry diskSessionInfoEntry
			if err := json.Unmarshal([]byte(raw), &entry); err == nil && strings.TrimSpace(entry.Name) != "" {
				info.Name = strings.TrimSpace(entry.Name)
			}
		case "message":
			var entry diskSessionMessageEntry
			if err := json.Unmarshal([]byte(raw), &entry); err != nil {
				continue
			}
			text := summaryTextFromAgentMessage(entry.Message)
			if info.FirstMessage == "" && text != "" && entry.Message.Role == "user" {
				info.FirstMessage = text
			}
			if ts := messageTimestamp(entry.Message); !ts.IsZero() && ts.After(info.UpdatedAt) {
				info.UpdatedAt = ts
			}
			if info.Name == "" && text != "" {
				info.Name = trimPreview(text)
			}
		}
	}
	if info.Name == "" {
		info.Name = trimPreview(info.FirstMessage)
	}
	stat, err := os.Stat(path)
	if err == nil && info.UpdatedAt.IsZero() {
		info.UpdatedAt = stat.ModTime()
	}
	if info.CreatedAt.IsZero() && err == nil {
		info.CreatedAt = stat.ModTime()
	}
	return info, nil
}

func historyThreadFromMessages(threadID string, cwd string, messages []rpcAgentMessage) codex.Thread {
	items := make([]codex.Turn, 0, len(messages))
	for idx, message := range messages {
		switch strings.TrimSpace(message.Role) {
		case "user":
			text := summaryTextFromAgentMessage(message)
			if text == "" {
				continue
			}
			items = append(items, codex.Turn{
				ID: fmt.Sprintf("msg-%d", idx+1),
				Items: []codex.ThreadItem{{
					ID:   fmt.Sprintf("msg-%d-user", idx+1),
					Type: "userMessage",
					Content: []codex.UserInput{{
						Type: "text",
						Text: text,
					}},
				}},
			})
		case "assistant":
			text := assistantText(message)
			if text == "" {
				continue
			}
			items = append(items, codex.Turn{
				ID: fmt.Sprintf("msg-%d", idx+1),
				Items: []codex.ThreadItem{{
					ID:   fmt.Sprintf("msg-%d-assistant", idx+1),
					Type: "agentMessage",
					Text: text,
				}},
			})
		}
	}
	return codex.Thread{
		ID:    threadID,
		CWD:   cwd,
		Turns: items,
	}
}

func promptFromInputs(input []codex.UserInput, options codex.RunOptions) (string, []rpcImageContent, error) {
	parts := make([]string, 0, len(input)+1)
	if instructions := composeInstructionPrefix(options); instructions != "" {
		parts = append(parts, instructions)
	}

	images := make([]rpcImageContent, 0, len(input))
	for _, item := range input {
		switch strings.ToLower(strings.TrimSpace(item.Type)) {
		case "text":
			if text := strings.TrimSpace(item.Text); text != "" {
				parts = append(parts, text)
			}
		case "mention":
			text := strings.TrimSpace(item.Text)
			switch {
			case text != "":
				parts = append(parts, text)
			case strings.TrimSpace(item.Path) != "":
				parts = append(parts, "[Mention: "+strings.TrimSpace(item.Path)+"]")
			case strings.TrimSpace(item.Name) != "":
				parts = append(parts, "[Mention: "+strings.TrimSpace(item.Name)+"]")
			}
		case "image":
			image, note, err := rpcImageFromInput(item)
			if err != nil {
				return "", nil, err
			}
			if image != nil {
				images = append(images, *image)
			}
			if note != "" {
				parts = append(parts, note)
			}
		case "localimage":
			image, note, err := rpcImageFromInput(item)
			if err != nil {
				return "", nil, err
			}
			if image != nil {
				images = append(images, *image)
			}
			if note != "" {
				parts = append(parts, note)
			}
		default:
			if text := strings.TrimSpace(item.Text); text != "" {
				parts = append(parts, text)
			} else if path := strings.TrimSpace(item.Path); path != "" {
				parts = append(parts, path)
			}
		}
	}

	message := strings.TrimSpace(strings.Join(parts, "\n\n"))
	if message == "" && len(images) > 0 {
		message = "Please analyze the attached images."
	}
	if message == "" {
		return "", nil, fmt.Errorf("pi rpc prompt is empty")
	}
	return message, images, nil
}

func reviewPrompt(instructions string) string {
	instructions = strings.TrimSpace(instructions)
	if instructions == "" {
		return "Review the current workspace changes. Focus on bugs, regressions, and missing tests."
	}
	return "Review the current workspace changes. " + instructions
}

func composeInstructionPrefix(options codex.RunOptions) string {
	chunks := make([]string, 0, 2)
	if text := strings.TrimSpace(options.SystemInstructions); text != "" {
		chunks = append(chunks, "[adapter system instructions]\n"+text)
	}
	if text := strings.TrimSpace(options.Personality); text != "" {
		chunks = append(chunks, "[adapter collaboration mode]\n"+text)
	}
	if len(chunks) == 0 {
		return ""
	}
	return strings.Join(chunks, "\n\n")
}

func rpcImageFromInput(item codex.UserInput) (*rpcImageContent, string, error) {
	if url := strings.TrimSpace(item.URL); url != "" {
		if strings.HasPrefix(strings.ToLower(url), "data:") {
			mimeType, payload, err := splitDataURI(url)
			if err != nil {
				return nil, "", err
			}
			return &rpcImageContent{
				Type:     "image",
				Data:     payload,
				MimeType: mimeType,
			}, "", nil
		}
		return nil, "[Image URL: " + url + "]", nil
	}

	path := strings.TrimSpace(item.Path)
	if path == "" {
		return nil, "", nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "", fmt.Errorf("read image %s: %w", path, err)
	}
	if len(data) > defaultImageReadLimit {
		return nil, "", fmt.Errorf("image %s exceeds %d bytes", path, defaultImageReadLimit)
	}
	mimeType := mimeFromPath(path, data)
	return &rpcImageContent{
		Type:     "image",
		Data:     base64.StdEncoding.EncodeToString(data),
		MimeType: mimeType,
	}, "", nil
}

func approvalFromGatePayload(payload gateRequestPayload) codex.ApprovalRequest {
	kind := codex.ApprovalKindCommand
	switch strings.ToLower(strings.TrimSpace(payload.Approval)) {
	case "file":
		kind = codex.ApprovalKindFile
	case "network":
		kind = codex.ApprovalKindNetwork
	}
	return codex.ApprovalRequest{
		ToolCallID: payload.ToolCallID,
		Kind:       kind,
		Command:    strings.TrimSpace(payload.Command),
		Files:      append([]string(nil), payload.Files...),
		Message:    strings.TrimSpace(payload.Message),
	}
}

func tokenUsageFromStats(stats *rpcSessionStats) *codex.ThreadTokenUsage {
	if stats == nil {
		return nil
	}
	var inputTokens int64
	var contextWindow *int64
	if stats.ContextUsage != nil {
		if stats.ContextUsage.Tokens != nil {
			inputTokens = *stats.ContextUsage.Tokens
		}
		if stats.ContextUsage.ContextWindow > 0 {
			window := stats.ContextUsage.ContextWindow
			contextWindow = &window
		}
	}
	if inputTokens == 0 {
		inputTokens = stats.Tokens.Input
	}
	total := stats.Tokens.Total
	return &codex.ThreadTokenUsage{
		Last: codex.TokenUsageBreakdown{
			InputTokens: inputTokens,
			TotalTokens: inputTokens,
		},
		ModelContextWindow: contextWindow,
		Total: codex.TokenUsageBreakdown{
			InputTokens:           stats.Tokens.Input,
			OutputTokens:          stats.Tokens.Output,
			CachedInputTokens:     stats.Tokens.CacheRead + stats.Tokens.CacheWrite,
			ReasoningOutputTokens: 0,
			TotalTokens:           total,
		},
	}
}

func stopReasonFromMessages(messages []rpcAgentMessage) (string, string) {
	for i := len(messages) - 1; i >= 0; i-- {
		message := messages[i]
		if strings.TrimSpace(message.Role) != "assistant" {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(message.StopReason)) {
		case "aborted":
			return "cancelled", strings.TrimSpace(message.ErrorMessage)
		case "error":
			return "error", strings.TrimSpace(message.ErrorMessage)
		default:
			return "end_turn", strings.TrimSpace(message.ErrorMessage)
		}
	}
	return "end_turn", ""
}

func modelOptionsFromRPC(models []rpcModel) []codex.ModelOption {
	out := make([]codex.ModelOption, 0, len(models))
	seen := make(map[string]struct{}, len(models))
	for _, model := range models {
		id := model.FullID()
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		efforts := supportedEffortsForModel(model)
		defaultEffort := "off"
		if model.Reasoning {
			defaultEffort = "medium"
		}
		out = append(out, codex.ModelOption{
			ID:                        id,
			Name:                      chooseFirstNonEmpty(strings.TrimSpace(model.Name), id),
			Description:               strings.TrimSpace(model.Provider),
			DefaultReasoningEffort:    defaultEffort,
			SupportedReasoningEfforts: efforts,
		})
	}
	return out
}

func supportedEffortsForModel(model rpcModel) []codex.ReasoningEffortOption {
	if !model.Reasoning {
		return []codex.ReasoningEffortOption{{Value: "off", Description: "No reasoning"}}
	}
	values := []string{"off", "minimal", "low", "medium", "high"}
	fullID := strings.ToLower(model.FullID())
	if strings.Contains(fullID, "codex") && strings.Contains(fullID, "max") {
		values = append(values, "xhigh")
	}
	out := make([]codex.ReasoningEffortOption, 0, len(values))
	for _, value := range values {
		out = append(out, codex.ReasoningEffortOption{Value: value})
	}
	return out
}

func toolOutputItems(result *rpcToolResult) []codex.ToolOutputContentItem {
	if result == nil || len(result.Content) == 0 {
		return nil
	}
	out := make([]codex.ToolOutputContentItem, 0, len(result.Content)+1)
	for _, block := range result.Content {
		switch strings.ToLower(strings.TrimSpace(block.Type)) {
		case "text":
			if text := strings.TrimSpace(block.Text); text != "" {
				out = append(out, codex.ToolOutputContentItem{
					Type: "text",
					Text: block.Text,
				})
			}
		case "image":
			out = append(out, codex.ToolOutputContentItem{
				Type:     "image",
				Data:     block.Data,
				MimeType: block.MimeType,
			})
		}
	}
	if result.Details != nil {
		var details struct {
			Diff string `json:"diff,omitempty"`
		}
		if err := json.Unmarshal(result.Details, &details); err == nil && strings.TrimSpace(details.Diff) != "" {
			out = append(out, codex.ToolOutputContentItem{
				Type: "text",
				Text: details.Diff,
			})
		}
	}
	return out
}

func summaryTextFromAgentMessage(message rpcAgentMessage) string {
	if strings.TrimSpace(message.Role) == "assistant" {
		return assistantText(message)
	}
	return userText(message)
}

func userText(message rpcAgentMessage) string {
	if len(message.Content) == 0 {
		return ""
	}
	var raw any
	if err := json.Unmarshal(message.Content, &raw); err != nil {
		return ""
	}
	switch typed := raw.(type) {
	case string:
		return strings.TrimSpace(typed)
	case []any:
		parts := make([]string, 0, len(typed))
		for _, item := range typed {
			block, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if strings.EqualFold(stringValue(block["type"]), "text") {
				parts = append(parts, strings.TrimSpace(stringValue(block["text"])))
			} else if strings.EqualFold(stringValue(block["type"]), "image") {
				parts = append(parts, "[Image]")
			}
		}
		return strings.TrimSpace(strings.Join(parts, "\n"))
	default:
		return ""
	}
}

func assistantText(message rpcAgentMessage) string {
	if len(message.Content) == 0 {
		return ""
	}
	var blocks []rpcContentBlock
	if err := json.Unmarshal(message.Content, &blocks); err != nil {
		return ""
	}
	return textFromRPCBlocks(blocks)
}

func textFromRPCBlocks(blocks []rpcContentBlock) string {
	parts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		switch strings.ToLower(strings.TrimSpace(block.Type)) {
		case "text":
			if strings.TrimSpace(block.Text) != "" {
				parts = append(parts, block.Text)
			}
		case "thinking":
			if strings.TrimSpace(block.Thinking) != "" {
				parts = append(parts, block.Thinking)
			}
		}
	}
	return strings.Join(parts, "")
}

func messageTimestamp(message rpcAgentMessage) time.Time {
	if message.Timestamp <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(message.Timestamp)
}

func trimPreview(text string) string {
	text = strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
	if len(text) <= 120 {
		return text
	}
	return text[:117] + "..."
}

func modelValue(model *rpcModel) string {
	if model == nil {
		return ""
	}
	return model.FullID()
}

func normalizeThreadID(sessionFile string, sessionID string) string {
	if trimmed := strings.TrimSpace(sessionFile); trimmed != "" {
		return trimmed
	}
	return strings.TrimSpace(sessionID)
}

func chooseFirstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func splitProviderModel(value string) (string, string, bool) {
	value = strings.TrimSpace(value)
	parts := strings.SplitN(value, "/", 2)
	if len(parts) != 2 {
		return "", "", false
	}
	provider := strings.TrimSpace(parts[0])
	modelID := strings.TrimSpace(parts[1])
	if provider == "" || modelID == "" {
		return "", "", false
	}
	return provider, modelID, true
}

func jsonStringField(raw json.RawMessage, key string) string {
	if len(raw) == 0 {
		return ""
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return ""
	}
	return strings.TrimSpace(stringValue(payload[key]))
}

func stringValue(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	default:
		return ""
	}
}

func ternaryString(condition bool, whenTrue string, whenFalse string) string {
	if condition {
		return whenTrue
	}
	return whenFalse
}

func bytesTrimSpace(value []byte) []byte {
	return []byte(strings.TrimSpace(string(value)))
}

func parseOffsetCursor(raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	offset, err := strconv.Atoi(raw)
	if err != nil || offset < 0 {
		return 0, fmt.Errorf("invalid cursor")
	}
	return offset, nil
}

func piAgentDir() string {
	if env := strings.TrimSpace(os.Getenv("PI_CODING_AGENT_DIR")); env != "" {
		if strings.HasPrefix(env, "~/") {
			home, _ := os.UserHomeDir()
			return filepath.Join(home, strings.TrimPrefix(env, "~/"))
		}
		return env
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".pi", "agent")
}

func defaultSessionDir(root string, cwd string) string {
	safePath := "--" + strings.NewReplacer("/", "-", "\\", "-", ":", "-").Replace(strings.TrimLeft(cwd, `/\`)) + "--"
	return filepath.Join(root, safePath)
}

func mimeFromPath(path string, data []byte) string {
	if ext := strings.TrimSpace(filepath.Ext(path)); ext != "" {
		if detected := mime.TypeByExtension(ext); detected != "" {
			return detected
		}
	}
	return http.DetectContentType(data)
}

func splitDataURI(uri string) (string, string, error) {
	if !strings.HasPrefix(strings.ToLower(uri), "data:") {
		return "", "", fmt.Errorf("unsupported image uri: %s", uri)
	}
	parts := strings.SplitN(uri, ",", 2)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("invalid data uri")
	}
	header := strings.TrimPrefix(parts[0], "data:")
	header = strings.TrimPrefix(header, "DATA:")
	mimeType := header
	if idx := strings.Index(header, ";"); idx >= 0 {
		mimeType = header[:idx]
	}
	mimeType = strings.TrimSpace(mimeType)
	if mimeType == "" {
		return "", "", fmt.Errorf("data uri missing mime type")
	}
	return mimeType, parts[1], nil
}

func extractCWDFromSessionPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	session, err := parseDiskSession(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(session.CWD)
}
