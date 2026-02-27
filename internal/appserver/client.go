package appserver

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
)

const turnStreamBufferSize = 32

var errClientClosed = errors.New("app-server client is closed")

type pendingApproval struct {
	requestID json.RawMessage
	turnID    string
}

// Client is a minimal JSON-RPC client for codex app-server.
type Client struct {
	process *Process
	codec   *JSONLCodec
	logger  *slog.Logger

	nextID uint64

	mu          sync.Mutex
	pending     map[string]chan RPCMessage
	approvals   map[string]pendingApproval
	turnStreams map[string]chan TurnEvent
	queuedTurns map[string][]TurnEvent
	closed      bool
}

// NewClient wires process pipes and starts the response reader.
func NewClient(process *Process, logger *slog.Logger) *Client {
	if logger == nil {
		logger = slog.New(slog.NewJSONHandler(io.Discard, nil))
	}

	client := &Client{
		process:     process,
		codec:       NewJSONLCodecWithTrace(process.Stdout(), process.Stdin(), process.trace),
		logger:      logger,
		pending:     make(map[string]chan RPCMessage),
		approvals:   make(map[string]pendingApproval),
		turnStreams: make(map[string]chan TurnEvent),
		queuedTurns: make(map[string][]TurnEvent),
	}

	go client.readLoop()
	return client
}

// Initialize performs app-server initialize request.
func (c *Client) Initialize(ctx context.Context) error {
	params := InitializeParams{
		ClientInfo: ClientInfo{
			Name:    "codex-acp-go",
			Version: "0.1.0",
		},
		Capabilities: map[string]any{},
	}
	var result InitializeResult
	return c.call(ctx, "initialize", params, &result)
}

// Initialized sends initialized notification.
func (c *Client) Initialized() error {
	return c.notify(methodInitialized, map[string]any{})
}

// ThreadStart starts a new thread and returns thread id.
func (c *Client) ThreadStart(ctx context.Context, cwd string, options RunOptions) (string, error) {
	params := ThreadStartParams{
		CWD:        cwd,
		RunOptions: options,
	}
	var result ThreadStartResult
	if err := c.call(ctx, methodThreadStart, params, &result); err != nil {
		return "", err
	}
	threadID := strings.TrimSpace(result.ThreadID)
	if threadID == "" && result.Thread != nil {
		threadID = strings.TrimSpace(result.Thread.ID)
	}
	if threadID == "" {
		return "", fmt.Errorf("thread/start returned empty threadId")
	}
	return threadID, nil
}

// TurnStart starts a turn and returns turn id plus event stream.
func (c *Client) TurnStart(
	ctx context.Context,
	threadID string,
	input []UserInput,
	options RunOptions,
) (string, <-chan TurnEvent, error) {
	if len(input) == 0 {
		return "", nil, fmt.Errorf("turn/start requires at least one input item")
	}
	params := TurnStartParams{
		ThreadID:   threadID,
		Input:      input,
		RunOptions: options,
	}
	var result TurnStartResult
	if err := c.call(ctx, methodTurnStart, params, &result); err != nil {
		return "", nil, err
	}
	turnID := strings.TrimSpace(result.TurnID)
	if turnID == "" && result.Turn != nil {
		turnID = strings.TrimSpace(result.Turn.ID)
	}
	if turnID == "" {
		return "", nil, fmt.Errorf("turn/start returned empty turnId")
	}

	stream := c.registerTurnStream(turnID)
	return turnID, stream, nil
}

// ReviewStart starts one review workflow turn.
func (c *Client) ReviewStart(
	ctx context.Context,
	threadID string,
	instructions string,
	options RunOptions,
) (string, <-chan TurnEvent, error) {
	params := ReviewStartParams{
		ThreadID:     threadID,
		Instructions: instructions,
		RunOptions:   options,
	}
	var result ReviewStartResult
	if err := c.call(ctx, methodReviewStart, params, &result); err != nil {
		return "", nil, err
	}
	turnID := strings.TrimSpace(result.TurnID)
	if turnID == "" && result.Turn != nil {
		turnID = strings.TrimSpace(result.Turn.ID)
	}
	if turnID == "" {
		return "", nil, fmt.Errorf("review/start returned empty turnId")
	}

	stream := c.registerTurnStream(turnID)
	return turnID, stream, nil
}

// CompactStart starts one compact operation and returns turn stream.
func (c *Client) CompactStart(ctx context.Context, threadID string) (string, <-chan TurnEvent, error) {
	params := CompactStartParams{
		ThreadID: threadID,
	}
	var result CompactStartResult
	if err := c.call(ctx, methodThreadCompact, params, &result); err != nil {
		return "", nil, err
	}
	turnID := strings.TrimSpace(result.TurnID)
	if turnID == "" && result.Turn != nil {
		turnID = strings.TrimSpace(result.Turn.ID)
	}
	if turnID == "" {
		return "", nil, fmt.Errorf("thread/compact/start returned empty turnId")
	}
	stream := c.registerTurnStream(turnID)
	return turnID, stream, nil
}

// TurnInterrupt requests turn interruption.
func (c *Client) TurnInterrupt(ctx context.Context, threadID, turnID string) error {
	params := TurnInterruptParams{
		ThreadID: threadID,
		TurnID:   turnID,
	}
	var result map[string]any
	return c.call(ctx, methodTurnInterrupt, params, &result)
}

// MCPServersList fetches available MCP servers.
func (c *Client) MCPServersList(ctx context.Context) ([]MCPServer, error) {
	var result MCPServerListResult
	if err := c.call(ctx, methodMCPServerList, map[string]any{}, &result); err != nil {
		return nil, err
	}
	return result.Servers, nil
}

// MCPToolCall calls one MCP tool.
func (c *Client) MCPToolCall(ctx context.Context, params MCPToolCallParams) (MCPToolCallResult, error) {
	var result MCPToolCallResult
	if err := c.call(ctx, methodMCPServerCall, params, &result); err != nil {
		return MCPToolCallResult{}, err
	}
	return result, nil
}

// MCPOAuthLogin starts OAuth flow for one MCP server.
func (c *Client) MCPOAuthLogin(ctx context.Context, server string) (MCPOAuthLoginResult, error) {
	params := MCPOAuthLoginParams{Server: server}
	var result MCPOAuthLoginResult
	if err := c.call(ctx, methodMCPOAuthLogin, params, &result); err != nil {
		return MCPOAuthLoginResult{}, err
	}
	return result, nil
}

// Logout clears app-server auth state when supported.
func (c *Client) Logout(ctx context.Context) error {
	var result map[string]any
	if err := c.call(ctx, methodAccountLogout, map[string]any{}, &result); err == nil {
		return nil
	} else if !isMethodNotFoundRPCError(err) {
		return err
	}

	if err := c.call(ctx, methodAuthLogout, map[string]any{}, &result); err == nil {
		return nil
	} else if !isMethodNotFoundRPCError(err) {
		return err
	}
	return nil
}

// ApprovalRespond sends user decision for one server-initiated approval request.
func (c *Client) ApprovalRespond(ctx context.Context, approvalID string, decision ApprovalDecision) error {
	if approvalID == "" {
		return fmt.Errorf("approval id is required")
	}

	c.mu.Lock()
	approval, ok := c.approvals[approvalID]
	if ok {
		delete(c.approvals, approvalID)
	}
	c.mu.Unlock()
	if !ok {
		return fmt.Errorf("approval request not found: %s", approvalID)
	}

	select {
	case <-ctx.Done():
		return fmt.Errorf("approval response cancelled: %w", ctx.Err())
	default:
	}

	resultRaw, err := json.Marshal(ApprovalDecisionResult{Outcome: string(decision)})
	if err != nil {
		return fmt.Errorf("encode approval response: %w", err)
	}
	return c.codec.WriteMessage(RPCMessage{
		JSONRPC: "2.0",
		ID:      cloneRawMessage(approval.requestID),
		Result:  resultRaw,
	})
}

// Close shuts down request waiters and underlying process.
func (c *Client) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	c.mu.Unlock()

	c.failAll(errClientClosed)
	return c.process.Close()
}

func (c *Client) registerTurnStream(turnID string) <-chan TurnEvent {
	ch := make(chan TurnEvent, turnStreamBufferSize)

	c.mu.Lock()
	c.turnStreams[turnID] = ch
	queued := c.queuedTurns[turnID]
	delete(c.queuedTurns, turnID)
	c.mu.Unlock()

	for _, event := range queued {
		if isTerminalTurnEvent(event.Type) {
			ch <- event
			c.mu.Lock()
			delete(c.turnStreams, turnID)
			c.mu.Unlock()
			close(ch)
			return ch
		}
		ch <- event
	}
	return ch
}

func (c *Client) call(ctx context.Context, method string, params any, out any) error {
	id := strconv.FormatUint(atomic.AddUint64(&c.nextID, 1), 10)
	rawID := json.RawMessage(strconv.Quote(id))

	msg, err := buildRequest(rawID, method, params)
	if err != nil {
		return err
	}

	respCh := make(chan RPCMessage, 1)
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return errClientClosed
	}
	c.pending[id] = respCh
	c.mu.Unlock()

	if err := c.codec.WriteMessage(msg); err != nil {
		c.removePending(id)
		return fmt.Errorf("%s write request: %w", method, err)
	}

	var resp RPCMessage
	select {
	case <-ctx.Done():
		c.removePending(id)
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

func (c *Client) notify(method string, params any) error {
	var paramsRaw json.RawMessage
	if params != nil {
		data, err := json.Marshal(params)
		if err != nil {
			return fmt.Errorf("%s encode params: %w", method, err)
		}
		paramsRaw = data
	}
	return c.codec.WriteMessage(RPCMessage{
		JSONRPC: "2.0",
		Method:  method,
		Params:  paramsRaw,
	})
}

func (c *Client) readLoop() {
	for {
		msg, err := c.codec.ReadMessage()
		if err != nil {
			c.failAll(fmt.Errorf("app-server read loop: %w", err))
			return
		}

		switch {
		case msg.ID != nil && msg.Method == "":
			c.handleResponse(msg)
		case msg.ID != nil && msg.Method != "":
			c.handleServerRequest(msg)
		case msg.Method != "" && msg.ID == nil:
			c.handleNotification(msg)
		}
	}
}

func (c *Client) handleResponse(msg RPCMessage) {
	if msg.ID == nil {
		return
	}
	id := normalizeID(*msg.ID)

	c.mu.Lock()
	ch, ok := c.pending[id]
	if ok {
		delete(c.pending, id)
	}
	c.mu.Unlock()

	if ok {
		ch <- msg
		close(ch)
	}
}

func (c *Client) handleServerRequest(msg RPCMessage) {
	if msg.ID == nil {
		return
	}

	switch msg.Method {
	case methodApprovalReq:
		var approval ApprovalRequest
		if err := json.Unmarshal(msg.Params, &approval); err != nil {
			c.writeServerErrorResponse(*msg.ID, -32602, "invalid approval/request params")
			return
		}
		if approval.TurnID == "" {
			c.writeServerErrorResponse(*msg.ID, -32602, "approval/request requires turnId")
			return
		}

		if approval.ApprovalID == "" {
			approval.ApprovalID = normalizeID(*msg.ID)
		}
		c.mu.Lock()
		c.approvals[approval.ApprovalID] = pendingApproval{
			requestID: *cloneRawMessage(*msg.ID),
			turnID:    approval.TurnID,
		}
		c.mu.Unlock()

		c.pushTurnEvent(approval.TurnID, TurnEvent{
			Type:     TurnEventTypeApprovalRequired,
			ThreadID: approval.ThreadID,
			TurnID:   approval.TurnID,
			Approval: approval,
		}, false)
	default:
		c.writeServerErrorResponse(*msg.ID, -32601, "method not found")
	}
}

func (c *Client) handleNotification(msg RPCMessage) {
	switch msg.Method {
	case notificationTurnStarted:
		var note TurnStartedNotification
		if err := json.Unmarshal(msg.Params, &note); err != nil {
			c.logger.Warn("ignore malformed turn/started", slog.String("error", err.Error()))
			return
		}
		turnID := effectiveTurnID(note.TurnID, note.Turn)
		c.pushTurnEvent(turnID, TurnEvent{
			Type:     TurnEventTypeStarted,
			ThreadID: note.ThreadID,
			TurnID:   turnID,
		}, false)
	case notificationTurnUpdate:
		var note TurnUpdateNotification
		if err := json.Unmarshal(msg.Params, &note); err != nil {
			c.logger.Warn("ignore malformed turn/update", slog.String("error", err.Error()))
			return
		}
		c.pushTurnEvent(note.TurnID, TurnEvent{
			Type:     TurnEventTypeUpdate,
			ThreadID: note.ThreadID,
			TurnID:   note.TurnID,
			Delta:    note.Delta,
		}, false)
	case notificationItemAgentMessageDelta:
		var note ItemAgentMessageDeltaNotification
		if err := json.Unmarshal(msg.Params, &note); err != nil {
			c.logger.Warn("ignore malformed item/agentMessage/delta", slog.String("error", err.Error()))
			return
		}
		c.pushTurnEvent(note.TurnID, TurnEvent{
			Type:     TurnEventTypeAgentMessageDelta,
			ThreadID: note.ThreadID,
			TurnID:   note.TurnID,
			ItemID:   note.ItemID,
			Delta:    note.Delta,
		}, false)
	case notificationItemStarted:
		var note ItemStartedNotification
		if err := json.Unmarshal(msg.Params, &note); err != nil {
			c.logger.Warn("ignore malformed item/started", slog.String("error", err.Error()))
			return
		}
		itemID := strings.TrimSpace(note.ItemID)
		itemType := strings.TrimSpace(note.ItemType)
		if note.Item != nil {
			if itemID == "" {
				itemID = strings.TrimSpace(note.Item.ID)
			}
			if itemType == "" {
				itemType = strings.TrimSpace(note.Item.Type)
			}
		}
		c.pushTurnEvent(note.TurnID, TurnEvent{
			Type:     TurnEventTypeItemStarted,
			ThreadID: note.ThreadID,
			TurnID:   note.TurnID,
			ItemID:   itemID,
			ItemType: itemType,
		}, false)
	case notificationItemCompleted:
		var note ItemCompletedNotification
		if err := json.Unmarshal(msg.Params, &note); err != nil {
			c.logger.Warn("ignore malformed item/completed", slog.String("error", err.Error()))
			return
		}
		itemID := strings.TrimSpace(note.ItemID)
		itemType := strings.TrimSpace(note.ItemType)
		if note.Item != nil {
			if itemID == "" {
				itemID = strings.TrimSpace(note.Item.ID)
			}
			if itemType == "" {
				itemType = strings.TrimSpace(note.Item.Type)
			}
		}
		c.pushTurnEvent(note.TurnID, TurnEvent{
			Type:     TurnEventTypeItemCompleted,
			ThreadID: note.ThreadID,
			TurnID:   note.TurnID,
			ItemID:   itemID,
			ItemType: itemType,
		}, false)
	case notificationTurnCompleted:
		var note TurnCompletedNotification
		if err := json.Unmarshal(msg.Params, &note); err != nil {
			c.logger.Warn("ignore malformed turn/completed", slog.String("error", err.Error()))
			return
		}
		turnID := effectiveTurnID(note.TurnID, note.Turn)
		stopReason := strings.TrimSpace(note.StopReason)
		if stopReason == "" && note.Turn != nil {
			stopReason = stopReasonFromTurnStatus(note.Turn.Status)
		}
		c.pushTurnEvent(turnID, TurnEvent{
			Type:       TurnEventTypeCompleted,
			ThreadID:   note.ThreadID,
			TurnID:     turnID,
			StopReason: stopReason,
		}, true)
	case notificationReviewModeEntered:
		var note ReviewModeNotification
		if err := json.Unmarshal(msg.Params, &note); err != nil {
			c.logger.Warn("ignore malformed review/mode_entered", slog.String("error", err.Error()))
			return
		}
		c.pushTurnEvent(note.TurnID, TurnEvent{
			Type:     TurnEventTypeReviewModeEntered,
			ThreadID: note.ThreadID,
			TurnID:   note.TurnID,
		}, false)
	case notificationReviewModeExited:
		var note ReviewModeNotification
		if err := json.Unmarshal(msg.Params, &note); err != nil {
			c.logger.Warn("ignore malformed review/mode_exited", slog.String("error", err.Error()))
			return
		}
		c.pushTurnEvent(note.TurnID, TurnEvent{
			Type:     TurnEventTypeReviewModeExited,
			ThreadID: note.ThreadID,
			TurnID:   note.TurnID,
		}, false)
	default:
		// Ignore notifications not used by the adapter.
	}
}

func effectiveTurnID(turnID string, turn *TurnRef) string {
	id := strings.TrimSpace(turnID)
	if id != "" {
		return id
	}
	if turn == nil {
		return ""
	}
	return strings.TrimSpace(turn.ID)
}

func stopReasonFromTurnStatus(status string) string {
	switch strings.TrimSpace(strings.ToLower(status)) {
	case "interrupted":
		return "cancelled"
	case "failed":
		return "error"
	case "completed":
		return "end_turn"
	default:
		return ""
	}
}

func (c *Client) pushTurnEvent(turnID string, event TurnEvent, closeAfter bool) {
	c.mu.Lock()
	ch, ok := c.turnStreams[turnID]
	if !ok {
		c.queuedTurns[turnID] = append(c.queuedTurns[turnID], event)
		c.mu.Unlock()
		return
	}
	if closeAfter {
		delete(c.turnStreams, turnID)
	}
	c.mu.Unlock()

	select {
	case ch <- event:
	default:
		c.logger.Warn("turn stream channel full; dropping event", slog.String("turnId", turnID))
	}

	if closeAfter {
		close(ch)
	}
}

func (c *Client) writeServerErrorResponse(id json.RawMessage, code int, message string) {
	if err := c.codec.WriteMessage(RPCMessage{
		JSONRPC: "2.0",
		ID:      cloneRawMessage(id),
		Error: &RPCError{
			Code:    code,
			Message: message,
		},
	}); err != nil {
		c.logger.Warn("failed to write app-server request error", slog.String("error", err.Error()))
	}
}

func (c *Client) failAll(err error) {
	c.mu.Lock()
	pending := c.pending
	streams := c.turnStreams
	approvals := c.approvals
	c.pending = make(map[string]chan RPCMessage)
	c.approvals = make(map[string]pendingApproval)
	c.turnStreams = make(map[string]chan TurnEvent)
	c.queuedTurns = make(map[string][]TurnEvent)
	c.mu.Unlock()

	for _, ch := range pending {
		ch <- RPCMessage{
			Error: &RPCError{
				Code:    -32000,
				Message: err.Error(),
			},
		}
		close(ch)
	}
	for _, ch := range streams {
		select {
		case ch <- TurnEvent{
			Type:       TurnEventTypeError,
			StopReason: "error",
			Message:    err.Error(),
		}:
		default:
		}
		close(ch)
	}

	for approvalID := range approvals {
		c.logger.Warn("dropping pending approval due to client shutdown", slog.String("approvalId", approvalID))
	}
}

func (c *Client) removePending(id string) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

func buildRequest(rawID json.RawMessage, method string, params any) (RPCMessage, error) {
	msg := RPCMessage{
		JSONRPC: "2.0",
		Method:  method,
		ID:      cloneRawMessage(rawID),
	}
	if params != nil {
		raw, err := json.Marshal(params)
		if err != nil {
			return RPCMessage{}, fmt.Errorf("%s encode params: %w", method, err)
		}
		msg.Params = raw
	}
	return msg, nil
}

func normalizeID(raw json.RawMessage) string {
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

func cloneRawMessage(raw json.RawMessage) *json.RawMessage {
	cp := append(json.RawMessage(nil), raw...)
	return &cp
}

func isTerminalTurnEvent(eventType TurnEventType) bool {
	switch eventType {
	case TurnEventTypeCompleted, TurnEventTypeError:
		return true
	default:
		return false
	}
}

func isMethodNotFoundRPCError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "rpc error code=-32601")
}
