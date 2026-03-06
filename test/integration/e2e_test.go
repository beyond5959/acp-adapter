package integration

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	responseTimeout = 4 * time.Second
	cancelTimeout   = 2 * time.Second
	quietWindow     = 300 * time.Millisecond
)

var (
	realSchemaOnce sync.Once
	realSchemaErr  error
)

type rpcMessage struct {
	JSONRPC string           `json:"jsonrpc,omitempty"`
	ID      *json.RawMessage `json:"id,omitempty"`
	Method  string           `json:"method,omitempty"`
	Params  json.RawMessage  `json:"params,omitempty"`
	Result  json.RawMessage  `json:"result,omitempty"`
	Error   *rpcError        `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

type sessionUpdateParams struct {
	SessionID          string                `json:"sessionId"`
	TurnID             string                `json:"turnId"`
	Type               string                `json:"type"`
	Phase              string                `json:"phase,omitempty"`
	ItemID             string                `json:"itemId,omitempty"`
	ItemType           string                `json:"itemType,omitempty"`
	Delta              string                `json:"delta,omitempty"`
	Status             string                `json:"status,omitempty"`
	Message            string                `json:"message,omitempty"`
	ToolCallID         string                `json:"toolCallId,omitempty"`
	Approval           string                `json:"approval,omitempty"`
	PermissionDecision string                `json:"permissionDecision,omitempty"`
	Todo               []todoItem            `json:"todo,omitempty"`
	ConfigOptions      []sessionConfigOption `json:"configOptions,omitempty"`
}

type todoItem struct {
	Text string `json:"text"`
	Done bool   `json:"done"`
}

type sessionConfigOption struct {
	ID           string               `json:"id"`
	Category     string               `json:"category,omitempty"`
	Name         string               `json:"name,omitempty"`
	Description  string               `json:"description,omitempty"`
	Type         string               `json:"type,omitempty"`
	CurrentValue string               `json:"currentValue,omitempty"`
	Options      []sessionConfigValue `json:"options,omitempty"`
}

type sessionConfigValue struct {
	Value       string `json:"value"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
}

type sessionRequestPermissionParams struct {
	SessionID  string   `json:"sessionId"`
	TurnID     string   `json:"turnId"`
	Approval   string   `json:"approval"`
	ToolCallID string   `json:"toolCallId,omitempty"`
	Command    string   `json:"command,omitempty"`
	Files      []string `json:"files,omitempty"`
	Host       string   `json:"host,omitempty"`
	Protocol   string   `json:"protocol,omitempty"`
	Port       int      `json:"port,omitempty"`
	MCPServer  string   `json:"mcpServer,omitempty"`
	MCPTool    string   `json:"mcpTool,omitempty"`
	Message    string   `json:"message,omitempty"`
}

type fsWriteTextFileParams struct {
	SessionID string `json:"sessionId,omitempty"`
	TurnID    string `json:"turnId,omitempty"`
	Path      string `json:"path"`
	Text      string `json:"text"`
	Patch     string `json:"patch,omitempty"`
}

type fsReadTextFileParams struct {
	SessionID string `json:"sessionId,omitempty"`
	Path      string `json:"path"`
}

type rpcReader struct {
	t        *testing.T
	messages chan rpcMessage

	mu  sync.Mutex
	err error
}

func newRPCReader(t *testing.T, stdout io.Reader) *rpcReader {
	t.Helper()

	reader := &rpcReader{
		t:        t,
		messages: make(chan rpcMessage, 256),
	}

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 1024), 2*1024*1024)
	go func() {
		defer close(reader.messages)
		for scanner.Scan() {
			line := scanner.Bytes()
			var msg rpcMessage
			if err := json.Unmarshal(line, &msg); err != nil {
				reader.setErr(fmt.Errorf("stdout line is not json-rpc: %w; line=%q", err, string(line)))
				return
			}
			if err := validateJSONRPCMessage(msg); err != nil {
				reader.setErr(fmt.Errorf("stdout line is not valid ACP JSON-RPC: %w; line=%q", err, string(line)))
				return
			}
			reader.messages <- msg
		}
		if err := scanner.Err(); err != nil {
			reader.setErr(fmt.Errorf("stdout scanner: %w", err))
		}
	}()

	return reader
}

func (r *rpcReader) next(timeout time.Duration) rpcMessage {
	r.t.Helper()
	select {
	case msg, ok := <-r.messages:
		if !ok {
			r.t.Fatalf("adapter stdout closed: %v", r.readErr())
		}
		return msg
	case <-time.After(timeout):
		r.t.Fatalf("timed out waiting for adapter message")
	}
	return rpcMessage{}
}

func (r *rpcReader) poll(timeout time.Duration) (rpcMessage, bool) {
	r.t.Helper()
	select {
	case msg, ok := <-r.messages:
		if !ok {
			r.t.Fatalf("adapter stdout closed: %v", r.readErr())
		}
		return msg, true
	case <-time.After(timeout):
		return rpcMessage{}, false
	}
}

func (r *rpcReader) readErr() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.err
}

func (r *rpcReader) setErr(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err == nil {
		r.err = err
	}
}

type adapterHarness struct {
	t      *testing.T
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	reader *rpcReader

	waitCh   chan error
	stopOnce sync.Once
}

func startAdapter(t *testing.T, extraEnv ...string) *adapterHarness {
	if isRealCodexEnabled() {
		t.Skip("fake app-server e2e skipped when E2E_REAL_CODEX=1; run real codex e2e tests")
	}
	return startAdapterWithConfig(t, nil, false, extraEnv...)
}

func startAdapterReal(t *testing.T, args []string, extraEnv ...string) *adapterHarness {
	t.Helper()
	requireRealCodex(t)
	ensureRealSchema(t)
	return startAdapterWithConfig(t, args, true, extraEnv...)
}

func startAdapterWithConfig(t *testing.T, args []string, useRealCodex bool, extraEnv ...string) *adapterHarness {
	t.Helper()

	rootDir := repoRoot(t)
	adapterBin := buildBinary(t, rootDir, "./cmd/acp")

	cmdArgs := append([]string(nil), args...)
	cmd := exec.Command(adapterBin, cmdArgs...)
	cmd.Dir = rootDir
	envBase := []string{"LOG_LEVEL=debug"}
	if useRealCodex {
		codexCmd := firstNonEmpty(strings.TrimSpace(os.Getenv("E2E_REAL_CODEX_CMD")), "codex")
		codexArgs := firstNonEmpty(strings.TrimSpace(os.Getenv("E2E_REAL_CODEX_ARGS")), "app-server")
		if _, err := exec.LookPath(codexCmd); err != nil {
			t.Skipf("real codex command %q not found: %v", codexCmd, err)
		}
		envBase = append(envBase,
			"CODEX_APP_SERVER_CMD="+codexCmd,
			"CODEX_APP_SERVER_ARGS="+codexArgs,
		)
	} else {
		fakeServerBin := buildBinary(t, rootDir, "./testdata/fake_codex_app_server")
		envBase = append(envBase,
			"CODEX_APP_SERVER_CMD="+fakeServerBin,
			"CODEX_APP_SERVER_ARGS=",
			"CHATGPT_SUBSCRIPTION_ACTIVE=1",
		)
	}
	cmd.Env = append(os.Environ(), append(envBase, extraEnv...)...)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatalf("stderr pipe: %v", err)
	}

	if err := cmd.Start(); err != nil {
		t.Fatalf("start adapter: %v", err)
	}

	waitCh := make(chan error, 1)
	go func() {
		waitCh <- cmd.Wait()
		close(waitCh)
	}()
	go func() { _, _ = io.Copy(io.Discard, stderr) }()

	h := &adapterHarness{
		t:      t,
		cmd:    cmd,
		stdin:  stdin,
		reader: newRPCReader(t, stdout),
		waitCh: waitCh,
	}
	t.Cleanup(h.stop)
	return h
}

func (h *adapterHarness) stop() {
	h.stopOnce.Do(func() {
		_ = h.stdin.Close()
		select {
		case err := <-h.waitCh:
			if err != nil {
				h.t.Fatalf("adapter exited with error: %v", err)
			}
		case <-time.After(3 * time.Second):
			_ = h.cmd.Process.Kill()
			<-h.waitCh
			h.t.Fatalf("adapter did not exit after stdin closed")
		}
	})
}

func (h *adapterHarness) sendRequest(id, method string, params any) {
	writeRequest(h.t, h.stdin, id, method, params)
}

func (h *adapterHarness) sendResultResponse(id string, result any) {
	writeResultResponse(h.t, h.stdin, id, result)
}

func (h *adapterHarness) waitResponse(id string, timeout time.Duration) rpcMessage {
	return waitForResponse(h.t, h.reader, id, timeout, nil)
}

func (h *adapterHarness) nextMessage(timeout time.Duration) rpcMessage {
	return h.reader.next(timeout)
}

func (h *adapterHarness) assertStdoutPureJSONRPC() {
	if err := h.reader.readErr(); err != nil {
		h.t.Fatalf("stdout contains non JSON-RPC output: %v", err)
	}
}

func TestE2EAcceptanceA1ToA5AndB1(t *testing.T) {
	h := startAdapter(t)

	// A2 initialize
	h.sendRequest("1", "initialize", map[string]any{})
	initResp := h.waitResponse("1", responseTimeout)
	var initResult struct {
		AgentCapabilities struct {
			Sessions      bool `json:"sessions"`
			Images        bool `json:"images"`
			ToolCalls     bool `json:"toolCalls"`
			SlashCommands bool `json:"slashCommands"`
			Permissions   bool `json:"permissions"`
		} `json:"agentCapabilities"`
	}
	unmarshalResult(t, initResp, &initResult)
	if !initResult.AgentCapabilities.Sessions ||
		!initResult.AgentCapabilities.Images ||
		!initResult.AgentCapabilities.ToolCalls ||
		!initResult.AgentCapabilities.SlashCommands ||
		!initResult.AgentCapabilities.Permissions {
		t.Fatalf("initialize capabilities mismatch: %+v", initResult.AgentCapabilities)
	}

	// A3 + B1: fake app-server will reject thread/start if adapter did not
	// complete initialize/initialized handshake before serving ACP requests.
	h.sendRequest("2", "session/new", map[string]any{})
	newResp := h.waitResponse("2", responseTimeout)
	var newResult struct {
		SessionID string `json:"sessionId"`
	}
	unmarshalResult(t, newResp, &newResult)
	if newResult.SessionID == "" {
		t.Fatalf("session/new returned empty sessionId")
	}

	// A4 session/prompt streaming
	h.sendRequest("3", "session/prompt", map[string]any{
		"sessionId": newResult.SessionID,
		"prompt":    "hello",
	})

	gotPromptResp := false
	lifecycle := map[string]bool{
		"turn_started":   false,
		"item_started":   false,
		"item_completed": false,
		"turn_completed": false,
	}
	sawStreamingMessage := false
	turnID := ""
	for !gotPromptResp {
		msg := h.nextMessage(responseTimeout)
		if msg.Method == "session/update" {
			update := decodeSessionUpdate(t, msg)
			if update.SessionID != newResult.SessionID {
				t.Fatalf("session/update routed to wrong session: got %q want %q", update.SessionID, newResult.SessionID)
			}
			if turnID == "" {
				turnID = update.TurnID
			} else if update.TurnID != turnID {
				t.Fatalf("same prompt received mixed turn IDs: %q vs %q", turnID, update.TurnID)
			}

			if update.Status != "" {
				if _, ok := lifecycle[update.Status]; ok {
					lifecycle[update.Status] = true
				}
			}
			if update.Type == "message" {
				if update.Phase != "streaming" {
					t.Fatalf("message update expected phase=streaming, got %q", update.Phase)
				}
				sawStreamingMessage = true
			}
			continue
		}
		if messageID(msg) != "3" {
			continue
		}
		var promptResult struct {
			StopReason string `json:"stopReason"`
		}
		unmarshalResult(t, msg, &promptResult)
		if promptResult.StopReason != "end_turn" {
			t.Fatalf("session/prompt expected stopReason=end_turn, got %q", promptResult.StopReason)
		}
		gotPromptResp = true
	}
	for status, seen := range lifecycle {
		if !seen {
			t.Fatalf("session/prompt missing lifecycle status %q", status)
		}
	}
	if !sawStreamingMessage {
		t.Fatalf("session/prompt expected streaming message update")
	}

	// A5 session/cancel should quickly stop turn and end with cancelled.
	h.sendRequest("4", "session/prompt", map[string]any{
		"sessionId": newResult.SessionID,
		"prompt":    "slow task",
	})

	cancelledTurnID := ""
	for cancelledTurnID == "" {
		msg := h.nextMessage(responseTimeout)
		if msg.Method == "session/update" {
			update := decodeSessionUpdate(t, msg)
			cancelledTurnID = update.TurnID
			continue
		}
		if messageID(msg) == "4" {
			var earlyResult struct {
				StopReason string `json:"stopReason"`
			}
			unmarshalResult(t, msg, &earlyResult)
			t.Fatalf("slow prompt ended before cancel, stopReason=%q", earlyResult.StopReason)
		}
	}

	h.sendRequest("5", "session/cancel", map[string]any{
		"sessionId": newResult.SessionID,
	})

	cancelStartedAt := time.Now()
	gotCancelResp := false
	gotCancelledPromptResp := false
	gotCancelledStatusUpdate := false
	for !(gotCancelResp && gotCancelledPromptResp) {
		if time.Since(cancelStartedAt) > cancelTimeout {
			t.Fatalf("cancel did not complete within %s", cancelTimeout)
		}

		msg := h.nextMessage(cancelTimeout)
		if msg.Method == "session/update" {
			update := decodeSessionUpdate(t, msg)
			if update.TurnID == cancelledTurnID && update.Status == "turn_cancelled" {
				gotCancelledStatusUpdate = true
			}
			continue
		}
		switch messageID(msg) {
		case "5":
			var cancelResult struct {
				Cancelled bool `json:"cancelled"`
			}
			unmarshalResult(t, msg, &cancelResult)
			if !cancelResult.Cancelled {
				t.Fatalf("session/cancel expected cancelled=true")
			}
			gotCancelResp = true
		case "4":
			var promptResult struct {
				StopReason string `json:"stopReason"`
			}
			unmarshalResult(t, msg, &promptResult)
			if promptResult.StopReason != "cancelled" {
				t.Fatalf("cancelled prompt expected stopReason=cancelled, got %q", promptResult.StopReason)
			}
			gotCancelledPromptResp = true
		}
	}

	if !gotCancelledStatusUpdate {
		t.Fatalf("cancel expected turn_cancelled status update")
	}
	assertNoFurtherTurnUpdates(t, h.reader, cancelledTurnID, quietWindow)

	// Keep adapter usable after cancel (sanity for no crash/no stuck process).
	h.sendRequest("6", "session/prompt", map[string]any{
		"sessionId": newResult.SessionID,
		"prompt":    "after cancel",
	})
	gotPostCancelUpdate := false
	gotPostCancelResp := false
	for !gotPostCancelResp {
		msg := h.nextMessage(responseTimeout)
		if msg.Method == "session/update" {
			gotPostCancelUpdate = true
			continue
		}
		if messageID(msg) != "6" {
			continue
		}
		var promptResult struct {
			StopReason string `json:"stopReason"`
		}
		unmarshalResult(t, msg, &promptResult)
		if promptResult.StopReason != "end_turn" {
			t.Fatalf("post-cancel prompt expected stopReason=end_turn, got %q", promptResult.StopReason)
		}
		gotPostCancelResp = true
	}
	if !gotPostCancelUpdate {
		t.Fatalf("post-cancel prompt expected at least one session/update")
	}

	// A1: stdout purity is continuously checked by rpcReader scanner.
	h.assertStdoutPureJSONRPC()
}

func TestE2EInitializeIncludesACPStandardFields(t *testing.T) {
	h := startAdapter(t)

	h.sendRequest("init-standard", "initialize", map[string]any{
		"protocolVersion": 1,
		"clientCapabilities": map[string]any{
			"fs": map[string]any{
				"readTextFile": true,
			},
		},
	})
	resp := h.waitResponse("init-standard", responseTimeout)
	var initResult struct {
		ProtocolVersion   int `json:"protocolVersion"`
		AgentCapabilities struct {
			PromptCapabilities struct {
				Image           bool `json:"image"`
				EmbeddedContext bool `json:"embeddedContext"`
			} `json:"promptCapabilities"`
		} `json:"agentCapabilities"`
	}
	unmarshalResult(t, resp, &initResult)
	if initResult.ProtocolVersion != 1 {
		t.Fatalf("initialize should return protocolVersion=1, got %d", initResult.ProtocolVersion)
	}
	if !initResult.AgentCapabilities.PromptCapabilities.Image {
		t.Fatalf("initialize should advertise promptCapabilities.image=true")
	}
	if !initResult.AgentCapabilities.PromptCapabilities.EmbeddedContext {
		t.Fatalf("initialize should advertise promptCapabilities.embeddedContext=true")
	}
}

func TestE2EPromptArrayContentBlocksAccepted(t *testing.T) {
	h := startAdapter(t)

	h.sendRequest("1", "initialize", map[string]any{})
	_ = h.waitResponse("1", responseTimeout)

	h.sendRequest("2", "session/new", map[string]any{})
	newResp := h.waitResponse("2", responseTimeout)
	var newResult struct {
		SessionID string `json:"sessionId"`
	}
	unmarshalResult(t, newResp, &newResult)
	if strings.TrimSpace(newResult.SessionID) == "" {
		t.Fatalf("session/new returned empty sessionId")
	}

	h.sendRequest("3", "session/prompt", map[string]any{
		"sessionId": newResult.SessionID,
		"prompt": []map[string]any{
			{
				"type": "text",
				"text": "hello from prompt array",
			},
		},
	})

	gotUpdate := false
	gotPromptResp := false
	for !gotPromptResp {
		msg := h.nextMessage(responseTimeout)
		if msg.Method == "session/update" {
			update := decodeSessionUpdate(t, msg)
			if update.SessionID != newResult.SessionID {
				t.Fatalf("session/update routed to wrong session: got %q want %q", update.SessionID, newResult.SessionID)
			}
			gotUpdate = true
			continue
		}
		if messageID(msg) != "3" {
			continue
		}
		var promptResult struct {
			StopReason string `json:"stopReason"`
		}
		unmarshalResult(t, msg, &promptResult)
		if promptResult.StopReason != "end_turn" {
			t.Fatalf("session/prompt with prompt array expected stopReason=end_turn, got %q", promptResult.StopReason)
		}
		gotPromptResp = true
	}
	if !gotUpdate {
		t.Fatalf("session/prompt with prompt array expected at least one session/update")
	}

	h.assertStdoutPureJSONRPC()
}

func TestE2EMessageUpdateIncludesACPUpdateEnvelope(t *testing.T) {
	h := startAdapter(t)

	h.sendRequest("1", "initialize", map[string]any{})
	_ = h.waitResponse("1", responseTimeout)

	h.sendRequest("2", "session/new", map[string]any{})
	newResp := h.waitResponse("2", responseTimeout)
	var newResult struct {
		SessionID string `json:"sessionId"`
	}
	unmarshalResult(t, newResp, &newResult)

	h.sendRequest("3", "session/prompt", map[string]any{
		"sessionId": newResult.SessionID,
		"prompt":    "hello",
	})

	sawMappedMessage := false
	sawAnySessionUpdate := false
	gotPromptResp := false
	for !gotPromptResp {
		msg := h.nextMessage(responseTimeout)
		if msg.Method == "session/update" {
			sawAnySessionUpdate = true
			var raw map[string]any
			if err := json.Unmarshal(msg.Params, &raw); err != nil {
				t.Fatalf("decode raw session/update: %v", err)
			}
			updateObj, _ := raw["update"].(map[string]any)
			if updateObj == nil {
				t.Fatalf("session/update must include params.update envelope for ACP compatibility")
			}
			kind, _ := updateObj["sessionUpdate"].(string)
			if strings.TrimSpace(kind) == "" {
				t.Fatalf("session/update.params.update.sessionUpdate should not be empty")
			}
			if kind == "agent_message_chunk" {
				content, _ := updateObj["content"].(map[string]any)
				if content != nil {
					if ctype, _ := content["type"].(string); ctype == "text" {
						if _, ok := content["text"].(string); ok {
							sawMappedMessage = true
						}
					}
				}
			}
			continue
		}
		if messageID(msg) != "3" {
			continue
		}
		var promptResult struct {
			StopReason string `json:"stopReason"`
		}
		unmarshalResult(t, msg, &promptResult)
		if promptResult.StopReason != "end_turn" {
			t.Fatalf("prompt expected stopReason=end_turn, got %q", promptResult.StopReason)
		}
		gotPromptResp = true
	}

	if !sawMappedMessage {
		t.Fatalf("expected session/update to include update.sessionUpdate=agent_message_chunk")
	}
	if !sawAnySessionUpdate {
		t.Fatalf("expected at least one session/update notification")
	}
}

func TestE2EAcceptanceB1AppServerCrashReturnsClearError(t *testing.T) {
	crashMarker := filepath.Join(t.TempDir(), "crash-once.marker")
	h := startAdapter(t, "FAKE_APP_SERVER_CRASH_ON_THREAD_START_ONCE_FILE="+crashMarker)

	h.sendRequest("1", "initialize", map[string]any{})
	initResp := h.waitResponse("1", responseTimeout)
	var initResult struct {
		AgentCapabilities struct {
			Sessions bool `json:"sessions"`
		} `json:"agentCapabilities"`
	}
	unmarshalResult(t, initResp, &initResult)
	if !initResult.AgentCapabilities.Sessions {
		t.Fatalf("initialize should still succeed before app-server crash scenario")
	}

	h.sendRequest("2", "session/new", map[string]any{})
	resp := h.waitResponse("2", responseTimeout)
	if resp.Error == nil {
		t.Fatalf("session/new expected error when app-server crashes")
	}
	if !strings.Contains(resp.Error.Message, "thread/start") {
		t.Fatalf("session/new error should be clear, got %q", resp.Error.Message)
	}

	// Next call should succeed after supervisor restarts app-server.
	h.sendRequest("3", "session/new", map[string]any{})
	recoveredResp := h.waitResponse("3", responseTimeout)
	var recovered struct {
		SessionID string `json:"sessionId"`
	}
	unmarshalResult(t, recoveredResp, &recovered)
	if recovered.SessionID == "" {
		t.Fatalf("expected recovery session/new to succeed after app-server restart")
	}

	h.assertStdoutPureJSONRPC()
}

func TestE2EAcceptanceB1AppServerCrashDuringTurnAutoRetry(t *testing.T) {
	crashMarker := filepath.Join(t.TempDir(), "crash-turn-once.marker")
	h := startAdapter(t, "FAKE_APP_SERVER_CRASH_DURING_TURN_ONCE_FILE="+crashMarker)

	h.sendRequest("1", "initialize", map[string]any{})
	_ = h.waitResponse("1", responseTimeout)

	h.sendRequest("2", "session/new", map[string]any{})
	newResp := h.waitResponse("2", responseTimeout)
	var newResult struct {
		SessionID string `json:"sessionId"`
	}
	unmarshalResult(t, newResp, &newResult)

	h.sendRequest("3", "session/prompt", map[string]any{
		"sessionId": newResult.SessionID,
		"prompt":    "recover current prompt after backend crash",
	})

	gotPromptResp := false
	sawRetrying := false
	sawTurnError := false
	turnErrorMessage := ""
	doneCount := 0
	deadline := time.Now().Add(2 * responseTimeout)
	for !gotPromptResp {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for auto-retry prompt completion")
		}
		msg := h.nextMessage(time.Until(deadline))
		if msg.Method == "session/update" {
			update := decodeSessionUpdate(t, msg)
			if update.Status == "backend_restarted_retrying" {
				sawRetrying = true
			}
			if update.Status == "turn_error" {
				sawTurnError = true
				turnErrorMessage = update.Message
			}
			if update.Type == "message" && strings.Contains(update.Delta, "done") {
				doneCount++
			}
			continue
		}
		if messageID(msg) != "3" {
			continue
		}
		var result struct {
			StopReason string `json:"stopReason"`
		}
		unmarshalResult(t, msg, &result)
		if result.StopReason != "end_turn" {
			t.Fatalf(
				"auto-retry prompt expected stopReason=end_turn, got %q (retrying=%t turnError=%t message=%q)",
				result.StopReason,
				sawRetrying,
				sawTurnError,
				turnErrorMessage,
			)
		}
		gotPromptResp = true
	}

	if !sawRetrying {
		t.Fatalf("auto-retry flow expected backend_restarted_retrying status update")
	}
	if sawTurnError {
		t.Fatalf("auto-retry success flow should not emit turn_error status")
	}
	if doneCount != 1 {
		t.Fatalf("auto-retry flow should emit terminal output exactly once, got %d", doneCount)
	}

	h.assertStdoutPureJSONRPC()
}

func TestE2EAcceptanceB1AppServerCrashDuringTurnRetryFailureHasHint(t *testing.T) {
	h := startAdapter(t, "FAKE_APP_SERVER_CRASH_DURING_TURN=1")

	h.sendRequest("1", "initialize", map[string]any{})
	_ = h.waitResponse("1", responseTimeout)

	h.sendRequest("2", "session/new", map[string]any{})
	newResp := h.waitResponse("2", responseTimeout)
	var newResult struct {
		SessionID string `json:"sessionId"`
	}
	unmarshalResult(t, newResp, &newResult)

	h.sendRequest("3", "session/prompt", map[string]any{
		"sessionId": newResult.SessionID,
		"prompt":    "this prompt should still fail after one internal retry",
	})

	gotPromptResp := false
	sawRetrying := false
	turnErrorMessage := ""
	deadline := time.Now().Add(2 * responseTimeout)
	for !gotPromptResp {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for retry-failure prompt completion")
		}
		msg := h.nextMessage(time.Until(deadline))
		if msg.Method == "session/update" {
			update := decodeSessionUpdate(t, msg)
			if update.Status == "backend_restarted_retrying" {
				sawRetrying = true
			}
			if update.Status == "turn_error" {
				turnErrorMessage = update.Message
			}
			continue
		}
		if messageID(msg) != "3" {
			continue
		}
		var result struct {
			StopReason string `json:"stopReason"`
		}
		unmarshalResult(t, msg, &result)
		if result.StopReason != "error" {
			t.Fatalf("retry-failure prompt expected stopReason=error, got %q", result.StopReason)
		}
		gotPromptResp = true
	}

	if !sawRetrying {
		t.Fatalf("retry-failure flow expected backend_restarted_retrying status update")
	}
	if !strings.Contains(strings.ToLower(turnErrorMessage), "retry this prompt once") {
		t.Fatalf("retry-failure flow expected clear retry hint, got %q", turnErrorMessage)
	}

	h.assertStdoutPureJSONRPC()
}

func TestE2ENotificationRoutingBySessionAndTurn(t *testing.T) {
	h := startAdapter(t)

	h.sendRequest("1", "initialize", map[string]any{})
	_ = h.waitResponse("1", responseTimeout)

	h.sendRequest("2", "session/new", map[string]any{})
	resp2 := h.waitResponse("2", responseTimeout)
	var s1 struct {
		SessionID string `json:"sessionId"`
	}
	unmarshalResult(t, resp2, &s1)

	h.sendRequest("3", "session/new", map[string]any{})
	resp3 := h.waitResponse("3", responseTimeout)
	var s2 struct {
		SessionID string `json:"sessionId"`
	}
	unmarshalResult(t, resp3, &s2)

	h.sendRequest("10", "session/prompt", map[string]any{
		"sessionId": s1.SessionID,
		"prompt":    "slow one",
	})
	h.sendRequest("11", "session/prompt", map[string]any{
		"sessionId": s2.SessionID,
		"prompt":    "slow two",
	})

	turnToSession := make(map[string]string)
	gotDone10 := false
	gotDone11 := false
	for !(gotDone10 && gotDone11) {
		msg := h.nextMessage(responseTimeout)
		if msg.Method == "session/update" {
			update := decodeSessionUpdate(t, msg)
			if update.SessionID != s1.SessionID && update.SessionID != s2.SessionID {
				t.Fatalf("unexpected sessionId in update: %q", update.SessionID)
			}
			if update.TurnID == "" {
				t.Fatalf("session/update missing turnId")
			}

			existing, ok := turnToSession[update.TurnID]
			if !ok {
				turnToSession[update.TurnID] = update.SessionID
			} else if existing != update.SessionID {
				t.Fatalf("turn %q routed to mixed sessions: %q vs %q", update.TurnID, existing, update.SessionID)
			}
			continue
		}

		switch messageID(msg) {
		case "10":
			var result struct {
				StopReason string `json:"stopReason"`
			}
			unmarshalResult(t, msg, &result)
			if result.StopReason != "end_turn" {
				t.Fatalf("prompt 10 expected stopReason=end_turn, got %q", result.StopReason)
			}
			gotDone10 = true
		case "11":
			var result struct {
				StopReason string `json:"stopReason"`
			}
			unmarshalResult(t, msg, &result)
			if result.StopReason != "end_turn" {
				t.Fatalf("prompt 11 expected stopReason=end_turn, got %q", result.StopReason)
			}
			gotDone11 = true
		}
	}

	if len(turnToSession) < 2 {
		t.Fatalf("expected updates for two independent turns, got %d turn(s)", len(turnToSession))
	}
}

func TestE2EAcceptanceC1MentionsResourcePreserved(t *testing.T) {
	h := startAdapter(t)

	h.sendRequest("1", "initialize", map[string]any{
		"capabilities": map[string]any{
			"fs": map[string]any{
				"read_text_file": true,
			},
		},
	})
	_ = h.waitResponse("1", responseTimeout)

	h.sendRequest("2", "session/new", map[string]any{})
	newResp := h.waitResponse("2", responseTimeout)
	var newResult struct {
		SessionID string `json:"sessionId"`
	}
	unmarshalResult(t, newResp, &newResult)

	h.sendRequest("3", "session/prompt", map[string]any{
		"sessionId": newResult.SessionID,
		"content": []map[string]any{
			{
				"type": "text",
				"text": "use mention context and summarize",
			},
			{
				"type":     "mention",
				"name":     "PROGRESS.md",
				"path":     "PROGRESS.md",
				"uri":      "file:///workspace/PROGRESS.md",
				"mimeType": "text/markdown",
				"range": map[string]any{
					"start": 0,
					"end":   128,
				},
			},
		},
	})

	gotPromptResp := false
	sawFSRead := false
	sawMentionMessage := false
	for !gotPromptResp {
		msg := h.nextMessage(responseTimeout)
		switch msg.Method {
		case "fs/read_text_file":
			req := decodeFSReadTextFileParams(t, msg)
			if strings.TrimSpace(req.Path) == "" {
				t.Fatalf("fs/read_text_file missing path")
			}
			sawFSRead = true
			h.sendResultResponse(messageID(msg), map[string]any{
				"text": "C1 resource inline content from fs",
			})
		case "session/update":
			update := decodeSessionUpdate(t, msg)
			if update.Type == "message" && strings.Contains(update.Delta, "mention[PROGRESS.md]") {
				sawMentionMessage = true
			}
		default:
			if messageID(msg) != "3" {
				continue
			}
			var result struct {
				StopReason string `json:"stopReason"`
			}
			unmarshalResult(t, msg, &result)
			if result.StopReason != "end_turn" {
				t.Fatalf("mentions prompt expected stopReason=end_turn, got %q", result.StopReason)
			}
			gotPromptResp = true
		}
	}

	if !sawFSRead {
		t.Fatalf("mention context should attempt fs/read_text_file when capability is available")
	}
	if !sawMentionMessage {
		t.Fatalf("mention prompt expected mention-aware response")
	}
}

func TestE2EEdgeC1MentionWithoutFSCapabilityDegrades(t *testing.T) {
	h := startAdapter(t)

	h.sendRequest("1", "initialize", map[string]any{})
	_ = h.waitResponse("1", responseTimeout)

	h.sendRequest("2", "session/new", map[string]any{})
	newResp := h.waitResponse("2", responseTimeout)
	var newResult struct {
		SessionID string `json:"sessionId"`
	}
	unmarshalResult(t, newResp, &newResult)

	h.sendRequest("3", "session/prompt", map[string]any{
		"sessionId": newResult.SessionID,
		"content": []map[string]any{
			{
				"type": "text",
				"text": "summarize mention",
			},
			{
				"type": "mention",
				"name": "SPEC.md",
				"path": "docs/SPEC.md",
			},
		},
	})

	gotPromptResp := false
	sawWarning := false
	sawFSRead := false
	for !gotPromptResp {
		msg := h.nextMessage(responseTimeout)
		switch msg.Method {
		case "fs/read_text_file":
			sawFSRead = true
			h.sendResultResponse(messageID(msg), map[string]any{"text": "unexpected"})
		case "session/update":
			update := decodeSessionUpdate(t, msg)
			if update.Type == "message" && strings.Contains(update.Delta, "missing mention context") {
				sawWarning = true
			}
		default:
			if messageID(msg) != "3" {
				continue
			}
			var result struct {
				StopReason string `json:"stopReason"`
			}
			unmarshalResult(t, msg, &result)
			if result.StopReason != "end_turn" {
				t.Fatalf("degrade mention prompt expected stopReason=end_turn, got %q", result.StopReason)
			}
			gotPromptResp = true
		}
	}

	if sawFSRead {
		t.Fatalf("adapter should not call fs/read_text_file when capability is absent")
	}
	if !sawWarning {
		t.Fatalf("adapter should emit missing-context warning when mention cannot be read")
	}
}

func TestE2EAcceptanceC2ImageContentBlock(t *testing.T) {
	h := startAdapter(t)

	h.sendRequest("1", "initialize", map[string]any{})
	_ = h.waitResponse("1", responseTimeout)

	h.sendRequest("2", "session/new", map[string]any{})
	newResp := h.waitResponse("2", responseTimeout)
	var newResult struct {
		SessionID string `json:"sessionId"`
	}
	unmarshalResult(t, newResp, &newResult)

	const tinyPNGBase64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO5W9tQAAAAASUVORK5CYII="
	h.sendRequest("3", "session/prompt", map[string]any{
		"sessionId": newResult.SessionID,
		"content": []map[string]any{
			{
				"type": "text",
				"text": "describe the image briefly",
			},
			{
				"type":     "image",
				"mimeType": "image/png",
				"data":     tinyPNGBase64,
			},
		},
	})

	gotPromptResp := false
	sawImageMessage := false
	for !gotPromptResp {
		msg := h.nextMessage(responseTimeout)
		if msg.Method == "session/update" {
			update := decodeSessionUpdate(t, msg)
			if update.Type == "message" && strings.Contains(update.Delta, "image context received") {
				sawImageMessage = true
			}
			continue
		}
		if messageID(msg) != "3" {
			continue
		}
		var result struct {
			StopReason string `json:"stopReason"`
		}
		unmarshalResult(t, msg, &result)
		if result.StopReason != "end_turn" {
			t.Fatalf("image prompt expected stopReason=end_turn, got %q", result.StopReason)
		}
		gotPromptResp = true
	}

	if !sawImageMessage {
		t.Fatalf("image prompt expected image-aware streaming output")
	}
}

func TestE2EEdgeC2InvalidImageBase64Rejected(t *testing.T) {
	h := startAdapter(t)

	h.sendRequest("1", "initialize", map[string]any{})
	_ = h.waitResponse("1", responseTimeout)

	h.sendRequest("2", "session/new", map[string]any{})
	newResp := h.waitResponse("2", responseTimeout)
	var newResult struct {
		SessionID string `json:"sessionId"`
	}
	unmarshalResult(t, newResp, &newResult)

	h.sendRequest("3", "session/prompt", map[string]any{
		"sessionId": newResult.SessionID,
		"content": []map[string]any{
			{
				"type": "text",
				"text": "this should fail due to invalid image payload",
			},
			{
				"type":     "image",
				"mimeType": "image/png",
				"data":     "%%%invalid-base64%%%",
			},
		},
	})

	resp := h.waitResponse("3", responseTimeout)
	if resp.Error == nil {
		t.Fatalf("invalid image payload should return error")
	}
	if resp.Error.Code != -32602 {
		t.Fatalf("invalid image payload expected -32602, got %d", resp.Error.Code)
	}
	if !strings.Contains(strings.ToLower(resp.Error.Message), "invalid params") {
		t.Fatalf("invalid image payload should surface invalid params, got %q", resp.Error.Message)
	}
}

func TestE2EAcceptanceF1StructuredTODOAcrossTurns(t *testing.T) {
	h := startAdapter(t)

	h.sendRequest("1", "initialize", map[string]any{})
	_ = h.waitResponse("1", responseTimeout)

	h.sendRequest("2", "session/new", map[string]any{})
	newResp := h.waitResponse("2", responseTimeout)
	var newResult struct {
		SessionID string `json:"sessionId"`
	}
	unmarshalResult(t, newResp, &newResult)

	runTodoTurn := func(requestID string, prompt string) ([]todoItem, string) {
		t.Helper()
		h.sendRequest(requestID, "session/prompt", map[string]any{
			"sessionId": newResult.SessionID,
			"prompt":    prompt,
		})

		gotPromptResp := false
		var todos []todoItem
		var deltas []string
		for !gotPromptResp {
			msg := h.nextMessage(responseTimeout)
			if msg.Method == "session/update" {
				update := decodeSessionUpdate(t, msg)
				if update.Type == "message" && strings.TrimSpace(update.Delta) != "" {
					deltas = append(deltas, update.Delta)
				}
				if len(update.Todo) > 0 {
					todos = update.Todo
				}
				continue
			}
			if messageID(msg) != requestID {
				continue
			}
			var result struct {
				StopReason string `json:"stopReason"`
			}
			unmarshalResult(t, msg, &result)
			if result.StopReason != "end_turn" {
				t.Fatalf("todo prompt expected stopReason=end_turn, got %q", result.StopReason)
			}
			gotPromptResp = true
		}
		return todos, strings.Join(deltas, "\n")
	}

	todos1, text1 := runTodoTurn("3", "generate todo checklist")
	if !strings.Contains(text1, "- [ ]") {
		t.Fatalf("first todo turn should include markdown checklist, got: %s", text1)
	}
	if len(todos1) < 2 {
		t.Fatalf("first todo turn should include structured todo items, got %+v", todos1)
	}

	todos2, text2 := runTodoTurn("4", "continue todo and mark first done")
	if !strings.Contains(text2, "- [x]") {
		t.Fatalf("second todo turn should include updated checklist, got: %s", text2)
	}
	if len(todos2) < 3 {
		t.Fatalf("second todo turn should include updated structured todo items, got %+v", todos2)
	}
	if !todos2[0].Done {
		t.Fatalf("second todo turn should mark first item done, got %+v", todos2[0])
	}
}

func TestE2EAcceptanceD1ToD5ApprovalsBridge(t *testing.T) {
	h := startAdapter(t)

	h.sendRequest("1", "initialize", map[string]any{})
	_ = h.waitResponse("1", responseTimeout)

	h.sendRequest("2", "session/new", map[string]any{})
	newResp := h.waitResponse("2", responseTimeout)
	var newResult struct {
		SessionID string `json:"sessionId"`
	}
	unmarshalResult(t, newResp, &newResult)
	if newResult.SessionID == "" {
		t.Fatalf("session/new returned empty sessionId")
	}

	testCases := []struct {
		name         string
		prompt       string
		approvalKind string
		outcome      string
		expectStatus string
		expectExec   bool
		validate     func(*testing.T, sessionRequestPermissionParams)
	}{
		{
			name:         "command_approve",
			prompt:       "approval command",
			approvalKind: "command",
			outcome:      "approved",
			expectStatus: "completed",
			expectExec:   true,
			validate: func(t *testing.T, req sessionRequestPermissionParams) {
				if req.Command == "" {
					t.Fatalf("command approval must include command")
				}
			},
		},
		{
			name:         "command_decline",
			prompt:       "approval command",
			approvalKind: "command",
			outcome:      "declined",
			expectStatus: "failed",
			expectExec:   false,
			validate: func(t *testing.T, req sessionRequestPermissionParams) {
				if req.Command == "" {
					t.Fatalf("command approval must include command")
				}
			},
		},
		{
			name:         "file_decline",
			prompt:       "approval file",
			approvalKind: "file",
			outcome:      "declined",
			expectStatus: "failed",
			expectExec:   false,
			validate: func(t *testing.T, req sessionRequestPermissionParams) {
				if len(req.Files) == 0 {
					t.Fatalf("file approval must include file targets")
				}
			},
		},
		{
			name:         "network_cancel",
			prompt:       "approval network",
			approvalKind: "network",
			outcome:      "cancelled",
			expectStatus: "failed",
			expectExec:   false,
			validate: func(t *testing.T, req sessionRequestPermissionParams) {
				if req.Host == "" || req.Protocol == "" || req.Port <= 0 {
					t.Fatalf("network approval must include host/protocol/port, got %+v", req)
				}
			},
		},
		{
			name:         "mcp_decline",
			prompt:       "approval mcp",
			approvalKind: "mcp",
			outcome:      "declined",
			expectStatus: "failed",
			expectExec:   false,
			validate: func(t *testing.T, req sessionRequestPermissionParams) {
				if req.MCPServer == "" || req.MCPTool == "" {
					t.Fatalf("mcp approval must include server/tool, got %+v", req)
				}
			},
		},
	}

	for idx, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			requestID := fmt.Sprintf("30%d", idx+1)
			h.sendRequest(requestID, "session/prompt", map[string]any{
				"sessionId": newResult.SessionID,
				"prompt":    tc.prompt,
			})

			deadline := time.Now().Add(2 * responseTimeout)
			gotPermission := false
			gotPromptResp := false
			sawToolInProgress := false
			finalToolStatus := ""
			finalDecision := ""
			sawExecuted := false
			sawNotExecuted := false

			for !(gotPermission && gotPromptResp && finalToolStatus != "") {
				if time.Now().After(deadline) {
					t.Fatalf("approval flow timeout: permission=%v promptResp=%v finalToolStatus=%q", gotPermission, gotPromptResp, finalToolStatus)
				}

				msg := h.nextMessage(time.Until(deadline))
				switch msg.Method {
				case "session/request_permission":
					gotPermission = true
					req := decodeSessionRequestPermission(t, msg)
					if req.SessionID != newResult.SessionID {
						t.Fatalf("permission routed to wrong session: got=%q want=%q", req.SessionID, newResult.SessionID)
					}
					if req.Approval != tc.approvalKind {
						t.Fatalf("permission kind mismatch: got=%q want=%q", req.Approval, tc.approvalKind)
					}
					if req.ToolCallID == "" {
						t.Fatalf("permission missing toolCallId")
					}
					tc.validate(t, req)
					h.sendResultResponse(messageID(msg), map[string]any{"outcome": tc.outcome})
				case "session/update":
					update := decodeSessionUpdate(t, msg)
					if update.Type == "message" {
						if strings.Contains(update.Delta, "executed ") {
							sawExecuted = true
						}
						if strings.Contains(update.Delta, "not executed") {
							sawNotExecuted = true
						}
						continue
					}
					if update.Type != "tool_call_update" {
						continue
					}
					if update.Approval != tc.approvalKind {
						t.Fatalf("tool_call_update approval mismatch: got=%q want=%q", update.Approval, tc.approvalKind)
					}
					if update.ToolCallID == "" {
						t.Fatalf("tool_call_update missing toolCallId")
					}
					if update.Status == "in_progress" {
						sawToolInProgress = true
					}
					if update.Status == "completed" || update.Status == "failed" {
						finalToolStatus = update.Status
						finalDecision = update.PermissionDecision
					}
				default:
					if messageID(msg) != requestID {
						continue
					}
					var promptResult struct {
						StopReason string `json:"stopReason"`
					}
					unmarshalResult(t, msg, &promptResult)
					if promptResult.StopReason != "end_turn" {
						t.Fatalf("approval prompt expected stopReason=end_turn, got %q", promptResult.StopReason)
					}
					gotPromptResp = true
				}
			}

			if !gotPermission {
				t.Fatalf("approval flow expected session/request_permission")
			}
			if !sawToolInProgress {
				t.Fatalf("approval flow expected tool_call_update in_progress")
			}
			if finalToolStatus != tc.expectStatus {
				t.Fatalf("tool_call_update status mismatch: got=%q want=%q", finalToolStatus, tc.expectStatus)
			}
			if finalDecision != tc.outcome {
				t.Fatalf("permission decision mismatch: got=%q want=%q", finalDecision, tc.outcome)
			}
			if tc.expectExec {
				if !sawExecuted {
					t.Fatalf("approved flow expected executed marker")
				}
			} else {
				if sawExecuted {
					t.Fatalf("declined/cancelled flow must not execute tool")
				}
				if !sawNotExecuted {
					t.Fatalf("declined/cancelled flow expected not executed marker")
				}
			}
		})
	}

	h.assertStdoutPureJSONRPC()
}

func TestE2EAcceptanceE1ReviewWorkflow(t *testing.T) {
	h := startAdapter(t)

	h.sendRequest("1", "initialize", map[string]any{})
	_ = h.waitResponse("1", responseTimeout)

	h.sendRequest("2", "session/new", map[string]any{})
	newResp := h.waitResponse("2", responseTimeout)
	var newResult struct {
		SessionID string `json:"sessionId"`
	}
	unmarshalResult(t, newResp, &newResult)

	h.sendRequest("3", "session/prompt", map[string]any{
		"sessionId": newResult.SessionID,
		"prompt":    "/review check recent changes",
	})

	gotPromptResp := false
	sawReviewEnter := false
	sawReviewExit := false
	sawDiff := false
	for !gotPromptResp {
		msg := h.nextMessage(responseTimeout)
		switch msg.Method {
		case "session/request_permission":
			req := decodeSessionRequestPermission(t, msg)
			if req.Approval != "file" {
				t.Fatalf("review should request file permission, got %q", req.Approval)
			}
			h.sendResultResponse(messageID(msg), map[string]any{"outcome": "approved"})
		case "session/update":
			update := decodeSessionUpdate(t, msg)
			if update.Status == "review_mode_entered" {
				sawReviewEnter = true
			}
			if update.Status == "review_mode_exited" {
				sawReviewExit = true
			}
			if update.Type == "message" && strings.Contains(update.Delta, "```diff") {
				sawDiff = true
			}
		default:
			if messageID(msg) != "3" {
				continue
			}
			var result struct {
				StopReason string `json:"stopReason"`
			}
			unmarshalResult(t, msg, &result)
			if result.StopReason != "end_turn" {
				t.Fatalf("review prompt expected stopReason=end_turn, got %q", result.StopReason)
			}
			gotPromptResp = true
		}
	}

	if !sawReviewEnter || !sawReviewExit {
		t.Fatalf("review mode transitions missing: entered=%v exited=%v", sawReviewEnter, sawReviewExit)
	}
	if !sawDiff {
		t.Fatalf("review workflow expected readable diff output")
	}
}

func TestE2EAcceptanceE2PatchModeAAppServer(t *testing.T) {
	h := startAdapter(t)

	h.sendRequest("1", "initialize", map[string]any{})
	_ = h.waitResponse("1", responseTimeout)

	h.sendRequest("2", "session/new", map[string]any{})
	newResp := h.waitResponse("2", responseTimeout)
	var newResult struct {
		SessionID string `json:"sessionId"`
	}
	unmarshalResult(t, newResp, &newResult)

	h.sendRequest("3", "session/prompt", map[string]any{
		"sessionId": newResult.SessionID,
		"prompt":    "/review mode appserver",
	})

	gotPromptResp := false
	sawToolCompleted := false
	sawApplyMessage := false
	sawFSWriteCall := false
	for !gotPromptResp {
		msg := h.nextMessage(responseTimeout)
		switch msg.Method {
		case "session/request_permission":
			req := decodeSessionRequestPermission(t, msg)
			if req.Approval != "file" {
				t.Fatalf("review permission expected file, got %q", req.Approval)
			}
			h.sendResultResponse(messageID(msg), map[string]any{"outcome": "approved"})
		case "fs/write_text_file":
			sawFSWriteCall = true
			h.sendResultResponse(messageID(msg), map[string]any{"ok": true})
		case "session/update":
			update := decodeSessionUpdate(t, msg)
			if update.Type == "message" && strings.Contains(update.Delta, "patch applied via appserver") {
				sawApplyMessage = true
			}
			if update.Type == "tool_call_update" && update.Status == "completed" && update.PermissionDecision == "approved" {
				sawToolCompleted = true
			}
		default:
			if messageID(msg) != "3" {
				continue
			}
			var result struct {
				StopReason string `json:"stopReason"`
			}
			unmarshalResult(t, msg, &result)
			if result.StopReason != "end_turn" {
				t.Fatalf("review apply mode A expected stopReason=end_turn, got %q", result.StopReason)
			}
			gotPromptResp = true
		}
	}

	if sawFSWriteCall {
		t.Fatalf("mode A should not call ACP fs/write_text_file")
	}
	if !sawToolCompleted || !sawApplyMessage {
		t.Fatalf("mode A expected completed tool update and apply message")
	}
}

func TestE2EAcceptanceE2PatchModeBACPFS(t *testing.T) {
	h := startAdapter(t, "PATCH_APPLY_MODE=acp_fs")

	h.sendRequest("1", "initialize", map[string]any{})
	_ = h.waitResponse("1", responseTimeout)

	h.sendRequest("2", "session/new", map[string]any{})
	newResp := h.waitResponse("2", responseTimeout)
	var newResult struct {
		SessionID string `json:"sessionId"`
	}
	unmarshalResult(t, newResp, &newResult)

	h.sendRequest("3", "session/prompt", map[string]any{
		"sessionId": newResult.SessionID,
		"prompt":    "/review mode acpfs",
	})

	gotPromptResp := false
	sawFSWriteCall := false
	sawApplyStatus := false
	sawToolCompleted := false
	for !gotPromptResp {
		msg := h.nextMessage(responseTimeout)
		switch msg.Method {
		case "session/request_permission":
			h.sendResultResponse(messageID(msg), map[string]any{"outcome": "approved"})
		case "fs/write_text_file":
			sawFSWriteCall = true
			writeReq := decodeFSWriteTextFileParams(t, msg)
			if writeReq.Path == "" || writeReq.Text == "" {
				t.Fatalf("fs/write_text_file missing path or text: %+v", writeReq)
			}
			h.sendResultResponse(messageID(msg), map[string]any{"ok": true})
		case "session/update":
			update := decodeSessionUpdate(t, msg)
			if update.Status == "review_apply_applied" {
				sawApplyStatus = true
			}
			if update.Type == "tool_call_update" && update.Status == "completed" && update.PermissionDecision == "approved" {
				sawToolCompleted = true
			}
		default:
			if messageID(msg) != "3" {
				continue
			}
			var result struct {
				StopReason string `json:"stopReason"`
			}
			unmarshalResult(t, msg, &result)
			if result.StopReason != "end_turn" {
				t.Fatalf("review apply mode B expected stopReason=end_turn, got %q", result.StopReason)
			}
			gotPromptResp = true
		}
	}

	if !sawFSWriteCall {
		t.Fatalf("mode B must call ACP fs/write_text_file")
	}
	if !sawApplyStatus || !sawToolCompleted {
		t.Fatalf("mode B expected review_apply_applied and completed tool update")
	}
}

func TestE2EReviewPatchConflictVisibleModeB(t *testing.T) {
	h := startAdapter(t, "PATCH_APPLY_MODE=acp_fs")

	h.sendRequest("1", "initialize", map[string]any{})
	_ = h.waitResponse("1", responseTimeout)

	h.sendRequest("2", "session/new", map[string]any{})
	newResp := h.waitResponse("2", responseTimeout)
	var newResult struct {
		SessionID string `json:"sessionId"`
	}
	unmarshalResult(t, newResp, &newResult)

	h.sendRequest("3", "session/prompt", map[string]any{
		"sessionId": newResult.SessionID,
		"prompt":    "/review conflict",
	})

	gotPromptResp := false
	sawFailureStatus := false
	sawToolFailed := false
	for !gotPromptResp {
		msg := h.nextMessage(responseTimeout)
		switch msg.Method {
		case "session/request_permission":
			h.sendResultResponse(messageID(msg), map[string]any{"outcome": "approved"})
		case "fs/write_text_file":
			h.sendResultResponse(messageID(msg), map[string]any{
				"conflict": true,
				"message":  "merge conflict on docs/README.md",
			})
		case "session/update":
			update := decodeSessionUpdate(t, msg)
			if update.Status == "review_apply_failed" && strings.Contains(strings.ToLower(update.Message), "conflict") {
				sawFailureStatus = true
			}
			if update.Type == "tool_call_update" && update.Status == "failed" && update.PermissionDecision == "approved" {
				sawToolFailed = true
			}
		default:
			if messageID(msg) != "3" {
				continue
			}
			var result struct {
				StopReason string `json:"stopReason"`
			}
			unmarshalResult(t, msg, &result)
			if result.StopReason != "end_turn" {
				t.Fatalf("conflict review expected stopReason=end_turn, got %q", result.StopReason)
			}
			gotPromptResp = true
		}
	}

	if !sawFailureStatus || !sawToolFailed {
		t.Fatalf("conflict flow expected visible review_apply_failed and failed tool_call_update")
	}
}

func TestE2EAcceptanceG2G3ReviewBranchAndCommit(t *testing.T) {
	h := startAdapter(t)

	h.sendRequest("1", "initialize", map[string]any{})
	_ = h.waitResponse("1", responseTimeout)

	h.sendRequest("2", "session/new", map[string]any{})
	newResp := h.waitResponse("2", responseTimeout)
	var newResult struct {
		SessionID string `json:"sessionId"`
	}
	unmarshalResult(t, newResp, &newResult)

	cases := []struct {
		id      string
		command string
	}{
		{id: "3", command: "/review-branch main"},
		{id: "4", command: "/review-commit abc1234"},
	}

	for _, tc := range cases {
		t.Run(tc.command, func(t *testing.T) {
			h.sendRequest(tc.id, "session/prompt", map[string]any{
				"sessionId": newResult.SessionID,
				"prompt":    tc.command,
			})

			gotPromptResp := false
			sawReviewEnter := false
			sawReviewExit := false

			for !gotPromptResp {
				msg := h.nextMessage(responseTimeout)
				switch msg.Method {
				case "session/request_permission":
					req := decodeSessionRequestPermission(t, msg)
					if req.Approval != "file" {
						t.Fatalf("review command should request file approval, got %q", req.Approval)
					}
					h.sendResultResponse(messageID(msg), map[string]any{"outcome": "approved"})
				case "session/update":
					update := decodeSessionUpdate(t, msg)
					if update.Status == "review_mode_entered" {
						sawReviewEnter = true
					}
					if update.Status == "review_mode_exited" {
						sawReviewExit = true
					}
				default:
					if messageID(msg) != tc.id {
						continue
					}
					var result struct {
						StopReason string `json:"stopReason"`
					}
					unmarshalResult(t, msg, &result)
					if result.StopReason != "end_turn" {
						t.Fatalf("%s expected stopReason=end_turn, got %q", tc.command, result.StopReason)
					}
					gotPromptResp = true
				}
			}

			if !sawReviewEnter || !sawReviewExit {
				t.Fatalf("%s expected review mode transitions, entered=%v exited=%v", tc.command, sawReviewEnter, sawReviewExit)
			}
		})
	}
}

func TestE2EAcceptanceG4InitRequiresPermission(t *testing.T) {
	h := startAdapter(t)

	h.sendRequest("1", "initialize", map[string]any{})
	_ = h.waitResponse("1", responseTimeout)

	h.sendRequest("2", "session/new", map[string]any{})
	newResp := h.waitResponse("2", responseTimeout)
	var newResult struct {
		SessionID string `json:"sessionId"`
	}
	unmarshalResult(t, newResp, &newResult)

	h.sendRequest("3", "session/prompt", map[string]any{
		"sessionId": newResult.SessionID,
		"prompt":    "/init setup project defaults",
	})

	gotPermission := false
	gotPromptResp := false
	gotToolCompleted := false
	for !gotPromptResp {
		msg := h.nextMessage(responseTimeout)
		switch msg.Method {
		case "session/request_permission":
			gotPermission = true
			req := decodeSessionRequestPermission(t, msg)
			if req.Approval != "file" {
				t.Fatalf("/init should request file approval, got %q", req.Approval)
			}
			h.sendResultResponse(messageID(msg), map[string]any{"outcome": "approved"})
		case "session/update":
			update := decodeSessionUpdate(t, msg)
			if update.Type == "tool_call_update" && update.Status == "completed" {
				gotToolCompleted = true
			}
		default:
			if messageID(msg) != "3" {
				continue
			}
			var result struct {
				StopReason string `json:"stopReason"`
			}
			unmarshalResult(t, msg, &result)
			if result.StopReason != "end_turn" {
				t.Fatalf("/init expected stopReason=end_turn, got %q", result.StopReason)
			}
			gotPromptResp = true
		}
	}

	if !gotPermission {
		t.Fatalf("/init expected permission request")
	}
	if !gotToolCompleted {
		t.Fatalf("/init expected completed tool_call_update")
	}
}

func TestE2EAcceptanceG5Compact(t *testing.T) {
	h := startAdapter(t)

	h.sendRequest("1", "initialize", map[string]any{})
	_ = h.waitResponse("1", responseTimeout)

	h.sendRequest("2", "session/new", map[string]any{})
	newResp := h.waitResponse("2", responseTimeout)
	var newResult struct {
		SessionID string `json:"sessionId"`
	}
	unmarshalResult(t, newResp, &newResult)

	h.sendRequest("3", "session/prompt", map[string]any{
		"sessionId": newResult.SessionID,
		"prompt":    "/compact",
	})

	gotPromptResp := false
	sawCompactMessage := false
	for !gotPromptResp {
		msg := h.nextMessage(responseTimeout)
		if msg.Method == "session/update" {
			update := decodeSessionUpdate(t, msg)
			if update.Type == "message" && strings.Contains(update.Delta, "conversation compacted") {
				sawCompactMessage = true
			}
			continue
		}
		if messageID(msg) != "3" {
			continue
		}
		var result struct {
			StopReason string `json:"stopReason"`
		}
		unmarshalResult(t, msg, &result)
		if result.StopReason != "end_turn" {
			t.Fatalf("/compact expected stopReason=end_turn, got %q", result.StopReason)
		}
		gotPromptResp = true
	}

	if !sawCompactMessage {
		t.Fatalf("/compact expected compact summary message")
	}
}

func TestE2EAcceptanceG6LogoutRequiresReauth(t *testing.T) {
	h := startAdapter(t)

	h.sendRequest("1", "initialize", map[string]any{})
	_ = h.waitResponse("1", responseTimeout)

	h.sendRequest("2", "session/new", map[string]any{})
	newResp := h.waitResponse("2", responseTimeout)
	var newResult struct {
		SessionID string `json:"sessionId"`
	}
	unmarshalResult(t, newResp, &newResult)

	h.sendRequest("3", "session/prompt", map[string]any{
		"sessionId": newResult.SessionID,
		"prompt":    "/logout",
	})

	gotLogoutResp := false
	sawLogoutStatus := false
	sawSubscriptionGuidance := false
	for !gotLogoutResp {
		msg := h.nextMessage(responseTimeout)
		if msg.Method == "session/update" {
			update := decodeSessionUpdate(t, msg)
			if update.Status == "auth_logged_out" {
				sawLogoutStatus = true
			}
			if update.Type == "message" && strings.Contains(update.Delta, "codex login") {
				sawSubscriptionGuidance = true
			}
			continue
		}
		if messageID(msg) != "3" {
			continue
		}
		var result struct {
			StopReason string `json:"stopReason"`
		}
		unmarshalResult(t, msg, &result)
		if result.StopReason != "end_turn" {
			t.Fatalf("/logout expected stopReason=end_turn, got %q", result.StopReason)
		}
		gotLogoutResp = true
	}
	if !sawLogoutStatus {
		t.Fatalf("/logout expected auth_logged_out status update")
	}
	if !sawSubscriptionGuidance {
		t.Fatalf("/logout expected subscription re-login guidance containing codex login")
	}

	h.sendRequest("4", "session/prompt", map[string]any{
		"sessionId": newResult.SessionID,
		"prompt":    "after logout",
	})
	resp4 := h.waitResponse("4", responseTimeout)
	if resp4.Error == nil || !strings.Contains(strings.ToLower(resp4.Error.Message), "authentication") {
		t.Fatalf("session/prompt after logout should require authentication, got %+v", resp4.Error)
	}
	data4 := decodeRPCErrorDataMap(t, resp4.Error)
	hint4 := valueAsStringAny(data4["hint"])
	if !strings.Contains(strings.ToLower(hint4), "codex login") {
		t.Fatalf("session/prompt auth error hint should suggest codex login, got %q", hint4)
	}

	h.sendRequest("5", "session/new", map[string]any{})
	resp5 := h.waitResponse("5", responseTimeout)
	if resp5.Error == nil || !strings.Contains(strings.ToLower(resp5.Error.Message), "authentication") {
		t.Fatalf("session/new after logout should require authentication, got %+v", resp5.Error)
	}
}

func TestE2EAcceptanceG6LogoutGuidanceWithAPIKeysAndRecoveryAfterRestart(t *testing.T) {
	cases := []struct {
		name        string
		env         []string
		guidanceHit []string
	}{
		{
			name: "codex_api_key",
			env: []string{
				"CODEX_API_KEY=sk-codex",
				"OPENAI_API_KEY=",
				"CHATGPT_SUBSCRIPTION_ACTIVE=0",
			},
			guidanceHit: []string{"export CODEX_API_KEY", "unset OPENAI_API_KEY"},
		},
		{
			name: "openai_api_key",
			env: []string{
				"CODEX_API_KEY=",
				"OPENAI_API_KEY=sk-openai",
				"CHATGPT_SUBSCRIPTION_ACTIVE=0",
			},
			guidanceHit: []string{"export OPENAI_API_KEY", "unset CODEX_API_KEY"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := startAdapter(t, tc.env...)

			h.sendRequest("1", "initialize", map[string]any{})
			_ = h.waitResponse("1", responseTimeout)

			h.sendRequest("2", "session/new", map[string]any{})
			newResp := h.waitResponse("2", responseTimeout)
			var newResult struct {
				SessionID string `json:"sessionId"`
			}
			unmarshalResult(t, newResp, &newResult)

			h.sendRequest("3", "session/prompt", map[string]any{
				"sessionId": newResult.SessionID,
				"prompt":    "/logout",
			})

			gotLogoutResp := false
			var guidance string
			for !gotLogoutResp {
				msg := h.nextMessage(responseTimeout)
				if msg.Method == "session/update" {
					update := decodeSessionUpdate(t, msg)
					if update.Type == "message" {
						guidance += "\n" + update.Delta
					}
					continue
				}
				if messageID(msg) != "3" {
					continue
				}
				var result struct {
					StopReason string `json:"stopReason"`
				}
				unmarshalResult(t, msg, &result)
				if result.StopReason != "end_turn" {
					t.Fatalf("/logout expected stopReason=end_turn, got %q", result.StopReason)
				}
				gotLogoutResp = true
			}
			for _, token := range tc.guidanceHit {
				if !strings.Contains(guidance, token) {
					t.Fatalf("logout guidance missing %q, got: %s", token, guidance)
				}
			}

			h.sendRequest("4", "session/prompt", map[string]any{
				"sessionId": newResult.SessionID,
				"prompt":    "after logout",
			})
			resp4 := h.waitResponse("4", responseTimeout)
			if resp4.Error == nil || !strings.Contains(strings.ToLower(resp4.Error.Message), "authentication") {
				t.Fatalf("session/prompt after logout should require authentication, got %+v", resp4.Error)
			}
			data4 := decodeRPCErrorDataMap(t, resp4.Error)
			command := valueAsStringAny(data4["nextStepCommand"])
			if command == "" {
				t.Fatalf("auth error should include nextStepCommand")
			}
			for _, token := range tc.guidanceHit {
				if !strings.Contains(command, strings.Fields(token)[1]) && strings.Contains(token, "export") {
					t.Fatalf("nextStepCommand should align with guidance token %q, got %q", token, command)
				}
			}

			h.stop()

			h2 := startAdapter(t, tc.env...)
			h2.sendRequest("r1", "initialize", map[string]any{})
			_ = h2.waitResponse("r1", responseTimeout)
			h2.sendRequest("r2", "session/new", map[string]any{})
			recoveredNew := h2.waitResponse("r2", responseTimeout)
			var recovered struct {
				SessionID string `json:"sessionId"`
			}
			unmarshalResult(t, recoveredNew, &recovered)
			if recovered.SessionID == "" {
				t.Fatalf("session/new should recover after restart with injected key")
			}
			h2.sendRequest("r3", "session/prompt", map[string]any{
				"sessionId": recovered.SessionID,
				"prompt":    "hello after relogin",
			})
			resp := waitForResponse(t, h2.reader, "r3", responseTimeout, nil)
			var promptResult struct {
				StopReason string `json:"stopReason"`
			}
			unmarshalResult(t, resp, &promptResult)
			if promptResult.StopReason != "end_turn" {
				t.Fatalf("prompt after restart should succeed, got stopReason=%q", promptResult.StopReason)
			}
		})
	}
}

func TestE2EAcceptanceH1ProfilesAffectRuntime(t *testing.T) {
	profilesJSON := `[{"name":"safe","model":"gpt-safe","approvalPolicy":"on-request","sandbox":"read-only","personality":"cautious","systemInstructions":"safe-first"},{"name":"fast","model":"gpt-fast","approvalPolicy":"never","sandbox":"workspace-write","personality":"direct","systemInstructions":"ship-it"}]`
	h := startAdapter(t, "CODEX_ACP_PROFILES_JSON="+profilesJSON)

	h.sendRequest("1", "initialize", map[string]any{})
	_ = h.waitResponse("1", responseTimeout)

	runProbe := func(requestPrefix string, profile string) string {
		h.sendRequest(requestPrefix+"-new", "session/new", map[string]any{
			"profile": profile,
		})
		newResp := h.waitResponse(requestPrefix+"-new", responseTimeout)
		var newResult struct {
			SessionID string `json:"sessionId"`
		}
		unmarshalResult(t, newResp, &newResult)

		h.sendRequest(requestPrefix+"-prompt", "session/prompt", map[string]any{
			"sessionId": newResult.SessionID,
			"prompt":    "profile probe",
		})

		var deltas []string
		gotPromptResp := false
		for !gotPromptResp {
			msg := h.nextMessage(responseTimeout)
			if msg.Method == "session/update" {
				update := decodeSessionUpdate(t, msg)
				if update.Type == "message" && update.Delta != "" {
					deltas = append(deltas, update.Delta)
				}
				continue
			}
			if messageID(msg) != requestPrefix+"-prompt" {
				continue
			}
			var result struct {
				StopReason string `json:"stopReason"`
			}
			unmarshalResult(t, msg, &result)
			if result.StopReason != "end_turn" {
				t.Fatalf("profile probe expected stopReason=end_turn, got %q", result.StopReason)
			}
			gotPromptResp = true
		}
		return strings.Join(deltas, "\n")
	}

	safeOutput := runProbe("safe", "safe")
	if !strings.Contains(safeOutput, "model=gpt-safe") || !strings.Contains(safeOutput, "approval=on-request") {
		t.Fatalf("safe profile output mismatch: %s", safeOutput)
	}

	fastOutput := runProbe("fast", "fast")
	if !strings.Contains(fastOutput, "model=gpt-fast") || !strings.Contains(fastOutput, "approval=never") {
		t.Fatalf("fast profile output mismatch: %s", fastOutput)
	}
	if safeOutput == fastOutput {
		t.Fatalf("expected profile-specific runtime behavior to differ")
	}
}

func TestE2ESessionConfigOptionsModelListAndSwitch(t *testing.T) {
	profilesJSON := `[{"name":"alt","model":"gpt-alt-profile"}]`
	h := startAdapter(t, "CODEX_ACP_PROFILES_JSON="+profilesJSON)

	h.sendRequest("1", "initialize", map[string]any{})
	_ = h.waitResponse("1", responseTimeout)

	h.sendRequest("2", "session/new", map[string]any{})
	newResp := h.waitResponse("2", responseTimeout)
	var newResult struct {
		SessionID     string                `json:"sessionId"`
		ConfigOptions []sessionConfigOption `json:"configOptions"`
	}
	unmarshalResult(t, newResp, &newResult)
	if newResult.SessionID == "" {
		t.Fatalf("session/new returned empty sessionId")
	}
	if len(newResult.ConfigOptions) == 0 {
		t.Fatalf("session/new expected configOptions")
	}

	var modelConfig sessionConfigOption
	var thoughtConfig sessionConfigOption
	foundModelConfig := false
	foundThoughtConfig := false
	for _, option := range newResult.ConfigOptions {
		if option.ID == "model" {
			modelConfig = option
			foundModelConfig = true
			continue
		}
		if option.ID == "thought_level" {
			thoughtConfig = option
			foundThoughtConfig = true
		}
	}
	if !foundModelConfig {
		t.Fatalf("session/new configOptions should include model")
	}
	if !foundThoughtConfig {
		t.Fatalf("session/new configOptions should include thought_level")
	}
	if len(modelConfig.Options) == 0 {
		t.Fatalf("model configOptions should include selectable models")
	}
	if strings.TrimSpace(modelConfig.CurrentValue) == "" {
		t.Fatalf("model configOptions should include current value")
	}
	if len(thoughtConfig.Options) == 0 {
		t.Fatalf("thought_level configOptions should include selectable options")
	}
	if strings.TrimSpace(thoughtConfig.CurrentValue) == "" {
		t.Fatalf("thought_level configOptions should include current value")
	}

	targetModel := ""
	for _, option := range modelConfig.Options {
		if strings.TrimSpace(option.Value) != "" && option.Value != modelConfig.CurrentValue {
			targetModel = option.Value
			break
		}
	}
	if targetModel == "" {
		targetModel = modelConfig.CurrentValue
	}

	h.sendRequest("3", "session/set_config_option", map[string]any{
		"sessionId": newResult.SessionID,
		"configId":  "model",
		"value":     targetModel,
	})

	gotSetResp := false
	sawConfigUpdate := false
	var latestConfigOptions []sessionConfigOption
	for !gotSetResp {
		msg := h.nextMessage(responseTimeout)
		if msg.Method == "session/update" {
			update := decodeSessionUpdate(t, msg)
			if update.Type == "config_options_update" {
				latestConfigOptions = update.ConfigOptions
				for _, option := range update.ConfigOptions {
					if option.ID == "model" && option.CurrentValue == targetModel {
						sawConfigUpdate = true
						break
					}
				}
			}
			continue
		}
		if messageID(msg) != "3" {
			continue
		}
		var setResult struct {
			ConfigOptions []sessionConfigOption `json:"configOptions"`
		}
		unmarshalResult(t, msg, &setResult)
		applied := false
		for _, option := range setResult.ConfigOptions {
			if option.ID == "model" && option.CurrentValue == targetModel {
				applied = true
				break
			}
		}
		if !applied {
			t.Fatalf("session/set_config_option should apply target model=%q", targetModel)
		}
		latestConfigOptions = setResult.ConfigOptions
		gotSetResp = true
	}
	if !sawConfigUpdate {
		t.Fatalf("session/set_config_option should emit config_options_update")
	}

	updatedThought := sessionConfigOption{}
	foundUpdatedThought := false
	for _, option := range latestConfigOptions {
		if option.ID == "thought_level" {
			updatedThought = option
			foundUpdatedThought = true
			break
		}
	}
	if !foundUpdatedThought {
		t.Fatalf("config options update should include thought_level after model switch")
	}
	targetThought := ""
	for _, option := range updatedThought.Options {
		if strings.TrimSpace(option.Value) != "" && option.Value != updatedThought.CurrentValue {
			targetThought = option.Value
			break
		}
	}
	if targetThought == "" {
		targetThought = updatedThought.CurrentValue
	}
	if targetThought == "" {
		t.Fatalf("thought_level should provide a non-empty selectable value")
	}

	h.sendRequest("4", "session/set_config_option", map[string]any{
		"sessionId": newResult.SessionID,
		"configId":  "thought_level",
		"value":     targetThought,
	})

	gotThoughtSetResp := false
	sawThoughtUpdate := false
	for !gotThoughtSetResp {
		msg := h.nextMessage(responseTimeout)
		if msg.Method == "session/update" {
			update := decodeSessionUpdate(t, msg)
			if update.Type == "config_options_update" {
				for _, option := range update.ConfigOptions {
					if option.ID == "thought_level" && option.CurrentValue == targetThought {
						sawThoughtUpdate = true
						break
					}
				}
			}
			continue
		}
		if messageID(msg) != "4" {
			continue
		}
		var setResult struct {
			ConfigOptions []sessionConfigOption `json:"configOptions"`
		}
		unmarshalResult(t, msg, &setResult)
		applied := false
		for _, option := range setResult.ConfigOptions {
			if option.ID == "thought_level" && option.CurrentValue == targetThought {
				applied = true
				break
			}
		}
		if !applied {
			t.Fatalf("session/set_config_option should apply thought_level=%q", targetThought)
		}
		gotThoughtSetResp = true
	}
	if !sawThoughtUpdate {
		t.Fatalf("session/set_config_option thought_level should emit config_options_update")
	}

	h.sendRequest("5", "session/prompt", map[string]any{
		"sessionId": newResult.SessionID,
		"prompt":    "profile probe",
	})

	gotPromptResp := false
	var deltas []string
	for !gotPromptResp {
		msg := h.nextMessage(responseTimeout)
		if msg.Method == "session/update" {
			update := decodeSessionUpdate(t, msg)
			if update.Type == "message" && update.Delta != "" {
				deltas = append(deltas, update.Delta)
			}
			continue
		}
		if messageID(msg) != "5" {
			continue
		}
		var result struct {
			StopReason string `json:"stopReason"`
		}
		unmarshalResult(t, msg, &result)
		if result.StopReason != "end_turn" {
			t.Fatalf("prompt expected stopReason=end_turn, got %q", result.StopReason)
		}
		gotPromptResp = true
	}

	output := strings.Join(deltas, "\n")
	flatOutput := strings.ReplaceAll(output, "\n", "")
	if !strings.Contains(flatOutput, "model="+targetModel) {
		t.Fatalf("prompt should run with switched model=%q, output=%s", targetModel, output)
	}
	if !strings.Contains(flatOutput, "thought="+targetThought) {
		t.Fatalf("prompt should run with switched thought_level=%q, output=%s", targetThought, output)
	}
}

func TestE2EAcceptanceMCPListCallAndOAuth(t *testing.T) {
	h := startAdapter(t)

	h.sendRequest("1", "initialize", map[string]any{})
	_ = h.waitResponse("1", responseTimeout)

	h.sendRequest("2", "session/new", map[string]any{})
	newResp := h.waitResponse("2", responseTimeout)
	var newResult struct {
		SessionID string `json:"sessionId"`
	}
	unmarshalResult(t, newResp, &newResult)

	h.sendRequest("3", "session/prompt", map[string]any{
		"sessionId": newResult.SessionID,
		"prompt":    "/mcp list",
	})
	gotListResp := false
	sawServerList := false
	for !gotListResp {
		msg := h.nextMessage(responseTimeout)
		if msg.Method == "session/update" {
			update := decodeSessionUpdate(t, msg)
			if update.Type == "message" && strings.Contains(update.Delta, "demo-mcp") {
				sawServerList = true
			}
			continue
		}
		if messageID(msg) != "3" {
			continue
		}
		var result struct {
			StopReason string `json:"stopReason"`
		}
		unmarshalResult(t, msg, &result)
		if result.StopReason != "end_turn" {
			t.Fatalf("/mcp list expected stopReason=end_turn, got %q", result.StopReason)
		}
		gotListResp = true
	}
	if !sawServerList {
		t.Fatalf("/mcp list expected server listing output")
	}

	h.sendRequest("4", "session/prompt", map[string]any{
		"sessionId": newResult.SessionID,
		"prompt":    `/mcp call demo-mcp dangerous-write {"path":"docs/README.md"}`,
	})
	gotCallResp := false
	sawPermission := false
	sawToolCompleted := false
	sawCallOutput := false
	for !gotCallResp {
		msg := h.nextMessage(responseTimeout)
		switch msg.Method {
		case "session/request_permission":
			req := decodeSessionRequestPermission(t, msg)
			if req.Approval != "mcp" {
				t.Fatalf("/mcp call expected mcp approval, got %q", req.Approval)
			}
			sawPermission = true
			h.sendResultResponse(messageID(msg), map[string]any{"outcome": "approved"})
		case "session/update":
			update := decodeSessionUpdate(t, msg)
			if update.Type == "tool_call_update" && update.Status == "completed" {
				sawToolCompleted = true
			}
			if update.Type == "message" && strings.Contains(update.Delta, "mcp call ok") {
				sawCallOutput = true
			}
		default:
			if messageID(msg) != "4" {
				continue
			}
			var result struct {
				StopReason string `json:"stopReason"`
			}
			unmarshalResult(t, msg, &result)
			if result.StopReason != "end_turn" {
				t.Fatalf("/mcp call expected stopReason=end_turn, got %q", result.StopReason)
			}
			gotCallResp = true
		}
	}
	if !sawPermission || !sawToolCompleted || !sawCallOutput {
		t.Fatalf("/mcp call expected permission + completed tool update + output")
	}

	h.sendRequest("5", "session/prompt", map[string]any{
		"sessionId": newResult.SessionID,
		"prompt":    "/mcp oauth demo-mcp",
	})
	gotOAuthResp := false
	sawOAuthOutput := false
	for !gotOAuthResp {
		msg := h.nextMessage(responseTimeout)
		if msg.Method == "session/update" {
			update := decodeSessionUpdate(t, msg)
			if update.Type == "message" && strings.Contains(update.Delta, "oauth") {
				sawOAuthOutput = true
			}
			continue
		}
		if messageID(msg) != "5" {
			continue
		}
		var result struct {
			StopReason string `json:"stopReason"`
		}
		unmarshalResult(t, msg, &result)
		if result.StopReason != "end_turn" {
			t.Fatalf("/mcp oauth expected stopReason=end_turn, got %q", result.StopReason)
		}
		gotOAuthResp = true
	}
	if !sawOAuthOutput {
		t.Fatalf("/mcp oauth expected oauth output")
	}
}

func TestE2EAcceptanceI1ToI3AuthMethods(t *testing.T) {
	cases := []struct {
		name           string
		env            []string
		activeAuthMode string
	}{
		{
			name: "codex_api_key",
			env: []string{
				"CODEX_API_KEY=sk-codex",
				"OPENAI_API_KEY=",
				"CHATGPT_SUBSCRIPTION_ACTIVE=0",
			},
			activeAuthMode: "codex_api_key",
		},
		{
			name: "openai_api_key",
			env: []string{
				"CODEX_API_KEY=",
				"OPENAI_API_KEY=sk-openai",
				"CHATGPT_SUBSCRIPTION_ACTIVE=0",
			},
			activeAuthMode: "openai_api_key",
		},
		{
			name: "subscription",
			env: []string{
				"CODEX_API_KEY=",
				"OPENAI_API_KEY=",
				"CHATGPT_SUBSCRIPTION_ACTIVE=1",
			},
			activeAuthMode: "chatgpt_subscription",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := startAdapter(t, tc.env...)

			h.sendRequest("1", "initialize", map[string]any{})
			initResp := h.waitResponse("1", responseTimeout)
			var initResult struct {
				ActiveAuthMethod string `json:"activeAuthMethod"`
			}
			unmarshalResult(t, initResp, &initResult)
			if initResult.ActiveAuthMethod != tc.activeAuthMode {
				t.Fatalf("active auth mode mismatch: got=%q want=%q", initResult.ActiveAuthMethod, tc.activeAuthMode)
			}

			h.sendRequest("2", "session/new", map[string]any{})
			newResp := h.waitResponse("2", responseTimeout)
			var newResult struct {
				SessionID string `json:"sessionId"`
			}
			unmarshalResult(t, newResp, &newResult)
			if newResult.SessionID == "" {
				t.Fatalf("session/new should succeed for auth mode %s", tc.name)
			}

			h.sendRequest("3", "session/prompt", map[string]any{
				"sessionId": newResult.SessionID,
				"prompt":    "hello",
			})
			resp := waitForResponse(t, h.reader, "3", responseTimeout, nil)
			var promptResult struct {
				StopReason string `json:"stopReason"`
			}
			unmarshalResult(t, resp, &promptResult)
			if promptResult.StopReason != "end_turn" {
				t.Fatalf("session/prompt expected stopReason=end_turn, got %q", promptResult.StopReason)
			}
		})
	}
}

func TestE2EAuthRequiredWithoutConfiguredMethod(t *testing.T) {
	h := startAdapter(t,
		"CODEX_API_KEY=",
		"OPENAI_API_KEY=",
		"CHATGPT_SUBSCRIPTION_ACTIVE=0",
	)

	h.sendRequest("1", "initialize", map[string]any{})
	initResp := h.waitResponse("1", responseTimeout)
	var initResult struct {
		ActiveAuthMethod string `json:"activeAuthMethod"`
	}
	unmarshalResult(t, initResp, &initResult)
	if initResult.ActiveAuthMethod != "" {
		t.Fatalf("expected empty active auth method when no auth configured, got %q", initResult.ActiveAuthMethod)
	}

	h.sendRequest("2", "session/new", map[string]any{})
	resp := h.waitResponse("2", responseTimeout)
	if resp.Error == nil || !strings.Contains(strings.ToLower(resp.Error.Message), "authentication") {
		t.Fatalf("session/new without auth expected clear auth error, got %+v", resp.Error)
	}
}

func TestE2EAuthMethodsAndAuthenticateFlow(t *testing.T) {
	h := startAdapter(t,
		"CODEX_API_KEY=",
		"OPENAI_API_KEY=",
		"CHATGPT_SUBSCRIPTION_ACTIVE=0",
	)

	h.sendRequest("1", "initialize", map[string]any{})
	initResp := h.waitResponse("1", responseTimeout)
	var initResult struct {
		AuthMethods []struct {
			ID          string `json:"id"`
			Name        string `json:"name"`
			Description string `json:"description"`
			Type        string `json:"type"`
			Label       string `json:"label"`
		} `json:"authMethods"`
		ActiveAuthMethod string `json:"activeAuthMethod"`
	}
	unmarshalResult(t, initResp, &initResult)
	if initResult.ActiveAuthMethod != "" {
		t.Fatalf("expected empty active auth method before authenticate, got %q", initResult.ActiveAuthMethod)
	}
	if len(initResult.AuthMethods) < 3 {
		t.Fatalf("expected >=3 auth methods, got %d", len(initResult.AuthMethods))
	}
	seenSubscription := false
	for _, method := range initResult.AuthMethods {
		if strings.TrimSpace(method.ID) == "" || strings.TrimSpace(method.Name) == "" {
			t.Fatalf("authMethods should include id/name: %+v", method)
		}
		if method.ID == "chatgpt_subscription" {
			seenSubscription = true
		}
	}
	if !seenSubscription {
		t.Fatalf("expected chatgpt_subscription in authMethods")
	}

	h.sendRequest("2", "session/new", map[string]any{})
	newRespBefore := h.waitResponse("2", responseTimeout)
	if newRespBefore.Error == nil || !strings.Contains(strings.ToLower(newRespBefore.Error.Message), "authentication") {
		t.Fatalf("session/new without auth expected auth error, got %+v", newRespBefore.Error)
	}

	h.sendRequest("3", "authenticate", map[string]any{
		"methodId": "chatgpt_subscription",
	})
	authResp := h.waitResponse("3", responseTimeout)
	var authResult struct {
		Authenticated    bool   `json:"authenticated"`
		ActiveAuthMethod string `json:"activeAuthMethod"`
	}
	unmarshalResult(t, authResp, &authResult)
	if !authResult.Authenticated {
		t.Fatalf("authenticate should return authenticated=true")
	}
	if authResult.ActiveAuthMethod != "chatgpt_subscription" {
		t.Fatalf("authenticate should set activeAuthMethod=chatgpt_subscription, got %q", authResult.ActiveAuthMethod)
	}

	h.sendRequest("4", "session/new", map[string]any{})
	newRespAfter := h.waitResponse("4", responseTimeout)
	var newResult struct {
		SessionID string `json:"sessionId"`
	}
	unmarshalResult(t, newRespAfter, &newResult)
	if strings.TrimSpace(newResult.SessionID) == "" {
		t.Fatalf("session/new should succeed after authenticate")
	}

	h.assertStdoutPureJSONRPC()
}

func TestE2EAcceptanceJ1Stress100Turns(t *testing.T) {
	if os.Getenv("RUN_STRESS_J1") != "1" {
		t.Skip("set RUN_STRESS_J1=1 to run J1 stress regression")
	}

	h := startAdapter(t)
	h.sendRequest("1", "initialize", map[string]any{})
	_ = h.waitResponse("1", responseTimeout)

	h.sendRequest("2", "session/new", map[string]any{})
	newResp := h.waitResponse("2", responseTimeout)
	var newResult struct {
		SessionID string `json:"sessionId"`
	}
	unmarshalResult(t, newResp, &newResult)

	for i := 0; i < 100; i++ {
		switch {
		case i%10 == 0:
			promptID := fmt.Sprintf("p-cancel-%d", i)
			cancelID := fmt.Sprintf("c-%d", i)
			h.sendRequest(promptID, "session/prompt", map[string]any{
				"sessionId": newResult.SessionID,
				"prompt":    "slow task",
			})

			seenTurn := false
			for !seenTurn {
				msg := h.nextMessage(responseTimeout)
				if msg.Method == "session/update" {
					update := decodeSessionUpdate(t, msg)
					if update.TurnID != "" {
						seenTurn = true
					}
					continue
				}
				if messageID(msg) == promptID {
					t.Fatalf("cancel stress prompt completed before cancel")
				}
			}

			h.sendRequest(cancelID, "session/cancel", map[string]any{
				"sessionId": newResult.SessionID,
			})

			gotCancelResp := false
			gotPromptResp := false
			deadline := time.Now().Add(cancelTimeout)
			for !(gotCancelResp && gotPromptResp) {
				if time.Now().After(deadline) {
					t.Fatalf("cancel stress flow timed out")
				}
				msg := h.nextMessage(time.Until(deadline))
				switch messageID(msg) {
				case cancelID:
					gotCancelResp = true
				case promptID:
					var result struct {
						StopReason string `json:"stopReason"`
					}
					unmarshalResult(t, msg, &result)
					if result.StopReason != "cancelled" {
						t.Fatalf("cancel stress expected cancelled stopReason, got %q", result.StopReason)
					}
					gotPromptResp = true
				}
			}
		case i%4 == 0:
			promptID := fmt.Sprintf("p-approval-%d", i)
			outcome := "declined"
			if i%8 == 0 {
				outcome = "approved"
			}
			h.sendRequest(promptID, "session/prompt", map[string]any{
				"sessionId": newResult.SessionID,
				"prompt":    "approval command",
			})

			resp := waitForResponse(t, h.reader, promptID, 2*responseTimeout, func(msg rpcMessage) {
				if msg.Method != "session/request_permission" {
					return
				}
				h.sendResultResponse(messageID(msg), map[string]any{"outcome": outcome})
			})
			var promptResult struct {
				StopReason string `json:"stopReason"`
			}
			unmarshalResult(t, resp, &promptResult)
			if promptResult.StopReason != "end_turn" {
				t.Fatalf("stress approval prompt expected stopReason=end_turn, got %q", promptResult.StopReason)
			}
		default:
			promptID := fmt.Sprintf("p-%d", i)
			h.sendRequest(promptID, "session/prompt", map[string]any{
				"sessionId": newResult.SessionID,
				"prompt":    fmt.Sprintf("ping %d", i),
			})
			resp := waitForResponse(t, h.reader, promptID, responseTimeout, nil)
			var promptResult struct {
				StopReason string `json:"stopReason"`
			}
			unmarshalResult(t, resp, &promptResult)
			if promptResult.StopReason != "end_turn" {
				t.Fatalf("stress prompt expected stopReason=end_turn, got %q", promptResult.StopReason)
			}
		}
	}

	h.assertStdoutPureJSONRPC()
}

func TestE2ERealCodexContentBlocksMentionsImagesAndTODO(t *testing.T) {
	requireRealCodex(t)

	h := startAdapterReal(t, nil)
	sessionID := realCodexInitializeAndSession(t, h, nil)

	root := repoRoot(t)
	mentionPath := filepath.Join(root, "PROGRESS.md")
	mentionURI := "file://" + filepath.ToSlash(mentionPath)
	const tinyPNGBase64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO5W9tQAAAAASUVORK5CYII="

	h.sendRequest("real-content", "session/prompt", map[string]any{
		"sessionId": sessionID,
		"content": []map[string]any{
			{
				"type": "text",
				"text": "Use the mention and image context, then output a markdown checklist with exactly two items.",
			},
			{
				"type":     "mention",
				"name":     "PROGRESS.md",
				"path":     mentionPath,
				"uri":      mentionURI,
				"mimeType": "text/markdown",
			},
			{
				"type":     "image",
				"mimeType": "image/png",
				"data":     tinyPNGBase64,
			},
		},
	})

	gotPromptResp := false
	var deltas []string
	sawStructuredTodo := false
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) && !gotPromptResp {
		msg := h.nextMessage(time.Until(deadline))
		switch msg.Method {
		case "session/update":
			update := decodeSessionUpdate(t, msg)
			if update.Type == "message" && strings.TrimSpace(update.Delta) != "" {
				deltas = append(deltas, update.Delta)
			}
			if len(update.Todo) > 0 {
				sawStructuredTodo = true
			}
		case "session/request_permission":
			h.sendResultResponse(messageID(msg), map[string]any{"outcome": "declined"})
		default:
			if messageID(msg) != "real-content" {
				continue
			}
			var result struct {
				StopReason string `json:"stopReason"`
			}
			unmarshalResult(t, msg, &result)
			if result.StopReason != "end_turn" {
				t.Fatalf("real content prompt expected stopReason=end_turn, got %q", result.StopReason)
			}
			gotPromptResp = true
		}
	}

	if !gotPromptResp {
		t.Fatalf("timed out waiting for real content prompt response")
	}

	joined := strings.Join(deltas, "\n")
	if !strings.Contains(joined, "- [ ]") && !strings.Contains(joined, "- [x]") {
		t.Fatalf("real content prompt should return markdown checklist, got: %s", joined)
	}
	if !sawStructuredTodo {
		t.Fatalf("real content prompt should emit structured TODO metadata")
	}
}

func TestE2ERealCodexAppServer_BasicPromptAndCancel(t *testing.T) {
	requireRealCodex(t)

	traceFile := filepath.Join(t.TempDir(), "real-e2e-trace.jsonl")
	h := startAdapterReal(t, []string{"--trace-json", "--trace-json-file", traceFile})
	sessionID := realCodexInitializeAndSession(t, h, map[string]any{
		"apiKey": "sk-local-secret-for-redaction-check",
	})
	_ = realCodexPromptRoundtrip(
		t,
		h,
		sessionID,
		"3",
		"Reply with one short sentence confirming this is a real codex e2e smoke test.",
		1,
	)

	cancelSuccess := false
	for attempt := 1; attempt <= 3 && !cancelSuccess; attempt++ {
		promptID := fmt.Sprintf("4-%d", attempt)
		cancelID := fmt.Sprintf("5-%d", attempt)

		h.sendRequest(promptID, "session/prompt", map[string]any{
			"sessionId": sessionID,
			"prompt": "Think in detail for a while and do not finalize quickly. " +
				"Produce a long intermediate reasoning summary before any conclusion.",
		})

		cancelSent := false
		gotCancelResp := false
		gotPromptResp := false
		cancelled := false
		deadline := time.Now().Add(40 * time.Second)
		for time.Now().Before(deadline) && !(gotCancelResp && gotPromptResp) {
			msg := h.nextMessage(time.Until(deadline))
			switch msg.Method {
			case "session/update":
				if cancelSent {
					continue
				}
				update := decodeSessionUpdate(t, msg)
				if update.SessionID != sessionID {
					continue
				}
				h.sendRequest(cancelID, "session/cancel", map[string]any{"sessionId": sessionID})
				cancelSent = true
			case "session/request_permission":
				h.sendResultResponse(messageID(msg), map[string]any{"outcome": "declined"})
			default:
				switch messageID(msg) {
				case cancelID:
					var result struct {
						Cancelled bool `json:"cancelled"`
					}
					unmarshalResult(t, msg, &result)
					gotCancelResp = true
				case promptID:
					var result struct {
						StopReason string `json:"stopReason"`
					}
					unmarshalResult(t, msg, &result)
					if result.StopReason == "cancelled" {
						cancelled = true
					}
					gotPromptResp = true
				}
			}
		}

		if gotCancelResp && gotPromptResp && cancelled {
			cancelSuccess = true
		}
	}
	if !cancelSuccess {
		t.Fatalf("session/cancel e2e expected stopReason=cancelled within retry window")
	}

	h.assertStdoutPureJSONRPC()

	data, err := os.ReadFile(traceFile)
	if err != nil {
		t.Fatalf("read trace file: %v", err)
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		t.Fatalf("trace file should not be empty")
	}
	traceText := string(data)
	if !strings.Contains(traceText, `"stream":"acp"`) || !strings.Contains(traceText, `"stream":"appserver"`) {
		t.Fatalf("trace file should include both acp and appserver streams")
	}
	if strings.Contains(traceText, "sk-local-secret-for-redaction-check") {
		t.Fatalf("trace file leaked unredacted secret")
	}
	if !strings.Contains(traceText, "[REDACTED]") {
		t.Fatalf("trace file should contain redacted marker")
	}
	assertTraceContainsAppServerMethods(
		t,
		traceFile,
		"initialize",
		"initialized",
		"thread/start",
		"turn/start",
	)
}

func TestE2ERealCodexAppServer_AuthMissingReturnsClearError(t *testing.T) {
	requireRealCodex(t)

	h := startAdapterReal(
		t,
		nil,
		"CODEX_API_KEY=",
		"OPENAI_API_KEY=",
		"CHATGPT_SUBSCRIPTION_ACTIVE=0",
	)

	h.sendRequest("real-auth-missing-init", "initialize", map[string]any{})
	initResp := h.waitResponse("real-auth-missing-init", 20*time.Second)
	var initResult struct {
		ActiveAuthMethod string `json:"activeAuthMethod"`
	}
	unmarshalResult(t, initResp, &initResult)
	if initResult.ActiveAuthMethod != "" {
		t.Fatalf("expected empty active auth method when auth env is disabled, got %q", initResult.ActiveAuthMethod)
	}

	h.sendRequest("real-auth-missing-new", "session/new", map[string]any{})
	newResp := h.waitResponse("real-auth-missing-new", 20*time.Second)
	if newResp.Error == nil {
		t.Fatalf("session/new without auth should return clear authentication error")
	}
	if !strings.Contains(strings.ToLower(newResp.Error.Message), "authentication") {
		t.Fatalf("session/new auth error should mention authentication, got %q", newResp.Error.Message)
	}
	data := decodeRPCErrorDataMap(t, newResp.Error)
	if strings.TrimSpace(valueAsStringAny(data["nextStepCommand"])) == "" {
		t.Fatalf("auth error should include nextStepCommand for recovery")
	}

	h.assertStdoutPureJSONRPC()
}

func TestE2ERealCodexAppServer_AuthInjectedKeyRecovers(t *testing.T) {
	requireRealCodex(t)

	extraEnv, expectedMode := realCodexRecoveryAuthEnv(t)
	h := startAdapterReal(t, nil, extraEnv...)

	h.sendRequest("real-auth-key-init", "initialize", map[string]any{})
	initResp := h.waitResponse("real-auth-key-init", 20*time.Second)
	var initResult struct {
		ActiveAuthMethod string `json:"activeAuthMethod"`
	}
	unmarshalResult(t, initResp, &initResult)
	if initResult.ActiveAuthMethod != expectedMode {
		t.Fatalf("active auth mode mismatch: got=%q want=%q", initResult.ActiveAuthMethod, expectedMode)
	}

	h.sendRequest("real-auth-key-new", "session/new", map[string]any{})
	newResp := h.waitResponse("real-auth-key-new", 30*time.Second)
	if newResp.Error != nil {
		t.Fatalf("session/new should recover with injected key, got error=%s", newResp.Error.Message)
	}
	var newResult struct {
		SessionID string `json:"sessionId"`
	}
	unmarshalResult(t, newResp, &newResult)
	if strings.TrimSpace(newResult.SessionID) == "" {
		t.Fatalf("session/new returned empty sessionId after auth recovery")
	}

	_ = realCodexPromptRoundtrip(
		t,
		h,
		newResult.SessionID,
		"real-auth-key-prompt",
		"Reply with one short sentence confirming auth recovery.",
		1,
	)
	h.assertStdoutPureJSONRPC()
}

func TestE2ERealCodexAppServer_MCPListAndOptionalCall(t *testing.T) {
	requireRealCodex(t)

	h := startAdapterReal(t, nil)
	sessionID := realCodexInitializeAndSession(t, h, nil)

	h.sendRequest("real-mcp-list", "session/prompt", map[string]any{
		"sessionId": sessionID,
		"prompt":    "/mcp list",
	})

	gotListResp := false
	sawListUpdate := false
	var listDeltas []string
	listDeadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(listDeadline) && !gotListResp {
		msg := h.nextMessage(time.Until(listDeadline))
		switch msg.Method {
		case "session/update":
			update := decodeSessionUpdate(t, msg)
			if update.SessionID != sessionID {
				continue
			}
			sawListUpdate = true
			if update.Type == "message" && strings.TrimSpace(update.Delta) != "" {
				listDeltas = append(listDeltas, update.Delta)
			}
		case "session/request_permission":
			h.sendResultResponse(messageID(msg), map[string]any{"outcome": "declined"})
		default:
			if messageID(msg) != "real-mcp-list" {
				continue
			}
			var result struct {
				StopReason string `json:"stopReason"`
			}
			unmarshalResult(t, msg, &result)
			if result.StopReason != "end_turn" {
				t.Fatalf("/mcp list expected stopReason=end_turn, got %q", result.StopReason)
			}
			gotListResp = true
		}
	}
	if !gotListResp {
		t.Fatalf("timed out waiting for /mcp list response")
	}
	if !sawListUpdate {
		t.Fatalf("/mcp list expected at least one session/update output")
	}

	server, tool, hasServer := parseFirstMCPServerFromOutput(strings.Join(listDeltas, "\n"))
	if !hasServer {
		h.assertStdoutPureJSONRPC()
		return
	}
	if strings.TrimSpace(tool) == "" {
		tool = "ping"
	}

	h.sendRequest("real-mcp-call", "session/prompt", map[string]any{
		"sessionId": sessionID,
		"prompt":    fmt.Sprintf("/mcp call %s %s", server, tool),
	})

	gotCallResp := false
	sawPermission := false
	sawToolTerminal := false
	sawCallUpdate := false
	callDeadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(callDeadline) && !gotCallResp {
		msg := h.nextMessage(time.Until(callDeadline))
		switch msg.Method {
		case "session/request_permission":
			req := decodeSessionRequestPermission(t, msg)
			if req.Approval != "mcp" {
				t.Fatalf("/mcp call expected mcp approval, got %q", req.Approval)
			}
			sawPermission = true
			h.sendResultResponse(messageID(msg), map[string]any{"outcome": "approved"})
		case "session/update":
			update := decodeSessionUpdate(t, msg)
			if update.SessionID != sessionID {
				continue
			}
			sawCallUpdate = true
			if update.Type == "tool_call_update" && (update.Status == "completed" || update.Status == "failed") {
				sawToolTerminal = true
			}
		default:
			if messageID(msg) != "real-mcp-call" {
				continue
			}
			var result struct {
				StopReason string `json:"stopReason"`
			}
			unmarshalResult(t, msg, &result)
			if result.StopReason != "end_turn" {
				t.Fatalf("/mcp call expected stopReason=end_turn, got %q", result.StopReason)
			}
			gotCallResp = true
		}
	}
	if !gotCallResp {
		t.Fatalf("timed out waiting for /mcp call response")
	}
	if !sawPermission {
		t.Fatalf("/mcp call expected one permission request")
	}
	if !sawCallUpdate {
		t.Fatalf("/mcp call expected session/update outputs")
	}
	if !sawToolTerminal {
		t.Fatalf("/mcp call expected terminal tool_call_update (completed/failed)")
	}

	h.assertStdoutPureJSONRPC()
}

func TestE2ERealCodexAppServer_CompactProducesVisibleUpdates(t *testing.T) {
	requireRealCodex(t)

	h := startAdapterReal(t, nil)
	sessionID := realCodexInitializeAndSession(t, h, nil)
	_ = realCodexPromptRoundtrip(
		t,
		h,
		sessionID,
		"real-compact-prime",
		"Reply with one short sentence so the thread has context before compact.",
		1,
	)

	h.sendRequest("real-compact", "session/prompt", map[string]any{
		"sessionId": sessionID,
		"prompt":    "/compact",
	})

	gotCompactResp := false
	sawCompactUpdate := false
	compactDeadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(compactDeadline) && !gotCompactResp {
		msg := h.nextMessage(time.Until(compactDeadline))
		switch msg.Method {
		case "session/update":
			update := decodeSessionUpdate(t, msg)
			if update.SessionID != sessionID {
				continue
			}
			sawCompactUpdate = true
		case "session/request_permission":
			h.sendResultResponse(messageID(msg), map[string]any{"outcome": "declined"})
		default:
			if messageID(msg) != "real-compact" {
				continue
			}
			if msg.Error != nil {
				data := decodeRPCErrorDataMap(t, msg.Error)
				detail := strings.ToLower(valueAsStringAny(data["error"]))
				if strings.Contains(detail, "method not found") || strings.Contains(detail, "not supported") {
					t.Skipf("local codex app-server does not support compact endpoint: %s", detail)
				}
				t.Fatalf("/compact failed: code=%d message=%s detail=%v", msg.Error.Code, msg.Error.Message, data["error"])
			}
			var result struct {
				StopReason string `json:"stopReason"`
			}
			unmarshalResult(t, msg, &result)
			if result.StopReason != "end_turn" {
				t.Fatalf("/compact expected stopReason=end_turn, got %q", result.StopReason)
			}
			gotCompactResp = true
		}
	}
	if !gotCompactResp {
		t.Fatalf("timed out waiting for /compact response")
	}
	if !sawCompactUpdate {
		t.Fatalf("/compact expected at least one visible session/update")
	}

	_ = realCodexPromptRoundtrip(
		t,
		h,
		sessionID,
		"real-compact-post",
		"Reply with OK after compact.",
		1,
	)
	h.assertStdoutPureJSONRPC()
}

func TestE2ERealCodexPromptInteractions(t *testing.T) {
	requireRealCodex(t)

	h := startAdapterReal(t, nil)
	sessionID := realCodexInitializeAndSession(t, h, nil)

	prompts := []struct {
		name   string
		prompt string
	}{
		{
			name:   "ProjectQuestion",
			prompt: "What is this project?",
		},
		{
			name:   "CapabilitiesQuestion",
			prompt: "List two key capabilities this repository implements.",
		},
		{
			name:   "EntryPointQuestion",
			prompt: "What does cmd/acp do? Answer in one sentence.",
		},
	}

	for i, tc := range prompts {
		t.Run(tc.name, func(t *testing.T) {
			responseText := realCodexPromptRoundtrip(
				t,
				h,
				sessionID,
				fmt.Sprintf("real-prompt-%d", i+1),
				tc.prompt,
				1,
			)
			if strings.TrimSpace(responseText) == "" {
				t.Fatalf("real prompt %q produced empty message output", tc.prompt)
			}
		})
	}

	h.assertStdoutPureJSONRPC()
}

func realCodexInitializeAndSession(t *testing.T, h *adapterHarness, initParams map[string]any) string {
	t.Helper()

	if initParams == nil {
		initParams = map[string]any{}
	}
	h.sendRequest("real-init", "initialize", initParams)
	initResp := h.waitResponse("real-init", 20*time.Second)
	var initResult struct {
		AgentCapabilities struct {
			Sessions bool `json:"sessions"`
		} `json:"agentCapabilities"`
	}
	unmarshalResult(t, initResp, &initResult)
	if !initResult.AgentCapabilities.Sessions {
		t.Fatalf("initialize should report sessions capability")
	}

	h.sendRequest("real-new", "session/new", map[string]any{})
	newResp := h.waitResponse("real-new", 30*time.Second)
	if newResp.Error != nil {
		lower := strings.ToLower(newResp.Error.Message)
		if strings.Contains(lower, "thread/start failed") || strings.Contains(lower, "authentication") {
			t.Skipf(
				"real codex e2e requires available local codex auth/session: %s",
				newResp.Error.Message,
			)
		}
		t.Fatalf("session/new failed: code=%d message=%s", newResp.Error.Code, newResp.Error.Message)
	}

	var newResult struct {
		SessionID string `json:"sessionId"`
	}
	unmarshalResult(t, newResp, &newResult)
	if newResult.SessionID == "" {
		t.Fatalf("session/new returned empty sessionId")
	}
	return newResult.SessionID
}

func realCodexPromptRoundtrip(
	t *testing.T,
	h *adapterHarness,
	sessionID string,
	requestID string,
	prompt string,
	minUpdates int,
) string {
	t.Helper()

	h.sendRequest(requestID, "session/prompt", map[string]any{
		"sessionId": sessionID,
		"prompt":    prompt,
	})

	updates := 0
	var deltas []string
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		msg := h.nextMessage(time.Until(deadline))
		switch msg.Method {
		case "session/update":
			update := decodeSessionUpdate(t, msg)
			if update.SessionID != sessionID {
				continue
			}
			updates++
			if update.Type == "message" && strings.TrimSpace(update.Delta) != "" {
				deltas = append(deltas, update.Delta)
			}
		case "session/request_permission":
			// Keep real prompt test non-interactive if side-effect approval is requested.
			h.sendResultResponse(messageID(msg), map[string]any{"outcome": "declined"})
		default:
			if messageID(msg) != requestID {
				continue
			}
			var result struct {
				StopReason string `json:"stopReason"`
			}
			unmarshalResult(t, msg, &result)
			if result.StopReason != "end_turn" {
				t.Fatalf("prompt %q expected stopReason=end_turn, got %q", prompt, result.StopReason)
			}
			if updates < minUpdates {
				t.Fatalf("prompt %q expected at least %d updates, got %d", prompt, minUpdates, updates)
			}
			return strings.Join(deltas, "")
		}
	}

	t.Fatalf("timed out waiting for prompt response id=%s", requestID)
	return ""
}

func realCodexRecoveryAuthEnv(t *testing.T) ([]string, string) {
	t.Helper()

	if key := strings.TrimSpace(os.Getenv("E2E_REAL_CODEX_RECOVERY_CODEX_API_KEY")); key != "" {
		return []string{
			"CODEX_API_KEY=" + key,
			"OPENAI_API_KEY=",
			"CHATGPT_SUBSCRIPTION_ACTIVE=0",
		}, "codex_api_key"
	}
	if key := strings.TrimSpace(os.Getenv("E2E_REAL_CODEX_RECOVERY_OPENAI_API_KEY")); key != "" {
		return []string{
			"CODEX_API_KEY=",
			"OPENAI_API_KEY=" + key,
			"CHATGPT_SUBSCRIPTION_ACTIVE=0",
		}, "openai_api_key"
	}
	if key := strings.TrimSpace(os.Getenv("CODEX_API_KEY")); key != "" {
		return []string{
			"CODEX_API_KEY=" + key,
			"OPENAI_API_KEY=",
			"CHATGPT_SUBSCRIPTION_ACTIVE=0",
		}, "codex_api_key"
	}
	if key := strings.TrimSpace(os.Getenv("OPENAI_API_KEY")); key != "" {
		return []string{
			"CODEX_API_KEY=",
			"OPENAI_API_KEY=" + key,
			"CHATGPT_SUBSCRIPTION_ACTIVE=0",
		}, "openai_api_key"
	}

	t.Skip(
		"auth recovery real-e2e requires one key env: " +
			"E2E_REAL_CODEX_RECOVERY_CODEX_API_KEY or E2E_REAL_CODEX_RECOVERY_OPENAI_API_KEY",
	)
	return nil, ""
}

func parseFirstMCPServerFromOutput(output string) (string, string, bool) {
	normalized := strings.ToLower(output)
	start := strings.Index(normalized, "mcp servers:")
	if start < 0 {
		return "", "", false
	}

	rest := strings.TrimSpace(output[start+len("mcp servers:"):])
	if rest == "" {
		return "", "", false
	}

	first := strings.TrimSpace(strings.Split(rest, ";")[0])
	if first == "" {
		return "", "", false
	}

	openParen := strings.Index(first, "(")
	if openParen < 0 {
		parts := strings.Fields(first)
		if len(parts) == 0 {
			return "", "", false
		}
		return parts[0], "", true
	}

	server := strings.TrimSpace(first[:openParen])
	if server == "" {
		return "", "", false
	}

	details := first[openParen+1:]
	if closeParen := strings.LastIndex(details, ")"); closeParen >= 0 {
		details = details[:closeParen]
	}
	tool := ""
	if idx := strings.Index(strings.ToLower(details), "tools="); idx >= 0 {
		toolsRaw := strings.TrimSpace(details[idx+len("tools="):])
		if space := strings.IndexAny(toolsRaw, " \t\r\n"); space >= 0 {
			toolsRaw = toolsRaw[:space]
		}
		for _, candidate := range strings.Split(toolsRaw, ",") {
			candidate = strings.TrimSpace(candidate)
			if candidate != "" && candidate != "-" {
				tool = candidate
				break
			}
		}
	}

	return server, tool, true
}

func assertTraceContainsAppServerMethods(t *testing.T, traceFile string, methods ...string) {
	t.Helper()

	data, err := os.ReadFile(traceFile)
	if err != nil {
		t.Fatalf("read trace file: %v", err)
	}
	text := strings.TrimSpace(string(data))
	if text == "" {
		t.Fatalf("trace file is empty")
	}

	type traceEntry struct {
		Stream  string          `json:"stream"`
		Payload json.RawMessage `json:"payload"`
	}

	seen := make(map[string]bool, len(methods))
	lines := strings.Split(text, "\n")
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var entry traceEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		if entry.Stream != "appserver" || len(entry.Payload) == 0 {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal(entry.Payload, &payload); err != nil {
			continue
		}
		method, _ := payload["method"].(string)
		if strings.TrimSpace(method) == "" {
			continue
		}
		seen[method] = true
	}

	for _, method := range methods {
		if !seen[method] {
			t.Fatalf("trace should include appserver method %q, seen=%v", method, seen)
		}
	}
}

func TestE2ETraceJSONFileRedaction(t *testing.T) {
	if isRealCodexEnabled() {
		t.Skip("trace fake harness test skipped when E2E_REAL_CODEX=1")
	}

	traceFile := filepath.Join(t.TempDir(), "trace-redaction.jsonl")
	h := startAdapterWithConfig(
		t,
		[]string{"--trace-json", "--trace-json-file", traceFile},
		false,
	)

	h.sendRequest("1", "initialize", map[string]any{
		"apiKey": "sk-test-redaction-secret",
	})
	_ = h.waitResponse("1", responseTimeout)

	h.sendRequest("2", "session/new", map[string]any{})
	resp := h.waitResponse("2", responseTimeout)
	var result struct {
		SessionID string `json:"sessionId"`
	}
	unmarshalResult(t, resp, &result)
	if result.SessionID == "" {
		t.Fatalf("session/new returned empty sessionId")
	}

	h.assertStdoutPureJSONRPC()

	data, err := os.ReadFile(traceFile)
	if err != nil {
		t.Fatalf("read trace file: %v", err)
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		t.Fatalf("trace file should contain at least one entry")
	}
	text := string(data)
	if strings.Contains(text, "sk-test-redaction-secret") {
		t.Fatalf("trace file leaked secret")
	}
	if !strings.Contains(text, "[REDACTED]") {
		t.Fatalf("trace file should include redacted marker")
	}
	if !strings.Contains(text, `"stream":"acp"`) || !strings.Contains(text, `"stream":"appserver"`) {
		t.Fatalf("trace file should include both acp and appserver streams")
	}
}

func TestRPCReaderDetectsInvalidStdoutLine(t *testing.T) {
	reader := newRPCReader(t, strings.NewReader("not-json-rpc\n"))
	time.Sleep(100 * time.Millisecond)
	if err := reader.readErr(); err == nil {
		t.Fatalf("expected rpcReader to detect invalid stdout line")
	}
}

func isRealCodexEnabled() bool {
	return strings.TrimSpace(os.Getenv("E2E_REAL_CODEX")) == "1"
}

func requireRealCodex(t *testing.T) {
	t.Helper()
	if !isRealCodexEnabled() {
		t.Skip("set E2E_REAL_CODEX=1 to run real codex app-server e2e")
	}
}

func ensureRealSchema(t *testing.T) {
	t.Helper()
	realSchemaOnce.Do(func() {
		root := repoRoot(t)
		cmd := exec.Command("make", "schema")
		cmd.Dir = root
		if data, err := cmd.CombinedOutput(); err != nil {
			realSchemaErr = fmt.Errorf("make schema failed: %w\n%s", err, string(data))
		}
	})
	if realSchemaErr != nil {
		t.Fatalf("schema preparation failed: %v", realSchemaErr)
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatalf("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", ".."))
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func buildBinary(t *testing.T, rootDir, pkg string) string {
	t.Helper()

	binName := strings.TrimPrefix(pkg, "./")
	binName = strings.ReplaceAll(binName, "/", "-")
	if runtime.GOOS == "windows" {
		binName += ".exe"
	}

	output := filepath.Join(t.TempDir(), binName)
	cmd := exec.Command("go", "build", "-o", output, pkg)
	cmd.Dir = rootDir
	if data, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build %s failed: %v\n%s", pkg, err, string(data))
	}
	return output
}

func writeRequest(t *testing.T, w io.Writer, id, method string, params any) {
	t.Helper()
	payload := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal request %s: %v", method, err)
	}
	if _, err := w.Write(append(data, '\n')); err != nil {
		t.Fatalf("write request %s: %v", method, err)
	}
}

func writeResultResponse(t *testing.T, w io.Writer, id string, result any) {
	t.Helper()
	payload := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"result":  result,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal result response id=%s: %v", id, err)
	}
	if _, err := w.Write(append(data, '\n')); err != nil {
		t.Fatalf("write result response id=%s: %v", id, err)
	}
}

func waitForResponse(t *testing.T, reader *rpcReader, id string, timeout time.Duration, onOther func(rpcMessage)) rpcMessage {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		left := time.Until(deadline)
		if left <= 0 {
			t.Fatalf("timed out waiting for response id=%s", id)
		}
		msg := reader.next(left)
		if messageID(msg) == id {
			return msg
		}
		if onOther != nil {
			onOther(msg)
		}
	}
}

func assertNoFurtherTurnUpdates(t *testing.T, reader *rpcReader, turnID string, duration time.Duration) {
	t.Helper()
	deadline := time.Now().Add(duration)
	for time.Now().Before(deadline) {
		msg, ok := reader.poll(time.Until(deadline))
		if !ok {
			return
		}
		if msg.Method != "session/update" {
			continue
		}
		update := decodeSessionUpdate(t, msg)
		if update.TurnID == turnID {
			t.Fatalf("received unexpected update for cancelled turn %q after cancellation", turnID)
		}
	}
}

func decodeSessionUpdate(t *testing.T, msg rpcMessage) sessionUpdateParams {
	t.Helper()
	var params sessionUpdateParams
	if err := json.Unmarshal(msg.Params, &params); err != nil {
		t.Fatalf("decode session/update params: %v", err)
	}
	return params
}

func decodeRPCErrorDataMap(t *testing.T, errObj *rpcError) map[string]any {
	t.Helper()
	if errObj == nil || errObj.Data == nil {
		return map[string]any{}
	}

	raw, err := json.Marshal(errObj.Data)
	if err != nil {
		t.Fatalf("marshal rpc error data: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal rpc error data map: %v", err)
	}
	return out
}

func valueAsStringAny(value any) string {
	text, _ := value.(string)
	return text
}

func decodeSessionRequestPermission(t *testing.T, msg rpcMessage) sessionRequestPermissionParams {
	t.Helper()
	var params sessionRequestPermissionParams
	if err := json.Unmarshal(msg.Params, &params); err != nil {
		t.Fatalf("decode session/request_permission params: %v", err)
	}
	return params
}

func decodeFSWriteTextFileParams(t *testing.T, msg rpcMessage) fsWriteTextFileParams {
	t.Helper()
	var params fsWriteTextFileParams
	if err := json.Unmarshal(msg.Params, &params); err != nil {
		t.Fatalf("decode fs/write_text_file params: %v", err)
	}
	return params
}

func decodeFSReadTextFileParams(t *testing.T, msg rpcMessage) fsReadTextFileParams {
	t.Helper()
	var params fsReadTextFileParams
	if err := json.Unmarshal(msg.Params, &params); err != nil {
		t.Fatalf("decode fs/read_text_file params: %v", err)
	}
	return params
}

func validateJSONRPCMessage(msg rpcMessage) error {
	if msg.JSONRPC != "2.0" {
		return fmt.Errorf("unexpected jsonrpc version: %q", msg.JSONRPC)
	}

	if msg.Method != "" {
		if len(msg.Result) > 0 || msg.Error != nil {
			return fmt.Errorf("method message cannot include result/error")
		}
		return nil
	}

	if msg.ID == nil {
		return fmt.Errorf("response message missing id")
	}
	if len(msg.Result) == 0 && msg.Error == nil {
		return fmt.Errorf("response message missing both result and error")
	}
	if len(msg.Result) > 0 && msg.Error != nil {
		return fmt.Errorf("response message includes both result and error")
	}
	return nil
}

func messageID(msg rpcMessage) string {
	if msg.ID == nil {
		return ""
	}
	var idString string
	if err := json.Unmarshal(*msg.ID, &idString); err == nil {
		return idString
	}
	var idNumber int64
	if err := json.Unmarshal(*msg.ID, &idNumber); err == nil {
		return fmt.Sprintf("%d", idNumber)
	}
	return string(*msg.ID)
}

func unmarshalResult(t *testing.T, msg rpcMessage, out any) {
	t.Helper()
	if msg.Error != nil {
		t.Fatalf("rpc error code=%d message=%s", msg.Error.Code, msg.Error.Message)
	}
	if len(msg.Result) == 0 {
		t.Fatalf("empty rpc result")
	}
	if err := json.Unmarshal(msg.Result, out); err != nil {
		t.Fatalf("decode result: %v", err)
	}
}
