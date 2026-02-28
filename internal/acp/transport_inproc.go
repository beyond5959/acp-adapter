package acp

import (
	"encoding/json"
	"fmt"
	"io"
	"sync"
)

// InProcTransport is an in-memory Transport implementation backed by channels.
//
// It is intended for embedded/in-process adapters and tests.
type InProcTransport struct {
	in             <-chan RPCMessage
	inStreamClosed <-chan struct{}
	out            chan<- RPCMessage

	localClosed chan struct{}
	peerClosed  <-chan struct{}
	closeOnce   sync.Once
}

var _ Transport = (*InProcTransport)(nil)

// NewInProcTransportPair creates two connected transport endpoints.
//
// Messages written by endpoint A are read by endpoint B, and vice versa.
func NewInProcTransportPair(buffer int) (*InProcTransport, *InProcTransport) {
	if buffer < 0 {
		buffer = 0
	}

	aToB := make(chan RPCMessage, buffer)
	bToA := make(chan RPCMessage, buffer)
	aClosed := make(chan struct{})
	bClosed := make(chan struct{})

	a := &InProcTransport{
		in:             bToA,
		inStreamClosed: bClosed,
		out:            aToB,
		localClosed:    aClosed,
		peerClosed:     bClosed,
	}
	b := &InProcTransport{
		in:             aToB,
		inStreamClosed: aClosed,
		out:            bToA,
		localClosed:    bClosed,
		peerClosed:     aClosed,
	}
	return a, b
}

// Close closes one endpoint.
//
// The peer will receive io.EOF from ReadMessage after draining pending inbound
// messages from this endpoint.
func (t *InProcTransport) Close() error {
	t.closeOnce.Do(func() {
		close(t.localClosed)
	})
	return nil
}

// ReadMessage reads one message from peer endpoint.
func (t *InProcTransport) ReadMessage() (RPCMessage, error) {
	for {
		select {
		case <-t.localClosed:
			return RPCMessage{}, io.EOF
		case msg := <-t.in:
			return msg, nil
		case <-t.inStreamClosed:
			select {
			case msg := <-t.in:
				return msg, nil
			default:
				return RPCMessage{}, io.EOF
			}
		}
	}
}

// WriteMessage writes one message to peer endpoint.
func (t *InProcTransport) WriteMessage(msg RPCMessage) error {
	if msg.JSONRPC == "" {
		msg.JSONRPC = "2.0"
	}

	select {
	case <-t.localClosed:
		return io.EOF
	case <-t.peerClosed:
		return io.EOF
	default:
	}

	select {
	case <-t.localClosed:
		return io.EOF
	case <-t.peerClosed:
		return io.EOF
	case t.out <- msg:
		return nil
	}
}

// WriteResult writes one JSON-RPC success response.
func (t *InProcTransport) WriteResult(id json.RawMessage, result any) error {
	var resultRaw json.RawMessage
	if result != nil {
		raw, err := json.Marshal(result)
		if err != nil {
			return fmt.Errorf("encode acp result: %w", err)
		}
		resultRaw = raw
	}
	return t.WriteMessage(RPCMessage{
		JSONRPC: "2.0",
		ID:      cloneRawMessage(id),
		Result:  resultRaw,
	})
}

// WriteError writes one JSON-RPC error response.
func (t *InProcTransport) WriteError(id json.RawMessage, code int, message string, data any) error {
	return t.WriteMessage(RPCMessage{
		JSONRPC: "2.0",
		ID:      cloneRawMessage(id),
		Error: &RPCError{
			Code:    code,
			Message: message,
			Data:    data,
		},
	})
}

// WriteNotification writes one JSON-RPC notification.
func (t *InProcTransport) WriteNotification(method string, params any) error {
	var paramsRaw json.RawMessage
	if params != nil {
		raw, err := json.Marshal(params)
		if err != nil {
			return fmt.Errorf("%s encode params: %w", method, err)
		}
		paramsRaw = raw
	}
	return t.WriteMessage(RPCMessage{
		JSONRPC: "2.0",
		Method:  method,
		Params:  paramsRaw,
	})
}
