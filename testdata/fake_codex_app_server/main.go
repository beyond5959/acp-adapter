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

	"github.com/beyond5959/acp-adapter/internal/codex"
)

const fakePNGBase64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aap8AAAAASUVORK5CYII="

type turnControl struct {
	cancel chan struct{}
	once   sync.Once
}

type fakeThreadRecord struct {
	thread          codex.Thread
	archived        bool
	model           string
	reasoningEffort string
	approvalPolicy  string
	sandbox         string
}

type fakeServer struct {
	codec *codex.JSONLCodec

	mu         sync.Mutex
	nextThread int
	nextTurn   int
	nextReq    int
	turns      map[string]*turnControl
	pending    map[string]chan codex.RPCMessage
	threadOpts map[string]codex.RunOptions
	threads    []fakeThreadRecord

	receivedInitialize  bool
	receivedInitialized bool
	crashOnThreadStart  bool
	crashOnceFile       string
	crashDuringTurn     bool
	crashTurnOnceFile   string
}

func main() {
	server := &fakeServer{
		codec:              codex.NewJSONLCodec(os.Stdin, os.Stdout),
		turns:              make(map[string]*turnControl),
		pending:            make(map[string]chan codex.RPCMessage),
		threadOpts:         make(map[string]codex.RunOptions),
		threads:            seedThreads(),
		crashOnThreadStart: os.Getenv("FAKE_APP_SERVER_CRASH_ON_THREAD_START") == "1",
		crashOnceFile:      os.Getenv("FAKE_APP_SERVER_CRASH_ON_THREAD_START_ONCE_FILE"),
		crashDuringTurn:    os.Getenv("FAKE_APP_SERVER_CRASH_DURING_TURN") == "1",
		crashTurnOnceFile:  os.Getenv("FAKE_APP_SERVER_CRASH_DURING_TURN_ONCE_FILE"),
	}

	if err := server.serve(); err != nil && err != io.EOF {
		_, _ = fmt.Fprintf(os.Stderr, "fake app-server error: %v\n", err)
		os.Exit(1)
	}
}

func seedThreads() []fakeThreadRecord {
	base := time.Date(2026, 3, 10, 9, 0, 0, 0, time.UTC).Unix()
	return []fakeThreadRecord{
		{
			thread: codex.Thread{
				ID:            "seed-active-1",
				CWD:           "/workspace/session-list",
				Name:          "Seed Active Session",
				Preview:       "seed active preview",
				ModelProvider: "openai",
				CreatedAt:     base - 3600,
				UpdatedAt:     base - 300,
				Source:        map[string]any{"kind": "appServer"},
				Status:        map[string]any{"kind": "idle"},
				Turns: []codex.Turn{
					{
						ID:     "seed-turn-1",
						Status: "completed",
						Items: []codex.ThreadItem{
							{
								ID:   "seed-user-1",
								Type: "userMessage",
								Content: []codex.UserInput{
									{Type: "text", Text: "Explain the adapter architecture."},
								},
							},
							{
								ID:   "seed-agent-1",
								Type: "agentMessage",
								Text: "It bridges ACP stdio to Codex app-server JSON-RPC.",
							},
						},
					},
					{
						ID:     "seed-turn-2",
						Status: "completed",
						Items: []codex.ThreadItem{
							{
								ID:   "seed-user-2",
								Type: "userMessage",
								Content: []codex.UserInput{
									{Type: "text", Text: "What should be tested next?"},
								},
							},
							{
								ID:   "seed-agent-2",
								Type: "agentMessage",
								Text: "- [ ] add session/load coverage\n- [ ] run go test ./...",
							},
						},
					},
				},
			},
			model:           "gpt-4.1",
			reasoningEffort: "low",
			approvalPolicy:  "never",
			sandbox:         "workspace-write",
		},
		{
			thread: codex.Thread{
				ID:            "seed-active-2",
				CWD:           "/workspace/session-list",
				Name:          "Seed Active Session Two",
				Preview:       "seed active preview two",
				ModelProvider: "openai",
				CreatedAt:     base - 7200,
				UpdatedAt:     base - 600,
				Source:        map[string]any{"kind": "appServer"},
				Status:        map[string]any{"kind": "idle"},
			},
			model:           "gpt-5.1-codex",
			reasoningEffort: "medium",
			approvalPolicy:  "on-request",
			sandbox:         "workspace-write",
		},
		{
			thread: codex.Thread{
				ID:            "seed-archived-1",
				CWD:           "/workspace/session-list",
				Name:          "Archived Session",
				Preview:       "archived preview",
				ModelProvider: "openai",
				CreatedAt:     base - 10800,
				UpdatedAt:     base - 900,
				Source:        map[string]any{"kind": "appServer"},
				Status:        map[string]any{"kind": "idle"},
			},
			archived: true,
			model:    "gpt-5-codex",
			sandbox:  "workspace-write",
		},
		{
			thread: codex.Thread{
				ID:            "seed-other-cwd",
				CWD:           "/workspace/other",
				Name:          "Other Workspace Session",
				Preview:       "other cwd preview",
				ModelProvider: "openai",
				CreatedAt:     base - 14400,
				UpdatedAt:     base - 1200,
				Source:        map[string]any{"kind": "appServer"},
				Status:        map[string]any{"kind": "idle"},
			},
			model:           "gpt-5.1-codex",
			reasoningEffort: "high",
			approvalPolicy:  "on-failure",
			sandbox:         "workspace-write",
		},
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

func (s *fakeServer) handle(msg codex.RPCMessage) {
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
		var params codex.ThreadStartParams
		if err := json.Unmarshal(msg.Params, &params); err != nil {
			s.writeError(msg.ID, -32602, "invalid params")
			return
		}
		if s.shouldCrashOnThreadStart() {
			os.Exit(42)
		}
		threadID := s.newThreadID()
		s.storeThreadOptions(threadID, params.RunOptions)
		s.appendThread(fakeThreadRecord{
			thread: codex.Thread{
				ID:            threadID,
				CWD:           strings.TrimSpace(params.CWD),
				Name:          fmt.Sprintf("Live Session %s", threadID),
				ModelProvider: "openai",
				CreatedAt:     time.Now().UTC().Unix(),
				UpdatedAt:     time.Now().UTC().Unix(),
				Source:        map[string]any{"kind": "appServer"},
				Status:        map[string]any{"kind": "idle"},
			},
			model:           "gpt-5.1-codex",
			reasoningEffort: "medium",
			approvalPolicy:  "on-request",
			sandbox:         "workspace-write",
		})
		s.writeResult(msg.ID, codex.ThreadStartResult{
			ThreadID: threadID,
		})
	case "thread/list":
		var params codex.ThreadListParams
		if err := json.Unmarshal(msg.Params, &params); err != nil {
			s.writeError(msg.ID, -32602, "invalid params")
			return
		}
		s.writeResult(msg.ID, s.listThreads(params))
	case "thread/resume":
		var params codex.ThreadResumeParams
		if err := json.Unmarshal(msg.Params, &params); err != nil {
			s.writeError(msg.ID, -32602, "invalid params")
			return
		}
		record, ok := s.lookupThread(strings.TrimSpace(params.ThreadID))
		if !ok {
			s.writeError(msg.ID, -32000, "thread not found")
			return
		}
		s.writeResult(msg.ID, codex.ThreadResumeResult{
			ApprovalPolicy:  firstNonEmpty(record.approvalPolicy, "never"),
			CWD:             firstNonEmpty(strings.TrimSpace(params.CWD), record.thread.CWD),
			Model:           firstNonEmpty(record.model, "gpt-5.1-codex"),
			ModelProvider:   firstNonEmpty(record.thread.ModelProvider, "openai"),
			ReasoningEffort: record.reasoningEffort,
			Sandbox:         firstNonEmpty(record.sandbox, "workspace-write"),
			Thread:          record.thread,
		})
	case "turn/start":
		var params codex.TurnStartParams
		if err := json.Unmarshal(msg.Params, &params); err != nil {
			s.writeError(msg.ID, -32602, "invalid params")
			return
		}
		if params.ThreadID == "" {
			s.writeError(msg.ID, -32602, "threadId required")
			return
		}

		turnID, control := s.newTurn()
		s.writeResult(msg.ID, codex.TurnStartResult{
			TurnID: turnID,
		})

		effective := s.effectiveRunOptions(params.ThreadID, params.RunOptions)
		if value := strings.TrimSpace(params.Effort); value != "" {
			effective.Effort = value
		}
		go s.runTurn(params.ThreadID, turnID, params.Input, effective, control)
	case "review/start":
		var params codex.ReviewStartParams
		if err := json.Unmarshal(msg.Params, &params); err != nil {
			s.writeError(msg.ID, -32602, "invalid params")
			return
		}
		if params.ThreadID == "" {
			s.writeError(msg.ID, -32602, "threadId required")
			return
		}

		turnID, control := s.newTurn()
		s.writeResult(msg.ID, codex.ReviewStartResult{TurnID: turnID})

		effective := s.effectiveRunOptions(params.ThreadID, params.RunOptions)
		go s.runReviewTurn(params.ThreadID, turnID, params.Instructions, effective, control)
	case "thread/compact/start":
		var params codex.CompactStartParams
		if err := json.Unmarshal(msg.Params, &params); err != nil {
			s.writeError(msg.ID, -32602, "invalid params")
			return
		}
		if params.ThreadID == "" {
			s.writeError(msg.ID, -32602, "threadId required")
			return
		}
		turnID, control := s.newTurn()
		s.writeResult(msg.ID, codex.CompactStartResult{TurnID: turnID})
		go s.runCompactTurn(params.ThreadID, turnID, control)
	case "mcpServer/list":
		s.writeResult(msg.ID, codex.MCPServerListResult{
			Servers: []codex.MCPServer{
				{Name: "demo-mcp", OAuthRequired: true, Tools: []string{"dangerous-write", "read-info", "render-image"}},
				{Name: "metrics-mcp", OAuthRequired: false, Tools: []string{"query"}},
			},
		})
	case "mcpServer/call":
		var params codex.MCPToolCallParams
		if err := json.Unmarshal(msg.Params, &params); err != nil {
			s.writeError(msg.ID, -32602, "invalid params")
			return
		}
		if strings.TrimSpace(params.Server) == "" || strings.TrimSpace(params.Tool) == "" {
			s.writeError(msg.ID, -32602, "server and tool are required")
			return
		}
		if strings.Contains(strings.ToLower(params.Tool), "image") {
			s.writeResult(msg.ID, codex.MCPToolCallResult{
				Content: []json.RawMessage{
					json.RawMessage(`{"type":"text","text":"mcp image result"}`),
					json.RawMessage(`{"type":"image","mimeType":"image/png","data":"` + fakePNGBase64 + `"}`),
				},
			})
			return
		}
		s.writeResult(msg.ID, codex.MCPToolCallResult{
			Output: fmt.Sprintf(
				"mcp call ok server=%s tool=%s args=%s",
				params.Server,
				params.Tool,
				params.Arguments,
			),
		})
	case "mcpServer/oauth/login":
		var params codex.MCPOAuthLoginParams
		if err := json.Unmarshal(msg.Params, &params); err != nil {
			s.writeError(msg.ID, -32602, "invalid params")
			return
		}
		if strings.TrimSpace(params.Server) == "" {
			s.writeError(msg.ID, -32602, "server is required")
			return
		}
		s.writeResult(msg.ID, codex.MCPOAuthLoginResult{
			Status:  "started",
			URL:     fmt.Sprintf("https://auth.example.com/%s", params.Server),
			Message: "open the URL to complete oauth",
		})
	case "auth/logout":
		s.writeResult(msg.ID, map[string]any{"loggedOut": true})
	case "account/logout":
		s.writeResult(msg.ID, map[string]any{"loggedOut": true})
	case "turn/interrupt":
		var params codex.TurnInterruptParams
		if err := json.Unmarshal(msg.Params, &params); err != nil {
			s.writeError(msg.ID, -32602, "invalid params")
			return
		}
		s.cancelTurn(params.TurnID)
		s.writeResult(msg.ID, map[string]any{"interrupted": true})
	case "model/list":
		s.writeResult(msg.ID, codex.ModelListResult{
			Data: []codex.ModelListItem{
				{
					ID:                     "gpt-5.1-codex",
					Model:                  "gpt-5.1-codex",
					DisplayName:            "GPT-5.1 Codex",
					Description:            "default coding model",
					IsDefault:              true,
					DefaultReasoningEffort: "medium",
					SupportedReasoningEfforts: []codex.ModelReasoningEffortListEntry{
						{ReasoningEffort: "low", Description: "faster response"},
						{ReasoningEffort: "medium", Description: "balanced quality and latency"},
						{ReasoningEffort: "high", Description: "deeper reasoning"},
					},
				},
				{
					ID:                     "gpt-5-codex",
					Model:                  "gpt-5-codex",
					DisplayName:            "GPT-5 Codex",
					Description:            "balanced coding model",
					DefaultReasoningEffort: "low",
					SupportedReasoningEfforts: []codex.ModelReasoningEffortListEntry{
						{ReasoningEffort: "low", Description: "default"},
						{ReasoningEffort: "medium", Description: "higher quality"},
					},
				},
				{
					ID:                     "gpt-4.1",
					Model:                  "gpt-4.1",
					DisplayName:            "GPT-4.1",
					Description:            "fast fallback model",
					DefaultReasoningEffort: "minimal",
					SupportedReasoningEfforts: []codex.ModelReasoningEffortListEntry{
						{ReasoningEffort: "minimal", Description: "minimal reasoning"},
						{ReasoningEffort: "low", Description: "slightly deeper reasoning"},
					},
				},
			},
		})
	default:
		s.writeError(msg.ID, -32601, "method not found")
	}
}

func (s *fakeServer) handleResponse(msg codex.RPCMessage) {
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

func (s *fakeServer) storeThreadOptions(threadID string, options codex.RunOptions) {
	s.mu.Lock()
	s.threadOpts[threadID] = options
	s.mu.Unlock()
}

func (s *fakeServer) effectiveRunOptions(threadID string, turnOptions codex.RunOptions) codex.RunOptions {
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
	if strings.TrimSpace(turnOptions.Effort) != "" {
		base.Effort = turnOptions.Effort
	}
	return base
}

func (s *fakeServer) appendThread(record fakeThreadRecord) {
	s.mu.Lock()
	s.threads = append([]fakeThreadRecord{record}, s.threads...)
	s.mu.Unlock()
}

func (s *fakeServer) lookupThread(threadID string) (fakeThreadRecord, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, record := range s.threads {
		if record.thread.ID == threadID {
			return record, true
		}
	}
	return fakeThreadRecord{}, false
}

func (s *fakeServer) listThreads(params codex.ThreadListParams) codex.ThreadListResult {
	s.mu.Lock()
	records := append([]fakeThreadRecord(nil), s.threads...)
	s.mu.Unlock()

	filtered := make([]codex.Thread, 0, len(records))
	archived := params.Archived != nil && *params.Archived
	cwd := strings.TrimSpace(params.CWD)
	for _, record := range records {
		if record.archived != archived {
			continue
		}
		if cwd != "" && record.thread.CWD != cwd {
			continue
		}
		filtered = append(filtered, record.thread)
	}

	offset := 0
	if raw := strings.TrimSpace(params.Cursor); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err == nil && parsed >= 0 {
			offset = parsed
		}
	}
	if offset >= len(filtered) {
		return codex.ThreadListResult{Data: []codex.Thread{}}
	}

	limit := 2
	if params.Limit != nil && *params.Limit > 0 {
		limit = int(*params.Limit)
	}
	end := offset + limit
	if end > len(filtered) {
		end = len(filtered)
	}

	result := codex.ThreadListResult{
		Data: append([]codex.Thread(nil), filtered[offset:end]...),
	}
	if end < len(filtered) {
		result.NextCursor = strconv.Itoa(end)
	}
	return result
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func flattenUserInputText(input []codex.UserInput) string {
	parts := make([]string, 0, len(input))
	for _, item := range input {
		switch strings.ToLower(strings.TrimSpace(item.Type)) {
		case "text":
			if text := strings.TrimSpace(item.Text); text != "" {
				parts = append(parts, text)
			}
		case "mention":
			parts = append(parts, fmt.Sprintf("mention:%s:%s", item.Name, item.Path))
		case "image":
			parts = append(parts, "image")
		case "localimage":
			parts = append(parts, fmt.Sprintf("localimage:%s", item.Path))
		}
	}
	return strings.Join(parts, "\n")
}

type mentionInput struct {
	Name string
	Path string
}

func extractMentionInputs(input []codex.UserInput) []mentionInput {
	mentions := make([]mentionInput, 0, len(input))
	for _, item := range input {
		if strings.ToLower(strings.TrimSpace(item.Type)) != "mention" {
			continue
		}
		name := strings.TrimSpace(item.Name)
		path := strings.TrimSpace(item.Path)
		if name == "" && path == "" {
			continue
		}
		if name == "" {
			name = path
		}
		mentions = append(mentions, mentionInput{Name: name, Path: path})
	}
	return mentions
}

func countImageInputs(input []codex.UserInput) int {
	count := 0
	for _, item := range input {
		switch strings.ToLower(strings.TrimSpace(item.Type)) {
		case "image", "localimage":
			count++
		}
	}
	return count
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
	input []codex.UserInput,
	options codex.RunOptions,
	control *turnControl,
) {
	defer s.removeTurn(turnID)
	lowerInput := strings.ToLower(flattenUserInputText(input))
	mentions := extractMentionInputs(input)
	imageCount := countImageInputs(input)
	if strings.Contains(lowerInput, "approval command") ||
		strings.Contains(lowerInput, "approval file") ||
		strings.Contains(lowerInput, "approval network") ||
		strings.Contains(lowerInput, "approval mcp") {
		s.runApprovalTurn(threadID, turnID, lowerInput, control)
		return
	}
	if strings.Contains(lowerInput, "command execution streaming mapping") {
		s.runCommandExecutionStreamingTurn(threadID, turnID, control)
		return
	}
	if strings.Contains(lowerInput, "command execution mapping") {
		s.runCommandExecutionTurn(threadID, turnID, control)
		return
	}
	if strings.Contains(lowerInput, "turn diff mapping") {
		s.runTurnDiffTurn(threadID, turnID, control)
		return
	}
	if strings.Contains(lowerInput, "tool image mapping") {
		s.runToolImageTurn(threadID, turnID, control)
		return
	}
	if strings.Contains(lowerInput, "error notification retry") {
		s.runErrorNotificationRetryTurn(threadID, turnID, control)
		return
	}
	if strings.Contains(lowerInput, "turn completed error detail") {
		s.runTurnCompletedErrorTurn(threadID, turnID, control)
		return
	}

	itemID := fmt.Sprintf("item-%s", turnID)
	s.writeTurnStarted(threadID, turnID)
	if s.shouldCrashDuringTurn() {
		os.Exit(43)
	}

	select {
	case <-control.cancel:
		s.writeTurnCompleted(threadID, turnID, "cancelled")
		return
	case <-time.After(40 * time.Millisecond):
		s.writeItemStarted(threadID, turnID, itemID, "agent_message")
		s.writeAgentMessageDelta(threadID, turnID, itemID, "working")
	}

	for _, mention := range mentions {
		s.writeAgentMessageDelta(
			threadID,
			turnID,
			itemID,
			fmt.Sprintf("mention[%s] from %s", mention.Name, mention.Path),
		)
	}
	if imageCount > 0 {
		s.writeAgentMessageDelta(
			threadID,
			turnID,
			itemID,
			fmt.Sprintf("image context received (%d image)", imageCount),
		)
	}

	if strings.Contains(lowerInput, "profile probe") {
		s.writeAgentMessageDelta(
			threadID,
			turnID,
			itemID,
			fmt.Sprintf(
				"profile model=%s thought=%s approval=%s sandbox=%s personality=%s system=%s",
				options.Model,
				options.Effort,
				options.ApprovalPolicy,
				options.Sandbox,
				options.Personality,
				options.SystemInstructions,
			),
		)
	}

	if strings.Contains(lowerInput, "delta plan fallback") {
		planItemOne := fmt.Sprintf("plan-item-1-%s", turnID)
		planItemTwo := fmt.Sprintf("plan-item-2-%s", turnID)

		s.writePlanItemStarted(threadID, turnID, planItemOne)
		s.writePlanDelta(threadID, turnID, planItemOne, "capture ")
		s.writePlanDelta(threadID, turnID, planItemOne, "requirements")
		s.writePlanItemCompleted(threadID, turnID, planItemOne, "capture requirements")

		s.writePlanItemStarted(threadID, turnID, planItemTwo)
		s.writePlanDelta(threadID, turnID, planItemTwo, "implement ")
		s.writePlanDelta(threadID, turnID, planItemTwo, "mapping")
		s.writePlanItemCompleted(threadID, turnID, planItemTwo, "implement mapping")

		s.writeAgentMessageDelta(threadID, turnID, itemID, "plan via delta ready")
		s.writeItemCompleted(threadID, turnID, itemID, "agent_message")
		s.writeTurnCompleted(threadID, turnID, "end_turn")
		return
	}

	if strings.Contains(lowerInput, "structured plan") {
		s.writeTurnPlanUpdated(
			threadID,
			turnID,
			"break the work into ordered steps before editing",
			[]codex.TurnPlanStep{
				{Step: "capture requirements", Status: "pending"},
				{Step: "implement mapping", Status: "pending"},
			},
		)
		s.writeTurnPlanUpdated(
			threadID,
			turnID,
			"start implementation after requirements are locked",
			[]codex.TurnPlanStep{
				{Step: "capture requirements", Status: "completed"},
				{Step: "implement mapping", Status: "inProgress"},
				{Step: "run go test ./...", Status: "pending"},
			},
		)
		s.writeAgentMessageDelta(threadID, turnID, itemID, "plan ready")
		s.writeItemCompleted(threadID, turnID, itemID, "agent_message")
		s.writeTurnCompleted(threadID, turnID, "end_turn")
		return
	}

	if strings.Contains(lowerInput, "reasoning summary probe") {
		reasoningItemID := fmt.Sprintf("reasoning-%s", turnID)
		s.writeReasoningSummaryPartAdded(threadID, turnID, reasoningItemID, 0)
		s.writeReasoningSummaryTextDelta(threadID, turnID, reasoningItemID, 0, "Inspect repository state.")
		s.writeReasoningSummaryPartAdded(threadID, turnID, reasoningItemID, 1)
		s.writeReasoningSummaryTextDelta(threadID, turnID, reasoningItemID, 1, "Confirm reasoning plumbing.")
		s.writeAgentMessageDelta(threadID, turnID, itemID, "done")
		s.writeItemCompleted(threadID, turnID, itemID, "agent_message")
		s.writeTurnCompleted(threadID, turnID, "end_turn")
		return
	}

	if strings.Contains(lowerInput, "reasoning raw probe") {
		reasoningItemID := fmt.Sprintf("reasoning-%s", turnID)
		s.writeReasoningTextDelta(threadID, turnID, reasoningItemID, "Raw reasoning step 1. ")
		s.writeReasoningTextDelta(threadID, turnID, reasoningItemID, "Raw reasoning step 2.")
		s.writeAgentMessageDelta(threadID, turnID, itemID, "done")
		s.writeItemCompleted(threadID, turnID, itemID, "agent_message")
		s.writeTurnCompleted(threadID, turnID, "end_turn")
		return
	}

	if strings.Contains(lowerInput, "todo") {
		checklist := "\n- [ ] capture requirements\n- [ ] implement mapping\n"
		if strings.Contains(lowerInput, "continue") ||
			strings.Contains(lowerInput, "update") ||
			strings.Contains(lowerInput, "done") {
			checklist = "\n- [x] capture requirements\n- [ ] implement mapping\n- [ ] run go test ./...\n"
		}
		s.writeAgentMessageDelta(threadID, turnID, itemID, checklist)
		s.writeItemCompleted(threadID, turnID, itemID, "agent_message")
		s.writeTurnCompleted(threadID, turnID, "end_turn")
		return
	}

	duration := 120 * time.Millisecond
	if strings.Contains(lowerInput, "slow") {
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
	approval := codex.ApprovalRequest{
		ThreadID:   threadID,
		TurnID:     turnID,
		ApprovalID: approvalID,
		ToolCallID: toolCallID,
		Message:    "approval required before side effect tool call",
	}

	switch {
	case strings.Contains(input, "approval command"):
		approval.Kind = codex.ApprovalKindCommand
		approval.Command = "go test ./..."
	case strings.Contains(input, "approval file"):
		approval.Kind = codex.ApprovalKindFile
		approval.Files = []string{"docs/README.md"}
	case strings.Contains(input, "approval network"):
		approval.Kind = codex.ApprovalKindNetwork
		approval.Host = "api.example.com"
		approval.Protocol = "https"
		approval.Port = 443
	case strings.Contains(input, "approval mcp"):
		approval.Kind = codex.ApprovalKindMCP
		approval.MCPServer = "demo-mcp"
		approval.MCPTool = "dangerous-write"
	default:
		approval.Kind = codex.ApprovalKindCommand
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
	case codex.ApprovalDecisionApproved:
		s.writeAgentMessageDelta(threadID, turnID, itemID, fmt.Sprintf("executed %s tool", approval.Kind))
	default:
		s.writeAgentMessageDelta(threadID, turnID, itemID, fmt.Sprintf("permission %s; tool not executed", decision))
	}
	s.writeItemCompleted(threadID, turnID, itemID, "tool_call")
	s.writeTurnCompleted(threadID, turnID, "end_turn")
}

func (s *fakeServer) runCommandExecutionTurn(threadID, turnID string, control *turnControl) {
	defer s.removeTurn(turnID)

	commandItemID := fmt.Sprintf("command-%s", turnID)
	messageItemID := fmt.Sprintf("item-%s", turnID)
	cwd := fakeWorkspaceCWD()

	s.writeTurnStarted(threadID, turnID)
	select {
	case <-control.cancel:
		s.writeTurnCompleted(threadID, turnID, "cancelled")
		return
	default:
	}

	command := `/bin/zsh -lc "pwd"`
	s.writeCommandExecutionStarted(
		threadID,
		turnID,
		commandItemID,
		command,
		cwd,
		[]codex.CommandAction{{
			Type:    "read",
			Command: "pwd",
			Name:    "cwd",
		}},
	)
	output := cwd + "\n"
	s.writeCommandExecutionCompleted(
		threadID,
		turnID,
		commandItemID,
		command,
		cwd,
		[]codex.CommandAction{{
			Type:    "read",
			Command: "pwd",
			Name:    "cwd",
		}},
		output,
		0,
		"completed",
	)

	s.writeItemStarted(threadID, turnID, messageItemID, "agent_message")
	s.writeAgentMessageDelta(threadID, turnID, messageItemID, "done")
	s.writeItemCompleted(threadID, turnID, messageItemID, "agent_message")
	s.writeTurnCompleted(threadID, turnID, "end_turn")
}

func (s *fakeServer) runCommandExecutionStreamingTurn(threadID, turnID string, control *turnControl) {
	defer s.removeTurn(turnID)

	commandItemID := fmt.Sprintf("command-stream-%s", turnID)
	messageItemID := fmt.Sprintf("item-%s", turnID)
	cwd := fakeWorkspaceCWD()
	command := `/bin/zsh -lc "printf '\''line1\nline2\n'\''"`
	actions := []codex.CommandAction{{
		Type:    "read",
		Command: `printf 'line1\nline2\n'`,
		Name:    "stdout",
	}}

	s.writeTurnStarted(threadID, turnID)
	select {
	case <-control.cancel:
		s.writeTurnCompleted(threadID, turnID, "cancelled")
		return
	default:
	}

	s.writeCommandExecutionStarted(
		threadID,
		turnID,
		commandItemID,
		command,
		cwd,
		actions,
	)
	s.writeCommandExecutionOutputDelta(threadID, turnID, commandItemID, "line1\n")
	s.writeCommandExecutionOutputDelta(threadID, turnID, commandItemID, "line2\n")
	s.writeCommandExecutionCompleted(
		threadID,
		turnID,
		commandItemID,
		command,
		cwd,
		actions,
		"line1\nline2\n",
		0,
		"completed",
	)

	s.writeItemStarted(threadID, turnID, messageItemID, "agent_message")
	s.writeAgentMessageDelta(threadID, turnID, messageItemID, "done")
	s.writeItemCompleted(threadID, turnID, messageItemID, "agent_message")
	s.writeTurnCompleted(threadID, turnID, "end_turn")
}

func (s *fakeServer) runErrorNotificationRetryTurn(threadID, turnID string, control *turnControl) {
	defer s.removeTurn(turnID)

	itemID := fmt.Sprintf("item-%s", turnID)
	s.writeTurnStarted(threadID, turnID)
	s.writeItemStarted(threadID, turnID, itemID, "agent_message")
	s.writeAgentMessageDelta(threadID, turnID, itemID, "working")
	s.writeErrorNotification(
		threadID,
		turnID,
		codex.TurnError{
			Message:        "temporary upstream connection drop",
			CodexErrorInfo: json.RawMessage(`{"responseStreamDisconnected":{"httpStatusCode":502}}`),
		},
		true,
	)

	select {
	case <-control.cancel:
		s.writeTurnCompleted(threadID, turnID, "cancelled")
		return
	case <-time.After(40 * time.Millisecond):
	}

	s.writeAgentMessageDelta(threadID, turnID, itemID, "done")
	s.writeItemCompleted(threadID, turnID, itemID, "agent_message")
	s.writeTurnCompleted(threadID, turnID, "end_turn")
}

func (s *fakeServer) runTurnCompletedErrorTurn(threadID, turnID string, control *turnControl) {
	defer s.removeTurn(turnID)

	itemID := fmt.Sprintf("item-%s", turnID)
	s.writeTurnStarted(threadID, turnID)
	s.writeItemStarted(threadID, turnID, itemID, "agent_message")
	s.writeAgentMessageDelta(threadID, turnID, itemID, "working")

	select {
	case <-control.cancel:
		s.writeTurnCompleted(threadID, turnID, "cancelled")
		return
	case <-time.After(40 * time.Millisecond):
	}

	s.writeItemCompleted(threadID, turnID, itemID, "agent_message")
	s.writeTurnCompletedWithError(
		threadID,
		turnID,
		"apply_patch verification failed: Failed to find expected lines in /Users/niuniu/git_local/ngent/internal/httpapi/httpapi.go",
		"patch no longer matches current file contents",
		json.RawMessage(`"other"`),
	)
}

func fakeWorkspaceCWD() string {
	cwd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return cwd
}

func (s *fakeServer) runToolImageTurn(threadID, turnID string, control *turnControl) {
	defer s.removeTurn(turnID)

	toolItemID := fmt.Sprintf("tool-image-%s", turnID)
	messageItemID := fmt.Sprintf("item-%s", turnID)
	success := true

	s.writeTurnStarted(threadID, turnID)
	select {
	case <-control.cancel:
		s.writeTurnCompleted(threadID, turnID, "cancelled")
		return
	default:
	}

	s.writeNotification("item/started", codex.ItemStartedNotification{
		ThreadID: threadID,
		TurnID:   turnID,
		ItemID:   toolItemID,
		ItemType: "dynamicToolCall",
		Item: &codex.ThreadItemRef{
			ID:     toolItemID,
			Type:   "dynamicToolCall",
			Tool:   "render_image",
			Status: "inProgress",
		},
	})
	s.writeNotification("item/completed", codex.ItemCompletedNotification{
		ThreadID: threadID,
		TurnID:   turnID,
		ItemID:   toolItemID,
		ItemType: "dynamicToolCall",
		Item: &codex.ThreadItemRef{
			ID:      toolItemID,
			Type:    "dynamicToolCall",
			Tool:    "render_image",
			Status:  "completed",
			Success: &success,
			ContentItems: []codex.DynamicToolCallOutputContentItem{
				{
					Type: "inputText",
					Text: "tool image ready",
				},
				{
					Type:     "inputImage",
					ImageURL: "data:image/png;base64," + fakePNGBase64,
				},
			},
		},
	})

	s.writeItemStarted(threadID, turnID, messageItemID, "agent_message")
	s.writeAgentMessageDelta(threadID, turnID, messageItemID, "done")
	s.writeItemCompleted(threadID, turnID, messageItemID, "agent_message")
	s.writeTurnCompleted(threadID, turnID, "end_turn")
}

func (s *fakeServer) runTurnDiffTurn(threadID, turnID string, control *turnControl) {
	defer s.removeTurn(turnID)

	itemID := fmt.Sprintf("item-%s", turnID)
	s.writeTurnStarted(threadID, turnID)
	select {
	case <-control.cancel:
		s.writeTurnCompleted(threadID, turnID, "cancelled")
		return
	default:
	}

	s.writeTurnDiffUpdated(threadID, turnID, strings.Join([]string{
		"diff --git a/docs/README.md b/docs/README.md",
		"--- a/docs/README.md",
		"+++ b/docs/README.md",
		"@@ -1 +1 @@",
		"-old line",
		"+new line",
		"",
	}, "\n"))
	s.writeItemStarted(threadID, turnID, itemID, "agent_message")
	s.writeAgentMessageDelta(threadID, turnID, itemID, "done")
	s.writeItemCompleted(threadID, turnID, itemID, "agent_message")
	s.writeTurnCompleted(threadID, turnID, "end_turn")
}

func (s *fakeServer) runReviewTurn(
	threadID string,
	turnID string,
	instructions string,
	_ codex.RunOptions,
	control *turnControl,
) {
	defer s.removeTurn(turnID)

	itemID := fmt.Sprintf("review-item-%s", turnID)
	toolCallID := fmt.Sprintf("review-tool-%s", turnID)
	approvalID := fmt.Sprintf("review-approval-%s", turnID)
	lower := strings.ToLower(instructions)

	approval := codex.ApprovalRequest{
		ThreadID:   threadID,
		TurnID:     turnID,
		ApprovalID: approvalID,
		ToolCallID: toolCallID,
		Kind:       codex.ApprovalKindFile,
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
	case codex.ApprovalDecisionApproved:
		s.writeAgentMessageDelta(threadID, turnID, itemID, "patch applied via appserver")
	case codex.ApprovalDecisionDeclined:
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
	s.writeNotification("turn/started", codex.TurnStartedNotification{
		ThreadID: threadID,
		TurnID:   turnID,
	})
}

func (s *fakeServer) writeAgentMessageDelta(threadID, turnID, itemID, delta string) {
	s.writeNotification("item/agentMessage/delta", codex.ItemAgentMessageDeltaNotification{
		ThreadID: threadID,
		TurnID:   turnID,
		ItemID:   itemID,
		Delta:    delta,
	})
}

func (s *fakeServer) writePlanDelta(threadID, turnID, itemID, delta string) {
	s.writeNotification("item/plan/delta", codex.PlanDeltaNotification{
		ThreadID: threadID,
		TurnID:   turnID,
		ItemID:   itemID,
		Delta:    delta,
	})
}

func (s *fakeServer) writeReasoningSummaryTextDelta(
	threadID string,
	turnID string,
	itemID string,
	summaryIndex int64,
	delta string,
) {
	s.writeNotification("item/reasoning/summaryTextDelta", codex.ReasoningSummaryTextDeltaNotification{
		ThreadID:     threadID,
		TurnID:       turnID,
		ItemID:       itemID,
		SummaryIndex: summaryIndex,
		Delta:        delta,
	})
}

func (s *fakeServer) writeReasoningSummaryPartAdded(
	threadID string,
	turnID string,
	itemID string,
	summaryIndex int64,
) {
	s.writeNotification("item/reasoning/summaryPartAdded", codex.ReasoningSummaryPartAddedNotification{
		ThreadID:     threadID,
		TurnID:       turnID,
		ItemID:       itemID,
		SummaryIndex: summaryIndex,
	})
}

func (s *fakeServer) writeReasoningTextDelta(threadID, turnID, itemID, delta string) {
	s.writeNotification("item/reasoning/textDelta", codex.ReasoningTextDeltaNotification{
		ThreadID: threadID,
		TurnID:   turnID,
		ItemID:   itemID,
		Delta:    delta,
	})
}

func (s *fakeServer) writeItemStarted(threadID, turnID, itemID, itemType string) {
	s.writeNotification("item/started", codex.ItemStartedNotification{
		ThreadID: threadID,
		TurnID:   turnID,
		ItemID:   itemID,
		ItemType: itemType,
	})
}

func (s *fakeServer) writePlanItemStarted(threadID, turnID, itemID string) {
	s.writeNotification("item/started", codex.ItemStartedNotification{
		ThreadID: threadID,
		TurnID:   turnID,
		ItemID:   itemID,
		ItemType: "plan",
		Item: &codex.ThreadItemRef{
			ID:   itemID,
			Type: "plan",
		},
	})
}

func (s *fakeServer) writeItemCompleted(threadID, turnID, itemID, itemType string) {
	s.writeNotification("item/completed", codex.ItemCompletedNotification{
		ThreadID: threadID,
		TurnID:   turnID,
		ItemID:   itemID,
		ItemType: itemType,
	})
}

func (s *fakeServer) writeCommandExecutionStarted(
	threadID string,
	turnID string,
	itemID string,
	command string,
	cwd string,
	actions []codex.CommandAction,
) {
	s.writeNotification("item/started", codex.ItemStartedNotification{
		ThreadID: threadID,
		TurnID:   turnID,
		ItemID:   itemID,
		ItemType: "commandExecution",
		Item: &codex.ThreadItemRef{
			ID:             itemID,
			Type:           "commandExecution",
			Command:        command,
			CommandActions: append([]codex.CommandAction(nil), actions...),
			CWD:            cwd,
			ProcessID:      fmt.Sprintf("process-%s", itemID),
			Status:         "inProgress",
		},
	})
}

func (s *fakeServer) writeCommandExecutionCompleted(
	threadID string,
	turnID string,
	itemID string,
	command string,
	cwd string,
	actions []codex.CommandAction,
	aggregatedOutput string,
	exitCode int,
	status string,
) {
	output := aggregatedOutput
	s.writeNotification("item/completed", codex.ItemCompletedNotification{
		ThreadID: threadID,
		TurnID:   turnID,
		ItemID:   itemID,
		ItemType: "commandExecution",
		Item: &codex.ThreadItemRef{
			ID:               itemID,
			Type:             "commandExecution",
			Command:          command,
			CommandActions:   append([]codex.CommandAction(nil), actions...),
			CWD:              cwd,
			AggregatedOutput: &output,
			ProcessID:        fmt.Sprintf("process-%s", itemID),
			ExitCode:         &exitCode,
			Status:           status,
		},
	})
}

func (s *fakeServer) writeCommandExecutionOutputDelta(threadID, turnID, itemID, delta string) {
	s.writeNotification("item/commandExecution/outputDelta", codex.CommandExecutionOutputDeltaNotification{
		ThreadID: threadID,
		TurnID:   turnID,
		ItemID:   itemID,
		Delta:    delta,
	})
}

func (s *fakeServer) writeTurnDiffUpdated(threadID, turnID, diff string) {
	s.writeNotification("turn/diff/updated", codex.TurnDiffUpdatedNotification{
		ThreadID: threadID,
		TurnID:   turnID,
		Diff:     diff,
	})
}

func (s *fakeServer) writePlanItemCompleted(threadID, turnID, itemID, text string) {
	s.writeNotification("item/completed", codex.ItemCompletedNotification{
		ThreadID: threadID,
		TurnID:   turnID,
		ItemID:   itemID,
		ItemType: "plan",
		Item: &codex.ThreadItemRef{
			ID:   itemID,
			Type: "plan",
			Text: text,
		},
	})
}

func (s *fakeServer) writeTurnCompleted(threadID, turnID, stopReason string) {
	s.writeNotification("turn/completed", codex.TurnCompletedNotification{
		ThreadID:   threadID,
		TurnID:     turnID,
		StopReason: stopReason,
	})
}

func (s *fakeServer) writeTurnCompletedWithError(
	threadID string,
	turnID string,
	message string,
	additionalDetails string,
	codexErrorInfo json.RawMessage,
) {
	s.writeNotification("turn/completed", codex.TurnCompletedNotification{
		ThreadID: threadID,
		Turn: &codex.TurnRef{
			ID:     turnID,
			Status: "failed",
			Error: &codex.TurnError{
				Message:           message,
				AdditionalDetails: additionalDetails,
				CodexErrorInfo:    codexErrorInfo,
			},
		},
	})
}

func (s *fakeServer) writeErrorNotification(
	threadID string,
	turnID string,
	turnErr codex.TurnError,
	willRetry bool,
) {
	s.writeNotification("error", codex.ErrorNotification{
		ThreadID:  threadID,
		TurnID:    turnID,
		Error:     turnErr,
		WillRetry: willRetry,
	})
}

func (s *fakeServer) writeReviewModeEntered(threadID, turnID string) {
	s.writeNotification("review/mode_entered", codex.ReviewModeNotification{
		ThreadID: threadID,
		TurnID:   turnID,
	})
}

func (s *fakeServer) writeReviewModeExited(threadID, turnID string) {
	s.writeNotification("review/mode_exited", codex.ReviewModeNotification{
		ThreadID: threadID,
		TurnID:   turnID,
	})
}

func (s *fakeServer) writeTurnPlanUpdated(
	threadID string,
	turnID string,
	explanation string,
	plan []codex.TurnPlanStep,
) {
	s.writeNotification("turn/plan/updated", codex.TurnPlanUpdatedNotification{
		ThreadID:    threadID,
		TurnID:      turnID,
		Explanation: explanation,
		Plan:        plan,
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
	_ = s.codec.WriteMessage(codex.RPCMessage{
		JSONRPC: "2.0",
		ID:      cloneID(id),
		Result:  resultRaw,
	})
}

func (s *fakeServer) writeError(id *json.RawMessage, code int, message string) {
	_ = s.codec.WriteMessage(codex.RPCMessage{
		JSONRPC: "2.0",
		ID:      cloneID(id),
		Error: &codex.RPCError{
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
	_ = s.codec.WriteMessage(codex.RPCMessage{
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

func (s *fakeServer) shouldCrashDuringTurn() bool {
	if s.crashDuringTurn {
		return true
	}
	if s.crashTurnOnceFile == "" {
		return false
	}

	if _, err := os.Stat(s.crashTurnOnceFile); err == nil {
		return false
	}

	if err := os.WriteFile(s.crashTurnOnceFile, []byte("crashed"), 0o644); err != nil {
		return false
	}
	return true
}

func (s *fakeServer) requestApproval(approval codex.ApprovalRequest) (codex.ApprovalDecision, error) {
	method := methodApprovalRequest
	params := any(approval)
	switch approval.Kind {
	case codex.ApprovalKindCommand:
		req := codex.CommandExecutionRequestApprovalParams{
			ThreadID:   approval.ThreadID,
			TurnID:     approval.TurnID,
			ItemID:     approval.ToolCallID,
			ApprovalID: approval.ApprovalID,
			Command:    approval.Command,
			Reason:     approval.Message,
		}
		method = methodItemCommandExecutionRequestApproval
		params = req
	}

	resp, err := s.callAdapter(method, params, 3*time.Second)
	if err != nil {
		return "", err
	}
	if resp.Error != nil {
		return "", fmt.Errorf("%s failed code=%d message=%s", method, resp.Error.Code, resp.Error.Message)
	}

	var result struct {
		Outcome  string `json:"outcome"`
		Decision string `json:"decision"`
	}
	if len(resp.Result) > 0 {
		if err := json.Unmarshal(resp.Result, &result); err != nil {
			return "", fmt.Errorf("decode approval decision: %w", err)
		}
	}
	decision := strings.TrimSpace(strings.ToLower(result.Outcome))
	if decision == "" {
		decision = strings.TrimSpace(strings.ToLower(result.Decision))
	}
	switch decision {
	case "approved", "approve", "accept", "acceptforsession", "approved_for_session":
		return codex.ApprovalDecisionApproved, nil
	case "declined", "decline", "denied":
		return codex.ApprovalDecisionDeclined, nil
	default:
		return codex.ApprovalDecisionCancelled, nil
	}
}

func (s *fakeServer) callAdapter(method string, params any, timeout time.Duration) (codex.RPCMessage, error) {
	id := s.nextRequestID()
	rawID := json.RawMessage(strconv.Quote(id))

	msg := codex.RPCMessage{
		JSONRPC: "2.0",
		Method:  method,
		ID:      cloneID(&rawID),
	}
	if params != nil {
		rawParams, err := json.Marshal(params)
		if err != nil {
			return codex.RPCMessage{}, fmt.Errorf("%s encode params: %w", method, err)
		}
		msg.Params = rawParams
	}

	ch := make(chan codex.RPCMessage, 1)
	s.mu.Lock()
	s.pending[id] = ch
	s.mu.Unlock()

	if err := s.codec.WriteMessage(msg); err != nil {
		s.mu.Lock()
		delete(s.pending, id)
		s.mu.Unlock()
		return codex.RPCMessage{}, fmt.Errorf("%s write request: %w", method, err)
	}

	select {
	case resp := <-ch:
		return resp, nil
	case <-time.After(timeout):
		s.mu.Lock()
		delete(s.pending, id)
		s.mu.Unlock()
		return codex.RPCMessage{}, fmt.Errorf("%s response timeout", method)
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
const methodItemCommandExecutionRequestApproval = "item/commandExecution/requestApproval"
