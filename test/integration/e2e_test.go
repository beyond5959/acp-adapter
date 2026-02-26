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

func TestE2EInitializeNewPromptAndCancel(t *testing.T) {
	rootDir := repoRoot(t)
	fakeServerBin := buildBinary(t, rootDir, "./testdata/fake_codex_app_server")
	adapterBin := buildBinary(t, rootDir, "./cmd/codex-acp-go")

	cmd := exec.Command(adapterBin)
	cmd.Dir = rootDir
	cmd.Env = append(os.Environ(),
		"CODEX_APP_SERVER_CMD="+fakeServerBin,
		"CODEX_APP_SERVER_ARGS=",
		"LOG_LEVEL=debug",
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
	t.Cleanup(func() {
		_ = stdin.Close()
		waitCh := make(chan error, 1)
		go func() { waitCh <- cmd.Wait() }()
		select {
		case <-time.After(3 * time.Second):
			_ = cmd.Process.Kill()
			<-waitCh
		case <-waitCh:
		}
	})
	go func() { _, _ = io.Copy(io.Discard, stderr) }()

	reader := newRPCReader(t, stdout)

	writeRequest(t, stdin, "1", "initialize", map[string]any{})
	initResp := waitForResponse(t, reader, "1", 3*time.Second, nil)
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

	writeRequest(t, stdin, "2", "session/new", map[string]any{})
	newResp := waitForResponse(t, reader, "2", 3*time.Second, nil)
	var newResult struct {
		SessionID string `json:"sessionId"`
	}
	unmarshalResult(t, newResp, &newResult)
	if newResult.SessionID == "" {
		t.Fatalf("session/new returned empty sessionId")
	}

	writeRequest(t, stdin, "3", "session/prompt", map[string]any{
		"sessionId": newResult.SessionID,
		"prompt":    "hello",
	})

	gotUpdate := false
	gotPromptResp := false
	for !gotPromptResp {
		msg := reader.next(4 * time.Second)
		if msg.Method == "session/update" {
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
			t.Fatalf("session/prompt expected stopReason=end_turn, got %q", promptResult.StopReason)
		}
		gotPromptResp = true
	}
	if !gotUpdate {
		t.Fatalf("session/prompt expected at least one session/update")
	}

	writeRequest(t, stdin, "4", "session/prompt", map[string]any{
		"sessionId": newResult.SessionID,
		"prompt":    "slow task",
	})

	sawSlowUpdate := false
	for !sawSlowUpdate {
		msg := reader.next(4 * time.Second)
		if msg.Method == "session/update" {
			sawSlowUpdate = true
			break
		}
		if messageID(msg) == "4" {
			var earlyResult struct {
				StopReason string `json:"stopReason"`
			}
			unmarshalResult(t, msg, &earlyResult)
			t.Fatalf("slow prompt ended before cancel, stopReason=%q", earlyResult.StopReason)
		}
	}

	writeRequest(t, stdin, "5", "session/cancel", map[string]any{
		"sessionId": newResult.SessionID,
	})

	gotCancelResp := false
	gotCancelledPromptResp := false
	for !(gotCancelResp && gotCancelledPromptResp) {
		msg := reader.next(5 * time.Second)
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
