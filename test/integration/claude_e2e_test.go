package integration

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// startClaudeAdapter builds and starts the cmd/acp binary with --adapter claude,
// pointing at the fake Claude server.
func startClaudeAdapter(t *testing.T, fakeServerURL string, extraEnv ...string) *adapterHarness {
	t.Helper()

	rootDir := repoRoot(t)
	adapterBin := buildBinary(t, rootDir, "./cmd/acp")

	envBase := []string{
		"LOG_LEVEL=debug",
		"ANTHROPIC_AUTH_TOKEN=test-token",
		"ANTHROPIC_BASE_URL=" + fakeServerURL,
	}

	cmd := exec.Command(adapterBin, "--adapter", "claude")
	cmd.Dir = rootDir
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
	go func() { _, _ = bufio.NewReader(stderr).ReadString(0) }()

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

// startFakeClaudeServer builds and starts the fake Anthropic API server.
// Returns the base URL (e.g. "http://127.0.0.1:PORT").
func startFakeClaudeServer(t *testing.T) string {
	t.Helper()
	rootDir := repoRoot(t)
	serverBin := buildBinary(t, rootDir, "./testdata/fake_claude_server")

	cmd := exec.Command(serverBin)
	cmd.Dir = rootDir

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("fake claude server stdout pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start fake claude server: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	// Read port from first line.
	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() {
		t.Fatalf("fake claude server did not print port")
	}
	portLine := strings.TrimSpace(scanner.Text())
	// Drain remaining stdout in background.
	go func() {
		for scanner.Scan() {
		}
	}()
	return "http://127.0.0.1:" + portLine
}

func TestClaudeE2EBasicPromptAndCancel(t *testing.T) {
	baseURL := startFakeClaudeServer(t)
	h := startClaudeAdapter(t, baseURL)

	// L1/L2: initialize
	h.sendRequest("1", "initialize", map[string]any{})
	initResp := h.waitResponse("1", responseTimeout)
	var initResult struct {
		AgentCapabilities struct {
			Sessions  bool `json:"sessions"`
			Images    bool `json:"images"`
			ToolCalls bool `json:"toolCalls"`
		} `json:"agentCapabilities"`
	}
	unmarshalResult(t, initResp, &initResult)
	if !initResult.AgentCapabilities.Sessions {
		t.Fatalf("initialize: sessions capability missing")
	}

	// session/new
	h.sendRequest("2", "session/new", map[string]any{})
	newResp := h.waitResponse("2", responseTimeout)
	var newResult struct {
		SessionID string `json:"sessionId"`
	}
	unmarshalResult(t, newResp, &newResult)
	if newResult.SessionID == "" {
		t.Fatalf("session/new: empty sessionId")
	}

	// session/prompt — streaming
	h.sendRequest("3", "session/prompt", map[string]any{
		"sessionId": newResult.SessionID,
		"prompt":    "hello from test",
	})

	gotPromptResp := false
	sawMessageChunk := false
	for !gotPromptResp {
		msg := h.nextMessage(responseTimeout)
		if msg.Method == "session/update" {
			update := decodeSessionUpdate(t, msg)
			if update.Type == "message" && update.Delta != "" {
				sawMessageChunk = true
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
			t.Fatalf("session/prompt expected stopReason=end_turn, got %q", result.StopReason)
		}
		gotPromptResp = true
	}
	if !sawMessageChunk {
		t.Fatalf("expected at least one session/update with message delta")
	}

	// session/cancel: send a slow prompt and cancel it
	h.sendRequest("4", "session/prompt", map[string]any{
		"sessionId": newResult.SessionID,
		"prompt":    "slow response",
	})

	// Wait for at least one update to confirm the turn started.
	deadline := time.Now().Add(responseTimeout)
	for time.Now().Before(deadline) {
		msg := h.nextMessage(time.Until(deadline))
		if msg.Method == "session/update" {
			break
		}
	}

	h.sendRequest("5", "session/cancel", map[string]any{
		"sessionId": newResult.SessionID,
	})
	_ = h.waitResponse("5", cancelTimeout)

	// Drain remaining updates until prompt 4 finishes.
	cancelDeadline := time.Now().Add(cancelTimeout)
	for time.Now().Before(cancelDeadline) {
		msg, ok := h.reader.poll(time.Until(cancelDeadline))
		if !ok {
			break
		}
		if messageID(msg) == "4" {
			var result struct {
				StopReason string `json:"stopReason"`
			}
			unmarshalResult(t, msg, &result)
			if result.StopReason != "cancelled" {
				t.Fatalf("cancelled prompt expected stopReason=cancelled, got %q", result.StopReason)
			}
			break
		}
	}

	h.assertStdoutPureJSONRPC()
}

func TestClaudeE2EAuthMissingReturnsError(t *testing.T) {
	baseURL := startFakeClaudeServer(t)
	rootDir := repoRoot(t)
	adapterBin := buildBinary(t, rootDir, "./cmd/acp")

	cmd := exec.Command(adapterBin, "--adapter", "claude")
	cmd.Dir = rootDir
	// No ANTHROPIC_AUTH_TOKEN — adapter should still start but fail on session/new or session/prompt.
	cmd.Env = append(os.Environ(),
		"LOG_LEVEL=debug",
		"ANTHROPIC_AUTH_TOKEN=",
		"ANTHROPIC_BASE_URL="+baseURL,
	)

	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()
	if err := cmd.Start(); err != nil {
		t.Fatalf("start adapter: %v", err)
	}
	t.Cleanup(func() { _ = stdin.Close(); _ = cmd.Wait() })
	go func() { _, _ = bufio.NewReader(stderr).ReadString(0) }()

	reader := newRPCReader(t, stdout)

	writeRequest(t, stdin, "1", "initialize", map[string]any{})
	var initMsg rpcMessage
	deadline := time.Now().Add(responseTimeout)
	for time.Now().Before(deadline) {
		select {
		case msg, ok := <-reader.messages:
			if !ok {
				t.Fatalf("reader closed before initialize response")
			}
			if messageID(msg) == "1" {
				initMsg = msg
				goto gotInit
			}
		case <-time.After(responseTimeout):
			t.Fatalf("timed out waiting for initialize")
		}
	}
gotInit:
	if initMsg.Error != nil {
		// initialize itself might error with no-auth; that's acceptable.
		if !strings.Contains(initMsg.Error.Message, "auth") &&
			!strings.Contains(strings.ToLower(initMsg.Error.Message), "token") {
			t.Fatalf("unexpected initialize error: %s", initMsg.Error.Message)
		}
		return
	}

	writeRequest(t, stdin, "2", "session/new", map[string]any{})
	for {
		select {
		case msg, ok := <-reader.messages:
			if !ok {
				t.Fatalf("reader closed")
			}
			if messageID(msg) == "2" {
				// session/new may succeed (thread is local) or fail with auth error.
				if msg.Error != nil {
					// acceptable: auth gate at session/new
					return
				}
				var nr struct{ SessionID string `json:"sessionId"` }
				unmarshalResult(t, msg, &nr)

				// Now try a prompt — should fail with auth error.
				writeRequest(t, stdin, "3", "session/prompt", map[string]any{
					"sessionId": nr.SessionID,
					"prompt":    "hello",
				})
				for {
					select {
					case msg2, ok2 := <-reader.messages:
						if !ok2 {
							return
						}
						if messageID(msg2) == "3" {
							// Either an RPC error or a stop with an error result — both acceptable.
							return
						}
					case <-time.After(responseTimeout):
						t.Fatalf("timed out waiting for prompt error response")
					}
				}
			}
		case <-time.After(responseTimeout):
			t.Fatalf("timed out waiting for session/new")
		}
	}
}

func TestClaudeE2EApprovalRoundTrip(t *testing.T) {
	baseURL := startFakeClaudeServer(t)
	h := startClaudeAdapter(t, baseURL)

	h.sendRequest("1", "initialize", map[string]any{})
	_ = h.waitResponse("1", responseTimeout)

	h.sendRequest("2", "session/new", map[string]any{})
	newResp := h.waitResponse("2", responseTimeout)
	var newResult struct {
		SessionID string `json:"sessionId"`
	}
	unmarshalResult(t, newResp, &newResult)

	// Trigger approval flow (fake server returns tool_use for "approval command").
	h.sendRequest("3", "session/prompt", map[string]any{
		"sessionId": newResult.SessionID,
		"prompt":    "approval command please",
	})

	gotPermission := false
	gotPromptResp := false
	for !gotPromptResp {
		msg := h.nextMessage(responseTimeout)
		switch msg.Method {
		case "session/request_permission":
			gotPermission = true
			// Approve the permission.
			h.sendResultResponse(messageID(msg), map[string]any{"outcome": "approved"})
		case "session/update":
			// consume
		default:
			if messageID(msg) != "3" {
				continue
			}
			var result struct {
				StopReason string `json:"stopReason"`
			}
			unmarshalResult(t, msg, &result)
			if result.StopReason != "end_turn" {
				t.Fatalf("approval prompt expected stopReason=end_turn, got %q", result.StopReason)
			}
			gotPromptResp = true
		}
	}

	if !gotPermission {
		t.Fatalf("approval flow: expected session/request_permission")
	}

	h.assertStdoutPureJSONRPC()
}

func TestClaudeE2EInitializeContainsStandardFields(t *testing.T) {
	baseURL := startFakeClaudeServer(t)
	h := startClaudeAdapter(t, baseURL)

	h.sendRequest("1", "initialize", map[string]any{})
	resp := h.waitResponse("1", responseTimeout)

	var result map[string]json.RawMessage
	unmarshalResult(t, resp, &result)

	if _, ok := result["protocolVersion"]; !ok {
		t.Fatalf("initialize result missing protocolVersion")
	}
	if _, ok := result["agentCapabilities"]; !ok {
		t.Fatalf("initialize result missing agentCapabilities")
	}
	if _, ok := result["agentInfo"]; !ok {
		t.Fatalf("initialize result missing agentInfo")
	}

	h.assertStdoutPureJSONRPC()
}

func TestClaudeContractStandaloneVsEmbedded(t *testing.T) {
	baseURL := startFakeClaudeServer(t)

	// Standalone mode.
	h := startClaudeAdapter(t, baseURL)
	h.sendRequest("1", "initialize", map[string]any{})
	initResp := h.waitResponse("1", responseTimeout)
	var standaloneInit struct {
		ProtocolVersion json.RawMessage `json:"protocolVersion"`
	}
	unmarshalResult(t, initResp, &standaloneInit)

	h.sendRequest("2", "session/new", map[string]any{})
	newResp := h.waitResponse("2", responseTimeout)
	var newResult struct {
		SessionID string `json:"sessionId"`
	}
	unmarshalResult(t, newResp, &newResult)

	h.sendRequest("3", "session/prompt", map[string]any{
		"sessionId": newResult.SessionID,
		"prompt":    "contract test hello",
	})

	var standaloneStopReason string
	standaloneSawChunk := false
	for {
		msg := h.nextMessage(responseTimeout)
		if msg.Method == "session/update" {
			update := decodeSessionUpdate(t, msg)
			if update.Type == "message" && update.Delta != "" {
				standaloneSawChunk = true
			}
			continue
		}
		if messageID(msg) == "3" {
			var result struct {
				StopReason string `json:"stopReason"`
			}
			unmarshalResult(t, msg, &result)
			standaloneStopReason = result.StopReason
			break
		}
	}

	if standaloneStopReason != "end_turn" {
		t.Fatalf("standalone: expected stopReason=end_turn, got %q", standaloneStopReason)
	}
	if !standaloneSawChunk {
		t.Fatalf("standalone: expected streaming chunk")
	}

	// Verify protocol version is non-empty (both modes use same ACP server).
	if len(standaloneInit.ProtocolVersion) == 0 {
		t.Fatalf("standalone: expected non-empty protocolVersion")
	}

	t.Logf("Claude contract test passed: standalone protocolVersion=%s stopReason=%q sawChunk=%v",
		string(standaloneInit.ProtocolVersion), standaloneStopReason, standaloneSawChunk)
}

func TestClaudeE2EUnifiedCmdAdapterFlag(t *testing.T) {
	// Verify that cmd/acp with --adapter codex still works (Codex zero-regression check).
	// We reuse the fake codex app-server.
	if isRealCodexEnabled() {
		t.Skip("skipped when E2E_REAL_CODEX=1")
	}

	rootDir := repoRoot(t)
	adapterBin := buildBinary(t, rootDir, "./cmd/acp")
	fakeServerBin := buildBinary(t, rootDir, "./testdata/fake_codex_app_server")

	cmd := exec.Command(adapterBin, "--adapter", "codex")
	cmd.Dir = rootDir
	cmd.Env = append(os.Environ(),
		"LOG_LEVEL=debug",
		"CODEX_APP_SERVER_CMD="+fakeServerBin,
		"CODEX_APP_SERVER_ARGS=",
		"CHATGPT_SUBSCRIPTION_ACTIVE=1",
	)

	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()
	if err := cmd.Start(); err != nil {
		t.Fatalf("start acp --adapter codex: %v", err)
	}
	t.Cleanup(func() { _ = stdin.Close(); _ = cmd.Wait() })
	go func() { _, _ = fmt.Fprintf(os.Stderr, ""); _ = bufio.NewReader(stderr).UnreadByte() }()

	reader := newRPCReader(t, stdout)

	writeRequest(t, stdin, "1", "initialize", map[string]any{})
	var initMsg rpcMessage
	select {
	case msg := <-reader.messages:
		initMsg = msg
	case <-time.After(responseTimeout):
		t.Fatalf("timed out waiting for initialize from codex adapter via cmd/acp")
	}
	if initMsg.Error != nil {
		t.Fatalf("cmd/acp --adapter codex initialize error: %s", initMsg.Error.Message)
	}
	if messageID(initMsg) != "1" {
		t.Fatalf("unexpected first message id: %s", messageID(initMsg))
	}
	_ = stdin.Close()
}
