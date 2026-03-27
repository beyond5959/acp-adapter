package integration

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
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
	SessionID          string                    `json:"sessionId"`
	TurnID             string                    `json:"turnId"`
	Type               string                    `json:"type"`
	Phase              string                    `json:"phase,omitempty"`
	ItemID             string                    `json:"itemId,omitempty"`
	ItemType           string                    `json:"itemType,omitempty"`
	Delta              string                    `json:"delta,omitempty"`
	Status             string                    `json:"status,omitempty"`
	Message            string                    `json:"message,omitempty"`
	ToolCallID         string                    `json:"toolCallId,omitempty"`
	Approval           string                    `json:"approval,omitempty"`
	PermissionDecision string                    `json:"permissionDecision,omitempty"`
	Todo               []todoItem                `json:"todo,omitempty"`
	Plan               []planEntry               `json:"plan,omitempty"`
	ConfigOptions      []sessionConfigOption     `json:"configOptions,omitempty"`
	AvailableCommands  []sessionAvailableCommand `json:"availableCommands,omitempty"`
	Update             *sessionUpdatePayload     `json:"update,omitempty"`
}

type todoItem struct {
	Text string `json:"text"`
	Done bool   `json:"done"`
}

type planEntry struct {
	Content  string `json:"content"`
	Priority string `json:"priority"`
	Status   string `json:"status"`
}

type sessionUpdatePayload struct {
	SessionUpdate     string                    `json:"sessionUpdate"`
	Entries           []planEntry               `json:"entries,omitempty"`
	Content           *sessionUpdateContent     `json:"-"`
	ToolContents      []sessionToolCallContent  `json:"-"`
	ConfigOptions     []sessionConfigOption     `json:"configOptions,omitempty"`
	AvailableCommands []sessionAvailableCommand `json:"availableCommands,omitempty"`
	Status            string                    `json:"status,omitempty"`
	Title             string                    `json:"title,omitempty"`
	ToolCallID        string                    `json:"toolCallId,omitempty"`
}

type sessionUpdateContent struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	Data     string `json:"data,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
	URI      string `json:"uri,omitempty"`
}

type sessionToolCallContent struct {
	Type    string                `json:"type"`
	Content *sessionUpdateContent `json:"content,omitempty"`
	Path    string                `json:"path,omitempty"`
	OldText string                `json:"oldText,omitempty"`
	NewText string                `json:"newText,omitempty"`
}

func (p *sessionUpdatePayload) UnmarshalJSON(data []byte) error {
	type rawPayload struct {
		SessionUpdate     string                    `json:"sessionUpdate"`
		Entries           []planEntry               `json:"entries,omitempty"`
		Content           json.RawMessage           `json:"content,omitempty"`
		ConfigOptions     []sessionConfigOption     `json:"configOptions,omitempty"`
		AvailableCommands []sessionAvailableCommand `json:"availableCommands,omitempty"`
		Status            string                    `json:"status,omitempty"`
		Title             string                    `json:"title,omitempty"`
		ToolCallID        string                    `json:"toolCallId,omitempty"`
	}

	var raw rawPayload
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	p.SessionUpdate = raw.SessionUpdate
	p.Entries = raw.Entries
	p.ConfigOptions = raw.ConfigOptions
	p.AvailableCommands = raw.AvailableCommands
	p.Status = raw.Status
	p.Title = raw.Title
	p.ToolCallID = raw.ToolCallID

	if len(raw.Content) == 0 {
		return nil
	}

	if raw.SessionUpdate == "tool_call_update" {
		var toolContents []sessionToolCallContent
		if err := json.Unmarshal(raw.Content, &toolContents); err == nil {
			p.ToolContents = toolContents
			for _, item := range toolContents {
				if item.Content != nil && item.Content.Type == "text" {
					contentCopy := *item.Content
					p.Content = &contentCopy
					break
				}
			}
			return nil
		}
	}

	var content sessionUpdateContent
	if err := json.Unmarshal(raw.Content, &content); err != nil {
		return err
	}
	p.Content = &content
	return nil
}

type sessionAvailableCommand struct {
	Name        string                        `json:"name"`
	Description string                        `json:"description,omitempty"`
	Input       *sessionAvailableCommandInput `json:"input,omitempty"`
}

type sessionAvailableCommandInput struct {
	Hint string `json:"hint,omitempty"`
}

func availableCommandNames(commands []sessionAvailableCommand) []string {
	names := make([]string, 0, len(commands))
	for _, command := range commands {
		names = append(names, command.Name)
	}
	return names
}

func hasAvailableCommand(commands []sessionAvailableCommand, want string) bool {
	for _, command := range commands {
		if command.Name == want {
			return true
		}
	}
	return false
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

func TestE2EAvailableCommandsPublishedAndRefreshedAfterLogout(t *testing.T) {
	h := startAdapter(t)

	h.sendRequest("1", "initialize", map[string]any{})
	_ = h.waitResponse("1", responseTimeout)

	h.sendRequest("2", "session/new", map[string]any{})
	newResp := h.waitResponse("2", responseTimeout)
	var newResult struct {
		SessionID string `json:"sessionId"`
	}
	unmarshalResult(t, newResp, &newResult)

	var initialUpdate sessionUpdateParams
	for {
		msg := h.nextMessage(responseTimeout)
		if msg.Method != "session/update" {
			continue
		}
		update := decodeSessionUpdate(t, msg)
		if update.SessionID != newResult.SessionID || update.Type != "available_commands_update" {
			continue
		}
		initialUpdate = update
		break
	}

	if initialUpdate.Update == nil || initialUpdate.Update.SessionUpdate != "available_commands_update" {
		t.Fatalf("initial available commands update envelope missing: %+v", initialUpdate)
	}
	wantInitial := []string{"review", "review-branch", "review-commit", "init", "compact", "logout", "mcp"}
	if got := availableCommandNames(initialUpdate.AvailableCommands); !reflect.DeepEqual(got, wantInitial) {
		t.Fatalf("initial availableCommands=%v, want %v", got, wantInitial)
	}
	if got := availableCommandNames(initialUpdate.Update.AvailableCommands); !reflect.DeepEqual(got, wantInitial) {
		t.Fatalf("nested initial availableCommands=%v, want %v", got, wantInitial)
	}

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
			if update.SessionID == newResult.SessionID && update.Type == "available_commands_update" {
				got := availableCommandNames(update.AvailableCommands)
				if len(got) == 1 && got[0] == "logout" {
					sawLoggedOutCatalog = true
				}
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
	if !sawLoggedOutCatalog {
		t.Fatalf("expected logout to publish a reduced available command catalog")
	}

	h.sendRequest("4", "authenticate", map[string]any{
		"methodId": "chatgpt_subscription",
	})
	authResp := h.waitResponse("4", responseTimeout)
	var authResult struct {
		Authenticated bool `json:"authenticated"`
	}
	unmarshalResult(t, authResp, &authResult)
	if !authResult.Authenticated {
		t.Fatalf("authenticate should succeed")
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
		got := availableCommandNames(update.AvailableCommands)
		if reflect.DeepEqual(got, wantInitial) {
			sawRestoredCatalog = true
		}
	}
}

func TestE2ESessionListMappedFromThreadList(t *testing.T) {
	h := startAdapter(t)

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
		t.Fatalf("initialize should advertise loadSession=true")
	}
	if len(initResult.AgentCapabilities.SessionCapabilities.List) == 0 {
		t.Fatalf("initialize should advertise session/list capability")
	}

	h.sendRequest("2", "session/new", map[string]any{
		"cwd": "/workspace/session-list",
	})
	newResp := h.waitResponse("2", responseTimeout)
	var newResult struct {
		SessionID string `json:"sessionId"`
	}
	unmarshalResult(t, newResp, &newResult)
	if newResult.SessionID == "" {
		t.Fatalf("session/new returned empty sessionId")
	}

	type sessionListItem struct {
		SessionID string         `json:"sessionId"`
		CWD       string         `json:"cwd"`
		Title     string         `json:"title"`
		UpdatedAt string         `json:"updatedAt"`
		Meta      map[string]any `json:"_meta"`
	}
	type sessionListResult struct {
		Sessions   []sessionListItem `json:"sessions"`
		NextCursor string            `json:"nextCursor"`
	}

	h.sendRequest("3", "session/list", map[string]any{
		"cwd": "/workspace/session-list",
	})
	listRespOne := h.waitResponse("3", responseTimeout)
	var pageOne sessionListResult
	unmarshalResult(t, listRespOne, &pageOne)
	if len(pageOne.Sessions) != 2 {
		t.Fatalf("session/list first page len=%d, want 2", len(pageOne.Sessions))
	}
	if pageOne.NextCursor == "" {
		t.Fatalf("session/list first page should include nextCursor")
	}

	foundCurrentSession := false
	for _, item := range pageOne.Sessions {
		if item.CWD != "/workspace/session-list" {
			t.Fatalf("session/list cwd filter mismatch: got %q", item.CWD)
		}
		if item.Title == "" {
			t.Fatalf("session/list title should not be empty: %+v", item)
		}
		if _, err := time.Parse(time.RFC3339, item.UpdatedAt); err != nil {
			t.Fatalf("session/list updatedAt should be RFC3339: %q (%v)", item.UpdatedAt, err)
		}
		if strings.TrimSpace(fmt.Sprint(item.Meta["threadId"])) == "" {
			t.Fatalf("session/list _meta.threadId should be present: %+v", item.Meta)
		}
		if item.SessionID == newResult.SessionID {
			foundCurrentSession = true
		}
	}
	if !foundCurrentSession {
		t.Fatalf("session/list should reuse current sessionId %q on first page", newResult.SessionID)
	}

	h.sendRequest("4", "session/list", map[string]any{
		"cwd":    "/workspace/session-list",
		"cursor": pageOne.NextCursor,
	})
	listRespTwo := h.waitResponse("4", responseTimeout)
	var pageTwo sessionListResult
	unmarshalResult(t, listRespTwo, &pageTwo)
	if len(pageTwo.Sessions) != 1 {
		t.Fatalf("session/list second page len=%d, want 1", len(pageTwo.Sessions))
	}
	if pageTwo.NextCursor == "" {
		t.Fatalf("session/list second page should continue into archived history")
	}

	h.sendRequest("5", "session/list", map[string]any{
		"cwd":    "/workspace/session-list",
		"cursor": pageTwo.NextCursor,
	})
	listRespThree := h.waitResponse("5", responseTimeout)
	var pageThree sessionListResult
	unmarshalResult(t, listRespThree, &pageThree)
	if len(pageThree.Sessions) != 1 {
		t.Fatalf("session/list archived page len=%d, want 1", len(pageThree.Sessions))
	}
	if pageThree.NextCursor != "" {
		t.Fatalf("session/list archived final page should not include nextCursor, got %q", pageThree.NextCursor)
	}
	if got := pageThree.Sessions[0].Title; got != "Archived Session" {
		t.Fatalf("session/list archived title=%q, want %q", got, "Archived Session")
	}
	archivedFlag, ok := pageThree.Sessions[0].Meta["archived"].(bool)
	if !ok || !archivedFlag {
		t.Fatalf("session/list archived page should mark _meta.archived=true: %+v", pageThree.Sessions[0].Meta)
	}
}

func TestE2ESessionLoadReplaysHistoryAndAllowsPrompt(t *testing.T) {
	h := startAdapter(t)

	h.sendRequest("1", "initialize", map[string]any{})
	_ = h.waitResponse("1", responseTimeout)

	type sessionListItem struct {
		SessionID string `json:"sessionId"`
		Title     string `json:"title"`
	}
	type sessionListResult struct {
		Sessions []sessionListItem `json:"sessions"`
	}

	h.sendRequest("2", "session/list", map[string]any{
		"cwd": "/workspace/session-list",
	})
	listResp := h.waitResponse("2", responseTimeout)
	var listResult sessionListResult
	unmarshalResult(t, listResp, &listResult)

	targetSessionID := ""
	for _, item := range listResult.Sessions {
		if item.Title == "Seed Active Session" {
			targetSessionID = item.SessionID
			break
		}
	}
	if targetSessionID == "" {
		t.Fatalf("seed session not found in session/list result: %+v", listResult.Sessions)
	}

	h.sendRequest("3", "session/load", map[string]any{
		"sessionId": targetSessionID,
		"cwd":       "/workspace/session-list",
	})

	var (
		sawUserHistory      bool
		sawAgentHistory     bool
		sawLoadedTodo       bool
		loadResultReceived  bool
		loadConfigOptionSet []sessionConfigOption
	)
	for !loadResultReceived {
		msg := h.reader.next(responseTimeout)
		if msg.Method == "session/update" {
			update := decodeSessionUpdate(t, msg)
			if update.SessionID != targetSessionID {
				t.Fatalf("session/load replay routed to wrong session: got %q want %q", update.SessionID, targetSessionID)
			}
			if update.Update != nil && update.Update.Content != nil {
				switch update.Update.SessionUpdate {
				case "user_message_chunk":
					if strings.Contains(update.Update.Content.Text, "Explain the adapter architecture.") {
						sawUserHistory = true
					}
				case "agent_message_chunk":
					if strings.Contains(update.Update.Content.Text, "bridges ACP stdio to Codex app-server") {
						sawAgentHistory = true
					}
				}
			}
			if len(update.Todo) > 0 {
				sawLoadedTodo = true
			}
			continue
		}
		if messageID(msg) != "3" {
			continue
		}

		var loadResult struct {
			ConfigOptions []sessionConfigOption `json:"configOptions"`
		}
		unmarshalResult(t, msg, &loadResult)
		loadConfigOptionSet = loadResult.ConfigOptions
		loadResultReceived = true
	}

	if !sawUserHistory {
		t.Fatalf("session/load should replay at least one user_message_chunk")
	}
	if !sawAgentHistory {
		t.Fatalf("session/load should replay at least one agent_message_chunk")
	}
	if !sawLoadedTodo {
		t.Fatalf("session/load should replay todo-bearing assistant history")
	}
	if len(loadConfigOptionSet) == 0 {
		t.Fatalf("session/load should return configOptions")
	}

	h.sendRequest("4", "session/prompt", map[string]any{
		"sessionId": targetSessionID,
		"prompt":    "profile probe",
	})

	var (
		sawProfileProbe bool
		gotPromptResp   bool
	)
	for !gotPromptResp {
		msg := h.reader.next(responseTimeout)
		if msg.Method == "session/update" {
			update := decodeSessionUpdate(t, msg)
			if update.Update != nil && update.Update.Content != nil &&
				strings.Contains(update.Update.Content.Text, "profile model=gpt-4.1 thought=low") {
				sawProfileProbe = true
			}
			continue
		}
		if messageID(msg) != "4" {
			continue
		}

		var promptResult struct {
			StopReason string `json:"stopReason"`
		}
		unmarshalResult(t, msg, &promptResult)
		if promptResult.StopReason != "end_turn" {
			t.Fatalf("loaded session prompt stopReason=%q, want end_turn", promptResult.StopReason)
		}
		gotPromptResp = true
	}
	if !sawProfileProbe {
		t.Fatalf("loaded session prompt should reuse resumed model/thought_level config")
	}
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

func TestE2ETurnCompletedFailedErrorDetailsSurfaced(t *testing.T) {
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
		"prompt":    "turn completed error detail",
	})

	gotPromptResp := false
	turnErrorMessage := ""
	deadline := time.Now().Add(responseTimeout)
	for !gotPromptResp {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for failed turn completion")
		}
		msg := h.nextMessage(time.Until(deadline))
		if msg.Method == "session/update" {
			update := decodeSessionUpdate(t, msg)
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
			t.Fatalf("failed turn expected stopReason=error, got %q", result.StopReason)
		}
		gotPromptResp = true
	}

	if !strings.Contains(turnErrorMessage, "apply_patch verification failed") {
		t.Fatalf("expected turn_error to include apply_patch verification failure, got %q", turnErrorMessage)
	}
	if !strings.Contains(turnErrorMessage, "internal/httpapi/httpapi.go") {
		t.Fatalf("expected turn_error to include target file path, got %q", turnErrorMessage)
	}

	h.assertStdoutPureJSONRPC()
}

func TestE2EErrorNotificationRetryingSurfaced(t *testing.T) {
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
		"prompt":    "error notification retry",
	})

	gotPromptResp := false
	retryingMessage := ""
	sawTurnError := false
	deadline := time.Now().Add(responseTimeout)
	for !gotPromptResp {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for retrying error notification flow")
		}
		msg := h.nextMessage(time.Until(deadline))
		if msg.Method == "session/update" {
			update := decodeSessionUpdate(t, msg)
			if update.Status == "backend_error_retrying" {
				retryingMessage = update.Message
			}
			if update.Status == "turn_error" {
				sawTurnError = true
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
			t.Fatalf("retrying error flow expected stopReason=end_turn, got %q", result.StopReason)
		}
		gotPromptResp = true
	}

	if !strings.Contains(retryingMessage, "temporary upstream connection drop") {
		t.Fatalf("expected backend_error_retrying message, got %q", retryingMessage)
	}
	if sawTurnError {
		t.Fatalf("retrying error notification should not terminate the turn")
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
				if update.Type == "available_commands_update" {
					continue
				}
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

func TestE2EAcceptanceC2ImageResourceLinkLocalFile(t *testing.T) {
	h := startAdapter(t)

	h.sendRequest("1", "initialize", map[string]any{})
	_ = h.waitResponse("1", responseTimeout)

	h.sendRequest("2", "session/new", map[string]any{})
	newResp := h.waitResponse("2", responseTimeout)
	var newResult struct {
		SessionID string `json:"sessionId"`
	}
	unmarshalResult(t, newResp, &newResult)

	imagePath := filepath.Join(t.TempDir(), "ngent-image-991527206.png")
	imageURI := "file://" + filepath.ToSlash(imagePath)
	h.sendRequest("3", "session/prompt", map[string]any{
		"sessionId": newResult.SessionID,
		"content": []map[string]any{
			{
				"type": "text",
				"text": "describe the image briefly",
			},
			{
				"type":     "resource_link",
				"name":     filepath.Base(imagePath),
				"mimeType": "image/png",
				"uri":      imageURI,
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
			t.Fatalf("image resource_link prompt expected stopReason=end_turn, got %q", result.StopReason)
		}
		gotPromptResp = true
	}

	if !sawImageMessage {
		t.Fatalf("image resource_link prompt expected image-aware streaming output")
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

func TestE2EACPPlanUpdateMappedFromTurnPlanUpdated(t *testing.T) {
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
		"prompt":    "generate structured plan for this task",
	})

	gotPromptResp := false
	var plans [][]planEntry
	for !gotPromptResp {
		msg := h.nextMessage(responseTimeout)
		if msg.Method == "session/update" {
			update := decodeSessionUpdate(t, msg)
			if update.Type != "plan" {
				continue
			}
			if update.Update == nil {
				t.Fatalf("plan update must include standard params.update envelope")
			}
			if update.Update.SessionUpdate != "plan" {
				t.Fatalf("plan update envelope kind=%q, want plan", update.Update.SessionUpdate)
			}
			if len(update.Update.Entries) == 0 {
				t.Fatalf("plan update must include entries")
			}
			if len(update.Plan) != len(update.Update.Entries) {
				t.Fatalf("top-level plan and standard entries length mismatch: %d vs %d", len(update.Plan), len(update.Update.Entries))
			}
			plans = append(plans, append([]planEntry(nil), update.Update.Entries...))
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
			t.Fatalf("plan prompt expected stopReason=end_turn, got %q", result.StopReason)
		}
		gotPromptResp = true
	}

	if len(plans) < 2 {
		t.Fatalf("expected >=2 plan updates, got %d", len(plans))
	}
	first := plans[0]
	last := plans[len(plans)-1]
	if len(first) != 2 {
		t.Fatalf("first plan should contain 2 entries, got %+v", first)
	}
	if first[0].Content != "capture requirements" || first[0].Priority != "medium" || first[0].Status != "pending" {
		t.Fatalf("first plan entry mismatch: %+v", first[0])
	}
	if len(last) != 3 {
		t.Fatalf("last plan should fully replace entries with 3 items, got %+v", last)
	}
	if last[0].Status != "completed" {
		t.Fatalf("expected first last-plan entry completed, got %+v", last[0])
	}
	if last[1].Status != "in_progress" {
		t.Fatalf("expected second last-plan entry in_progress, got %+v", last[1])
	}
	if last[2].Content != "run go test ./..." || last[2].Status != "pending" {
		t.Fatalf("expected final plan entry to be pending test step, got %+v", last[2])
	}
}

func TestE2EACPPlanUpdateMappedFromPlanDeltaFallback(t *testing.T) {
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
		"prompt":    "generate delta plan fallback for this task",
	})

	gotPromptResp := false
	var plans [][]planEntry
	for !gotPromptResp {
		msg := h.nextMessage(responseTimeout)
		if msg.Method == "session/update" {
			update := decodeSessionUpdate(t, msg)
			if update.Type != "plan" || update.Update == nil || update.Update.SessionUpdate != "plan" {
				continue
			}
			plans = append(plans, append([]planEntry(nil), update.Update.Entries...))
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
			t.Fatalf("delta plan prompt expected stopReason=end_turn, got %q", result.StopReason)
		}
		gotPromptResp = true
	}

	if len(plans) < 4 {
		t.Fatalf("expected streamed fallback plan updates from item/plan/delta, got %d", len(plans))
	}
	first := plans[0]
	if len(first) != 1 || first[0].Content != "capture" {
		t.Fatalf("first fallback plan update should expose delta content, got %+v", first)
	}
	last := plans[len(plans)-1]
	if len(last) != 2 {
		t.Fatalf("final fallback plan should contain 2 entries, got %+v", last)
	}
	if last[0].Content != "capture requirements" || last[0].Status != "pending" || last[0].Priority != "medium" {
		t.Fatalf("first final fallback plan entry mismatch: %+v", last[0])
	}
	if last[1].Content != "implement mapping" || last[1].Status != "pending" || last[1].Priority != "medium" {
		t.Fatalf("second final fallback plan entry mismatch: %+v", last[1])
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

func TestE2ECommandExecutionItemsMappedToToolCallUpdates(t *testing.T) {
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
		"prompt":    "command execution mapping",
	})

	gotPromptResp := false
	var started sessionUpdateParams
	var completed sessionUpdateParams
	sawCommandStatus := false
	for !gotPromptResp || completed.ToolCallID == "" {
		msg := h.nextMessage(responseTimeout)
		switch msg.Method {
		case "session/update":
			update := decodeSessionUpdate(t, msg)
			if update.SessionID != newResult.SessionID {
				continue
			}
			if update.Type == "status" && update.ItemType == "commandExecution" {
				sawCommandStatus = true
			}
			if update.Type != "tool_call_update" {
				continue
			}
			if update.Approval != "command" {
				t.Fatalf("command execution tool call must be classified as command, got %q", update.Approval)
			}
			if update.ToolCallID == "" {
				t.Fatalf("command execution tool call update missing toolCallId")
			}
			if update.Update == nil || update.Update.SessionUpdate != "tool_call_update" {
				t.Fatalf("command execution tool call must populate standard update.sessionUpdate envelope")
			}
			switch update.Status {
			case "in_progress":
				if update.Update.Content == nil || !strings.Contains(update.Update.Content.Text, "pwd") {
					t.Fatalf("in_progress command tool_call_update should expose command content, got %+v", update.Update.Content)
				}
				started = update
			case "completed":
				if update.Update.Content == nil || !strings.Contains(update.Update.Content.Text, repoRoot(t)) {
					t.Fatalf("completed command tool_call_update should expose aggregated output content, got %+v", update.Update.Content)
				}
				completed = update
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
				t.Fatalf("command execution prompt expected stopReason=end_turn, got %q", result.StopReason)
			}
			gotPromptResp = true
		}
	}

	if started.ToolCallID == "" {
		t.Fatalf("expected in_progress tool_call_update for commandExecution item")
	}
	if completed.ToolCallID == "" {
		t.Fatalf("expected terminal tool_call_update for commandExecution item")
	}
	if started.ToolCallID != completed.ToolCallID {
		t.Fatalf("tool call id mismatch: started=%q completed=%q", started.ToolCallID, completed.ToolCallID)
	}
	if !strings.Contains(started.Message, "pwd") {
		t.Fatalf("expected tool_call_update title/message to include command, got %q", started.Message)
	}
	if sawCommandStatus {
		t.Fatalf("commandExecution items should map to tool_call_update instead of generic status updates")
	}
}

func TestE2ECommandExecutionOutputDeltaMappedToToolCallContent(t *testing.T) {
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
		"prompt":    "command execution streaming mapping",
	})

	gotPromptResp := false
	sawLine1 := false
	sawLine2 := false
	sawCompletedOutput := false
	var toolCallID string
	for !gotPromptResp || !sawCompletedOutput {
		msg := h.nextMessage(responseTimeout)
		switch msg.Method {
		case "session/update":
			update := decodeSessionUpdate(t, msg)
			if update.SessionID != newResult.SessionID || update.Type != "tool_call_update" || update.Approval != "command" {
				continue
			}
			if update.ToolCallID == "" {
				t.Fatalf("streaming command tool_call_update missing toolCallId")
			}
			if toolCallID == "" {
				toolCallID = update.ToolCallID
			} else if toolCallID != update.ToolCallID {
				t.Fatalf("streaming command tool_call_update changed toolCallId: got=%q want=%q", update.ToolCallID, toolCallID)
			}
			if update.Update == nil || update.Update.Content == nil {
				continue
			}
			text := update.Update.Content.Text
			if update.Status == "in_progress" && strings.Contains(text, "line1\n") {
				sawLine1 = true
			}
			if update.Status == "in_progress" && strings.Contains(text, "line2\n") {
				sawLine2 = true
			}
			if update.Status == "completed" && strings.Contains(text, "line1\nline2\n") {
				sawCompletedOutput = true
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
				t.Fatalf("command execution streaming prompt expected stopReason=end_turn, got %q", result.StopReason)
			}
			gotPromptResp = true
		}
	}

	if !sawLine1 || !sawLine2 {
		t.Fatalf("expected streamed command output deltas in tool_call_update content, sawLine1=%v sawLine2=%v", sawLine1, sawLine2)
	}
}

func TestE2EToolImageItemsMappedToACPImageBlock(t *testing.T) {
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
		"prompt":    "tool image mapping",
	})

	gotPromptResp := false
	sawToolStarted := false
	sawToolText := false
	sawToolImage := false
	sawGenericStatus := false
	for !gotPromptResp || !sawToolImage {
		msg := h.nextMessage(responseTimeout)
		switch msg.Method {
		case "session/update":
			update := decodeSessionUpdate(t, msg)
			if update.SessionID != newResult.SessionID {
				continue
			}
			if update.Type == "status" && update.ItemType == "dynamicToolCall" {
				sawGenericStatus = true
			}
			if update.Type != "tool_call_update" {
				continue
			}
			if update.Status == "in_progress" && strings.Contains(update.Message, "render_image") {
				sawToolStarted = true
			}
			if update.Update == nil {
				continue
			}
			for _, item := range update.Update.ToolContents {
				if item.Content == nil {
					continue
				}
				if item.Content.Type == "text" && strings.Contains(item.Content.Text, "tool image ready") {
					sawToolText = true
				}
				if item.Content.Type == "image" && item.Content.MimeType == "image/png" && strings.TrimSpace(item.Content.Data) != "" {
					sawToolImage = true
				}
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
				t.Fatalf("tool image prompt expected stopReason=end_turn, got %q", result.StopReason)
			}
			gotPromptResp = true
		}
	}

	if !sawToolStarted {
		t.Fatalf("tool image flow expected in_progress tool_call_update")
	}
	if !sawToolText {
		t.Fatalf("tool image flow expected text tool content")
	}
	if !sawToolImage {
		t.Fatalf("tool image flow expected ACP image block in tool_call_update content")
	}
	if sawGenericStatus {
		t.Fatalf("dynamic tool items should map to tool_call_update instead of generic status updates")
	}
}

func TestE2ETurnDiffUpdatedMappedToToolCallDiffs(t *testing.T) {
	h := startAdapter(t)

	h.sendRequest("1", "initialize", map[string]any{
		"capabilities": map[string]any{
			"fs": map[string]any{
				"read_text_file": true,
			},
		},
	})
	_ = h.waitResponse("1", responseTimeout)

	h.sendRequest("2", "session/new", map[string]any{
		"cwd": "/workspace",
	})
	newResp := h.waitResponse("2", responseTimeout)
	var newResult struct {
		SessionID string `json:"sessionId"`
	}
	unmarshalResult(t, newResp, &newResult)

	h.sendRequest("3", "session/prompt", map[string]any{
		"sessionId": newResult.SessionID,
		"prompt":    "turn diff mapping",
	})

	gotPromptResp := false
	sawFSRead := false
	sawInProgress := false
	sawCompleted := false
	var toolCallID string
	for !gotPromptResp || !sawCompleted {
		msg := h.nextMessage(responseTimeout)
		switch msg.Method {
		case "fs/read_text_file":
			req := decodeFSReadTextFileParams(t, msg)
			if got, want := filepath.Clean(req.Path), "/workspace/docs/README.md"; got != want {
				t.Fatalf("turn diff fs/read_text_file path=%q, want %q", got, want)
			}
			sawFSRead = true
			h.sendResultResponse(messageID(msg), map[string]any{
				"text": "old line\n",
			})
		case "session/update":
			update := decodeSessionUpdate(t, msg)
			if update.SessionID != newResult.SessionID || update.Type != "tool_call_update" || update.ItemType != "turn_diff" {
				continue
			}
			if update.ToolCallID == "" {
				t.Fatalf("turn diff tool_call_update missing toolCallId")
			}
			if toolCallID == "" {
				toolCallID = update.ToolCallID
			} else if toolCallID != update.ToolCallID {
				t.Fatalf("turn diff toolCallId changed: got=%q want=%q", update.ToolCallID, toolCallID)
			}
			if update.Approval != "file" {
				t.Fatalf("turn diff tool call should be classified as file, got %q", update.Approval)
			}
			if update.Update == nil {
				t.Fatalf("turn diff tool call missing standard update envelope")
			}
			foundDiff := false
			for _, item := range update.Update.ToolContents {
				if item.Type != "diff" {
					continue
				}
				foundDiff = true
				if item.Path != "/workspace/docs/README.md" {
					t.Fatalf("turn diff path=%q, want /workspace/docs/README.md", item.Path)
				}
				if item.OldText != "old line\n" {
					t.Fatalf("turn diff oldText mismatch: %q", item.OldText)
				}
				if item.NewText != "new line\n" {
					t.Fatalf("turn diff newText mismatch: %q", item.NewText)
				}
			}
			if !foundDiff {
				t.Fatalf(
					"turn diff tool call expected at least one diff content item, got tool contents=%+v delta=%q",
					update.Update.ToolContents,
					update.Delta,
				)
			}
			switch update.Status {
			case "in_progress":
				sawInProgress = true
			case "completed":
				sawCompleted = true
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
				t.Fatalf("turn diff prompt expected stopReason=end_turn, got %q", result.StopReason)
			}
			gotPromptResp = true
		}
	}

	if !sawFSRead {
		t.Fatalf("turn diff mapping expected fs/read_text_file call")
	}
	if !sawInProgress || !sawCompleted {
		t.Fatalf("turn diff mapping expected in_progress and completed tool_call_update")
	}
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
		"prompt":    `/mcp call demo-mcp render-image {"topic":"adapter"}`,
	})
	gotImageCallResp := false
	sawImageToolCompleted := false
	sawImageToolOutput := false
	for !gotImageCallResp || !sawImageToolOutput {
		msg := h.nextMessage(responseTimeout)
		switch msg.Method {
		case "session/request_permission":
			req := decodeSessionRequestPermission(t, msg)
			if req.Approval != "mcp" {
				t.Fatalf("/mcp call image expected mcp approval, got %q", req.Approval)
			}
			h.sendResultResponse(messageID(msg), map[string]any{"outcome": "approved"})
		case "session/update":
			update := decodeSessionUpdate(t, msg)
			if update.Type != "tool_call_update" || update.Status != "completed" || update.Update == nil {
				continue
			}
			sawImageToolCompleted = true
			for _, item := range update.Update.ToolContents {
				if item.Content == nil {
					continue
				}
				if item.Content.Type == "image" && item.Content.MimeType == "image/png" && strings.TrimSpace(item.Content.Data) != "" {
					sawImageToolOutput = true
				}
			}
		default:
			if messageID(msg) != "5" {
				continue
			}
			var result struct {
				StopReason string `json:"stopReason"`
			}
			unmarshalResult(t, msg, &result)
			if result.StopReason != "end_turn" {
				t.Fatalf("/mcp call image expected stopReason=end_turn, got %q", result.StopReason)
			}
			gotImageCallResp = true
		}
	}
	if !sawImageToolCompleted || !sawImageToolOutput {
		t.Fatalf("/mcp call image expected completed tool update with ACP image block")
	}

	h.sendRequest("6", "session/prompt", map[string]any{
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
		if messageID(msg) != "6" {
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

func TestE2ERealCodexCommandExecutionMappedToToolCalls(t *testing.T) {
	requireRealCodex(t)

	traceFile := filepath.Join(t.TempDir(), "real-command-trace.jsonl")
	h := startAdapterReal(t, []string{"--trace-json", "--trace-json-file", traceFile})
	sessionID := realCodexInitializeAndSession(t, h, nil)

	h.sendRequest("real-command-prompt", "session/prompt", map[string]any{
		"sessionId": sessionID,
		"prompt":    "这是什么项目？",
	})

	sawCommandStarted := false
	sawCommandTerminal := false
	sawCommandStatusFallback := false
	sawCommandContent := false
	gotPromptResp := false
	cancelSent := false
	gotCancelResp := false
	deadline := time.Now().Add(90 * time.Second)

	for time.Now().Before(deadline) && !(gotPromptResp && (!cancelSent || gotCancelResp) && sawCommandTerminal) {
		msg := h.nextMessage(time.Until(deadline))
		switch msg.Method {
		case "session/update":
			update := decodeSessionUpdate(t, msg)
			if update.SessionID != sessionID {
				continue
			}
			if update.Type == "status" && update.ItemType == "commandExecution" {
				sawCommandStatusFallback = true
			}
			if update.Type != "tool_call_update" || update.Approval != "command" {
				continue
			}
			if update.ToolCallID == "" {
				t.Fatalf("real command tool_call_update missing toolCallId")
			}
			if update.Update != nil && update.Update.Content != nil && strings.TrimSpace(update.Update.Content.Text) != "" {
				sawCommandContent = true
			}
			switch update.Status {
			case "in_progress":
				sawCommandStarted = true
			case "completed", "failed":
				sawCommandTerminal = true
				if !cancelSent {
					h.sendRequest("real-command-cancel", "session/cancel", map[string]any{"sessionId": sessionID})
					cancelSent = true
				}
			}
		case "session/request_permission":
			h.sendResultResponse(messageID(msg), map[string]any{"outcome": "declined"})
		default:
			switch messageID(msg) {
			case "real-command-cancel":
				var result struct {
					Cancelled bool `json:"cancelled"`
				}
				unmarshalResult(t, msg, &result)
				gotCancelResp = true
			case "real-command-prompt":
				var result struct {
					StopReason string `json:"stopReason"`
				}
				unmarshalResult(t, msg, &result)
				if result.StopReason != "end_turn" && result.StopReason != "cancelled" {
					t.Fatalf("real command prompt expected stopReason end_turn/cancelled, got %q", result.StopReason)
				}
				gotPromptResp = true
			}
		}
	}

	if !sawCommandStarted || !sawCommandTerminal {
		t.Fatalf(
			"expected real codex prompt to emit command tool_call_update lifecycle, started=%v terminal=%v",
			sawCommandStarted,
			sawCommandTerminal,
		)
	}
	if !sawCommandContent {
		t.Fatalf("expected real codex command tool_call_update to include standard update.content text")
	}
	if sawCommandStatusFallback {
		t.Fatalf("real commandExecution items should not fall back to generic status updates once mapped to tool_call_update")
	}

	traceEntries := readTraceEntries(t, traceFile)
	commandItemIDs := make(map[string]struct{})
	toolCallIDs := make(map[string]struct{})
	sawAggregatedOutput := false
	for _, entry := range traceEntries {
		method, _ := entry.Payload["method"].(string)
		switch entry.Stream {
		case "appserver":
			if method != "item/completed" {
				continue
			}
			params, _ := entry.Payload["params"].(map[string]any)
			item, _ := params["item"].(map[string]any)
			if itemType, _ := item["type"].(string); itemType != "commandExecution" {
				continue
			}
			if itemID, _ := item["id"].(string); itemID != "" {
				commandItemIDs[itemID] = struct{}{}
			}
			if _, ok := item["aggregatedOutput"]; ok {
				sawAggregatedOutput = true
			}
		case "acp":
			if method != "session/update" {
				continue
			}
			params, _ := entry.Payload["params"].(map[string]any)
			if updateType, _ := params["type"].(string); updateType != "tool_call_update" {
				continue
			}
			if approval, _ := params["approval"].(string); approval != "command" {
				continue
			}
			if toolCallID, _ := params["toolCallId"].(string); toolCallID != "" {
				toolCallIDs[toolCallID] = struct{}{}
			}
		}
	}

	if !sawAggregatedOutput {
		t.Fatalf("real trace should include commandExecution aggregatedOutput in app-server item/completed payloads")
	}

	matched := false
	for itemID := range commandItemIDs {
		if _, ok := toolCallIDs[itemID]; ok {
			matched = true
			break
		}
	}
	if !matched {
		t.Fatalf("expected at least one commandExecution item id from app-server trace to be mapped to ACP toolCallId")
	}
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

	seen := make(map[string]bool, len(methods))
	for _, entry := range readTraceEntries(t, traceFile) {
		if entry.Stream != "appserver" {
			continue
		}
		method, _ := entry.Payload["method"].(string)
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

type traceLogEntry struct {
	Stream  string         `json:"stream"`
	Payload map[string]any `json:"payload"`
}

func readTraceEntries(t *testing.T, traceFile string) []traceLogEntry {
	t.Helper()

	data, err := os.ReadFile(traceFile)
	if err != nil {
		t.Fatalf("read trace file: %v", err)
	}
	text := strings.TrimSpace(string(data))
	if text == "" {
		t.Fatalf("trace file is empty")
	}

	lines := strings.Split(text, "\n")
	entries := make([]traceLogEntry, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var entry traceLogEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		entries = append(entries, entry)
	}
	return entries
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
