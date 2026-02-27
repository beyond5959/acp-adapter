package observability

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

// JSONTraceFile writes redacted ACP/AppServer protocol traces as JSONL.
type JSONTraceFile struct {
	mu   sync.Mutex
	file *os.File
	path string
}

// NewJSONTraceFile opens one trace output file in append mode.
func NewJSONTraceFile(path string) (*JSONTraceFile, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open trace file: %w", err)
	}
	return &JSONTraceFile{
		file: file,
		path: path,
	}, nil
}

// Path returns the trace file path.
func (t *JSONTraceFile) Path() string {
	return t.path
}

// Close closes the trace file.
func (t *JSONTraceFile) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.file == nil {
		return nil
	}
	err := t.file.Close()
	t.file = nil
	return err
}

// TraceACP records one ACP protocol line.
func (t *JSONTraceFile) TraceACP(direction string, payload []byte) {
	t.trace("acp", direction, payload)
}

// TraceAppServer records one app-server protocol line.
func (t *JSONTraceFile) TraceAppServer(direction string, payload []byte) {
	t.trace("appserver", direction, payload)
}

func (t *JSONTraceFile) trace(stream string, direction string, payload []byte) {
	if t == nil || len(payload) == 0 {
		return
	}

	entry := map[string]any{
		"ts":        time.Now().UTC().Format(time.RFC3339Nano),
		"stream":    stream,
		"direction": direction,
		"payload":   json.RawMessage(sanitizePayload(payload)),
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	if t.file == nil {
		return
	}
	_, _ = t.file.Write(append(data, '\n'))
}

func sanitizePayload(payload []byte) []byte {
	var v any
	if err := json.Unmarshal(payload, &v); err != nil {
		raw, marshalErr := json.Marshal(string(payload))
		if marshalErr != nil {
			return []byte(`"<invalid>"`)
		}
		return raw
	}

	redacted := redactValue("", v)
	out, err := json.Marshal(redacted)
	if err != nil {
		raw, marshalErr := json.Marshal(string(payload))
		if marshalErr != nil {
			return []byte(`"<invalid>"`)
		}
		return raw
	}
	return out
}

func redactValue(parentKey string, v any) any {
	switch value := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(value))
		for key, next := range value {
			if isSensitiveKey(key) {
				out[key] = "[REDACTED]"
				continue
			}
			out[key] = redactValue(key, next)
		}
		return out
	case []any:
		out := make([]any, len(value))
		for i, next := range value {
			out[i] = redactValue(parentKey, next)
		}
		return out
	case string:
		if isSensitiveKey(parentKey) && strings.TrimSpace(value) != "" {
			return "[REDACTED]"
		}
		if looksLikeSecret(value) {
			return "[REDACTED]"
		}
		return value
	default:
		return value
	}
}

func isSensitiveKey(key string) bool {
	normalized := strings.ToLower(strings.TrimSpace(key))
	if normalized == "" {
		return false
	}
	sensitiveTokens := []string{
		"api_key",
		"apikey",
		"authorization",
		"token",
		"secret",
		"password",
		"cookie",
		"set-cookie",
		"access_token",
		"refresh_token",
	}
	for _, token := range sensitiveTokens {
		if strings.Contains(normalized, token) {
			return true
		}
	}
	return false
}

func looksLikeSecret(value string) bool {
	trimmed := strings.TrimSpace(strings.ToLower(value))
	if trimmed == "" {
		return false
	}
	if strings.HasPrefix(trimmed, "sk-") || strings.HasPrefix(trimmed, "bearer ") {
		return true
	}
	if len(trimmed) >= 48 && strings.Count(trimmed, ".") >= 2 {
		// JWT-like token.
		return true
	}
	return false
}
