package piacp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/beyond5959/acp-adapter/internal/acp"
)

var (
	errEmbeddedAlreadyStarted = errors.New("embedded runtime already started")
	errEmbeddedNotStarted     = errors.New("embedded runtime is not started")
	errEmbeddedClosed         = errors.New("embedded runtime is closed")
	errInvalidRequest         = errors.New("client request must include id and method")
)

// RPCMessage is the embedded runtime JSON-RPC envelope.
type RPCMessage struct {
	JSONRPC string           `json:"jsonrpc,omitempty"`
	ID      *json.RawMessage `json:"id,omitempty"`
	Method  string           `json:"method,omitempty"`
	Params  json.RawMessage  `json:"params,omitempty"`
	Result  json.RawMessage  `json:"result,omitempty"`
	Error   *RPCError        `json:"error,omitempty"`
}

// RPCError is one JSON-RPC error object.
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// PermissionDecision is one session/request_permission response payload.
type PermissionDecision struct {
	Outcome          string `json:"outcome,omitempty"`
	SelectedOptionID string `json:"-"`
	Decision         string `json:"decision,omitempty"`
	Approved         *bool  `json:"approved,omitempty"`
}

// MarshalJSON keeps the legacy string outcome shape while allowing the ACP
// standard selected-option object outcome.
func (d PermissionDecision) MarshalJSON() ([]byte, error) {
	payload := map[string]any{}
	if d.SelectedOptionID != "" {
		payload["outcome"] = map[string]any{
			"outcome":  "selected",
			"optionId": d.SelectedOptionID,
		}
	} else if d.Outcome != "" {
		payload["outcome"] = d.Outcome
	}
	if d.Decision != "" {
		payload["decision"] = d.Decision
	}
	if d.Approved != nil {
		payload["approved"] = d.Approved
	}
	return json.Marshal(payload)
}

// EmbeddedRuntime hosts a Pi ACP server in-process using in-memory transport.
type EmbeddedRuntime struct {
	cfg    RuntimeConfig
	stderr io.Writer

	serverTransport *acp.InProcTransport
	clientTransport *acp.InProcTransport

	mu        sync.Mutex
	started   bool
	closed    bool
	cancel    context.CancelFunc
	runDone   chan error
	runErr    error
	routeDone chan struct{}

	stopCh   chan struct{}
	stopOnce sync.Once

	pendingMu sync.Mutex
	pending   map[string]chan RPCMessage

	subsMu     sync.RWMutex
	subs       map[uint64]chan RPCMessage
	nextSubID  uint64
	closeSubMu sync.Once
}

// NewEmbeddedRuntime creates one embedded Pi runtime instance.
func NewEmbeddedRuntime(cfg RuntimeConfig) *EmbeddedRuntime {
	serverTransport, clientTransport := acp.NewInProcTransportPair(256)
	return &EmbeddedRuntime{
		cfg:             cfg,
		stderr:          os.Stderr,
		serverTransport: serverTransport,
		clientTransport: clientTransport,
		stopCh:          make(chan struct{}),
		pending:         make(map[string]chan RPCMessage),
		subs:            make(map[uint64]chan RPCMessage),
	}
}

// Start starts the ACP server loop on the inproc transport.
func (r *EmbeddedRuntime) Start(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return errEmbeddedClosed
	}
	if r.started {
		return errEmbeddedAlreadyStarted
	}

	runCtx, cancel := context.WithCancel(ctx)
	r.cancel = cancel
	r.runDone = make(chan error, 1)
	r.routeDone = make(chan struct{})
	r.started = true

	go r.routeMessages()
	go r.runServer(runCtx)
	return nil
}

// ClientRequest sends one client-side request and waits for its response.
func (r *EmbeddedRuntime) ClientRequest(ctx context.Context, msg RPCMessage) (RPCMessage, error) {
	if err := r.requireStarted(); err != nil {
		return RPCMessage{}, err
	}
	if msg.ID == nil || msg.Method == "" {
		return RPCMessage{}, errInvalidRequest
	}

	request := toACPMessage(msg)
	requestID := normalizeID(*request.ID)

	responseCh := make(chan RPCMessage, 1)
	r.pendingMu.Lock()
	if _, exists := r.pending[requestID]; exists {
		r.pendingMu.Unlock()
		return RPCMessage{}, fmt.Errorf("duplicate request id: %s", requestID)
	}
	r.pending[requestID] = responseCh
	r.pendingMu.Unlock()

	if err := r.clientTransport.WriteMessage(request); err != nil {
		r.removePending(requestID)
		return RPCMessage{}, err
	}

	runClosed := r.runDoneChannel()
	select {
	case response, ok := <-responseCh:
		if !ok {
			return RPCMessage{}, errEmbeddedClosed
		}
		return response, nil
	case <-ctx.Done():
		r.removePending(requestID)
		return RPCMessage{}, ctx.Err()
	case <-runClosed:
		r.removePending(requestID)
		return RPCMessage{}, r.currentRunError()
	}
}

// SubscribeUpdates subscribes to server notifications and server-initiated requests.
func (r *EmbeddedRuntime) SubscribeUpdates(buffer int) (<-chan RPCMessage, func()) {
	if buffer <= 0 {
		buffer = 32
	}
	ch := make(chan RPCMessage, buffer)

	r.subsMu.Lock()
	id := r.nextSubID
	r.nextSubID++
	r.subs[id] = ch
	r.subsMu.Unlock()

	unsubscribe := func() {
		r.subsMu.Lock()
		target, ok := r.subs[id]
		if ok {
			delete(r.subs, id)
			close(target)
		}
		r.subsMu.Unlock()
	}
	return ch, unsubscribe
}

// RespondPermission sends a permission decision back to the ACP server.
func (r *EmbeddedRuntime) RespondPermission(
	ctx context.Context,
	requestID json.RawMessage,
	decision PermissionDecision,
) error {
	if err := r.requireStarted(); err != nil {
		return err
	}
	if len(requestID) == 0 {
		return errors.New("request id is required")
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	return r.clientTransport.WriteResult(requestID, decision)
}

// Close stops the runtime and releases all resources.
func (r *EmbeddedRuntime) Close() error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	cancel := r.cancel
	runDone := r.runDone
	routeDone := r.routeDone
	r.mu.Unlock()

	r.stopOnce.Do(func() { close(r.stopCh) })
	if cancel != nil {
		cancel()
	}
	_ = r.clientTransport.Close()
	_ = r.serverTransport.Close()

	if routeDone != nil {
		<-routeDone
	}
	if runDone != nil {
		<-runDone
	}

	r.failPending()
	r.closeSubscribers()
	return nil
}

func (r *EmbeddedRuntime) runServer(ctx context.Context) {
	err := runRuntime(ctx, r.cfg, r.stderr, func(_ acp.TraceFunc) acp.Transport {
		return r.serverTransport
	})

	r.mu.Lock()
	r.runErr = err
	runDone := r.runDone
	r.mu.Unlock()

	if runDone != nil {
		runDone <- err
		close(runDone)
	}
}

func (r *EmbeddedRuntime) routeMessages() {
	defer close(r.routeDone)
	for {
		msg, err := r.clientTransport.ReadMessage()
		if err != nil {
			r.failPending()
			return
		}

		if msg.ID != nil && msg.Method == "" {
			id := normalizeID(*msg.ID)
			r.pendingMu.Lock()
			ch, ok := r.pending[id]
			if ok {
				delete(r.pending, id)
			}
			r.pendingMu.Unlock()
			if ok {
				select {
				case ch <- fromACPMessage(msg):
				default:
				}
				close(ch)
			}
			continue
		}
		r.broadcast(fromACPMessage(msg))
	}
}

func (r *EmbeddedRuntime) broadcast(msg RPCMessage) {
	r.subsMu.RLock()
	defer r.subsMu.RUnlock()
	for _, sub := range r.subs {
		select {
		case sub <- msg:
		default:
		}
	}
}

func (r *EmbeddedRuntime) removePending(id string) {
	r.pendingMu.Lock()
	if ch, ok := r.pending[id]; ok {
		delete(r.pending, id)
		close(ch)
	}
	r.pendingMu.Unlock()
}

func (r *EmbeddedRuntime) failPending() {
	r.pendingMu.Lock()
	defer r.pendingMu.Unlock()
	for id, ch := range r.pending {
		delete(r.pending, id)
		close(ch)
	}
}

func (r *EmbeddedRuntime) closeSubscribers() {
	r.closeSubMu.Do(func() {
		r.subsMu.Lock()
		for id, ch := range r.subs {
			delete(r.subs, id)
			close(ch)
		}
		r.subsMu.Unlock()
	})
}

func (r *EmbeddedRuntime) requireStarted() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return errEmbeddedClosed
	}
	if !r.started {
		return errEmbeddedNotStarted
	}
	return nil
}

func (r *EmbeddedRuntime) runDoneChannel() <-chan error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.runDone == nil {
		closed := make(chan error)
		close(closed)
		return closed
	}
	return r.runDone
}

func (r *EmbeddedRuntime) currentRunError() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.runErr != nil {
		return r.runErr
	}
	return errEmbeddedClosed
}

func toACPMessage(msg RPCMessage) acp.RPCMessage {
	return acp.RPCMessage{
		JSONRPC: msg.JSONRPC,
		ID:      msg.ID,
		Method:  msg.Method,
		Params:  msg.Params,
		Result:  msg.Result,
		Error:   (*acp.RPCError)(msg.Error),
	}
}

func fromACPMessage(msg acp.RPCMessage) RPCMessage {
	return RPCMessage{
		JSONRPC: msg.JSONRPC,
		ID:      msg.ID,
		Method:  msg.Method,
		Params:  msg.Params,
		Result:  msg.Result,
		Error:   (*RPCError)(msg.Error),
	}
}

func normalizeID(raw json.RawMessage) string {
	return string(raw)
}
