package acp

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
)

// StdioCodec is newline-delimited ACP JSON-RPC codec.
type StdioCodec struct {
	reader *bufio.Reader
	writer *bufio.Writer
	trace  TraceFunc
	mu     sync.Mutex
}

// TraceFunc records one json-rpc line for debug tracing.
type TraceFunc func(direction string, payload []byte)

// NewStdioCodec binds ACP codec to stdin/stdout streams.
func NewStdioCodec(r io.Reader, w io.Writer) *StdioCodec {
	return NewStdioCodecWithTrace(r, w, nil)
}

// NewStdioCodecWithTrace binds ACP codec with optional line tracer.
func NewStdioCodecWithTrace(r io.Reader, w io.Writer, trace TraceFunc) *StdioCodec {
	return &StdioCodec{
		reader: bufio.NewReader(r),
		writer: bufio.NewWriter(w),
		trace:  trace,
	}
}

// ReadMessage reads one newline-delimited JSON-RPC envelope.
func (c *StdioCodec) ReadMessage() (RPCMessage, error) {
	for {
		line, err := c.reader.ReadString('\n')
		if err != nil {
			if errors.Is(err, io.EOF) && strings.TrimSpace(line) == "" {
				return RPCMessage{}, io.EOF
			}
			if strings.TrimSpace(line) == "" {
				return RPCMessage{}, err
			}
		}

		payload := strings.TrimSpace(line)
		if payload == "" {
			if err != nil {
				return RPCMessage{}, err
			}
			continue
		}

		var msg RPCMessage
		if unmarshalErr := json.Unmarshal([]byte(payload), &msg); unmarshalErr != nil {
			return RPCMessage{}, fmt.Errorf("decode acp json-rpc: %w", unmarshalErr)
		}
		if c.trace != nil {
			c.trace("in", []byte(payload))
		}
		return msg, nil
	}
}

// WriteMessage writes one compact JSON-RPC envelope plus trailing newline.
func (c *StdioCodec) WriteMessage(msg RPCMessage) error {
	if msg.JSONRPC == "" {
		msg.JSONRPC = "2.0"
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("encode acp json-rpc: %w", err)
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if _, err := c.writer.Write(data); err != nil {
		return fmt.Errorf("write acp payload: %w", err)
	}
	if err := c.writer.WriteByte('\n'); err != nil {
		return fmt.Errorf("write acp newline: %w", err)
	}
	if err := c.writer.Flush(); err != nil {
		return fmt.Errorf("flush acp payload: %w", err)
	}
	if c.trace != nil {
		c.trace("out", data)
	}
	return nil
}

// WriteResult writes a JSON-RPC success response.
func (c *StdioCodec) WriteResult(id json.RawMessage, result any) error {
	var resultRaw json.RawMessage
	if result != nil {
		raw, err := json.Marshal(result)
		if err != nil {
			return fmt.Errorf("encode acp result: %w", err)
		}
		resultRaw = raw
	}
	return c.WriteMessage(RPCMessage{
		JSONRPC: "2.0",
		ID:      cloneRawMessage(id),
		Result:  resultRaw,
	})
}

// WriteError writes a JSON-RPC error response.
func (c *StdioCodec) WriteError(id json.RawMessage, code int, message string, data any) error {
	return c.WriteMessage(RPCMessage{
		JSONRPC: "2.0",
		ID:      cloneRawMessage(id),
		Error: &RPCError{
			Code:    code,
			Message: message,
			Data:    data,
		},
	})
}

// WriteNotification writes a JSON-RPC notification (without id).
func (c *StdioCodec) WriteNotification(method string, params any) error {
	var paramsRaw json.RawMessage
	if params != nil {
		raw, err := json.Marshal(params)
		if err != nil {
			return fmt.Errorf("%s encode params: %w", method, err)
		}
		paramsRaw = raw
	}
	return c.WriteMessage(RPCMessage{
		JSONRPC: "2.0",
		Method:  method,
		Params:  paramsRaw,
	})
}

func cloneRawMessage(raw json.RawMessage) *json.RawMessage {
	cp := append(json.RawMessage(nil), raw...)
	return &cp
}
