// Command fake_claude_server is a minimal fake Anthropic API server for testing.
// It serves POST /v1/messages with SSE streaming, returning deterministic
// responses based on the prompt content.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

func main() {
	// Listen on a random port and print it to stdout for the test to read.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "fake_claude_server: listen: %v\n", err)
		os.Exit(1)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	// Print the port as the first line so test can parse it.
	fmt.Printf("%d\n", port)

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/messages", handleMessages)
	_ = http.Serve(ln, mux)
}

func handleMessages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}

	var req struct {
		Stream   bool              `json:"stream"`
		Messages []json.RawMessage `json:"messages"`
	}
	_ = json.Unmarshal(body, &req)

	// Extract the last user text to decide scenario.
	promptText := extractLastUserText(body)

	// Check for tool_use scenario.
	hasToolResult := strings.Contains(string(body), `"tool_result"`)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")

	if strings.Contains(promptText, "approval command") || strings.Contains(promptText, "approval mcp") {
		if !hasToolResult {
			// Return a tool_use block requesting approval.
			streamToolUseResponse(w, promptText)
			return
		}
		// Has tool_result — return final text response.
		streamTextResponse(w, "tool executed successfully")
		return
	}

	if strings.Contains(promptText, "slow response") {
		// Simulate slow streaming for cancel tests.
		streamSlowResponse(w)
		return
	}

	streamTextResponse(w, "Hello from Claude! This is a streaming response.")
}

func writeSSE(w io.Writer, eventType string, data any) {
	b, _ := json.Marshal(data)
	_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", eventType, string(b))
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

func streamTextResponse(w http.ResponseWriter, text string) {
	msgID := "msg_fake_" + fmt.Sprintf("%d", time.Now().UnixNano())

	// message_start
	writeSSE(w, "message_start", map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id":    msgID,
			"type":  "message",
			"role":  "assistant",
			"model": "claude-opus-4-6",
			"usage": map[string]any{"input_tokens": 10, "output_tokens": 0},
		},
	})

	// content_block_start (text)
	writeSSE(w, "content_block_start", map[string]any{
		"type":  "content_block_start",
		"index": 0,
		"content_block": map[string]any{
			"type": "text",
			"text": "",
		},
	})

	// Stream text in chunks.
	chunks := splitIntoChunks(text, 5)
	for _, chunk := range chunks {
		writeSSE(w, "content_block_delta", map[string]any{
			"type":  "content_block_delta",
			"index": 0,
			"delta": map[string]any{
				"type": "text_delta",
				"text": chunk,
			},
		})
	}

	// content_block_stop
	writeSSE(w, "content_block_stop", map[string]any{
		"type":  "content_block_stop",
		"index": 0,
	})

	// message_delta
	writeSSE(w, "message_delta", map[string]any{
		"type": "message_delta",
		"delta": map[string]any{
			"stop_reason":   "end_turn",
			"stop_sequence": nil,
		},
		"usage": map[string]any{"output_tokens": 10},
	})

	// message_stop
	writeSSE(w, "message_stop", map[string]any{
		"type": "message_stop",
	})
}

func streamToolUseResponse(w http.ResponseWriter, _ string) {
	msgID := "msg_fake_tool_" + fmt.Sprintf("%d", time.Now().UnixNano())
	toolID := "toolu_fake_" + fmt.Sprintf("%d", time.Now().UnixNano())

	writeSSE(w, "message_start", map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id":    msgID,
			"type":  "message",
			"role":  "assistant",
			"model": "claude-opus-4-6",
			"usage": map[string]any{"input_tokens": 10, "output_tokens": 0},
		},
	})

	// tool_use content_block_start
	writeSSE(w, "content_block_start", map[string]any{
		"type":  "content_block_start",
		"index": 0,
		"content_block": map[string]any{
			"type":  "tool_use",
			"id":    toolID,
			"name":  "bash",
			"input": map[string]any{},
		},
	})

	// input JSON delta
	writeSSE(w, "content_block_delta", map[string]any{
		"type":  "content_block_delta",
		"index": 0,
		"delta": map[string]any{
			"type":         "input_json_delta",
			"partial_json": `{"command":"echo hello"}`,
		},
	})

	// content_block_stop — this triggers the approval gate
	writeSSE(w, "content_block_stop", map[string]any{
		"type":  "content_block_stop",
		"index": 0,
	})

	writeSSE(w, "message_delta", map[string]any{
		"type": "message_delta",
		"delta": map[string]any{
			"stop_reason": "tool_use",
		},
		"usage": map[string]any{"output_tokens": 5},
	})

	writeSSE(w, "message_stop", map[string]any{
		"type": "message_stop",
	})
}

func streamSlowResponse(w http.ResponseWriter) {
	msgID := "msg_slow_" + fmt.Sprintf("%d", time.Now().UnixNano())

	writeSSE(w, "message_start", map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id":    msgID,
			"type":  "message",
			"role":  "assistant",
			"model": "claude-opus-4-6",
			"usage": map[string]any{"input_tokens": 5, "output_tokens": 0},
		},
	})

	writeSSE(w, "content_block_start", map[string]any{
		"type":  "content_block_start",
		"index": 0,
		"content_block": map[string]any{"type": "text", "text": ""},
	})

	// Emit a few chunks then sleep to allow cancel to arrive.
	writeSSE(w, "content_block_delta", map[string]any{
		"type":  "content_block_delta",
		"index": 0,
		"delta": map[string]any{"type": "text_delta", "text": "starting..."},
	})

	// Sleep to allow cancel; real HTTP connection cancellation will terminate us.
	time.Sleep(30 * time.Second)

	writeSSE(w, "content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
	writeSSE(w, "message_delta", map[string]any{
		"type":  "message_delta",
		"delta": map[string]any{"stop_reason": "end_turn"},
		"usage": map[string]any{"output_tokens": 5},
	})
	writeSSE(w, "message_stop", map[string]any{"type": "message_stop"})
}

func splitIntoChunks(text string, size int) []string {
	runes := []rune(text)
	var chunks []string
	for i := 0; i < len(runes); i += size {
		end := i + size
		if end > len(runes) {
			end = len(runes)
		}
		chunks = append(chunks, string(runes[i:end]))
	}
	return chunks
}

func extractLastUserText(body []byte) string {
	var req struct {
		Messages []struct {
			Role    string `json:"role"`
			Content any    `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return ""
	}
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role != "user" {
			continue
		}
		switch v := req.Messages[i].Content.(type) {
		case string:
			return v
		case []interface{}:
			for _, block := range v {
				if m, ok := block.(map[string]interface{}); ok {
					if t, ok := m["text"].(string); ok && t != "" {
						return t
					}
				}
			}
		}
	}
	return ""
}
