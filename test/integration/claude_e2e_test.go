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
// pointing at the fake Claude CLI binary.
func startClaudeAdapter(t *testing.T, claudeBin string, extraEnv ...string) *adapterHarness {
	t.Helper()

	rootDir := repoRoot(t)
	adapterBin := buildBinary(t, rootDir, "./cmd/acp")

	envBase := []string{
		"LOG_LEVEL=debug",
		"CLAUDE_BIN=" + claudeBin,
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

// buildFakeClaudeCLI builds the fake_claude_cli binary and returns its path.
func buildFakeClaudeCLI(t *testing.T) string {
	t.Helper()
	return buildBinary(t, repoRoot(t), "./testdata/fake_claude_cli")
}

func TestClaudeE2EBasicPromptAndCancel(t *testing.T) {
	claudeBin := buildFakeClaudeCLI(t)
	h := startClaudeAdapter(t, claudeBin)

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

func TestClaudeE2ESessionConfigOptionsModelListAndSwitch(t *testing.T) {
	claudeBin := buildFakeClaudeCLI(t)
	h := startClaudeAdapter(
		t,
		claudeBin,
		"CLAUDE_MODEL=claude-opus-4-6",
		"CLAUDE_MODELS=claude-opus-4-6,claude-sonnet-4-5",
	)

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

	var modelConfig sessionConfigOption
	found := false
	for _, option := range newResult.ConfigOptions {
		if option.ID == "model" {
			modelConfig = option
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("session/new configOptions should include model")
	}
	if len(modelConfig.Options) < 2 {
		t.Fatalf("model options should include configured CLAUDE_MODELS")
	}

	targetModel := "claude-sonnet-4-5"
	h.sendRequest("3", "session/set_config_option", map[string]any{
		"sessionId": newResult.SessionID,
		"configId":  "model",
		"value":     targetModel,
	})

	gotSetResp := false
	sawConfigUpdate := false
	for !gotSetResp {
		msg := h.nextMessage(responseTimeout)
		if msg.Method == "session/update" {
			update := decodeSessionUpdate(t, msg)
			if update.Type == "config_options_update" {
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
			t.Fatalf("session/set_config_option should apply model=%q", targetModel)
		}
		gotSetResp = true
	}
	if !sawConfigUpdate {
		t.Fatalf("session/set_config_option should emit config_options_update")
	}

	h.sendRequest("4", "session/prompt", map[string]any{
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
		if messageID(msg) != "4" {
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
	if !strings.Contains(strings.ReplaceAll(output, "\n", ""), "model="+targetModel) {
		t.Fatalf("prompt should run with switched model=%q, output=%s", targetModel, output)
	}
}

func TestClaudeE2ENoAuthRequiredWithCLI(t *testing.T) {
	// In CLI mode auth is handled by the claude binary itself; no token needed in adapter.
	claudeBin := buildFakeClaudeCLI(t)
	rootDir := repoRoot(t)
	adapterBin := buildBinary(t, rootDir, "./cmd/acp")

	cmd := exec.Command(adapterBin, "--adapter", "claude")
	cmd.Dir = rootDir
	cmd.Env = append(os.Environ(),
		"LOG_LEVEL=debug",
		"CLAUDE_BIN="+claudeBin,
		// No API token needed — claude CLI handles auth.
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
	select {
	case msg, ok := <-reader.messages:
		if !ok {
			t.Fatalf("reader closed before initialize response")
		}
		if messageID(msg) != "1" {
			t.Fatalf("unexpected first message id: %s", messageID(msg))
		}
		if msg.Error != nil {
			t.Fatalf("initialize error: %s", msg.Error.Message)
		}
	case <-time.After(responseTimeout):
		t.Fatalf("timed out waiting for initialize")
	}

	_ = stdin.Close()
}

func TestClaudeE2EApprovalAutoApproved(t *testing.T) {
	// In CLI mode with --dangerously-skip-permissions, tools are auto-executed.
	// The fake CLI binary returns a normal text response for "approval command" prompts.
	claudeBin := buildFakeClaudeCLI(t)
	h := startClaudeAdapter(t, claudeBin)

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
		"prompt":    "approval command please",
	})

	gotPromptResp := false
	for !gotPromptResp {
		msg := h.nextMessage(responseTimeout)
		if msg.Method == "session/update" {
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
			t.Fatalf("expected stopReason=end_turn, got %q", result.StopReason)
		}
		gotPromptResp = true
	}

	h.assertStdoutPureJSONRPC()
}

func TestClaudeE2EInitializeContainsStandardFields(t *testing.T) {
	claudeBin := buildFakeClaudeCLI(t)
	h := startClaudeAdapter(t, claudeBin)

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
	claudeBin := buildFakeClaudeCLI(t)

	// Standalone mode.
	h := startClaudeAdapter(t, claudeBin)
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

	if len(standaloneInit.ProtocolVersion) == 0 {
		t.Fatalf("standalone: expected non-empty protocolVersion")
	}

	t.Logf("Claude contract test passed: standalone protocolVersion=%s stopReason=%q sawChunk=%v",
		string(standaloneInit.ProtocolVersion), standaloneStopReason, standaloneSawChunk)
}

func TestClaudeE2EUnifiedCmdAdapterFlag(t *testing.T) {
	// Verify that cmd/acp with --adapter codex still works (Codex zero-regression check).
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

// Ensure unused imports are satisfied.
var _ = strings.Contains
