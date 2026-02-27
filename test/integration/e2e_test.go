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
}

type sessionUpdateParams struct {
	SessionID string `json:"sessionId"`
	TurnID    string `json:"turnId"`
	Type      string `json:"type"`
	Phase     string `json:"phase,omitempty"`
	ItemID    string `json:"itemId,omitempty"`
	ItemType  string `json:"itemType,omitempty"`
	Delta     string `json:"delta,omitempty"`
	Status    string `json:"status,omitempty"`
	Message   string `json:"message,omitempty"`
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
			if msg.JSONRPC != "" && msg.JSONRPC != "2.0" {
				reader.setErr(fmt.Errorf("unexpected jsonrpc version: %q", msg.JSONRPC))
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
	t.Helper()

	rootDir := repoRoot(t)
	fakeServerBin := buildBinary(t, rootDir, "./testdata/fake_codex_app_server")
	adapterBin := buildBinary(t, rootDir, "./cmd/codex-acp-go")

	cmd := exec.Command(adapterBin)
	cmd.Dir = rootDir
	cmd.Env = append(os.Environ(),
		append([]string{
			"CODEX_APP_SERVER_CMD=" + fakeServerBin,
			"CODEX_APP_SERVER_ARGS=",
			"LOG_LEVEL=debug",
		}, extraEnv...)...,
	)

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

func TestRPCReaderDetectsInvalidStdoutLine(t *testing.T) {
	reader := newRPCReader(t, strings.NewReader("not-json-rpc\n"))
	time.Sleep(100 * time.Millisecond)
	if err := reader.readErr(); err == nil {
		t.Fatalf("expected rpcReader to detect invalid stdout line")
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
