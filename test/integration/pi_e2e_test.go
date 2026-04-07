package integration

import (
	"bufio"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func startPiAdapter(t *testing.T, piBin string, sessionDir string, extraEnv ...string) *adapterHarness {
	t.Helper()

	rootDir := repoRoot(t)
	adapterBin := buildBinary(t, rootDir, "./cmd/acp")

	envBase := []string{
		"LOG_LEVEL=debug",
		"PI_BIN=" + piBin,
		"PI_SESSION_DIR=" + sessionDir,
	}

	cmd := exec.Command(adapterBin, "--adapter", "pi")
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

func buildFakePiRPC(t *testing.T) string {
	t.Helper()
	return buildBinary(t, repoRoot(t), "./testdata/fake_pi_rpc")
}

func TestPiE2EBasicPromptCancelAndAvailableCommands(t *testing.T) {
	piBin := buildFakePiRPC(t)
	h := startPiAdapter(t, piBin, t.TempDir())

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

	h.sendRequest("2", "session/new", map[string]any{
		"cwd": repoRoot(t),
	})
	newResp := h.waitResponse("2", responseTimeout)
	var newResult struct {
		SessionID string `json:"sessionId"`
	}
	unmarshalResult(t, newResp, &newResult)
	if newResult.SessionID == "" {
		t.Fatalf("session/new returned empty sessionId")
	}

	sawCommands := false
	for !sawCommands {
		msg := h.nextMessage(responseTimeout)
		if msg.Method != "session/update" {
			continue
		}
		update := decodeSessionUpdate(t, msg)
		if update.SessionID != newResult.SessionID || update.Type != "available_commands_update" {
			continue
		}
		want := []string{"review", "review-branch", "review-commit", "init", "compact", "logout"}
		if got := availableCommandNames(update.AvailableCommands); !reflect.DeepEqual(got, want) {
			t.Fatalf("Pi availableCommands=%v, want %v", got, want)
		}
		if hasAvailableCommand(update.AvailableCommands, "mcp") {
			t.Fatalf("Pi availableCommands should not advertise /mcp: %+v", update.AvailableCommands)
		}
		sawCommands = true
	}

	h.sendRequest("3", "session/prompt", map[string]any{
		"sessionId": newResult.SessionID,
		"prompt":    "hello from pi",
	})

	gotPromptResp := false
	sawMessageChunk := false
	for !gotPromptResp {
		msg := h.nextMessage(responseTimeout)
		if msg.Method == "session/update" {
			update := decodeSessionUpdate(t, msg)
			if update.SessionID == newResult.SessionID && update.Type == "message" && update.Delta != "" {
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
		t.Fatalf("expected at least one Pi message delta")
	}

	h.sendRequest("4", "session/prompt", map[string]any{
		"sessionId": newResult.SessionID,
		"prompt":    "slow response",
	})

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

	cancelDeadline := time.Now().Add(cancelTimeout)
	gotCancelResp := false
	gotCancelledPromptResp := false
	for !(gotCancelResp && gotCancelledPromptResp) {
		if time.Now().After(cancelDeadline) {
			t.Fatalf("cancelled Pi prompt did not finish in time")
		}

		msg := h.nextMessage(time.Until(cancelDeadline))
		switch messageID(msg) {
		case "5":
			var result struct {
				Cancelled bool `json:"cancelled"`
			}
			unmarshalResult(t, msg, &result)
			if !result.Cancelled {
				t.Fatalf("session/cancel expected cancelled=true")
			}
			gotCancelResp = true
		case "4":
			var result struct {
				StopReason string `json:"stopReason"`
			}
			unmarshalResult(t, msg, &result)
			if result.StopReason != "cancelled" {
				t.Fatalf("cancelled Pi prompt expected stopReason=cancelled, got %q", result.StopReason)
			}
			gotCancelledPromptResp = true
		}
	}

	h.assertStdoutPureJSONRPC()
}

func TestPiE2ESessionConfigOptionsModelListAndSwitch(t *testing.T) {
	piBin := buildFakePiRPC(t)
	h := startPiAdapter(
		t,
		piBin,
		t.TempDir(),
		"PI_PROVIDER=openai",
		"PI_MODEL=gpt-5.1",
	)

	h.sendRequest("1", "initialize", map[string]any{})
	_ = h.waitResponse("1", responseTimeout)

	h.sendRequest("2", "session/new", map[string]any{
		"cwd": repoRoot(t),
	})
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
	var thoughtConfig sessionConfigOption
	foundModel := false
	foundThought := false
	for _, option := range newResult.ConfigOptions {
		if option.ID == "model" {
			modelConfig = option
			foundModel = true
			continue
		}
		if option.ID == "thought_level" {
			thoughtConfig = option
			foundThought = true
		}
	}
	if !foundModel {
		t.Fatalf("session/new configOptions should include model")
	}
	if !foundThought {
		t.Fatalf("session/new configOptions should include thought_level")
	}
	if len(modelConfig.Options) < 2 {
		t.Fatalf("Pi model options should include both fake models")
	}
	if len(thoughtConfig.Options) < 2 {
		t.Fatalf("Pi thought_level options should include reasoning choices")
	}

	targetModel := "anthropic/claude-sonnet-4-5"
	h.sendRequest("3", "session/set_config_option", map[string]any{
		"sessionId": newResult.SessionID,
		"configId":  "model",
		"value":     targetModel,
	})

	gotModelResp := false
	sawModelUpdate := false
	for !gotModelResp {
		msg := h.nextMessage(responseTimeout)
		if msg.Method == "session/update" {
			update := decodeSessionUpdate(t, msg)
			if update.Type == "config_options_update" {
				for _, option := range update.ConfigOptions {
					if option.ID == "model" && option.CurrentValue == targetModel {
						sawModelUpdate = true
						break
					}
				}
			}
			continue
		}
		if messageID(msg) != "3" {
			continue
		}
		var result struct {
			ConfigOptions []sessionConfigOption `json:"configOptions"`
		}
		unmarshalResult(t, msg, &result)
		applied := false
		for _, option := range result.ConfigOptions {
			if option.ID == "model" && option.CurrentValue == targetModel {
				applied = true
				break
			}
		}
		if !applied {
			t.Fatalf("session/set_config_option should apply Pi model=%q", targetModel)
		}
		gotModelResp = true
	}
	if !sawModelUpdate {
		t.Fatalf("session/set_config_option should emit Pi config_options_update for model")
	}

	targetThought := "high"
	h.sendRequest("4", "session/set_config_option", map[string]any{
		"sessionId": newResult.SessionID,
		"configId":  "thought_level",
		"value":     targetThought,
	})

	gotThoughtResp := false
	sawThoughtUpdate := false
	for !gotThoughtResp {
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
		var result struct {
			ConfigOptions []sessionConfigOption `json:"configOptions"`
		}
		unmarshalResult(t, msg, &result)
		applied := false
		for _, option := range result.ConfigOptions {
			if option.ID == "thought_level" && option.CurrentValue == targetThought {
				applied = true
				break
			}
		}
		if !applied {
			t.Fatalf("session/set_config_option should apply Pi thought_level=%q", targetThought)
		}
		gotThoughtResp = true
	}
	if !sawThoughtUpdate {
		t.Fatalf("session/set_config_option should emit Pi config_options_update for thought_level")
	}
}

func TestPiE2ESessionListLoadAndPrompt(t *testing.T) {
	piBin := buildFakePiRPC(t)
	sessionDir := t.TempDir()
	loadCWD := repoRoot(t)
	writePiSeedSession(t, sessionDir, "seed-pi-session", "Seed Pi Session", loadCWD,
		"Explain the Pi adapter bridge.",
		"It bridges ACP stdio to Pi RPC mode.",
	)

	h := startPiAdapter(t, piBin, sessionDir)

	h.sendRequest("1", "initialize", map[string]any{})
	initResp := h.waitResponse("1", responseTimeout)
	var initResult struct {
		AgentCapabilities struct {
			LoadSession         bool `json:"loadSession"`
			SessionCapabilities struct {
				List json.RawMessage `json:"list"`
			} `json:"sessionCapabilities"`
		} `json:"agentCapabilities"`
	}
	unmarshalResult(t, initResp, &initResult)
	if !initResult.AgentCapabilities.LoadSession {
		t.Fatalf("initialize should advertise loadSession=true for Pi")
	}
	if len(initResult.AgentCapabilities.SessionCapabilities.List) == 0 {
		t.Fatalf("initialize should advertise session/list capability for Pi")
	}

	type sessionListItem struct {
		SessionID string `json:"sessionId"`
		Title     string `json:"title"`
		CWD       string `json:"cwd"`
	}
	type sessionListResult struct {
		Sessions []sessionListItem `json:"sessions"`
	}

	h.sendRequest("2", "session/list", map[string]any{
		"cwd": loadCWD,
	})
	listResp := h.waitResponse("2", responseTimeout)
	var listResult sessionListResult
	unmarshalResult(t, listResp, &listResult)

	targetSessionID := ""
	for _, item := range listResult.Sessions {
		if item.Title == "Seed Pi Session" {
			targetSessionID = item.SessionID
			if item.CWD != loadCWD {
				t.Fatalf("session/list cwd mismatch: got %q want %q", item.CWD, loadCWD)
			}
			break
		}
	}
	if targetSessionID == "" {
		t.Fatalf("seed Pi session not found in session/list result: %+v", listResult.Sessions)
	}

	h.sendRequest("3", "session/load", map[string]any{
		"sessionId": targetSessionID,
		"cwd":       loadCWD,
	})

	loadResultReceived := false
	sawUserHistory := false
	sawAgentHistory := false
	sawConfigOptions := false
	for !loadResultReceived {
		msg := h.nextMessage(responseTimeout)
		if msg.Method == "session/update" {
			update := decodeSessionUpdate(t, msg)
			if update.SessionID != targetSessionID {
				t.Fatalf("session/load replay routed to wrong session: got %q want %q", update.SessionID, targetSessionID)
			}
			if update.Update != nil && update.Update.Content != nil {
				switch update.Update.SessionUpdate {
				case "user_message_chunk":
					if strings.Contains(update.Update.Content.Text, "Explain the Pi adapter bridge.") {
						sawUserHistory = true
					}
				case "agent_message_chunk":
					if strings.Contains(update.Update.Content.Text, "bridges ACP stdio to Pi RPC mode") {
						sawAgentHistory = true
					}
				}
			}
			continue
		}
		if messageID(msg) != "3" {
			continue
		}
		var result struct {
			ConfigOptions []sessionConfigOption `json:"configOptions"`
		}
		unmarshalResult(t, msg, &result)
		sawConfigOptions = len(result.ConfigOptions) > 0
		loadResultReceived = true
	}
	if !sawUserHistory {
		t.Fatalf("session/load should replay at least one Pi user history chunk")
	}
	if !sawAgentHistory {
		t.Fatalf("session/load should replay at least one Pi agent history chunk")
	}
	if !sawConfigOptions {
		t.Fatalf("session/load should return Pi configOptions")
	}

	h.sendRequest("4", "session/prompt", map[string]any{
		"sessionId": targetSessionID,
		"prompt":    "hello after load",
	})

	gotPromptResp := false
	sawMessageChunk := false
	for !gotPromptResp {
		msg := h.nextMessage(responseTimeout)
		if msg.Method == "session/update" {
			update := decodeSessionUpdate(t, msg)
			if update.SessionID != targetSessionID {
				t.Fatalf("loaded Pi session update routed to wrong session: got %q want %q", update.SessionID, targetSessionID)
			}
			if update.Type == "message" && update.Delta != "" {
				sawMessageChunk = true
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
			t.Fatalf("loaded Pi prompt expected stopReason=end_turn, got %q", result.StopReason)
		}
		gotPromptResp = true
	}
	if !sawMessageChunk {
		t.Fatalf("loaded Pi session prompt should stream at least one message chunk")
	}
}

func TestPiE2EPermissionGate(t *testing.T) {
	piBin := buildFakePiRPC(t)
	h := startPiAdapter(t, piBin, t.TempDir())

	h.sendRequest("1", "initialize", map[string]any{})
	_ = h.waitResponse("1", responseTimeout)

	h.sendRequest("2", "session/new", map[string]any{
		"cwd": repoRoot(t),
	})
	newResp := h.waitResponse("2", responseTimeout)
	var newResult struct {
		SessionID string `json:"sessionId"`
	}
	unmarshalResult(t, newResp, &newResult)

	h.sendRequest("3", "session/prompt", map[string]any{
		"sessionId": newResult.SessionID,
		"prompt":    "approval command",
	})

	deadline := time.Now().Add(2 * responseTimeout)
	gotPermission := false
	gotPromptResp := false
	sawToolInProgress := false
	finalToolStatus := ""
	finalDecision := ""
	sawExecuted := false

	for !(gotPermission && gotPromptResp && finalToolStatus != "") {
		if time.Now().After(deadline) {
			t.Fatalf("Pi approval flow timeout: permission=%v promptResp=%v finalToolStatus=%q", gotPermission, gotPromptResp, finalToolStatus)
		}

		msg := h.nextMessage(time.Until(deadline))
		switch msg.Method {
		case "session/request_permission":
			gotPermission = true
			req := decodeSessionRequestPermission(t, msg)
			if req.SessionID != newResult.SessionID {
				t.Fatalf("Pi permission routed to wrong session: got=%q want=%q", req.SessionID, newResult.SessionID)
			}
			if req.Approval != "command" {
				t.Fatalf("Pi permission kind mismatch: got=%q want=command", req.Approval)
			}
			if req.Command != "echo approved by fake pi" {
				t.Fatalf("Pi permission command mismatch: got=%q", req.Command)
			}
			h.sendResultResponse(messageID(msg), map[string]any{"outcome": "approved"})
		case "session/update":
			update := decodeSessionUpdate(t, msg)
			if update.Type == "message" && strings.Contains(update.Delta, "executed command") {
				sawExecuted = true
			}
			if update.Type != "tool_call_update" {
				continue
			}
			if update.Status == "in_progress" {
				sawToolInProgress = true
			}
			if update.Status == "completed" || update.Status == "failed" {
				finalToolStatus = update.Status
				finalDecision = update.PermissionDecision
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
				t.Fatalf("Pi approval prompt expected stopReason=end_turn, got %q", result.StopReason)
			}
			gotPromptResp = true
		}
	}

	if !gotPermission {
		t.Fatalf("Pi approval flow expected session/request_permission")
	}
	if !sawToolInProgress {
		t.Fatalf("Pi approval flow expected tool_call_update in_progress")
	}
	if finalToolStatus != "completed" {
		t.Fatalf("Pi tool_call_update status mismatch: got=%q want=completed", finalToolStatus)
	}
	if finalDecision != "approved" {
		t.Fatalf("Pi permission decision mismatch: got=%q want=approved", finalDecision)
	}
	if !sawExecuted {
		t.Fatalf("approved Pi permission flow expected executed marker")
	}
}

func TestPiE2ELogoutAuthenticateAndPrompt(t *testing.T) {
	piBin := buildFakePiRPC(t)
	h := startPiAdapter(t, piBin, t.TempDir())

	h.sendRequest("1", "initialize", map[string]any{})
	_ = h.waitResponse("1", responseTimeout)

	h.sendRequest("2", "session/new", map[string]any{
		"cwd": repoRoot(t),
	})
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
	sawLoggedOutCatalog := false
	for !gotLogoutResp {
		msg := h.nextMessage(responseTimeout)
		if msg.Method == "session/update" {
			update := decodeSessionUpdate(t, msg)
			if update.SessionID != newResult.SessionID || update.Type != "available_commands_update" {
				continue
			}
			got := availableCommandNames(update.AvailableCommands)
			if len(got) == 1 && got[0] == "logout" {
				sawLoggedOutCatalog = true
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
			t.Fatalf("Pi /logout expected stopReason=end_turn, got %q", result.StopReason)
		}
		gotLogoutResp = true
	}
	if !sawLoggedOutCatalog {
		t.Fatalf("Pi logout should publish reduced available command catalog")
	}

	h.sendRequest("4", "authenticate", map[string]any{
		"methodId": "pi",
	})
	authResp := h.waitResponse("4", responseTimeout)
	var authResult struct {
		Authenticated bool `json:"authenticated"`
	}
	unmarshalResult(t, authResp, &authResult)
	if !authResult.Authenticated {
		t.Fatalf("Pi authenticate should succeed")
	}

	sawRestoredCatalog := false
	for !sawRestoredCatalog {
		msg := h.nextMessage(responseTimeout)
		if msg.Method != "session/update" {
			continue
		}
		update := decodeSessionUpdate(t, msg)
		if update.SessionID != newResult.SessionID || update.Type != "available_commands_update" {
			continue
		}
		want := []string{"review", "review-branch", "review-commit", "init", "compact", "logout"}
		if reflect.DeepEqual(availableCommandNames(update.AvailableCommands), want) {
			sawRestoredCatalog = true
		}
	}

	h.sendRequest("5", "session/prompt", map[string]any{
		"sessionId": newResult.SessionID,
		"prompt":    "hello after logout",
	})

	gotPromptResp := false
	sawMessage := false
	for !gotPromptResp {
		msg := h.nextMessage(responseTimeout)
		if msg.Method == "session/update" {
			update := decodeSessionUpdate(t, msg)
			if update.SessionID == newResult.SessionID && update.Type == "message" && strings.TrimSpace(update.Delta) != "" {
				sawMessage = true
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
			t.Fatalf("Pi prompt after authenticate expected stopReason=end_turn, got %q", result.StopReason)
		}
		gotPromptResp = true
	}
	if !sawMessage {
		t.Fatalf("Pi prompt after authenticate should emit a message")
	}
}

func writePiSeedSession(t *testing.T, sessionDir string, sessionID string, title string, cwd string, userText string, assistantText string) string {
	t.Helper()

	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}

	userContent, err := json.Marshal([]map[string]any{{
		"type": "text",
		"text": userText,
	}})
	if err != nil {
		t.Fatalf("marshal Pi user content: %v", err)
	}
	assistantContent, err := json.Marshal([]map[string]any{{
		"type": "text",
		"text": assistantText,
	}})
	if err != nil {
		t.Fatalf("marshal Pi assistant content: %v", err)
	}

	now := time.Now().UTC()
	lines := []string{
		mustJSONLine(t, map[string]any{
			"type":      "session",
			"id":        sessionID,
			"timestamp": now.Add(-2 * time.Minute).Format(time.RFC3339),
			"cwd":       cwd,
		}),
		mustJSONLine(t, map[string]any{
			"type": "session_info",
			"name": title,
		}),
		mustJSONLine(t, map[string]any{
			"type": "message",
			"id":   "msg-1",
			"message": map[string]any{
				"role":      "user",
				"content":   json.RawMessage(userContent),
				"timestamp": now.Add(-90 * time.Second).UnixMilli(),
			},
		}),
		mustJSONLine(t, map[string]any{
			"type": "message",
			"id":   "msg-2",
			"message": map[string]any{
				"role":      "assistant",
				"content":   json.RawMessage(assistantContent),
				"provider":  "fake",
				"model":     "pi-rpc",
				"timestamp": now.Add(-60 * time.Second).UnixMilli(),
			},
		}),
	}

	path := filepath.Join(sessionDir, sessionID+".jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatalf("write Pi seed session: %v", err)
	}
	return path
}

func mustJSONLine(t *testing.T, payload any) string {
	t.Helper()
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal json line: %v", err)
	}
	return string(data)
}
