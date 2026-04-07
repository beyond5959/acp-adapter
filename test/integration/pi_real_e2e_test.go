package integration

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

const (
	realPiProvider  = "openai-codex"
	realPiModel     = "gpt-5.4-mini"
	realPiFullModel = realPiProvider + "/" + realPiModel

	realPiResponseTimeout = 90 * time.Second
	realPiCancelTimeout   = 20 * time.Second
)

func requireRealPi(t *testing.T) {
	t.Helper()
	if strings.TrimSpace(os.Getenv("E2E_REAL_PI")) != "1" {
		t.Skip("set E2E_REAL_PI=1 to run real Pi RPC e2e")
	}
}

func realPiBin() string {
	if value := strings.TrimSpace(os.Getenv("E2E_REAL_PI_BIN")); value != "" {
		return value
	}
	return "pi"
}

func startRealPiAdapter(t *testing.T, sessionDir string, extraEnv ...string) *adapterHarness {
	t.Helper()
	env := []string{
		"PI_PROVIDER=" + realPiProvider,
		"PI_MODEL=" + realPiModel,
	}
	env = append(env, extraEnv...)
	return startPiAdapter(t, realPiBin(), sessionDir, env...)
}

func newRealPiSession(t *testing.T, h *adapterHarness, requestID string, cwd string) (string, []sessionConfigOption) {
	t.Helper()

	h.sendRequest(requestID, "session/new", map[string]any{
		"cwd":   cwd,
		"model": realPiFullModel,
	})
	resp := h.waitResponse(requestID, realPiResponseTimeout)
	var result struct {
		SessionID     string                `json:"sessionId"`
		ConfigOptions []sessionConfigOption `json:"configOptions"`
	}
	unmarshalResult(t, resp, &result)
	if result.SessionID == "" {
		t.Fatalf("real Pi session/new returned empty sessionId")
	}
	return result.SessionID, result.ConfigOptions
}

func approveRealPiPermission(
	t *testing.T,
	h *adapterHarness,
	msg rpcMessage,
	sessionID string,
) sessionRequestPermissionParams {
	t.Helper()

	req := decodeSessionRequestPermission(t, msg)
	if req.SessionID != sessionID {
		t.Fatalf("real Pi permission routed to wrong session: got=%q want=%q", req.SessionID, sessionID)
	}
	h.sendResultResponse(messageID(msg), map[string]any{"outcome": "approved"})
	return req
}

func requireConfigOption(
	t *testing.T,
	options []sessionConfigOption,
	id string,
) sessionConfigOption {
	t.Helper()

	for _, option := range options {
		if option.ID == id {
			return option
		}
	}
	t.Fatalf("missing real Pi config option %q in %+v", id, options)
	return sessionConfigOption{}
}

func configOptionTargetValue(option sessionConfigOption) string {
	for _, candidate := range option.Options {
		value := strings.TrimSpace(candidate.Value)
		if value != "" && value != option.CurrentValue {
			return value
		}
	}
	return option.CurrentValue
}

func mustRunGit(t *testing.T, dir string, args ...string) string {
	t.Helper()

	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	data, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, string(data))
	}
	return strings.TrimSpace(string(data))
}

func makeRealPiReviewRepo(t *testing.T) (string, string) {
	t.Helper()

	dir := t.TempDir()
	mustRunGit(t, dir, "init")
	mustRunGit(t, dir, "config", "user.name", "ACP Adapter")
	mustRunGit(t, dir, "config", "user.email", "acp-adapter@example.com")
	mustRunGit(t, dir, "checkout", "-b", "master")

	baseFile := filepath.Join(dir, "note.txt")
	if err := os.WriteFile(baseFile, []byte("base\n"), 0o600); err != nil {
		t.Fatalf("write base file: %v", err)
	}
	mustRunGit(t, dir, "add", "note.txt")
	mustRunGit(t, dir, "commit", "-m", "base commit")

	mustRunGit(t, dir, "checkout", "-b", "feature")
	if err := os.WriteFile(baseFile, []byte("feature branch change\n"), 0o600); err != nil {
		t.Fatalf("write feature file: %v", err)
	}
	mustRunGit(t, dir, "add", "note.txt")
	mustRunGit(t, dir, "commit", "-m", "feature commit")

	commit := mustRunGit(t, dir, "rev-parse", "--short", "HEAD")
	if err := os.WriteFile(filepath.Join(dir, "workspace.txt"), []byte("uncommitted workspace change\n"), 0o600); err != nil {
		t.Fatalf("write workspace file: %v", err)
	}
	return dir, commit
}

func TestRealPiBasicPromptCancelAndAvailableCommands(t *testing.T) {
	requireRealPi(t)

	h := startRealPiAdapter(t, t.TempDir())

	h.sendRequest("1", "initialize", map[string]any{})
	initResp := h.waitResponse("1", realPiResponseTimeout)
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
		t.Fatalf("real Pi initialize capabilities mismatch: %+v", initResult.AgentCapabilities)
	}

	sessionID, configOptions := newRealPiSession(t, h, "2", repoRoot(t))

	var modelValue string
	for _, option := range configOptions {
		if option.ID == "model" {
			modelValue = option.CurrentValue
			break
		}
	}
	if !strings.Contains(modelValue, realPiModel) && !strings.Contains(modelValue, realPiFullModel) {
		t.Fatalf("real Pi session/new model mismatch: got %q want contains %q", modelValue, realPiModel)
	}

	sawCommands := false
	for !sawCommands {
		msg := h.nextMessage(realPiResponseTimeout)
		if msg.Method != "session/update" {
			continue
		}
		update := decodeSessionUpdate(t, msg)
		if update.SessionID != sessionID || update.Type != "available_commands_update" {
			continue
		}
		want := []string{"review", "review-branch", "review-commit", "init", "compact", "logout"}
		if got := availableCommandNames(update.AvailableCommands); !reflect.DeepEqual(got, want) {
			t.Fatalf("real Pi availableCommands=%v, want %v", got, want)
		}
		if hasAvailableCommand(update.AvailableCommands, "mcp") {
			t.Fatalf("real Pi should not advertise /mcp: %+v", update.AvailableCommands)
		}
		sawCommands = true
	}

	h.sendRequest("3", "session/prompt", map[string]any{
		"sessionId": sessionID,
		"prompt":    "Reply with exactly PONG and nothing else.",
	})

	gotPromptResp := false
	sawMessageChunk := false
	var promptText strings.Builder
	for !gotPromptResp {
		msg := h.nextMessage(realPiResponseTimeout)
		if msg.Method == "session/update" {
			update := decodeSessionUpdate(t, msg)
			if update.SessionID == sessionID && update.Type == "message" && update.Delta != "" {
				sawMessageChunk = true
				promptText.WriteString(update.Delta)
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
			t.Fatalf("real Pi prompt expected stopReason=end_turn, got %q", result.StopReason)
		}
		gotPromptResp = true
	}
	if !sawMessageChunk {
		t.Fatalf("real Pi prompt expected at least one message chunk")
	}
	if !strings.Contains(strings.ToUpper(promptText.String()), "PONG") {
		t.Fatalf("real Pi prompt text=%q, want contains PONG", promptText.String())
	}

	h.sendRequest("4", "session/prompt", map[string]any{
		"sessionId": sessionID,
		"prompt":    "You must use the bash tool to run `sleep 15 && printf REAL_PI_CANCEL_DONE`. Do not answer before the command completes.",
	})

	gotPermission := false
	gotToolRunning := false
	for !(gotPermission && gotToolRunning) {
		msg := h.nextMessage(realPiResponseTimeout)
		switch msg.Method {
		case "session/request_permission":
			req := decodeSessionRequestPermission(t, msg)
			if req.SessionID != sessionID {
				t.Fatalf("real Pi cancel permission routed to wrong session: got=%q want=%q", req.SessionID, sessionID)
			}
			if req.Approval != "command" {
				t.Fatalf("real Pi cancel permission kind mismatch: got=%q want=command", req.Approval)
			}
			h.sendResultResponse(messageID(msg), map[string]any{"outcome": "approved"})
			gotPermission = true
		case "session/update":
			update := decodeSessionUpdate(t, msg)
			if update.Type == "tool_call_update" && update.Status == "in_progress" {
				gotToolRunning = true
			}
		}
	}

	h.sendRequest("5", "session/cancel", map[string]any{
		"sessionId": sessionID,
	})

	cancelDeadline := time.Now().Add(realPiCancelTimeout)
	gotCancelResp := false
	gotCancelledPromptResp := false
	for !(gotCancelResp && gotCancelledPromptResp) {
		if time.Now().After(cancelDeadline) {
			t.Fatalf("real Pi cancel did not finish in time")
		}
		msg := h.nextMessage(time.Until(cancelDeadline))
		switch messageID(msg) {
		case "5":
			var result struct {
				Cancelled bool `json:"cancelled"`
			}
			unmarshalResult(t, msg, &result)
			if !result.Cancelled {
				t.Fatalf("real Pi session/cancel expected cancelled=true")
			}
			gotCancelResp = true
		case "4":
			var result struct {
				StopReason string `json:"stopReason"`
			}
			unmarshalResult(t, msg, &result)
			if result.StopReason != "cancelled" {
				t.Fatalf("real Pi cancelled prompt expected stopReason=cancelled, got %q", result.StopReason)
			}
			gotCancelledPromptResp = true
		}
	}

	h.assertStdoutPureJSONRPC()
}

func TestRealPiSessionListLoadAndPrompt(t *testing.T) {
	requireRealPi(t)

	sessionDir := t.TempDir()
	loadCWD := repoRoot(t)

	h1 := startRealPiAdapter(t, sessionDir)
	h1.sendRequest("1", "initialize", map[string]any{})
	_ = h1.waitResponse("1", realPiResponseTimeout)

	sessionID, _ := newRealPiSession(t, h1, "2", loadCWD)

	h1.sendRequest("3", "session/prompt", map[string]any{
		"sessionId": sessionID,
		"prompt":    "Reply with exactly LOAD_SEED_OK and nothing else.",
	})
	for {
		msg := h1.nextMessage(realPiResponseTimeout)
		if messageID(msg) != "3" {
			continue
		}
		var result struct {
			StopReason string `json:"stopReason"`
		}
		unmarshalResult(t, msg, &result)
		if result.StopReason != "end_turn" {
			t.Fatalf("real Pi seed prompt expected stopReason=end_turn, got %q", result.StopReason)
		}
		break
	}
	h1.stop()

	h2 := startRealPiAdapter(t, sessionDir)
	h2.sendRequest("10", "initialize", map[string]any{})
	initResp := h2.waitResponse("10", realPiResponseTimeout)
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
		t.Fatalf("real Pi initialize should advertise loadSession=true")
	}
	if len(initResult.AgentCapabilities.SessionCapabilities.List) == 0 {
		t.Fatalf("real Pi initialize should advertise session/list capability")
	}

	type sessionListItem struct {
		SessionID string `json:"sessionId"`
		CWD       string `json:"cwd"`
	}
	type sessionListResult struct {
		Sessions []sessionListItem `json:"sessions"`
	}

	h2.sendRequest("11", "session/list", map[string]any{
		"cwd": loadCWD,
	})
	listResp := h2.waitResponse("11", realPiResponseTimeout)
	var listResult sessionListResult
	unmarshalResult(t, listResp, &listResult)
	if len(listResult.Sessions) == 0 {
		t.Fatalf("real Pi session/list should return at least one session")
	}
	targetSessionID := strings.TrimSpace(listResult.Sessions[0].SessionID)
	if targetSessionID == "" {
		t.Fatalf("real Pi session/list returned empty sessionId")
	}

	h2.sendRequest("12", "session/load", map[string]any{
		"sessionId": targetSessionID,
		"cwd":       loadCWD,
	})

	loadResultReceived := false
	sawHistory := false
	for !loadResultReceived {
		msg := h2.nextMessage(realPiResponseTimeout)
		if msg.Method == "session/update" {
			update := decodeSessionUpdate(t, msg)
			if update.SessionID != targetSessionID {
				t.Fatalf("real Pi session/load replay routed to wrong session: got %q want %q", update.SessionID, targetSessionID)
			}
			if update.Update != nil && update.Update.Content != nil && strings.Contains(update.Update.Content.Text, "LOAD_SEED_OK") {
				sawHistory = true
			}
			continue
		}
		if messageID(msg) != "12" {
			continue
		}
		loadResultReceived = true
	}
	if !sawHistory {
		t.Fatalf("real Pi session/load should replay prior history containing LOAD_SEED_OK")
	}

	h2.sendRequest("13", "session/prompt", map[string]any{
		"sessionId": targetSessionID,
		"prompt":    "Reply with exactly AFTER_LOAD_OK and nothing else.",
	})

	sawAfterLoadUpdate := false
	for {
		msg := h2.nextMessage(realPiResponseTimeout)
		if msg.Method == "session/update" {
			update := decodeSessionUpdate(t, msg)
			if update.SessionID == targetSessionID && update.Type == "message" && strings.TrimSpace(update.Delta) != "" {
				sawAfterLoadUpdate = true
			}
			continue
		}
		if messageID(msg) != "13" {
			continue
		}
		var result struct {
			StopReason string `json:"stopReason"`
		}
		unmarshalResult(t, msg, &result)
		if result.StopReason != "end_turn" {
			t.Fatalf("real Pi load prompt expected stopReason=end_turn, got %q", result.StopReason)
		}
		break
	}
	if !sawAfterLoadUpdate {
		t.Fatalf("real Pi loaded session prompt should emit at least one message chunk")
	}
}

func TestRealPiSessionConfigOptionsModelAndThoughtLevel(t *testing.T) {
	requireRealPi(t)

	h := startRealPiAdapter(t, t.TempDir())

	h.sendRequest("1", "initialize", map[string]any{})
	_ = h.waitResponse("1", realPiResponseTimeout)

	sessionID, configOptions := newRealPiSession(t, h, "2", repoRoot(t))

	modelOption := requireConfigOption(t, configOptions, "model")
	if strings.TrimSpace(modelOption.CurrentValue) == "" {
		t.Fatalf("real Pi model config should include currentValue")
	}

	thoughtOption := requireConfigOption(t, configOptions, "thought_level")
	if strings.TrimSpace(thoughtOption.CurrentValue) == "" {
		t.Fatalf("real Pi thought_level config should include currentValue")
	}

	setConfigOption := func(id string, configID string, target string) {
		t.Helper()

		h.sendRequest(id, "session/set_config_option", map[string]any{
			"sessionId": sessionID,
			"configId":  configID,
			"value":     target,
		})

		gotResponse := false
		sawUpdate := false
		for !gotResponse {
			msg := h.nextMessage(realPiResponseTimeout)
			if msg.Method == "session/update" {
				update := decodeSessionUpdate(t, msg)
				if update.SessionID != sessionID || update.Type != "config_options_update" {
					continue
				}
				for _, option := range update.ConfigOptions {
					if option.ID == configID && option.CurrentValue == target {
						sawUpdate = true
						break
					}
				}
				continue
			}
			if messageID(msg) != id {
				continue
			}
			var result struct {
				ConfigOptions []sessionConfigOption `json:"configOptions"`
			}
			unmarshalResult(t, msg, &result)
			applied := false
			for _, option := range result.ConfigOptions {
				if option.ID == configID && option.CurrentValue == target {
					applied = true
					break
				}
			}
			if !applied {
				t.Fatalf("real Pi session/set_config_option should apply %s=%q", configID, target)
			}
			gotResponse = true
		}
		if !sawUpdate {
			t.Fatalf("real Pi session/set_config_option should emit config_options_update for %s=%q", configID, target)
		}
	}

	setConfigOption("3", "model", configOptionTargetValue(modelOption))
	setConfigOption("4", "thought_level", configOptionTargetValue(thoughtOption))
}

func TestRealPiPermissionGateCommandAndWrite(t *testing.T) {
	requireRealPi(t)

	sessionDir := t.TempDir()
	workDir := t.TempDir()
	h := startRealPiAdapter(t, sessionDir)

	h.sendRequest("1", "initialize", map[string]any{})
	_ = h.waitResponse("1", realPiResponseTimeout)

	sessionID, _ := newRealPiSession(t, h, "2", workDir)

	h.sendRequest("3", "session/prompt", map[string]any{
		"sessionId": sessionID,
		"prompt":    "You must use the bash tool to run `printf REAL_PI_COMMAND_OK`. After it finishes, reply with exactly REAL_PI_COMMAND_OK.",
	})

	commandApproved := false
	commandCompleted := false
	commandPromptDone := false
	for !(commandApproved && commandCompleted && commandPromptDone) {
		msg := h.nextMessage(realPiResponseTimeout)
		switch msg.Method {
		case "session/request_permission":
			req := decodeSessionRequestPermission(t, msg)
			if req.Approval != "command" {
				continue
			}
			if !strings.Contains(req.Command, "REAL_PI_COMMAND_OK") {
				t.Fatalf("real Pi command permission mismatch: %q", req.Command)
			}
			h.sendResultResponse(messageID(msg), map[string]any{"outcome": "approved"})
			commandApproved = true
		case "session/update":
			update := decodeSessionUpdate(t, msg)
			if update.Type == "tool_call_update" && update.Status == "completed" && update.PermissionDecision == "approved" {
				commandCompleted = true
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
				t.Fatalf("real Pi command prompt expected stopReason=end_turn, got %q", result.StopReason)
			}
			commandPromptDone = true
		}
	}

	targetFile := filepath.Join(workDir, "real-pi-write.txt")
	h.sendRequest("4", "session/prompt", map[string]any{
		"sessionId": sessionID,
		"prompt":    "Use the write tool to create the file " + targetFile + " with exact contents REAL_PI_FILE_GATE. After writing it, reply with exactly FILE_DONE.",
	})

	fileApproved := false
	fileCompleted := false
	filePromptDone := false
	for !(fileApproved && fileCompleted && filePromptDone) {
		msg := h.nextMessage(realPiResponseTimeout)
		switch msg.Method {
		case "session/request_permission":
			req := decodeSessionRequestPermission(t, msg)
			if req.Approval != "file" {
				continue
			}
			if len(req.Files) == 0 || strings.TrimSpace(req.Files[0]) != targetFile {
				t.Fatalf("real Pi file permission files=%v, want %q", req.Files, targetFile)
			}
			h.sendResultResponse(messageID(msg), map[string]any{"outcome": "approved"})
			fileApproved = true
		case "session/update":
			update := decodeSessionUpdate(t, msg)
			if update.Type == "tool_call_update" && update.Status == "completed" && update.PermissionDecision == "approved" {
				fileCompleted = true
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
				t.Fatalf("real Pi file prompt expected stopReason=end_turn, got %q", result.StopReason)
			}
			filePromptDone = true
		}
	}
	data, err := os.ReadFile(targetFile)
	if err != nil {
		t.Fatalf("read real Pi written file: %v", err)
	}
	if strings.TrimSpace(string(data)) != "REAL_PI_FILE_GATE" {
		t.Fatalf("real Pi file contents=%q, want REAL_PI_FILE_GATE", string(data))
	}
}

func TestRealPiSlashCommandsAndLogout(t *testing.T) {
	requireRealPi(t)

	reviewRepo, commit := makeRealPiReviewRepo(t)
	h := startRealPiAdapter(t, t.TempDir())

	h.sendRequest("1", "initialize", map[string]any{})
	_ = h.waitResponse("1", realPiResponseTimeout)

	sessionID, _ := newRealPiSession(t, h, "2", reviewRepo)

	runPromptExpectingEndTurn := func(id string, prompt string, deadline time.Duration) []sessionUpdateParams {
		t.Helper()

		h.sendRequest(id, "session/prompt", map[string]any{
			"sessionId": sessionID,
			"prompt":    prompt,
		})

		updates := make([]sessionUpdateParams, 0, 16)
		for {
			msg := h.nextMessage(deadline)
			switch msg.Method {
			case "session/request_permission":
				_ = approveRealPiPermission(t, h, msg, sessionID)
				continue
			case "session/update":
				updates = append(updates, decodeSessionUpdate(t, msg))
				continue
			}
			if messageID(msg) != id {
				continue
			}
			var result struct {
				StopReason string `json:"stopReason"`
			}
			unmarshalResult(t, msg, &result)
			if result.StopReason != "end_turn" {
				t.Fatalf("real Pi prompt %q expected stopReason=end_turn, got %q", prompt, result.StopReason)
			}
			return updates
		}
	}

	assertReviewLifecycle := func(updates []sessionUpdateParams, label string) {
		t.Helper()
		sawEnter := false
		sawExit := false
		for _, update := range updates {
			if update.Status == "review_mode_entered" {
				sawEnter = true
			}
			if update.Status == "review_mode_exited" {
				sawExit = true
			}
		}
		if !sawEnter || !sawExit {
			t.Fatalf("%s missing review lifecycle: entered=%v exited=%v", label, sawEnter, sawExit)
		}
	}

	t.Log("real Pi: running /review")
	assertReviewLifecycle(
		runPromptExpectingEndTurn("3", "/review keep it short and focus on one concrete issue if any.", 2*realPiResponseTimeout),
		"/review",
	)
	t.Log("real Pi: running /review-branch master")
	assertReviewLifecycle(
		runPromptExpectingEndTurn("4", "/review-branch master", 2*realPiResponseTimeout),
		"/review-branch",
	)
	t.Logf("real Pi: running /review-commit %s", commit)
	assertReviewLifecycle(
		runPromptExpectingEndTurn("5", "/review-commit "+commit, 2*realPiResponseTimeout),
		"/review-commit",
	)

	t.Log("real Pi: running /init")
	initUpdates := runPromptExpectingEndTurn("6", "/init keep it under 20 words.", realPiResponseTimeout)
	sawInitMessage := false
	for _, update := range initUpdates {
		if update.Type == "message" && strings.TrimSpace(update.Delta) != "" {
			sawInitMessage = true
			break
		}
	}
	if !sawInitMessage {
		t.Fatalf("/init should emit at least one message chunk")
	}

	t.Log("real Pi: running /compact")
	compactUpdates := runPromptExpectingEndTurn("7", "/compact", realPiResponseTimeout)
	sawCompactMessage := false
	for _, update := range compactUpdates {
		if update.Type == "message" && strings.TrimSpace(update.Delta) != "" {
			sawCompactMessage = true
			break
		}
	}
	if !sawCompactMessage {
		t.Fatalf("/compact should emit at least one message chunk")
	}

	t.Log("real Pi: running /logout")
	h.sendRequest("8", "session/prompt", map[string]any{
		"sessionId": sessionID,
		"prompt":    "/logout",
	})

	gotLogoutResp := false
	sawLoggedOutCatalog := false
	for !gotLogoutResp {
		msg := h.nextMessage(realPiResponseTimeout)
		if msg.Method == "session/update" {
			update := decodeSessionUpdate(t, msg)
			if update.SessionID == sessionID && update.Type == "available_commands_update" {
				got := availableCommandNames(update.AvailableCommands)
				if len(got) == 1 && got[0] == "logout" {
					sawLoggedOutCatalog = true
				}
			}
			continue
		}
		if messageID(msg) != "8" {
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
	if !sawLoggedOutCatalog {
		t.Fatalf("real Pi logout should publish reduced available command catalog")
	}

	h.sendRequest("9", "authenticate", map[string]any{
		"methodId": "pi",
	})
	authResp := h.waitResponse("9", realPiResponseTimeout)
	var authResult struct {
		Authenticated bool `json:"authenticated"`
	}
	unmarshalResult(t, authResp, &authResult)
	if !authResult.Authenticated {
		t.Fatalf("real Pi authenticate should succeed")
	}

	sawRestoredCatalog := false
	for !sawRestoredCatalog {
		msg := h.nextMessage(realPiResponseTimeout)
		if msg.Method != "session/update" {
			continue
		}
		update := decodeSessionUpdate(t, msg)
		if update.SessionID != sessionID || update.Type != "available_commands_update" {
			continue
		}
		want := []string{"review", "review-branch", "review-commit", "init", "compact", "logout"}
		if reflect.DeepEqual(availableCommandNames(update.AvailableCommands), want) {
			sawRestoredCatalog = true
		}
	}

	postAuthUpdates := runPromptExpectingEndTurn("10", "Reply with exactly REAUTH_OK and nothing else.", realPiResponseTimeout)
	sawReauthMessage := false
	for _, update := range postAuthUpdates {
		if update.Type == "message" && strings.TrimSpace(update.Delta) != "" {
			sawReauthMessage = true
			break
		}
	}
	if !sawReauthMessage {
		t.Fatalf("real Pi prompt after authenticate should emit at least one message chunk")
	}
}
