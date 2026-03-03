package claude

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/beyond5959/acp-adapter/internal/appserver"
)

// streamLine is the top-level JSON object emitted by claude --output-format stream-json --verbose.
type streamLine struct {
	Type    string          `json:"type"`
	Subtype string          `json:"subtype"`
	Event   json.RawMessage `json:"event"`
	// For type=result
	Result  string `json:"result"`
	IsError bool   `json:"is_error"`
	// For type=assistant
	Message *assistantMsg `json:"message"`
}

type assistantMsg struct {
	Content    []contentBlock `json:"content"`
	StopReason string         `json:"stop_reason"`
}

type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// streamEvent wraps inner event from type=stream_event lines.
type streamEvent struct {
	Type  string        `json:"type"`
	Delta *streamDelta  `json:"delta"`
}

type streamDelta struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// streamToEvents reads lines from r (stdout of a claude -p subprocess) and emits
// TurnEvents on out. It closes out when done.
func streamToEvents(
	ctx context.Context,
	r io.Reader,
	threadID, turnID string,
	out chan<- appserver.TurnEvent,
) {
	defer close(out)

	out <- appserver.TurnEvent{
		Type:     appserver.TurnEventTypeStarted,
		ThreadID: threadID,
		TurnID:   turnID,
	}
	out <- appserver.TurnEvent{
		Type:     appserver.TurnEventTypeItemStarted,
		ThreadID: threadID,
		TurnID:   turnID,
		ItemID:   turnID + "-msg",
		ItemType: "agent_message",
	}

	scanner := bufio.NewScanner(r)
	// Increase buffer for large lines.
	scanner.Buffer(make([]byte, 1024*1024), 4*1024*1024)

	var stopReason string
	var streamedDeltas bool // true once we receive at least one stream_event delta

	for scanner.Scan() {
		if ctx.Err() != nil {
			emitCancelled(out, threadID, turnID)
			return
		}

		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var sl streamLine
		if err := json.Unmarshal(line, &sl); err != nil {
			// non-JSON line (e.g. debug output on stderr leaked) — skip
			continue
		}

		switch sl.Type {
		case "stream_event":
			var ev streamEvent
			if err := json.Unmarshal(sl.Event, &ev); err != nil {
				continue
			}
			if ev.Type == "content_block_delta" && ev.Delta != nil &&
				ev.Delta.Type == "text_delta" && ev.Delta.Text != "" {
				streamedDeltas = true
				out <- appserver.TurnEvent{
					Type:     appserver.TurnEventTypeAgentMessageDelta,
					ThreadID: threadID,
					TurnID:   turnID,
					ItemID:   turnID + "-msg",
					Delta:    ev.Delta.Text,
				}
			}

		case "assistant":
			// Emitted when --include-partial-messages is not set; also emitted as
			// the final accumulated message. Skip text emission if we already
			// streamed deltas via stream_event to avoid duplicate output.
			if sl.Message != nil {
				if !streamedDeltas {
					for _, cb := range sl.Message.Content {
						if cb.Type == "text" && cb.Text != "" {
							out <- appserver.TurnEvent{
								Type:     appserver.TurnEventTypeAgentMessageDelta,
								ThreadID: threadID,
								TurnID:   turnID,
								ItemID:   turnID + "-msg",
								Delta:    cb.Text,
							}
						}
					}
				}
				if sl.Message.StopReason != "" {
					stopReason = sl.Message.StopReason
				}
			}

		case "result":
			if sl.IsError {
				out <- appserver.TurnEvent{
					Type:     appserver.TurnEventTypeError,
					ThreadID: threadID,
					TurnID:   turnID,
					Message:  fmt.Sprintf("claude cli error: %s", sl.Result),
				}
				out <- appserver.TurnEvent{
					Type:       appserver.TurnEventTypeCompleted,
					ThreadID:   threadID,
					TurnID:     turnID,
					StopReason: "cancelled",
				}
				return
			}
			if stopReason == "" {
				stopReason = "end_turn"
			}
			// result line ends the stream; fall through to emit completed below.
			goto done

		case "system":
			// init event — ignore

		default:
			// unknown line type — ignore
		}
	}

	if err := scanner.Err(); err != nil {
		if ctx.Err() != nil {
			emitCancelled(out, threadID, turnID)
			return
		}
		out <- appserver.TurnEvent{
			Type:     appserver.TurnEventTypeError,
			ThreadID: threadID,
			TurnID:   turnID,
			Message:  err.Error(),
		}
		out <- appserver.TurnEvent{
			Type:       appserver.TurnEventTypeCompleted,
			ThreadID:   threadID,
			TurnID:     turnID,
			StopReason: "cancelled",
		}
		return
	}

done:
	if ctx.Err() != nil {
		emitCancelled(out, threadID, turnID)
		return
	}

	if stopReason == "" {
		stopReason = "end_turn"
	}

	out <- appserver.TurnEvent{
		Type:     appserver.TurnEventTypeItemCompleted,
		ThreadID: threadID,
		TurnID:   turnID,
		ItemID:   turnID + "-msg",
		ItemType: "agent_message",
	}
	out <- appserver.TurnEvent{
		Type:       appserver.TurnEventTypeCompleted,
		ThreadID:   threadID,
		TurnID:     turnID,
		StopReason: mapStopReason(stopReason),
	}
}

func emitCancelled(out chan<- appserver.TurnEvent, threadID, turnID string) {
	out <- appserver.TurnEvent{
		Type:       appserver.TurnEventTypeCompleted,
		ThreadID:   threadID,
		TurnID:     turnID,
		StopReason: "cancelled",
	}
}

func mapStopReason(reason string) string {
	switch reason {
	case "end_turn", "max_tokens", "cancelled":
		return reason
	default:
		return "end_turn"
	}
}
