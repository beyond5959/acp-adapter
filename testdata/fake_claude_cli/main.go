// Command fake_claude_cli is a fake claude binary for integration testing.
// It accepts a subset of claude -p flags and emits deterministic stream-json output.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"
)

func main() {
	fs := flag.NewFlagSet("claude", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	prompt := fs.String("p", "", "prompt")
	outputFormat := fs.String("output-format", "", "output format")
	_ = fs.Bool("verbose", false, "verbose")
	_ = fs.Bool("include-partial-messages", false, "include partial messages")
	_ = fs.Bool("dangerously-skip-permissions", false, "skip permissions")
	sessionID := fs.String("session-id", "", "session id")
	resumeID := fs.String("resume", "", "resume session id")
	model := fs.String("model", "claude-opus-4-6", "model")
	_ = fs.Int("max-turns", 10, "max turns")
	_ = fs.String("allowedTools", "", "allowed tools")
	_ = fs.String("append-system-prompt", "", "append system prompt")

	if err := fs.Parse(os.Args[1:]); err != nil {
		if err == flag.ErrHelp {
			os.Exit(0)
		}
		os.Exit(1)
	}

	// Use session-id or resume as the effective session.
	effSession := *sessionID
	if effSession == "" {
		effSession = *resumeID
	}
	if effSession == "" {
		effSession = "fake-session-1"
	}

	_ = *outputFormat
	_ = *model

	p := strings.ToLower(*prompt)

	if strings.Contains(p, "slow response") {
		emitSlowResponse(effSession)
		return
	}

	if strings.Contains(p, "approval command") || strings.Contains(p, "approval mcp") {
		// In CLI mode with --dangerously-skip-permissions the tool is auto-executed;
		// we just return a normal text response indicating execution.
		emitTextResponse(effSession, "tool executed successfully")
		return
	}

	emitTextResponse(effSession, "Hello from Claude! This is a streaming response.")
}

func writeJSON(v any) {
	b, _ := json.Marshal(v)
	fmt.Println(string(b))
}

func emitTextResponse(sessionID, text string) {
	// system init event
	writeJSON(map[string]any{
		"type":       "system",
		"subtype":    "init",
		"session_id": sessionID,
	})

	// stream text in chunks via stream_event lines
	chunks := splitChunks(text, 5)
	for _, chunk := range chunks {
		writeJSON(map[string]any{
			"type": "stream_event",
			"event": map[string]any{
				"type": "content_block_delta",
				"delta": map[string]any{
					"type": "text_delta",
					"text": chunk,
				},
			},
		})
	}

	// result line
	writeJSON(map[string]any{
		"type":       "result",
		"subtype":    "success",
		"result":     text,
		"session_id": sessionID,
		"is_error":   false,
	})
}

func emitSlowResponse(sessionID string) {
	writeJSON(map[string]any{
		"type":       "system",
		"subtype":    "init",
		"session_id": sessionID,
	})

	// Emit start chunk.
	writeJSON(map[string]any{
		"type": "stream_event",
		"event": map[string]any{
			"type": "content_block_delta",
			"delta": map[string]any{
				"type": "text_delta",
				"text": "starting...",
			},
		},
	})

	// Sleep long enough that a cancel/kill will arrive first.
	time.Sleep(30 * time.Second)

	writeJSON(map[string]any{
		"type":       "result",
		"subtype":    "success",
		"result":     "done",
		"session_id": sessionID,
		"is_error":   false,
	})
}

func splitChunks(text string, size int) []string {
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
