package codex

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
)

// JSONLCodec reads/writes newline-delimited JSON-RPC messages.
type JSONLCodec struct {
	reader *bufio.Reader
	writer *bufio.Writer
	trace  TraceFunc
	mu     sync.Mutex
}

// TraceFunc records one json-rpc line for debug tracing.
type TraceFunc func(direction string, payload []byte)

// NewJSONLCodec creates a codec backed by stdin/stdout-like streams.
func NewJSONLCodec(r io.Reader, w io.Writer) *JSONLCodec {
	return NewJSONLCodecWithTrace(r, w, nil)
}

// NewJSONLCodecWithTrace creates a codec with optional line tracer.
func NewJSONLCodecWithTrace(r io.Reader, w io.Writer, trace TraceFunc) *JSONLCodec {
	return &JSONLCodec{
		reader: bufio.NewReader(r),
		writer: bufio.NewWriter(w),
		trace:  trace,
	}
}

// ReadMessage reads one complete JSON-RPC line.
func (c *JSONLCodec) ReadMessage() (RPCMessage, error) {
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
			return RPCMessage{}, fmt.Errorf("decode app-server jsonl: %w", unmarshalErr)
		}
		if c.trace != nil {
			c.trace("in", []byte(payload))
		}
		return msg, nil
	}
}

// WriteMessage writes one compact JSON-RPC message plus trailing newline.
func (c *JSONLCodec) WriteMessage(msg RPCMessage) error {
	if msg.JSONRPC == "" {
		msg.JSONRPC = "2.0"
	}

	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("encode app-server jsonl: %w", err)
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if _, err := c.writer.Write(data); err != nil {
		return fmt.Errorf("write app-server jsonl payload: %w", err)
	}
	if err := c.writer.WriteByte('\n'); err != nil {
		return fmt.Errorf("write app-server jsonl newline: %w", err)
	}
	if err := c.writer.Flush(); err != nil {
		return fmt.Errorf("flush app-server jsonl: %w", err)
	}
	if c.trace != nil {
		c.trace("out", data)
	}
	return nil
}
