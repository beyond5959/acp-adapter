package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const gateExtensionTitle = "acp-adapter permission gate"

type stringList []string

func (s *stringList) String() string {
	return strings.Join(*s, ",")
}

func (s *stringList) Set(value string) error {
	*s = append(*s, value)
	return nil
}

type rpcInput struct {
	ID          string `json:"id,omitempty"`
	Type        string `json:"type"`
	Message     string `json:"message,omitempty"`
	Provider    string `json:"provider,omitempty"`
	ModelID     string `json:"modelId,omitempty"`
	Level       string `json:"level,omitempty"`
	SessionPath string `json:"sessionPath,omitempty"`
	Confirmed   *bool  `json:"confirmed,omitempty"`
	Cancelled   bool   `json:"cancelled,omitempty"`
}

type fakeModel struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Provider      string `json:"provider"`
	Reasoning     bool   `json:"reasoning"`
	ContextWindow int64  `json:"contextWindow,omitempty"`
}

func (m fakeModel) fullID() string {
	if strings.TrimSpace(m.Provider) == "" {
		return strings.TrimSpace(m.ID)
	}
	return strings.TrimSpace(m.Provider) + "/" + strings.TrimSpace(m.ID)
}

type approvalDecision struct {
	Confirmed *bool
	Cancelled bool
}

type activePrompt struct {
	cancel           chan struct{}
	cancelOnce       sync.Once
	approvalID       string
	approvalDecision chan approvalDecision
}

type sessionHeader struct {
	Type      string `json:"type"`
	ID        string `json:"id"`
	Timestamp string `json:"timestamp,omitempty"`
	CWD       string `json:"cwd,omitempty"`
}

type sessionInfo struct {
	Type string `json:"type"`
	Name string `json:"name,omitempty"`
}

type sessionMessage struct {
	Type    string        `json:"type"`
	ID      string        `json:"id,omitempty"`
	Message storedMessage `json:"message"`
}

type storedMessage struct {
	Role         string          `json:"role"`
	Content      json.RawMessage `json:"content,omitempty"`
	Provider     string          `json:"provider,omitempty"`
	Model        string          `json:"model,omitempty"`
	StopReason   string          `json:"stopReason,omitempty"`
	ErrorMessage string          `json:"errorMessage,omitempty"`
	Timestamp    int64           `json:"timestamp,omitempty"`
}

type sessionSnapshot struct {
	Header   sessionHeader
	Title    string
	Messages []storedMessage
}

type fakeServer struct {
	sessionDir string
	cwd        string
	models     []fakeModel

	writeMu sync.Mutex

	mu          sync.Mutex
	sessionPath string
	sessionID   string
	sessionName string
	model       fakeModel
	thinking    string
	nextTool    int
	nextPrompt  int
	active      *activePrompt
}

func main() {
	fs := flag.NewFlagSet("pi", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	mode := fs.String("mode", "", "execution mode")
	sessionDir := fs.String("session-dir", firstNonEmpty(os.Getenv("PI_SESSION_DIR"), ""), "session dir")
	provider := fs.String("provider", firstNonEmpty(os.Getenv("PI_PROVIDER"), "openai"), "provider")
	modelID := fs.String("model", firstNonEmpty(os.Getenv("PI_MODEL"), "gpt-5.1"), "model")
	var extensions stringList
	fs.Var(&extensions, "extension", "extension path")

	if err := fs.Parse(os.Args[1:]); err != nil {
		if err == flag.ErrHelp {
			return
		}
		_, _ = fmt.Fprintf(os.Stderr, "fake pi rpc parse flags: %v\n", err)
		os.Exit(1)
	}
	if strings.TrimSpace(*mode) != "rpc" {
		_, _ = fmt.Fprintf(os.Stderr, "fake pi rpc requires --mode rpc\n")
		os.Exit(1)
	}
	_ = extensions

	cwd, err := os.Getwd()
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "fake pi rpc cwd: %v\n", err)
		os.Exit(1)
	}

	models := []fakeModel{
		{ID: "gpt-5.1", Name: "GPT-5.1", Provider: "openai", Reasoning: true, ContextWindow: 200000},
		{ID: "claude-sonnet-4-5", Name: "Claude Sonnet 4.5", Provider: "anthropic", Reasoning: true, ContextWindow: 200000},
	}

	server := &fakeServer{
		sessionDir: strings.TrimSpace(*sessionDir),
		cwd:        cwd,
		models:     models,
		model:      resolveInitialModel(models, strings.TrimSpace(*provider), strings.TrimSpace(*modelID)),
		thinking:   "medium",
	}

	if err := server.serve(os.Stdin); err != nil && err != io.EOF {
		_, _ = fmt.Fprintf(os.Stderr, "fake pi rpc error: %v\n", err)
		os.Exit(1)
	}
}

func (s *fakeServer) serve(stdin io.Reader) error {
	scanner := bufio.NewScanner(stdin)
	scanner.Buffer(make([]byte, 1024), 2*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var input rpcInput
		if err := json.Unmarshal([]byte(line), &input); err != nil {
			continue
		}
		if err := s.handle(input); err != nil {
			if strings.TrimSpace(input.ID) != "" {
				s.respondError(input.ID, input.Type, err)
			}
		}
	}
	return scanner.Err()
}

func (s *fakeServer) handle(input rpcInput) error {
	switch input.Type {
	case "new_session":
		return s.handleNewSession(input)
	case "switch_session":
		return s.handleSwitchSession(input)
	case "get_state":
		return s.handleGetState(input)
	case "get_messages":
		return s.handleGetMessages(input)
	case "get_commands":
		return s.handleGetCommands(input)
	case "get_available_models":
		return s.handleGetAvailableModels(input)
	case "set_model":
		return s.handleSetModel(input)
	case "set_thinking_level":
		return s.handleSetThinkingLevel(input)
	case "prompt":
		return s.handlePrompt(input)
	case "abort":
		return s.handleAbort(input)
	case "compact":
		return s.handleCompact(input)
	case "get_session_stats":
		return s.handleGetSessionStats(input)
	case "extension_ui_response":
		s.handleExtensionUIResponse(input)
		return nil
	default:
		if strings.TrimSpace(input.ID) != "" {
			s.respondSuccess(input.ID, input.Type, map[string]any{})
		}
		return nil
	}
}

func (s *fakeServer) handleNewSession(input rpcInput) error {
	if err := os.MkdirAll(s.sessionDirPath(), 0o755); err != nil {
		return fmt.Errorf("mkdir session dir: %w", err)
	}

	sessionPath, sessionID := s.newSessionPath()
	title := "Pi Session " + sessionID
	header := sessionHeader{
		Type:      "session",
		ID:        sessionID,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		CWD:       s.cwd,
	}
	info := sessionInfo{
		Type: "session_info",
		Name: title,
	}
	if err := writeSessionFile(sessionPath, header, info, nil); err != nil {
		return err
	}

	s.mu.Lock()
	s.sessionPath = sessionPath
	s.sessionID = sessionID
	s.sessionName = title
	s.mu.Unlock()

	s.respondSuccess(input.ID, input.Type, map[string]any{"cancelled": false})
	return nil
}

func (s *fakeServer) handleSwitchSession(input rpcInput) error {
	path := strings.TrimSpace(input.SessionPath)
	if path == "" {
		return fmt.Errorf("sessionPath is required")
	}
	snapshot, err := readSessionSnapshot(path)
	if err != nil {
		return err
	}

	s.mu.Lock()
	s.sessionPath = path
	s.sessionID = firstNonEmpty(strings.TrimSpace(snapshot.Header.ID), strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)))
	s.sessionName = firstNonEmpty(strings.TrimSpace(snapshot.Title), "Loaded Pi Session")
	if strings.TrimSpace(snapshot.Header.CWD) != "" {
		s.cwd = strings.TrimSpace(snapshot.Header.CWD)
	}
	s.mu.Unlock()

	s.respondSuccess(input.ID, input.Type, map[string]any{"cancelled": false})
	return nil
}

func (s *fakeServer) handleGetState(input rpcInput) error {
	s.mu.Lock()
	state := map[string]any{
		"model":         s.model,
		"thinkingLevel": s.thinking,
		"sessionFile":   s.sessionPath,
		"sessionId":     s.sessionID,
		"sessionName":   s.sessionName,
	}
	s.mu.Unlock()

	s.respondSuccess(input.ID, input.Type, state)
	return nil
}

func (s *fakeServer) handleGetMessages(input rpcInput) error {
	s.mu.Lock()
	path := s.sessionPath
	s.mu.Unlock()

	var messages []storedMessage
	if strings.TrimSpace(path) != "" {
		snapshot, err := readSessionSnapshot(path)
		if err != nil {
			return err
		}
		messages = snapshot.Messages
	}

	s.respondSuccess(input.ID, input.Type, map[string]any{"messages": messages})
	return nil
}

func (s *fakeServer) handleGetCommands(input rpcInput) error {
	commands := []map[string]any{
		{"name": "review", "description": "Run a workspace review"},
		{"name": "review-branch", "description": "Review the current branch"},
		{"name": "review-commit", "description": "Review one commit"},
		{"name": "init", "description": "Initialize the workspace"},
		{"name": "compact", "description": "Compact the current session"},
		{"name": "logout", "description": "Clear the current login state"},
	}
	s.respondSuccess(input.ID, input.Type, map[string]any{"commands": commands})
	return nil
}

func (s *fakeServer) handleGetAvailableModels(input rpcInput) error {
	s.respondSuccess(input.ID, input.Type, map[string]any{"models": s.models})
	return nil
}

func (s *fakeServer) handleSetModel(input rpcInput) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	target := strings.TrimSpace(input.ModelID)
	provider := strings.TrimSpace(input.Provider)
	for _, model := range s.models {
		if provider != "" && !strings.EqualFold(strings.TrimSpace(model.Provider), provider) {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(model.ID), target) {
			s.model = model
			s.respondSuccess(input.ID, input.Type, map[string]any{})
			return nil
		}
	}
	return fmt.Errorf("model not found: %s/%s", provider, target)
}

func (s *fakeServer) handleSetThinkingLevel(input rpcInput) error {
	s.mu.Lock()
	s.thinking = strings.TrimSpace(input.Level)
	s.mu.Unlock()
	s.respondSuccess(input.ID, input.Type, map[string]any{})
	return nil
}

func (s *fakeServer) handlePrompt(input rpcInput) error {
	s.mu.Lock()
	if s.active != nil {
		s.mu.Unlock()
		return fmt.Errorf("prompt already active")
	}
	path := s.sessionPath
	active := &activePrompt{
		cancel:           make(chan struct{}),
		approvalDecision: make(chan approvalDecision, 1),
	}
	s.active = active
	s.mu.Unlock()

	if strings.TrimSpace(path) == "" {
		s.clearActive(active)
		return fmt.Errorf("no active session")
	}
	if err := s.appendSessionMessage(userMessage(input.Message)); err != nil {
		s.clearActive(active)
		return err
	}

	s.respondSuccess(input.ID, input.Type, map[string]any{})
	go s.runPrompt(active, strings.TrimSpace(input.Message))
	return nil
}

func (s *fakeServer) handleAbort(input rpcInput) error {
	s.mu.Lock()
	active := s.active
	s.mu.Unlock()
	if active != nil {
		active.cancelOnce.Do(func() {
			close(active.cancel)
		})
	}
	s.respondSuccess(input.ID, input.Type, map[string]any{})
	return nil
}

func (s *fakeServer) handleCompact(input rpcInput) error {
	s.respondSuccess(input.ID, input.Type, map[string]any{
		"summary": "Compacted fake Pi session",
	})
	return nil
}

func (s *fakeServer) handleGetSessionStats(input rpcInput) error {
	s.mu.Lock()
	path := s.sessionPath
	sessionID := s.sessionID
	s.mu.Unlock()

	messageCount := int64(0)
	if strings.TrimSpace(path) != "" {
		snapshot, err := readSessionSnapshot(path)
		if err != nil {
			return err
		}
		messageCount = int64(len(snapshot.Messages))
	}

	inputTokens := 40 + messageCount*10
	outputTokens := 24 + messageCount*6
	s.respondSuccess(input.ID, input.Type, map[string]any{
		"sessionFile": path,
		"sessionId":   sessionID,
		"tokens": map[string]any{
			"input":      inputTokens,
			"output":     outputTokens,
			"cacheRead":  int64(0),
			"cacheWrite": int64(0),
			"total":      inputTokens + outputTokens,
		},
		"contextUsage": map[string]any{
			"tokens":        inputTokens,
			"contextWindow": int64(200000),
			"percent":       0.01,
		},
	})
	return nil
}

func (s *fakeServer) handleExtensionUIResponse(input rpcInput) {
	s.mu.Lock()
	active := s.active
	if active == nil || strings.TrimSpace(active.approvalID) != strings.TrimSpace(input.ID) {
		s.mu.Unlock()
		return
	}
	active.approvalID = ""
	decisionCh := active.approvalDecision
	s.mu.Unlock()

	select {
	case decisionCh <- approvalDecision{Confirmed: input.Confirmed, Cancelled: input.Cancelled}:
	default:
	}
}

func (s *fakeServer) runPrompt(active *activePrompt, prompt string) {
	defer s.clearActive(active)

	s.emit(map[string]any{"type": "agent_start"})
	s.emit(map[string]any{"type": "turn_start"})

	lower := strings.ToLower(prompt)
	switch {
	case strings.Contains(lower, "approval command"):
		s.runApprovalPrompt(active)
	case strings.Contains(lower, "slow response"):
		s.emitTextDelta("starting...")
		if !s.waitOrCancelled(active, 5*time.Second) {
			s.finishAborted()
			return
		}
		s.emitTextDelta("slow response complete")
		s.finishCompleted("slow response complete")
	default:
		text := "Hello from Pi RPC."
		for _, chunk := range splitChunks(text, 6) {
			if !s.emitChunkOrAbort(active, chunk) {
				s.finishAborted()
				return
			}
		}
		s.finishCompleted(text)
	}
}

func (s *fakeServer) runApprovalPrompt(active *activePrompt) {
	toolCallID := s.nextToolCallID()
	command := "echo approved by fake pi"
	s.emit(map[string]any{
		"type":       "tool_execution_start",
		"toolCallId": toolCallID,
		"toolName":   "bash",
		"args": map[string]any{
			"command": command,
		},
	})

	approvalID := "approval-" + strconv.Itoa(s.nextPromptID())
	s.mu.Lock()
	if s.active == active {
		active.approvalID = approvalID
	}
	s.mu.Unlock()

	payload := map[string]any{
		"gate":       "acp-adapter",
		"version":    1,
		"toolCallId": toolCallID,
		"toolName":   "bash",
		"approval":   "command",
		"command":    command,
		"message":    command,
	}
	encoded, _ := json.Marshal(payload)
	s.emit(map[string]any{
		"type":    "extension_ui_request",
		"id":      approvalID,
		"method":  "confirm",
		"title":   gateExtensionTitle,
		"message": string(encoded),
	})

	decision, ok := s.waitApprovalOrCancel(active)
	if !ok {
		s.finishAborted()
		return
	}

	if decision.Cancelled || decision.Confirmed == nil || !*decision.Confirmed {
		s.emit(map[string]any{
			"type":       "tool_execution_end",
			"toolCallId": toolCallID,
			"toolName":   "bash",
			"isError":    true,
			"result": map[string]any{
				"content": []map[string]any{{
					"type": "text",
					"text": "Blocked by user",
				}},
			},
		})
		s.emitTextDelta("command not executed")
		s.finishCompleted("command not executed")
		return
	}

	s.emit(map[string]any{
		"type":       "tool_execution_update",
		"toolCallId": toolCallID,
		"toolName":   "bash",
		"partialResult": map[string]any{
			"content": []map[string]any{{
				"type": "text",
				"text": "permitted output\n",
			}},
		},
	})
	s.emit(map[string]any{
		"type":       "tool_execution_end",
		"toolCallId": toolCallID,
		"toolName":   "bash",
		"isError":    false,
		"result": map[string]any{
			"content": []map[string]any{{
				"type": "text",
				"text": "permitted output\n",
			}},
		},
	})
	s.emitTextDelta("executed command")
	s.finishCompleted("executed command")
}

func (s *fakeServer) finishCompleted(text string) {
	assistant := assistantMessage(text, "", "")
	if err := s.appendSessionMessage(assistant); err != nil {
		assistant = assistantMessage("", "error", err.Error())
		_ = s.appendSessionMessage(assistant)
	}
	s.emit(map[string]any{
		"type":     "agent_end",
		"messages": s.sessionMessages(),
	})
}

func (s *fakeServer) finishAborted() {
	assistant := assistantMessage("", "aborted", "aborted")
	_ = s.appendSessionMessage(assistant)
	s.emit(map[string]any{
		"type":     "agent_end",
		"messages": s.sessionMessages(),
	})
}

func (s *fakeServer) emitChunkOrAbort(active *activePrompt, chunk string) bool {
	select {
	case <-active.cancel:
		return false
	default:
	}
	s.emitTextDelta(chunk)
	time.Sleep(20 * time.Millisecond)
	select {
	case <-active.cancel:
		return false
	default:
		return true
	}
}

func (s *fakeServer) waitOrCancelled(active *activePrompt, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-active.cancel:
		return false
	case <-timer.C:
		return true
	}
}

func (s *fakeServer) waitApprovalOrCancel(active *activePrompt) (approvalDecision, bool) {
	select {
	case <-active.cancel:
		return approvalDecision{}, false
	case decision := <-active.approvalDecision:
		return decision, true
	}
}

func (s *fakeServer) emitTextDelta(delta string) {
	s.emit(map[string]any{
		"type": "message_update",
		"assistantMessageEvent": map[string]any{
			"type":  "text_delta",
			"delta": delta,
		},
	})
}

func (s *fakeServer) clearActive(active *activePrompt) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active == active {
		s.active = nil
	}
}

func (s *fakeServer) appendSessionMessage(message storedMessage) error {
	s.mu.Lock()
	path := s.sessionPath
	s.mu.Unlock()
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("no active session file")
	}

	snapshot, err := readSessionSnapshot(path)
	if err != nil {
		return err
	}
	snapshot.Messages = append(snapshot.Messages, message)
	info := sessionInfo{
		Type: "session_info",
		Name: firstNonEmpty(strings.TrimSpace(snapshot.Title), s.sessionName),
	}
	return writeSessionFile(path, snapshot.Header, info, snapshot.Messages)
}

func (s *fakeServer) sessionMessages() []storedMessage {
	s.mu.Lock()
	path := s.sessionPath
	s.mu.Unlock()
	if strings.TrimSpace(path) == "" {
		return nil
	}
	snapshot, err := readSessionSnapshot(path)
	if err != nil {
		return nil
	}
	return snapshot.Messages
}

func (s *fakeServer) respondSuccess(id string, command string, data any) {
	payload := map[string]any{
		"type":    "response",
		"id":      id,
		"command": command,
		"success": true,
	}
	if data != nil {
		payload["data"] = data
	}
	s.emit(payload)
}

func (s *fakeServer) respondError(id string, command string, err error) {
	s.emit(map[string]any{
		"type":    "response",
		"id":      id,
		"command": command,
		"success": false,
		"error":   err.Error(),
	})
}

func (s *fakeServer) emit(payload any) {
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, _ = os.Stdout.Write(append(data, '\n'))
}

func (s *fakeServer) sessionDirPath() string {
	if strings.TrimSpace(s.sessionDir) != "" {
		return s.sessionDir
	}
	return filepath.Join(s.cwd, ".fake-pi-sessions")
}

func (s *fakeServer) newSessionPath() (string, string) {
	id := fmt.Sprintf("session-%d", time.Now().UTC().UnixNano())
	return filepath.Join(s.sessionDirPath(), id+".jsonl"), id
}

func (s *fakeServer) nextToolCallID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextTool++
	return fmt.Sprintf("tool-%d", s.nextTool)
}

func (s *fakeServer) nextPromptID() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextPrompt++
	return s.nextPrompt
}

func resolveInitialModel(models []fakeModel, provider string, modelID string) fakeModel {
	for _, model := range models {
		if strings.EqualFold(strings.TrimSpace(model.Provider), provider) &&
			strings.EqualFold(strings.TrimSpace(model.ID), modelID) {
			return model
		}
	}
	return models[0]
}

func writeSessionFile(path string, header sessionHeader, info sessionInfo, messages []storedMessage) error {
	lines := make([]string, 0, 2+len(messages))

	headerLine, err := json.Marshal(header)
	if err != nil {
		return err
	}
	lines = append(lines, string(headerLine))

	infoLine, err := json.Marshal(info)
	if err != nil {
		return err
	}
	lines = append(lines, string(infoLine))

	for idx, message := range messages {
		entryLine, err := json.Marshal(sessionMessage{
			Type:    "message",
			ID:      fmt.Sprintf("msg-%d", idx+1),
			Message: message,
		})
		if err != nil {
			return err
		}
		lines = append(lines, string(entryLine))
	}

	return os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600)
}

func readSessionSnapshot(path string) (sessionSnapshot, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return sessionSnapshot{}, err
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) == 0 {
		return sessionSnapshot{}, fmt.Errorf("empty session file")
	}

	var header sessionHeader
	if err := json.Unmarshal([]byte(lines[0]), &header); err != nil {
		return sessionSnapshot{}, err
	}
	snapshot := sessionSnapshot{Header: header}

	for _, raw := range lines[1:] {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		var typed struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal([]byte(raw), &typed); err != nil {
			continue
		}
		switch typed.Type {
		case "session_info":
			var info sessionInfo
			if err := json.Unmarshal([]byte(raw), &info); err == nil {
				snapshot.Title = strings.TrimSpace(info.Name)
			}
		case "message":
			var entry sessionMessage
			if err := json.Unmarshal([]byte(raw), &entry); err == nil {
				snapshot.Messages = append(snapshot.Messages, entry.Message)
			}
		}
	}
	return snapshot, nil
}

func userMessage(text string) storedMessage {
	content, _ := json.Marshal([]map[string]any{{
		"type": "text",
		"text": text,
	}})
	return storedMessage{
		Role:      "user",
		Content:   content,
		Timestamp: time.Now().UTC().UnixMilli(),
	}
}

func assistantMessage(text string, stopReason string, errText string) storedMessage {
	blocks := []map[string]any{}
	if strings.TrimSpace(text) != "" {
		blocks = append(blocks, map[string]any{
			"type": "text",
			"text": text,
		})
	}
	content, _ := json.Marshal(blocks)
	return storedMessage{
		Role:         "assistant",
		Content:      content,
		Provider:     "fake",
		Model:        "pi-rpc",
		StopReason:   stopReason,
		ErrorMessage: errText,
		Timestamp:    time.Now().UTC().UnixMilli(),
	}
}

func splitChunks(text string, size int) []string {
	runes := []rune(text)
	chunks := make([]string, 0, (len(runes)+size-1)/size)
	for start := 0; start < len(runes); start += size {
		end := start + size
		if end > len(runes) {
			end = len(runes)
		}
		chunks = append(chunks, string(runes[start:end]))
	}
	return chunks
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
