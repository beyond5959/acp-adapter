package appserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"sync"
	"sync/atomic"
)

const turnStreamBufferSize = 32

var errClientClosed = errors.New("app-server client is closed")

// Client is a minimal JSON-RPC client for codex app-server.
type Client struct {
	process *Process
	codec   *JSONLCodec
	logger  *slog.Logger

	nextID uint64

	mu          sync.Mutex
	pending     map[string]chan RPCMessage
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
		codec:       NewJSONLCodec(process.Stdout(), process.Stdin()),
		logger:      logger,
		pending:     make(map[string]chan RPCMessage),
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
func (c *Client) ThreadStart(ctx context.Context, cwd string) (string, error) {
	params := ThreadStartParams{CWD: cwd}
	var result ThreadStartResult
	if err := c.call(ctx, methodThreadStart, params, &result); err != nil {
		return "", err
	}
	if result.ThreadID == "" {
		return "", fmt.Errorf("thread/start returned empty threadId")
	}
	return result.ThreadID, nil
}

// TurnStart starts a turn and returns turn id plus event stream.
func (c *Client) TurnStart(ctx context.Context, threadID, input string) (string, <-chan TurnEvent, error) {
	params := TurnStartParams{
		ThreadID: threadID,
		Input:    input,
	}
	var result TurnStartResult
	if err := c.call(ctx, methodTurnStart, params, &result); err != nil {
		return "", nil, err
	}
	if result.TurnID == "" {
		return "", nil, fmt.Errorf("turn/start returned empty turnId")
	}

	stream := c.registerTurnStream(result.TurnID)
	return result.TurnID, stream, nil
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
		if event.Type == TurnEventTypeCompleted {
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

func (c *Client) handleNotification(msg RPCMessage) {
	switch msg.Method {
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
	case notificationTurnCompleted:
		var note TurnCompletedNotification
		if err := json.Unmarshal(msg.Params, &note); err != nil {
			c.logger.Warn("ignore malformed turn/completed", slog.String("error", err.Error()))
			return
		}
		c.pushTurnEvent(note.TurnID, TurnEvent{
			Type:       TurnEventTypeCompleted,
			ThreadID:   note.ThreadID,
			TurnID:     note.TurnID,
			StopReason: note.StopReason,
		}, true)
	default:
		// Ignore notifications not used in PR1.
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

func (c *Client) failAll(err error) {
	c.mu.Lock()
	pending := c.pending
	streams := c.turnStreams
	c.pending = make(map[string]chan RPCMessage)
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
		case ch <- TurnEvent{Type: TurnEventTypeCompleted, StopReason: "error"}:
		default:
		}
		close(ch)
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
