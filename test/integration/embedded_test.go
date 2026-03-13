package integration

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/beyond5959/acp-adapter/pkg/codexacp"
)

func TestEmbeddedInitializeNewPromptCancel(t *testing.T) {
	t.Parallel()

	rootDir := repoRoot(t)
	fakeServerBin := buildBinary(t, rootDir, "./testdata/fake_codex_app_server")

	runtime := codexacp.NewEmbeddedRuntime(codexacp.RuntimeConfig{
		AppServerCommand: fakeServerBin,
		AppServerArgs:    nil,
		LogLevel:         "debug",
		PatchApplyMode:   "appserver",
		RetryTurnOnCrash: true,
		InitialAuthMode:  "chatgpt_subscription",
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := runtime.Start(ctx); err != nil {
		t.Fatalf("start embedded runtime: %v", err)
	}
	defer func() {
		_ = runtime.Close()
	}()

	updates, unsubscribe := runtime.SubscribeUpdates(256)
	defer unsubscribe()

	initResp := embeddedRequest(t, ctx, runtime, "1", "initialize", map[string]any{
		"protocolVersion": 1,
	})
	if initResp.Error != nil {
		t.Fatalf("initialize error: %+v", initResp.Error)
	}

	newResp := embeddedRequest(t, ctx, runtime, "2", "session/new", map[string]any{
		"cwd": rootDir,
	})
	if newResp.Error != nil {
		t.Fatalf("session/new error: %+v", newResp.Error)
	}
	var newResult struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(newResp.Result, &newResult); err != nil {
		t.Fatalf("decode session/new result: %v", err)
	}
	if strings.TrimSpace(newResult.SessionID) == "" {
		t.Fatalf("session/new returned empty sessionId")
	}

	promptDone := make(chan embeddedCallResult, 1)
	go func() {
		resp, err := runtime.ClientRequest(ctx, embeddedRPCRequest("3", "session/prompt", map[string]any{
			"sessionId": newResult.SessionID,
			"prompt":    "slow cancel please",
		}))
		promptDone <- embeddedCallResult{response: resp, err: err}
	}()

	if _, err := waitEmbeddedUpdate(updates, 4*time.Second, func(msg codexacp.RPCMessage) bool {
		if msg.Method != "session/update" {
			return false
		}
		var params struct {
			TurnID string `json:"turnId"`
		}
		if err := json.Unmarshal(msg.Params, &params); err != nil {
			return false
		}
		return strings.TrimSpace(params.TurnID) != ""
	}); err != nil {
		t.Fatalf("wait first session/update: %v", err)
	}

	cancelResp := embeddedRequest(t, ctx, runtime, "4", "session/cancel", map[string]any{
		"sessionId": newResult.SessionID,
	})
	if cancelResp.Error != nil {
		t.Fatalf("session/cancel error: %+v", cancelResp.Error)
	}
	var cancelResult struct {
		Cancelled bool `json:"cancelled"`
	}
	if err := json.Unmarshal(cancelResp.Result, &cancelResult); err != nil {
		t.Fatalf("decode session/cancel result: %v", err)
	}
	if !cancelResult.Cancelled {
		t.Fatalf("session/cancel expected cancelled=true")
	}

	select {
	case result := <-promptDone:
		if result.err != nil {
			t.Fatalf("session/prompt request failed: %v", result.err)
		}
		if result.response.Error != nil {
			t.Fatalf("session/prompt error: %+v", result.response.Error)
		}
		var promptResult struct {
			StopReason string `json:"stopReason"`
		}
		if err := json.Unmarshal(result.response.Result, &promptResult); err != nil {
			t.Fatalf("decode session/prompt result: %v", err)
		}
		if promptResult.StopReason != "cancelled" {
			t.Fatalf("session/prompt stopReason=%q, want cancelled", promptResult.StopReason)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting session/prompt result")
	}
}

func TestEmbeddedPermissionRoundTrip(t *testing.T) {
	t.Parallel()

	rootDir := repoRoot(t)
	fakeServerBin := buildBinary(t, rootDir, "./testdata/fake_codex_app_server")

	runtime := codexacp.NewEmbeddedRuntime(codexacp.RuntimeConfig{
		AppServerCommand: fakeServerBin,
		LogLevel:         "debug",
		PatchApplyMode:   "appserver",
		RetryTurnOnCrash: true,
		InitialAuthMode:  "chatgpt_subscription",
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := runtime.Start(ctx); err != nil {
		t.Fatalf("start embedded runtime: %v", err)
	}
	defer func() {
		_ = runtime.Close()
	}()

	updates, unsubscribe := runtime.SubscribeUpdates(256)
	defer unsubscribe()

	initResp := embeddedRequest(t, ctx, runtime, "1", "initialize", map[string]any{
		"protocolVersion": 1,
	})
	if initResp.Error != nil {
		t.Fatalf("initialize error: %+v", initResp.Error)
	}

	newResp := embeddedRequest(t, ctx, runtime, "2", "session/new", map[string]any{
		"cwd": rootDir,
	})
	if newResp.Error != nil {
		t.Fatalf("session/new error: %+v", newResp.Error)
	}
	var newResult struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(newResp.Result, &newResult); err != nil {
		t.Fatalf("decode session/new result: %v", err)
	}

	promptDone := make(chan embeddedCallResult, 1)
	go func() {
		resp, err := runtime.ClientRequest(ctx, embeddedRPCRequest("3", "session/prompt", map[string]any{
			"sessionId": newResult.SessionID,
			"prompt":    "approval command",
		}))
		promptDone <- embeddedCallResult{response: resp, err: err}
	}()

	permissionMsg, err := waitEmbeddedUpdate(updates, 5*time.Second, func(msg codexacp.RPCMessage) bool {
		return msg.Method == "session/request_permission" && msg.ID != nil
	})
	if err != nil {
		t.Fatalf("wait session/request_permission: %v", err)
	}

	var permissionParams struct {
		Approval string `json:"approval"`
	}
	if err := json.Unmarshal(permissionMsg.Params, &permissionParams); err != nil {
		t.Fatalf("decode permission params: %v", err)
	}
	if permissionParams.Approval != "command" {
		t.Fatalf("permission approval=%q, want command", permissionParams.Approval)
	}

	if err := runtime.RespondPermission(
		ctx,
		*permissionMsg.ID,
		codexacp.PermissionDecision{Outcome: "approved"},
	); err != nil {
		t.Fatalf("respond permission: %v", err)
	}

	select {
	case result := <-promptDone:
		if result.err != nil {
			t.Fatalf("session/prompt request failed: %v", result.err)
		}
		if result.response.Error != nil {
			t.Fatalf("session/prompt error: %+v", result.response.Error)
		}
		var promptResult struct {
			StopReason string `json:"stopReason"`
		}
		if err := json.Unmarshal(result.response.Result, &promptResult); err != nil {
			t.Fatalf("decode session/prompt result: %v", err)
		}
		if promptResult.StopReason != "end_turn" {
			t.Fatalf("session/prompt stopReason=%q, want end_turn", promptResult.StopReason)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting session/prompt result")
	}
}

type embeddedCallResult struct {
	response codexacp.RPCMessage
	err      error
}

func embeddedRequest(
	t *testing.T,
	ctx context.Context,
	runtime *codexacp.EmbeddedRuntime,
	id string,
	method string,
	params any,
) codexacp.RPCMessage {
	t.Helper()
	response, err := runtime.ClientRequest(ctx, embeddedRPCRequest(id, method, params))
	if err != nil {
		t.Fatalf("%s request failed: %v", method, err)
	}
	return response
}

func embeddedRPCRequest(id string, method string, params any) codexacp.RPCMessage {
	requestID := json.RawMessage(strconvQuote(id))
	message := codexacp.RPCMessage{
		JSONRPC: "2.0",
		ID:      &requestID,
		Method:  method,
	}
	if params != nil {
		raw, _ := json.Marshal(params)
		message.Params = raw
	}
	return message
}

func waitEmbeddedUpdate(
	updates <-chan codexacp.RPCMessage,
	timeout time.Duration,
	predicate func(codexacp.RPCMessage) bool,
) (codexacp.RPCMessage, error) {
	deadline := time.After(timeout)
	for {
		select {
		case msg, ok := <-updates:
			if !ok {
				return codexacp.RPCMessage{}, errors.New("updates channel closed")
			}
			if predicate(msg) {
				return msg, nil
			}
		case <-deadline:
			return codexacp.RPCMessage{}, errors.New("timed out waiting embedded update")
		}
	}
}

func strconvQuote(value string) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}
