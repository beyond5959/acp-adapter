package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"codex-acp/internal/appserver"
)

type turnControl struct {
	cancel chan struct{}
	once   sync.Once
}

type fakeServer struct {
	codec *appserver.JSONLCodec

	mu         sync.Mutex
	nextThread int
	nextTurn   int
	nextReq    int
	turns      map[string]*turnControl
	pending    map[string]chan appserver.RPCMessage
	threadOpts map[string]appserver.RunOptions

	receivedInitialize  bool
	receivedInitialized bool
	crashOnThreadStart  bool
	crashOnceFile       string
}

func main() {
	server := &fakeServer{
		codec:              appserver.NewJSONLCodec(os.Stdin, os.Stdout),
		turns:              make(map[string]*turnControl),
		pending:            make(map[string]chan appserver.RPCMessage),
		threadOpts:         make(map[string]appserver.RunOptions),
		crashOnThreadStart: os.Getenv("FAKE_APP_SERVER_CRASH_ON_THREAD_START") == "1",
		crashOnceFile:      os.Getenv("FAKE_APP_SERVER_CRASH_ON_THREAD_START_ONCE_FILE"),
	}

	if err := server.serve(); err != nil && err != io.EOF {
		_, _ = fmt.Fprintf(os.Stderr, "fake app-server error: %v\n", err)
		os.Exit(1)
	}
}

func (s *fakeServer) serve() error {
	for {
		msg, err := s.codec.ReadMessage()
		if err != nil {
			return err
		}
		switch {
		case msg.Method != "":
			s.handle(msg)
		case msg.Method == "" && msg.ID != nil:
			s.handleResponse(msg)
		}
	}
}

func (s *fakeServer) handle(msg appserver.RPCMessage) {
	switch msg.Method {
	case "initialize":
		s.receivedInitialize = true
		s.writeResult(msg.ID, map[string]any{
			"serverInfo": map[string]any{
				"name":    "fake-codex-app-server",
				"version": "0.1.0",
			},
			"capabilities": map[string]any{},
		})
	case "initialized":
		if !s.receivedInitialize {
			s.writeError(msg.ID, -32000, "initialize must be called before initialized")
			return
		}
		s.receivedInitialized = true
	case "thread/start":
		if !s.receivedInitialize || !s.receivedInitialized {
			s.writeError(msg.ID, -32000, "initialize/initialized handshake required")
			return
		}
		var params appserver.ThreadStartParams
		if err := json.Unmarshal(msg.Params, &params); err != nil {
			s.writeError(msg.ID, -32602, "invalid params")
			return
		}
		if s.shouldCrashOnThreadStart() {
			os.Exit(42)
		}
		threadID := s.newThreadID()
		s.storeThreadOptions(threadID, params.RunOptions)
		s.writeResult(msg.ID, appserver.ThreadStartResult{
			ThreadID: threadID,
		})
	case "turn/start":
		var params appserver.TurnStartParams
		if err := json.Unmarshal(msg.Params, &params); err != nil {
			s.writeError(msg.ID, -32602, "invalid params")
			return
		}
		if params.ThreadID == "" {
			s.writeError(msg.ID, -32602, "threadId required")
			return
		}

		turnID, control := s.newTurn()
		s.writeResult(msg.ID, appserver.TurnStartResult{
			TurnID: turnID,
		})

		effective := s.effectiveRunOptions(params.ThreadID, params.RunOptions)
		go s.runTurn(params.ThreadID, turnID, params.Input, effective, control)
	case "review/start":
		var params appserver.ReviewStartParams
		if err := json.Unmarshal(msg.Params, &params); err != nil {
			s.writeError(msg.ID, -32602, "invalid params")
			return
		}
		if params.ThreadID == "" {
			s.writeError(msg.ID, -32602, "threadId required")
			return
		}

		turnID, control := s.newTurn()
		s.writeResult(msg.ID, appserver.ReviewStartResult{TurnID: turnID})

		effective := s.effectiveRunOptions(params.ThreadID, params.RunOptions)
		go s.runReviewTurn(params.ThreadID, turnID, params.Instructions, effective, control)
	case "thread/compact/start":
		var params appserver.CompactStartParams
		if err := json.Unmarshal(msg.Params, &params); err != nil {
			s.writeError(msg.ID, -32602, "invalid params")
			return
		}
		if params.ThreadID == "" {
			s.writeError(msg.ID, -32602, "threadId required")
			return
		}
		turnID, control := s.newTurn()
		s.writeResult(msg.ID, appserver.CompactStartResult{TurnID: turnID})
		go s.runCompactTurn(params.ThreadID, turnID, control)
	case "mcpServer/list":
		s.writeResult(msg.ID, appserver.MCPServerListResult{
			Servers: []appserver.MCPServer{
				{Name: "demo-mcp", OAuthRequired: true, Tools: []string{"dangerous-write", "read-info"}},
				{Name: "metrics-mcp", OAuthRequired: false, Tools: []string{"query"}},
			},
		})
	case "mcpServer/call":
		var params appserver.MCPToolCallParams
		if err := json.Unmarshal(msg.Params, &params); err != nil {
			s.writeError(msg.ID, -32602, "invalid params")
			return
		}
		if strings.TrimSpace(params.Server) == "" || strings.TrimSpace(params.Tool) == "" {
			s.writeError(msg.ID, -32602, "server and tool are required")
			return
		}
		s.writeResult(msg.ID, appserver.MCPToolCallResult{
			Output: fmt.Sprintf(
				"mcp call ok server=%s tool=%s args=%s",
				params.Server,
				params.Tool,
				params.Arguments,
			),
		})
	case "mcpServer/oauth/login":
		var params appserver.MCPOAuthLoginParams
		if err := json.Unmarshal(msg.Params, &params); err != nil {
			s.writeError(msg.ID, -32602, "invalid params")
			return
		}
		if strings.TrimSpace(params.Server) == "" {
			s.writeError(msg.ID, -32602, "server is required")
			return
		}
		s.writeResult(msg.ID, appserver.MCPOAuthLoginResult{
			Status:  "started",
			URL:     fmt.Sprintf("https://auth.example.com/%s", params.Server),
			Message: "open the URL to complete oauth",
		})
	case "auth/logout":
		s.writeResult(msg.ID, map[string]any{"loggedOut": true})
	case "turn/interrupt":
		var params appserver.TurnInterruptParams
		if err := json.Unmarshal(msg.Params, &params); err != nil {
			s.writeError(msg.ID, -32602, "invalid params")
			return
		}
		s.cancelTurn(params.TurnID)
		s.writeResult(msg.ID, map[string]any{"interrupted": true})
	default:
		s.writeError(msg.ID, -32601, "method not found")
	}
}

func (s *fakeServer) handleResponse(msg appserver.RPCMessage) {
	if msg.ID == nil {
		return
	}
	id := normalizeID(*msg.ID)

	s.mu.Lock()
	ch, ok := s.pending[id]
	if ok {
		delete(s.pending, id)
	}
	s.mu.Unlock()
	if !ok {
		return
	}

	ch <- msg
	close(ch)
}

func (s *fakeServer) newThreadID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextThread++
	return fmt.Sprintf("thread-%d", s.nextThread)
}

func (s *fakeServer) storeThreadOptions(threadID string, options appserver.RunOptions) {
	s.mu.Lock()
	s.threadOpts[threadID] = options
	s.mu.Unlock()
}

func (s *fakeServer) effectiveRunOptions(threadID string, turnOptions appserver.RunOptions) appserver.RunOptions {
	s.mu.Lock()
	base := s.threadOpts[threadID]
	s.mu.Unlock()

	if strings.TrimSpace(turnOptions.Model) != "" {
		base.Model = turnOptions.Model
	}
	if strings.TrimSpace(turnOptions.ApprovalPolicy) != "" {
		base.ApprovalPolicy = turnOptions.ApprovalPolicy
	}
	if strings.TrimSpace(turnOptions.Sandbox) != "" {
		base.Sandbox = turnOptions.Sandbox
	}
	if strings.TrimSpace(turnOptions.Personality) != "" {
		base.Personality = turnOptions.Personality
	}
	if strings.TrimSpace(turnOptions.SystemInstructions) != "" {
		base.SystemInstructions = turnOptions.SystemInstructions
	}
	return base
}

func (s *fakeServer) newTurn() (string, *turnControl) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.nextTurn++
	turnID := fmt.Sprintf("turn-%d", s.nextTurn)
	control := &turnControl{cancel: make(chan struct{})}
	s.turns[turnID] = control
	return turnID, control
}

func (s *fakeServer) cancelTurn(turnID string) {
	s.mu.Lock()
	control, ok := s.turns[turnID]
	s.mu.Unlock()
	if !ok {
		return
	}
	control.once.Do(func() { close(control.cancel) })
}

func (s *fakeServer) removeTurn(turnID string) {
	s.mu.Lock()
	delete(s.turns, turnID)
	s.mu.Unlock()
}

func (s *fakeServer) runTurn(
	threadID string,
	turnID string,
	input string,
	options appserver.RunOptions,
	control *turnControl,
) {
	defer s.removeTurn(turnID)
	lowerInput := strings.ToLower(input)
	if strings.Contains(lowerInput, "approval command") ||
		strings.Contains(lowerInput, "approval file") ||
		strings.Contains(lowerInput, "approval network") ||
		strings.Contains(lowerInput, "approval mcp") {
		s.runApprovalTurn(threadID, turnID, lowerInput, control)
		return
	}

	itemID := fmt.Sprintf("item-%s", turnID)
	s.writeTurnStarted(threadID, turnID)

	select {
	case <-control.cancel:
		s.writeTurnCompleted(threadID, turnID, "cancelled")
		return
	case <-time.After(40 * time.Millisecond):
		s.writeItemStarted(threadID, turnID, itemID, "agent_message")
		s.writeAgentMessageDelta(threadID, turnID, itemID, "working")
	}

	if strings.Contains(lowerInput, "profile probe") {
		s.writeAgentMessageDelta(
			threadID,
			turnID,
			itemID,
			fmt.Sprintf(
				"profile model=%s approval=%s sandbox=%s personality=%s system=%s",
				options.Model,
				options.ApprovalPolicy,
				options.Sandbox,
				options.Personality,
				options.SystemInstructions,
			),
		)
	}

	duration := 120 * time.Millisecond
	if strings.Contains(strings.ToLower(input), "slow") {
		duration = 2 * time.Second
	}

	select {
	case <-control.cancel:
		s.writeTurnCompleted(threadID, turnID, "cancelled")
	case <-time.After(duration):
		s.writeAgentMessageDelta(threadID, turnID, itemID, "done")
		s.writeItemCompleted(threadID, turnID, itemID, "agent_message")
		s.writeTurnCompleted(threadID, turnID, "end_turn")
	}
}

func (s *fakeServer) runApprovalTurn(threadID, turnID, input string, control *turnControl) {
	itemID := fmt.Sprintf("item-%s", turnID)
	toolCallID := fmt.Sprintf("tool-%s", turnID)
	approvalID := fmt.Sprintf("approval-%s", turnID)
	approval := appserver.ApprovalRequest{
		ThreadID:   threadID,
		TurnID:     turnID,
		ApprovalID: approvalID,
		ToolCallID: toolCallID,
		Message:    "approval required before side effect tool call",
	}

	switch {
	case strings.Contains(input, "approval command"):
		approval.Kind = appserver.ApprovalKindCommand
		approval.Command = "go test ./..."
	case strings.Contains(input, "approval file"):
		approval.Kind = appserver.ApprovalKindFile
		approval.Files = []string{"docs/README.md"}
	case strings.Contains(input, "approval network"):
		approval.Kind = appserver.ApprovalKindNetwork
		approval.Host = "api.example.com"
		approval.Protocol = "https"
		approval.Port = 443
	case strings.Contains(input, "approval mcp"):
		approval.Kind = appserver.ApprovalKindMCP
		approval.MCPServer = "demo-mcp"
		approval.MCPTool = "dangerous-write"
	default:
		approval.Kind = appserver.ApprovalKindCommand
		approval.Command = "go test ./..."
	}

	s.writeTurnStarted(threadID, turnID)
	s.writeItemStarted(threadID, turnID, itemID, "tool_call")
	s.writeAgentMessageDelta(threadID, turnID, itemID, "waiting permission")

	select {
	case <-control.cancel:
		s.writeTurnCompleted(threadID, turnID, "cancelled")
		return
	default:
	}

	decision, err := s.requestApproval(approval)
	if err != nil {
		s.writeAgentMessageDelta(threadID, turnID, itemID, "permission bridge failed")
		s.writeItemCompleted(threadID, turnID, itemID, "tool_call")
		s.writeTurnCompleted(threadID, turnID, "error")
		return
	}

	switch decision {
	case appserver.ApprovalDecisionApproved:
		s.writeAgentMessageDelta(threadID, turnID, itemID, fmt.Sprintf("executed %s tool", approval.Kind))
	default:
		s.writeAgentMessageDelta(threadID, turnID, itemID, fmt.Sprintf("permission %s; tool not executed", decision))
	}
	s.writeItemCompleted(threadID, turnID, itemID, "tool_call")
	s.writeTurnCompleted(threadID, turnID, "end_turn")
}

func (s *fakeServer) runReviewTurn(
	threadID string,
	turnID string,
	instructions string,
	_ appserver.RunOptions,
	control *turnControl,
) {
	defer s.removeTurn(turnID)

	itemID := fmt.Sprintf("review-item-%s", turnID)
	toolCallID := fmt.Sprintf("review-tool-%s", turnID)
	approvalID := fmt.Sprintf("review-approval-%s", turnID)
	lower := strings.ToLower(instructions)

	approval := appserver.ApprovalRequest{
		ThreadID:   threadID,
		TurnID:     turnID,
		ApprovalID: approvalID,
		ToolCallID: toolCallID,
		Kind:       appserver.ApprovalKindFile,
		Files:      []string{"docs/README.md"},
		WritePath:  "docs/README.md",
		WriteText:  "# updated by review\n",
		Patch:      "--- a/docs/README.md\n+++ b/docs/README.md\n@@\n-old\n+new\n",
		Message:    "apply review patch to docs/README.md",
	}

	if strings.Contains(lower, "conflict") {
		approval.WriteText = "# conflict\n"
	}

	s.writeTurnStarted(threadID, turnID)
	s.writeReviewModeEntered(threadID, turnID)
	s.writeItemStarted(threadID, turnID, itemID, "review")
	s.writeAgentMessageDelta(
		threadID,
		turnID,
		itemID,
		"```diff\n- old line\n+ new line\n```",
	)

	select {
	case <-control.cancel:
		s.writeReviewModeExited(threadID, turnID)
		s.writeTurnCompleted(threadID, turnID, "cancelled")
		return
	default:
	}

	decision, err := s.requestApproval(approval)
	if err != nil {
		s.writeAgentMessageDelta(threadID, turnID, itemID, "review apply failed: approval bridge error")
		s.writeItemCompleted(threadID, turnID, itemID, "review")
		s.writeReviewModeExited(threadID, turnID)
		s.writeTurnCompleted(threadID, turnID, "error")
		return
	}

	switch decision {
	case appserver.ApprovalDecisionApproved:
		s.writeAgentMessageDelta(threadID, turnID, itemID, "patch applied via appserver")
	case appserver.ApprovalDecisionDeclined:
		s.writeAgentMessageDelta(threadID, turnID, itemID, "patch not applied (declined)")
	default:
		s.writeAgentMessageDelta(threadID, turnID, itemID, "patch not applied (cancelled)")
	}

	s.writeItemCompleted(threadID, turnID, itemID, "review")
	s.writeReviewModeExited(threadID, turnID)
	s.writeTurnCompleted(threadID, turnID, "end_turn")
}

func (s *fakeServer) runCompactTurn(threadID, turnID string, control *turnControl) {
	defer s.removeTurn(turnID)

	itemID := fmt.Sprintf("compact-item-%s", turnID)
	s.writeTurnStarted(threadID, turnID)
	s.writeItemStarted(threadID, turnID, itemID, "compact")

	select {
	case <-control.cancel:
		s.writeTurnCompleted(threadID, turnID, "cancelled")
		return
	case <-time.After(30 * time.Millisecond):
		s.writeAgentMessageDelta(threadID, turnID, itemID, "conversation compacted")
	}

	s.writeItemCompleted(threadID, turnID, itemID, "compact")
	s.writeTurnCompleted(threadID, turnID, "end_turn")
}

func (s *fakeServer) writeTurnStarted(threadID, turnID string) {
	s.writeNotification("turn/started", appserver.TurnStartedNotification{
		ThreadID: threadID,
		TurnID:   turnID,
	})
}

func (s *fakeServer) writeAgentMessageDelta(threadID, turnID, itemID, delta string) {
	s.writeNotification("item/agentMessage/delta", appserver.ItemAgentMessageDeltaNotification{
		ThreadID: threadID,
		TurnID:   turnID,
		ItemID:   itemID,
		Delta:    delta,
	})
}

func (s *fakeServer) writeItemStarted(threadID, turnID, itemID, itemType string) {
	s.writeNotification("item/started", appserver.ItemStartedNotification{
		ThreadID: threadID,
		TurnID:   turnID,
		ItemID:   itemID,
		ItemType: itemType,
	})
}

func (s *fakeServer) writeItemCompleted(threadID, turnID, itemID, itemType string) {
	s.writeNotification("item/completed", appserver.ItemCompletedNotification{
		ThreadID: threadID,
		TurnID:   turnID,
		ItemID:   itemID,
		ItemType: itemType,
	})
}

func (s *fakeServer) writeTurnCompleted(threadID, turnID, stopReason string) {
	s.writeNotification("turn/completed", appserver.TurnCompletedNotification{
		ThreadID:   threadID,
		TurnID:     turnID,
		StopReason: stopReason,
	})
}

func (s *fakeServer) writeReviewModeEntered(threadID, turnID string) {
	s.writeNotification("review/mode_entered", appserver.ReviewModeNotification{
		ThreadID: threadID,
		TurnID:   turnID,
	})
}

func (s *fakeServer) writeReviewModeExited(threadID, turnID string) {
	s.writeNotification("review/mode_exited", appserver.ReviewModeNotification{
		ThreadID: threadID,
		TurnID:   turnID,
	})
}

func (s *fakeServer) writeResult(id *json.RawMessage, result any) {
	var resultRaw json.RawMessage
	if result != nil {
		raw, err := json.Marshal(result)
		if err == nil {
			resultRaw = raw
		}
	}
	_ = s.codec.WriteMessage(appserver.RPCMessage{
		JSONRPC: "2.0",
		ID:      cloneID(id),
		Result:  resultRaw,
	})
}

func (s *fakeServer) writeError(id *json.RawMessage, code int, message string) {
	_ = s.codec.WriteMessage(appserver.RPCMessage{
		JSONRPC: "2.0",
		ID:      cloneID(id),
		Error: &appserver.RPCError{
			Code:    code,
			Message: message,
		},
	})
}

func (s *fakeServer) writeNotification(method string, params any) {
	var paramsRaw json.RawMessage
	if params != nil {
		raw, err := json.Marshal(params)
		if err == nil {
			paramsRaw = raw
		}
	}
	_ = s.codec.WriteMessage(appserver.RPCMessage{
		JSONRPC: "2.0",
		Method:  method,
		Params:  paramsRaw,
	})
}

func cloneID(id *json.RawMessage) *json.RawMessage {
	if id == nil {
		return nil
	}
	cp := append(json.RawMessage(nil), (*id)...)
	return &cp
}

func (s *fakeServer) shouldCrashOnThreadStart() bool {
	if s.crashOnThreadStart {
		return true
	}
	if s.crashOnceFile == "" {
		return false
	}

	if _, err := os.Stat(s.crashOnceFile); err == nil {
		return false
	}

	if err := os.WriteFile(s.crashOnceFile, []byte("crashed"), 0o644); err != nil {
		return false
	}
	return true
}

func (s *fakeServer) requestApproval(approval appserver.ApprovalRequest) (appserver.ApprovalDecision, error) {
	resp, err := s.callAdapter(methodApprovalRequest, approval, 3*time.Second)
	if err != nil {
		return "", err
	}
	if resp.Error != nil {
		return "", fmt.Errorf("approval/request failed code=%d message=%s", resp.Error.Code, resp.Error.Message)
	}

	var result appserver.ApprovalDecisionResult
	if len(resp.Result) > 0 {
		if err := json.Unmarshal(resp.Result, &result); err != nil {
			return "", fmt.Errorf("decode approval decision: %w", err)
		}
	}
	switch result.Outcome {
	case string(appserver.ApprovalDecisionApproved):
		return appserver.ApprovalDecisionApproved, nil
	case string(appserver.ApprovalDecisionDeclined):
		return appserver.ApprovalDecisionDeclined, nil
	default:
		return appserver.ApprovalDecisionCancelled, nil
	}
}

func (s *fakeServer) callAdapter(method string, params any, timeout time.Duration) (appserver.RPCMessage, error) {
	id := s.nextRequestID()
	rawID := json.RawMessage(strconv.Quote(id))

	msg := appserver.RPCMessage{
		JSONRPC: "2.0",
		Method:  method,
		ID:      cloneID(&rawID),
	}
	if params != nil {
		rawParams, err := json.Marshal(params)
		if err != nil {
			return appserver.RPCMessage{}, fmt.Errorf("%s encode params: %w", method, err)
		}
		msg.Params = rawParams
	}

	ch := make(chan appserver.RPCMessage, 1)
	s.mu.Lock()
	s.pending[id] = ch
	s.mu.Unlock()

	if err := s.codec.WriteMessage(msg); err != nil {
		s.mu.Lock()
		delete(s.pending, id)
		s.mu.Unlock()
		return appserver.RPCMessage{}, fmt.Errorf("%s write request: %w", method, err)
	}

	select {
	case resp := <-ch:
		return resp, nil
	case <-time.After(timeout):
		s.mu.Lock()
		delete(s.pending, id)
		s.mu.Unlock()
		return appserver.RPCMessage{}, fmt.Errorf("%s response timeout", method)
	}
}

func (s *fakeServer) nextRequestID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextReq++
	return fmt.Sprintf("server-request-%d", s.nextReq)
}

func normalizeID(raw json.RawMessage) string {
	var idString string
	if err := json.Unmarshal(raw, &idString); err == nil {
		return idString
	}

	var idNumber int64
	if err := json.Unmarshal(raw, &idNumber); err == nil {
		return strconv.FormatInt(idNumber, 10)
	}
	return string(raw)
}

const methodApprovalRequest = "approval/request"
