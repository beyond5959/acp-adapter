package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/beyond5959/acp-adapter/internal/bridge"
	"github.com/beyond5959/acp-adapter/internal/codex"
)

func TestServerStdioBaselineInitializeNewPrompt(t *testing.T) {
	t.Parallel()

	clientToServerReader, clientToServerWriter := io.Pipe()
	serverToClientReader, serverToClientWriter := io.Pipe()
	defer func() {
		_ = clientToServerReader.Close()
		_ = clientToServerWriter.Close()
		_ = serverToClientReader.Close()
		_ = serverToClientWriter.Close()
	}()

	mockApp := &stdioMockAppClient{}
	server := NewServer(
		NewStdioCodec(clientToServerReader, serverToClientWriter),
		mockApp,
		bridge.NewStore(),
		slog.New(slog.NewJSONHandler(io.Discard, nil)),
		ServerOptions{
			PatchApplyMode:   "appserver",
			RetryTurnOnCrash: true,
			InitialAuthMode:  "chatgpt_subscription",
		},
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serveErrCh := make(chan error, 1)
	go func() {
		serveErrCh <- server.Serve(ctx)
	}()

	msgCh := make(chan RPCMessage, 64)
	readErrCh := make(chan error, 1)
	go scanRPCStream(serverToClientReader, msgCh, readErrCh)

	writeRPCRequest(t, clientToServerWriter, "1", "initialize", map[string]any{
		"protocolVersion": 1,
	})
	initResponse := mustReadRPCMessage(t, msgCh, readErrCh, 2*time.Second)
	if initResponse.Error != nil {
		t.Fatalf("initialize failed: %+v", initResponse.Error)
	}
	var initPayload InitializeResult
	if err := json.Unmarshal(initResponse.Result, &initPayload); err != nil {
		t.Fatalf("decode initialize result: %v", err)
	}
	if initPayload.ProtocolVersion != 1 {
		t.Fatalf("protocolVersion=%d, want 1", initPayload.ProtocolVersion)
	}

	writeRPCRequest(t, clientToServerWriter, "2", "session/new", map[string]any{
		"cwd": "/tmp/workspace",
	})
	newResponse := mustReadRPCMessage(t, msgCh, readErrCh, 2*time.Second)
	if newResponse.Error != nil {
		t.Fatalf("session/new failed: %+v", newResponse.Error)
	}
	var newPayload SessionNewResult
	if err := json.Unmarshal(newResponse.Result, &newPayload); err != nil {
		t.Fatalf("decode session/new result: %v", err)
	}
	if strings.TrimSpace(newPayload.SessionID) == "" {
		t.Fatalf("session/new returned empty sessionId")
	}

	writeRPCRequest(t, clientToServerWriter, "3", "session/prompt", map[string]any{
		"sessionId": newPayload.SessionID,
		"prompt":    "hello",
	})

	updateCount := 0
	for {
		msg := mustReadRPCMessage(t, msgCh, readErrCh, 2*time.Second)
		if msg.Method == methodSessionUpdate {
			updateCount++
			var update SessionUpdateParams
			if err := json.Unmarshal(msg.Params, &update); err != nil {
				t.Fatalf("decode session/update params: %v", err)
			}
			if update.SessionID != newPayload.SessionID {
				t.Fatalf("session/update sessionId=%q, want %q", update.SessionID, newPayload.SessionID)
			}
			continue
		}
		if messageIDString(msg.ID) != "3" {
			continue
		}
		if msg.Error != nil {
			t.Fatalf("session/prompt failed: %+v", msg.Error)
		}
		var promptResult SessionPromptResult
		if err := json.Unmarshal(msg.Result, &promptResult); err != nil {
			t.Fatalf("decode session/prompt result: %v", err)
		}
		if promptResult.StopReason != "end_turn" {
			t.Fatalf("stopReason=%q, want end_turn", promptResult.StopReason)
		}
		break
	}
	if updateCount == 0 {
		t.Fatalf("expected >=1 session/update notification")
	}

	_ = clientToServerWriter.Close()
	select {
	case err := <-serveErrCh:
		if err != nil {
			t.Fatalf("server serve error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for server shutdown")
	}
}

func TestBuildSessionUpdatePayloadPlan(t *testing.T) {
	t.Parallel()

	payload := buildSessionUpdatePayload(SessionUpdateParams{
		SessionID: "session-1",
		TurnID:    "turn-1",
		Type:      sessionUpdateTypePlan,
		Plan: []PlanEntry{
			{
				Content:  "capture requirements",
				Priority: "medium",
				Status:   "pending",
			},
		},
	})

	update, ok := payload["update"].(map[string]any)
	if !ok {
		t.Fatalf("payload missing update envelope: %+v", payload)
	}
	if got, _ := update["sessionUpdate"].(string); got != sessionUpdateTypePlan {
		t.Fatalf("update.sessionUpdate=%q, want %s", got, sessionUpdateTypePlan)
	}
	entries, ok := update["entries"].([]PlanEntry)
	if !ok {
		t.Fatalf("update.entries has unexpected type %T", update["entries"])
	}
	if len(entries) != 1 || entries[0].Content != "capture requirements" {
		t.Fatalf("update.entries mismatch: %+v", entries)
	}
	topLevel, ok := payload["plan"].([]PlanEntry)
	if !ok {
		t.Fatalf("payload.plan has unexpected type %T", payload["plan"])
	}
	if len(topLevel) != 1 || topLevel[0].Status != "pending" {
		t.Fatalf("payload.plan mismatch: %+v", topLevel)
	}
}

func TestBuildSessionUpdatePayloadAvailableCommands(t *testing.T) {
	t.Parallel()

	payload := buildSessionUpdatePayload(SessionUpdateParams{
		SessionID: "session-1",
		Type:      sessionUpdateTypeAvailableCommands,
		AvailableCommands: []AvailableCommand{
			{
				Name:        "review",
				Description: "Review workspace changes.",
			},
			{
				Name:        "review-branch",
				Description: "Review a branch diff.",
				Input: &AvailableCommandInput{
					Hint: "<branch>",
				},
			},
		},
	})

	update, ok := payload["update"].(map[string]any)
	if !ok {
		t.Fatalf("payload missing update envelope: %+v", payload)
	}
	if got, _ := update["sessionUpdate"].(string); got != sessionUpdateTypeAvailableCommands {
		t.Fatalf("update.sessionUpdate=%q, want %s", got, sessionUpdateTypeAvailableCommands)
	}
	entries, ok := update["availableCommands"].([]AvailableCommand)
	if !ok {
		t.Fatalf("update.availableCommands has unexpected type %T", update["availableCommands"])
	}
	if len(entries) != 2 || entries[1].Input == nil || entries[1].Input.Hint != "<branch>" {
		t.Fatalf("update.availableCommands mismatch: %+v", entries)
	}
	topLevel, ok := payload["availableCommands"].([]AvailableCommand)
	if !ok {
		t.Fatalf("payload.availableCommands has unexpected type %T", payload["availableCommands"])
	}
	if len(topLevel) != 2 || topLevel[0].Name != "review" {
		t.Fatalf("payload.availableCommands mismatch: %+v", topLevel)
	}
}

func TestBuildSessionUpdatePayloadUsageUpdate(t *testing.T) {
	t.Parallel()

	used := int64(53000)
	size := int64(200000)
	payload := buildSessionUpdatePayload(SessionUpdateParams{
		SessionID: "session-1",
		TurnID:    "turn-1",
		Type:      sessionUpdateTypeUsage,
		Used:      &used,
		Size:      &size,
	})

	update, ok := payload["update"].(map[string]any)
	if !ok {
		t.Fatalf("payload missing update envelope: %+v", payload)
	}
	if got, _ := update["sessionUpdate"].(string); got != sessionUpdateTypeUsage {
		t.Fatalf("update.sessionUpdate=%q, want %s", got, sessionUpdateTypeUsage)
	}
	if got, _ := update["used"].(int64); got != used {
		t.Fatalf("update.used=%v, want %d", update["used"], used)
	}
	if got, _ := update["size"].(int64); got != size {
		t.Fatalf("update.size=%v, want %d", update["size"], size)
	}
	if got, _ := payload["used"].(int64); got != used {
		t.Fatalf("payload.used=%v, want %d", payload["used"], used)
	}
	if got, _ := payload["size"].(int64); got != size {
		t.Fatalf("payload.size=%v, want %d", payload["size"], size)
	}
}

func TestBuildSessionUpdatePayloadToolCallContent(t *testing.T) {
	t.Parallel()

	payload := buildSessionUpdatePayload(SessionUpdateParams{
		SessionID:  "session-1",
		TurnID:     "turn-1",
		Type:       sessionUpdateTypeToolCall,
		Status:     "completed",
		ToolCallID: "tool-1",
		ToolCallContent: []ToolCallContentItem{
			{
				Type: "content",
				Content: &PromptContentBlock{
					Type:     "image",
					Data:     "ZmFrZQ==",
					MimeType: "image/png",
				},
			},
			{
				Type:    "diff",
				Path:    "/workspace/docs/README.md",
				OldText: "old line\n",
				NewText: "new line\n",
			},
		},
	})

	update, ok := payload["update"].(map[string]any)
	if !ok {
		t.Fatalf("payload missing update envelope: %+v", payload)
	}
	if got, _ := update["sessionUpdate"].(string); got != sessionUpdateTypeToolCall {
		t.Fatalf("update.sessionUpdate=%q, want %s", got, sessionUpdateTypeToolCall)
	}
	content, ok := update["content"].([]ToolCallContentItem)
	if !ok {
		t.Fatalf("update.content has unexpected type %T", update["content"])
	}
	if len(content) != 2 || content[0].Content == nil || content[0].Content.Type != "image" || content[1].Type != "diff" {
		t.Fatalf("update.content mismatch: %+v", content)
	}
	topLevel, ok := payload["toolCallContent"].([]ToolCallContentItem)
	if !ok {
		t.Fatalf("payload.toolCallContent has unexpected type %T", payload["toolCallContent"])
	}
	if len(topLevel) != 2 || topLevel[0].Content == nil || topLevel[0].Content.MimeType != "image/png" || topLevel[1].Path != "/workspace/docs/README.md" {
		t.Fatalf("payload.toolCallContent mismatch: %+v", topLevel)
	}
}

func TestServerStdioSessionListIncludesLiveSessionBeforeThreadListHistory(t *testing.T) {
	t.Parallel()

	clientToServerReader, clientToServerWriter := io.Pipe()
	serverToClientReader, serverToClientWriter := io.Pipe()
	defer func() {
		_ = clientToServerReader.Close()
		_ = clientToServerWriter.Close()
		_ = serverToClientReader.Close()
		_ = serverToClientWriter.Close()
	}()

	mockApp := &stdioMockAppClient{}
	server := NewServer(
		NewStdioCodec(clientToServerReader, serverToClientWriter),
		mockApp,
		bridge.NewStore(),
		slog.New(slog.NewJSONHandler(io.Discard, nil)),
		ServerOptions{
			PatchApplyMode:   "appserver",
			RetryTurnOnCrash: true,
			InitialAuthMode:  "chatgpt_subscription",
		},
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serveErrCh := make(chan error, 1)
	go func() {
		serveErrCh <- server.Serve(ctx)
	}()

	msgCh := make(chan RPCMessage, 64)
	readErrCh := make(chan error, 1)
	go scanRPCStream(serverToClientReader, msgCh, readErrCh)

	writeRPCRequest(t, clientToServerWriter, "1", "initialize", map[string]any{
		"protocolVersion": 1,
	})
	initResponse := mustReadRPCMessage(t, msgCh, readErrCh, 2*time.Second)
	if initResponse.Error != nil {
		t.Fatalf("initialize failed: %+v", initResponse.Error)
	}

	writeRPCRequest(t, clientToServerWriter, "2", "session/new", map[string]any{
		"cwd": "/tmp/live-session",
	})
	newResponse := mustReadRPCMessage(t, msgCh, readErrCh, 2*time.Second)
	if newResponse.Error != nil {
		t.Fatalf("session/new failed: %+v", newResponse.Error)
	}
	var newPayload SessionNewResult
	if err := json.Unmarshal(newResponse.Result, &newPayload); err != nil {
		t.Fatalf("decode session/new result: %v", err)
	}
	if strings.TrimSpace(newPayload.SessionID) == "" {
		t.Fatalf("session/new returned empty sessionId")
	}

	writeRPCRequest(t, clientToServerWriter, "3", "session/list", map[string]any{
		"cwd": "/tmp/live-session",
	})
	var listResponse RPCMessage
	for {
		msg := mustReadRPCMessage(t, msgCh, readErrCh, 2*time.Second)
		if messageIDString(msg.ID) != "3" {
			continue
		}
		listResponse = msg
		break
	}
	if listResponse.Error != nil {
		t.Fatalf("session/list failed: %+v", listResponse.Error)
	}
	var listPayload SessionListResult
	if err := json.Unmarshal(listResponse.Result, &listPayload); err != nil {
		t.Fatalf("decode session/list result: %v", err)
	}
	if len(listPayload.Sessions) != 1 {
		t.Fatalf("len(session/list.sessions)=%d, want 1", len(listPayload.Sessions))
	}
	if got := strings.TrimSpace(listPayload.Sessions[0].SessionID); got != newPayload.SessionID {
		t.Fatalf("session/list sessionId=%q, want %q", got, newPayload.SessionID)
	}
	if got := strings.TrimSpace(listPayload.Sessions[0].CWD); got != "/tmp/live-session" {
		t.Fatalf("session/list cwd=%q, want %q", got, "/tmp/live-session")
	}
	if got := strings.TrimSpace(fmt.Sprint(listPayload.Sessions[0].Meta["threadId"])); got != "thread-1" {
		t.Fatalf("session/list _meta.threadId=%q, want %q", got, "thread-1")
	}

	_ = clientToServerWriter.Close()
	select {
	case err := <-serveErrCh:
		if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, context.Canceled) {
			t.Fatalf("server exited with error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for server shutdown")
	}
}

type stdioMockAppClient struct{}

func (m *stdioMockAppClient) ThreadStart(ctx context.Context, cwd string, options codex.RunOptions) (string, error) {
	return "thread-1", nil
}

func (m *stdioMockAppClient) TurnStart(
	ctx context.Context,
	threadID string,
	input []codex.UserInput,
	options codex.RunOptions,
) (string, <-chan codex.TurnEvent, error) {
	events := make(chan codex.TurnEvent, 5)
	turnID := "turn-1"
	go func() {
		defer close(events)
		events <- codex.TurnEvent{
			Type:     codex.TurnEventTypeStarted,
			ThreadID: threadID,
			TurnID:   turnID,
		}
		events <- codex.TurnEvent{
			Type:     codex.TurnEventTypeItemStarted,
			ThreadID: threadID,
			TurnID:   turnID,
			ItemID:   "item-1",
			ItemType: "agent_message",
		}
		events <- codex.TurnEvent{
			Type:     codex.TurnEventTypeAgentMessageDelta,
			ThreadID: threadID,
			TurnID:   turnID,
			ItemID:   "item-1",
			Delta:    "hello from mock",
		}
		events <- codex.TurnEvent{
			Type:     codex.TurnEventTypeItemCompleted,
			ThreadID: threadID,
			TurnID:   turnID,
			ItemID:   "item-1",
			ItemType: "agent_message",
		}
		events <- codex.TurnEvent{
			Type:       codex.TurnEventTypeCompleted,
			ThreadID:   threadID,
			TurnID:     turnID,
			StopReason: "end_turn",
		}
	}()
	return turnID, events, nil
}

func (m *stdioMockAppClient) ReviewStart(
	ctx context.Context,
	threadID string,
	instructions string,
	options codex.RunOptions,
) (string, <-chan codex.TurnEvent, error) {
	return "", nil, errors.New("not implemented")
}

func (m *stdioMockAppClient) CompactStart(
	ctx context.Context,
	threadID string,
) (string, <-chan codex.TurnEvent, error) {
	return "", nil, errors.New("not implemented")
}

func (m *stdioMockAppClient) TurnInterrupt(ctx context.Context, threadID, turnID string) error {
	return nil
}

func (m *stdioMockAppClient) ModelsList(ctx context.Context) ([]codex.ModelOption, error) {
	return []codex.ModelOption{
		{ID: "gpt-5.1-codex", Name: "GPT-5.1 Codex", IsDefault: true},
		{ID: "gpt-5", Name: "GPT-5"},
	}, nil
}

func (m *stdioMockAppClient) ThreadList(ctx context.Context, params codex.ThreadListParams) (codex.ThreadListResult, error) {
	return codex.ThreadListResult{}, nil
}

func (m *stdioMockAppClient) ApprovalRespond(
	ctx context.Context,
	approvalID string,
	decision codex.ApprovalDecision,
) error {
	return nil
}

func (m *stdioMockAppClient) MCPServersList(ctx context.Context) ([]codex.MCPServer, error) {
	return nil, nil
}

func (m *stdioMockAppClient) MCPToolCall(
	ctx context.Context,
	params codex.MCPToolCallParams,
) (codex.MCPToolCallResult, error) {
	return codex.MCPToolCallResult{}, nil
}

func (m *stdioMockAppClient) MCPOAuthLogin(ctx context.Context, server string) (codex.MCPOAuthLoginResult, error) {
	return codex.MCPOAuthLoginResult{}, nil
}

func (m *stdioMockAppClient) Logout(ctx context.Context) error {
	return nil
}

func scanRPCStream(reader io.Reader, msgCh chan<- RPCMessage, errCh chan<- error) {
	defer close(msgCh)
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 1024), 2*1024*1024)
	for scanner.Scan() {
		var msg RPCMessage
		if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
			errCh <- fmt.Errorf("decode rpc line: %w", err)
			return
		}
		msgCh <- msg
	}
	if err := scanner.Err(); err != nil {
		errCh <- err
	}
}

func writeRPCRequest(t *testing.T, writer io.Writer, id, method string, params any) {
	t.Helper()
	payload := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal %s request: %v", method, err)
	}
	if _, err := writer.Write(append(data, '\n')); err != nil {
		t.Fatalf("write %s request: %v", method, err)
	}
}

func mustReadRPCMessage(
	t *testing.T,
	msgCh <-chan RPCMessage,
	errCh <-chan error,
	timeout time.Duration,
) RPCMessage {
	t.Helper()
	select {
	case err := <-errCh:
		t.Fatalf("rpc stream error: %v", err)
	case msg, ok := <-msgCh:
		if !ok {
			t.Fatalf("rpc stream closed")
		}
		return msg
	case <-time.After(timeout):
		t.Fatalf("timed out waiting for rpc message")
	}
	return RPCMessage{}
}
