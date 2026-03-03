package acpadapter

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

type testRPCMessage struct {
	ID     *json.RawMessage `json:"id,omitempty"`
	Method string           `json:"method,omitempty"`
	Params json.RawMessage  `json:"params,omitempty"`
	Result json.RawMessage  `json:"result,omitempty"`
	Error  *testRPCError    `json:"error,omitempty"`
}

type testRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type testSessionUpdateParams struct {
	Delta string `json:"delta,omitempty"`
}

type testSessionNewResult struct {
	SessionID string `json:"sessionId"`
}

type testSessionPromptResult struct {
	StopReason string `json:"stopReason"`
}

func TestRunStdio_ProfileMappingWithFakeAppServer(t *testing.T) {
	t.Parallel()

	rootDir := repoRoot(t)
	fakeServerBin := buildBinary(t, rootDir, "./testdata/fake_codex_app_server")

	stdinReader, stdinWriter := io.Pipe()
	stdoutReader, stdoutWriter := io.Pipe()
	defer func() {
		_ = stdinWriter.Close()
		_ = stdoutWriter.Close()
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runDone := make(chan error, 1)
	go func() {
		runDone <- RunStdio(ctx, RuntimeConfig{
			AppServerCommand: fakeServerBin,
			AppServerArgs:    nil,
			LogLevel:         "debug",
			PatchApplyMode:   "appserver",
			RetryTurnOnCrash: true,
			Profiles: map[string]ProfileConfig{
				"safe": {
					Model:              "gpt-safe",
					ApprovalPolicy:     "on-request",
					Sandbox:            "read-only",
					Personality:        "cautious",
					SystemInstructions: "safe-first",
				},
			},
			DefaultProfile:  "safe",
			InitialAuthMode: "chatgpt_subscription",
		}, stdinReader, stdoutWriter, io.Discard)
	}()

	msgCh := make(chan testRPCMessage, 64)
	readErrCh := make(chan error, 1)
	go scanRPCLines(stdoutReader, msgCh, readErrCh)

	writeRPCRequest(t, stdinWriter, "1", "initialize", map[string]any{
		"protocolVersion": 1,
		"clientInfo": map[string]any{
			"name":    "pkg-test",
			"version": "1.0.0",
		},
	})
	initMsg := readRPCMessage(t, msgCh, readErrCh, 3*time.Second)
	if initMsg.Error != nil {
		t.Fatalf("initialize failed: code=%d message=%s", initMsg.Error.Code, initMsg.Error.Message)
	}

	writeRPCRequest(t, stdinWriter, "2", "session/new", map[string]any{"cwd": rootDir})
	newMsg := readRPCMessage(t, msgCh, readErrCh, 3*time.Second)
	if newMsg.Error != nil {
		t.Fatalf("session/new failed: code=%d message=%s", newMsg.Error.Code, newMsg.Error.Message)
	}
	var newResult testSessionNewResult
	if err := json.Unmarshal(newMsg.Result, &newResult); err != nil {
		t.Fatalf("decode session/new result: %v", err)
	}
	if strings.TrimSpace(newResult.SessionID) == "" {
		t.Fatalf("session/new returned empty sessionId")
	}

	writeRPCRequest(t, stdinWriter, "3", "session/prompt", map[string]any{
		"sessionId": newResult.SessionID,
		"prompt":    "profile probe",
	})

	profileLine := ""
	for {
		msg := readRPCMessage(t, msgCh, readErrCh, 5*time.Second)
		if msg.Method == "session/update" {
			var params testSessionUpdateParams
			if err := json.Unmarshal(msg.Params, &params); err != nil {
				t.Fatalf("decode session/update params: %v", err)
			}
			if strings.Contains(params.Delta, "profile model=") {
				profileLine = params.Delta
			}
			continue
		}

		if messageID(msg.ID) != "3" {
			continue
		}
		if msg.Error != nil {
			t.Fatalf("session/prompt failed: code=%d message=%s", msg.Error.Code, msg.Error.Message)
		}

		var promptResult testSessionPromptResult
		if err := json.Unmarshal(msg.Result, &promptResult); err != nil {
			t.Fatalf("decode session/prompt result: %v", err)
		}
		if promptResult.StopReason != "end_turn" {
			t.Fatalf("session/prompt stopReason=%q, want end_turn", promptResult.StopReason)
		}
		break
	}

	if !strings.Contains(profileLine, "profile model=gpt-safe") {
		t.Fatalf("profile model mapping missing: %q", profileLine)
	}
	if !strings.Contains(profileLine, "approval=on-request") {
		t.Fatalf("profile approval mapping missing: %q", profileLine)
	}
	if !strings.Contains(profileLine, "sandbox=read-only") {
		t.Fatalf("profile sandbox mapping missing: %q", profileLine)
	}
	if !strings.Contains(profileLine, "personality=cautious") {
		t.Fatalf("profile personality mapping missing: %q", profileLine)
	}
	if !strings.Contains(profileLine, "system=safe-first") {
		t.Fatalf("profile system instructions mapping missing: %q", profileLine)
	}

	_ = stdinWriter.Close()
	cancel()

	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("RunStdio returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("RunStdio did not stop in time")
	}
}

func scanRPCLines(reader io.Reader, msgCh chan<- testRPCMessage, errCh chan<- error) {
	defer close(msgCh)
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 1024), 2*1024*1024)

	for scanner.Scan() {
		var msg testRPCMessage
		if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
			errCh <- fmt.Errorf("decode rpc line: %w", err)
			return
		}
		msgCh <- msg
	}
	if err := scanner.Err(); err != nil {
		errCh <- err
	}
}

func readRPCMessage(
	t *testing.T,
	msgCh <-chan testRPCMessage,
	errCh <-chan error,
	timeout time.Duration,
) testRPCMessage {
	t.Helper()

	select {
	case err := <-errCh:
		t.Fatalf("rpc reader error: %v", err)
	case msg, ok := <-msgCh:
		if !ok {
			t.Fatalf("rpc reader closed")
		}
		return msg
	case <-time.After(timeout):
		t.Fatalf("timed out waiting for rpc message")
	}
	return testRPCMessage{}
}

func writeRPCRequest(t *testing.T, writer io.Writer, id, method string, params any) {
	t.Helper()

	payload := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal %s request: %v", method, err)
	}
	if _, err := writer.Write(append(data, '\n')); err != nil {
		t.Fatalf("write %s request: %v", method, err)
	}
}

func messageID(raw *json.RawMessage) string {
	if raw == nil {
		return ""
	}
	var value string
	if err := json.Unmarshal(*raw, &value); err == nil {
		return value
	}

	var number float64
	if err := json.Unmarshal(*raw, &number); err == nil {
		return fmt.Sprintf("%.0f", number)
	}
	return strings.TrimSpace(string(*raw))
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

	output := filepath.Join(t.TempDir(), strings.ReplaceAll(strings.TrimPrefix(pkg, "./"), "/", "-"))
	cmd := exec.Command("go", "build", "-o", output, pkg)
	cmd.Dir = rootDir
	if data, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build %s failed: %v\n%s", pkg, err, string(data))
	}
	return output
}
