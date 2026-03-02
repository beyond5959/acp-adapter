package claude

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/packages/ssestream"

	"github.com/beyond5959/codex-acp/internal/appserver"
)


// streamToEvents reads a Claude streaming response and converts it into
// TurnEvents on the provided channel. It handles text deltas, tool_use
// approval gates, and completion.
//
// The caller must close out or context cancellation terminates the stream.
func streamToEvents(
	ctx context.Context,
	stream *ssestream.Stream[anthropic.MessageStreamEventUnion],
	threadID, turnID string,
	approvals *approvalRegistry,
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

	var acc anthropic.Message
	var stopReason string

	for stream.Next() {
		if ctx.Err() != nil {
			_ = stream.Close()
			out <- appserver.TurnEvent{
				Type:       appserver.TurnEventTypeCompleted,
				ThreadID:   threadID,
				TurnID:     turnID,
				StopReason: "cancelled",
			}
			return
		}

		event := stream.Current()
		_ = acc.Accumulate(event)

		switch ev := event.AsAny().(type) {
		case anthropic.ContentBlockDeltaEvent:
			if delta, ok := ev.Delta.AsAny().(anthropic.TextDelta); ok && delta.Text != "" {
				out <- appserver.TurnEvent{
					Type:     appserver.TurnEventTypeAgentMessageDelta,
					ThreadID: threadID,
					TurnID:   turnID,
					ItemID:   turnID + "-msg",
					Delta:    delta.Text,
				}
			}

		case anthropic.ContentBlockStartEvent:
			cb := ev.ContentBlock
			if cb.Type == "tool_use" {
				// We will handle tool_use after the full block input is accumulated at
				// ContentBlockStopEvent — no action here.
			}

		case anthropic.ContentBlockStopEvent:
			// Check if the last accumulated content block is a tool_use.
			if len(acc.Content) == 0 {
				break
			}
			lastBlock := acc.Content[len(acc.Content)-1]
			if lastBlock.Type != "tool_use" {
				break
			}
			toolUse := lastBlock.AsToolUse()
			// Build the approval request.
			approvalID := fmt.Sprintf("%s-approval-%s", turnID, toolUse.ID)
			decisionCh := make(chan appserver.ApprovalDecision, 1)
			approvals.register(approvalID, decisionCh)

			inputBytes, _ := toolUse.Input.MarshalJSON()
			approvalReq := buildApprovalRequest(threadID, turnID, approvalID, toolUse.ID, toolUse.Name, inputBytes)

			out <- appserver.TurnEvent{
				Type:     appserver.TurnEventTypeApprovalRequired,
				ThreadID: threadID,
				TurnID:   turnID,
				Approval: approvalReq,
			}

			// Block until decision arrives or context is cancelled.
			var decision appserver.ApprovalDecision
			select {
			case decision = <-decisionCh:
			case <-ctx.Done():
				approvals.remove(approvalID)
				_ = stream.Close()
				out <- appserver.TurnEvent{
					Type:       appserver.TurnEventTypeCompleted,
					ThreadID:   threadID,
					TurnID:     turnID,
					StopReason: "cancelled",
				}
				return
			}
			approvals.remove(approvalID)

			// Emit tool result delta (decision text) so ACP layer has visible status.
			decisionText := fmt.Sprintf("[tool: %s, decision: %s]", toolUse.Name, decision)
			out <- appserver.TurnEvent{
				Type:     appserver.TurnEventTypeAgentMessageDelta,
				ThreadID: threadID,
				TurnID:   turnID,
				ItemID:   turnID + "-msg",
				Delta:    decisionText + "\n",
			}

			// We record the decision but the streaming response itself is
			// already in flight — Claude streaming doesn't allow injecting
			// tool results mid-stream.  We complete the current stream and
			// signal a synthetic stopReason so the caller can start a follow-up
			// turn with the tool result if approved.
			approvals.storeDecision(approvalID, decision, toolUse.ID, toolUse.Name, inputBytes)

		case anthropic.MessageDeltaEvent:
			if ev.Delta.StopReason != "" {
				stopReason = string(ev.Delta.StopReason)
			}
		}
	}

	if err := stream.Err(); err != nil {
		if ctx.Err() != nil {
			stopReason = "cancelled"
		} else {
			out <- appserver.TurnEvent{
				Type:     appserver.TurnEventTypeError,
				ThreadID: threadID,
				TurnID:   turnID,
				Message:  err.Error(),
			}
			return
		}
	}

	if stopReason == "" {
		if ctx.Err() != nil {
			stopReason = "cancelled"
		} else {
			stopReason = "end_turn"
		}
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

// mapStopReason converts Anthropic stop reasons to ACP stop reason strings.
func mapStopReason(anthropicReason string) string {
	switch anthropicReason {
	case "end_turn":
		return "end_turn"
	case "max_tokens":
		return "max_tokens"
	case "stop_sequence":
		return "end_turn"
	case "tool_use":
		return "end_turn"
	case "cancelled":
		return "cancelled"
	default:
		return "end_turn"
	}
}

// buildApprovalRequest constructs an appserver.ApprovalRequest from tool_use data.
func buildApprovalRequest(
	threadID, turnID, approvalID, toolUseID, toolName string,
	inputBytes []byte,
) appserver.ApprovalRequest {
	kind := appserver.ApprovalKindCommand
	var command, writePath, writeText, patch, host, mcpServer, mcpTool string
	var files []string

	// Infer approval kind from tool name conventions.
	switch toolName {
	case "bash", "execute", "run_command", "computer":
		kind = appserver.ApprovalKindCommand
		command = extractStringField(inputBytes, "command", "cmd", "input")
	case "str_replace_editor", "write_file", "text_editor":
		kind = appserver.ApprovalKindFile
		writePath = extractStringField(inputBytes, "path", "file_path")
		writeText = extractStringField(inputBytes, "new_str", "content", "file_text")
		patch = extractStringField(inputBytes, "patch")
		if writePath != "" {
			files = []string{writePath}
		}
	case "web_search", "web_fetch", "fetch":
		kind = appserver.ApprovalKindNetwork
		host = extractStringField(inputBytes, "host", "url", "query")
	default:
		// Treat unknown tools as MCP-style.
		kind = appserver.ApprovalKindMCP
		mcpServer = "claude"
		mcpTool = toolName
		command = string(inputBytes)
	}

	return appserver.ApprovalRequest{
		ThreadID:   threadID,
		TurnID:     turnID,
		ApprovalID: approvalID,
		ToolCallID: toolUseID,
		Kind:       kind,
		Command:    command,
		Files:      files,
		Host:       host,
		MCPServer:  mcpServer,
		MCPTool:    mcpTool,
		WritePath:  writePath,
		WriteText:  writeText,
		Patch:      patch,
		Message:    fmt.Sprintf("Claude tool call: %s", toolName),
	}
}

// extractStringField tries each key in order and returns the first non-empty string.
func extractStringField(input []byte, keys ...string) string {
	var m map[string]any
	if err := json.Unmarshal(input, &m); err != nil {
		return string(input)
	}
	for _, k := range keys {
		if v, ok := m[k]; ok {
			if s, ok := v.(string); ok && s != "" {
				return s
			}
		}
	}
	return ""
}
