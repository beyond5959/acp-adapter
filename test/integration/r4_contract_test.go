package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/beyond5959/acp-adapter/pkg/codexacp"
)

type contractPromptScenario struct {
	Name               string
	Prompt             string
	ExpectStopReason   string
	ExpectMessageChunk bool
	ExpectPermission   bool
	PermissionOutcome  string
	CancelAfterUpdate  bool
}

type contractTranscript struct {
	Initialize contractInitializeSnapshot
	Prompts    []contractPromptOutcome
}

type contractInitializeSnapshot struct {
	ProtocolVersion int

	HasLegacySessions      bool
	HasLegacyImages        bool
	HasLegacyToolCalls     bool
	HasLegacySlashCommands bool
	HasLegacyPermissions   bool

	LegacySessions      bool
	LegacyImages        bool
	LegacyToolCalls     bool
	LegacySlashCommands bool
	LegacyPermissions   bool

	HasPromptImage           bool
	HasPromptEmbeddedContext bool
	PromptImage              bool
	PromptEmbeddedContext    bool

	HasMCPHTTP bool
	HasMCPSSE  bool
	MCPHTTP    bool
	MCPSSE     bool

	HasSessionList   bool
	HasSessionFork   bool
	HasSessionResume bool
}

type contractPromptOutcome struct {
	Name               string
	StopReason         string
	SessionID          string
	TurnID             string
	SawMessageChunk    bool
	CancelAcknowledged bool

	LifecycleSequence  []string
	PermissionRequests []string
	ToolTransitions    []string
}

type embeddedPromptCallResult struct {
	response codexacp.RPCMessage
	err      error
}

func TestR4ContractStandaloneEqualsEmbedded(t *testing.T) {
	scenarios := []contractPromptScenario{
		{
			Name:               "prompt-streaming",
			Prompt:             "hello contract stream",
			ExpectStopReason:   "end_turn",
			ExpectMessageChunk: true,
		},
		{
			Name:              "prompt-cancel",
			Prompt:            "slow contract cancel",
			ExpectStopReason:  "cancelled",
			CancelAfterUpdate: true,
		},
		{
			Name:               "permission-approve",
			Prompt:             "approval command",
			ExpectStopReason:   "end_turn",
			ExpectMessageChunk: true,
			ExpectPermission:   true,
			PermissionOutcome:  "approved",
		},
		{
			Name:               "permission-decline",
			Prompt:             "approval command",
			ExpectStopReason:   "end_turn",
			ExpectMessageChunk: true,
			ExpectPermission:   true,
			PermissionOutcome:  "declined",
		},
	}

	standalone := runContractScriptStandalone(t, scenarios)
	embedded := runContractScriptEmbedded(t, scenarios)

	assertInitializeCompleteness(t, "standalone", standalone.Initialize)
	assertInitializeCompleteness(t, "embedded", embedded.Initialize)

	if !reflect.DeepEqual(standalone.Initialize, embedded.Initialize) {
		t.Fatalf("initialize contract mismatch standalone=%+v embedded=%+v", standalone.Initialize, embedded.Initialize)
	}

	if len(standalone.Prompts) != len(embedded.Prompts) {
		t.Fatalf(
			"prompt outcome length mismatch standalone=%d embedded=%d",
			len(standalone.Prompts),
			len(embedded.Prompts),
		)
	}

	for i := range scenarios {
		assertPromptExpectations(t, "standalone", scenarios[i], standalone.Prompts[i])
		assertPromptExpectations(t, "embedded", scenarios[i], embedded.Prompts[i])
		assertPromptContractEquivalent(t, scenarios[i].Name, standalone.Prompts[i], embedded.Prompts[i])
	}
}

func TestR4EmbeddedInvariants_NoDeadlock_NoCrossSessionCrosstalk(t *testing.T) {
	rootDir := repoRoot(t)
	fakeServerBin := buildBinary(t, rootDir, "./testdata/fake_codex_app_server")

	runtime := codexacp.NewEmbeddedRuntime(codexacp.RuntimeConfig{
		AppServerCommand: fakeServerBin,
		LogLevel:         "debug",
		PatchApplyMode:   "appserver",
		RetryTurnOnCrash: true,
		InitialAuthMode:  "chatgpt_subscription",
	})

	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	if err := runtime.Start(ctx); err != nil {
		t.Fatalf("start embedded runtime: %v", err)
	}
	defer func() {
		_ = runtime.Close()
	}()

	updates, unsubscribe := runtime.SubscribeUpdates(512)
	defer unsubscribe()

	initResp := embeddedRequest(t, ctx, runtime, "inv-init", "initialize", map[string]any{
		"protocolVersion": 1,
	})
	embeddedEnsureNoRPCError(t, "initialize", initResp)

	sessionA := embeddedNewSession(t, ctx, runtime, "inv-new-a", rootDir)
	sessionB := embeddedNewSession(t, ctx, runtime, "inv-new-b", rootDir)

	promptACh := make(chan embeddedPromptCallResult, 1)
	promptBCh := make(chan embeddedPromptCallResult, 1)
	go func() {
		resp, err := runtime.ClientRequest(ctx, embeddedRPCRequest("inv-prompt-a", "session/prompt", map[string]any{
			"sessionId": sessionA,
			"prompt":    "hello from session A",
		}))
		promptACh <- embeddedPromptCallResult{response: resp, err: err}
	}()
	go func() {
		resp, err := runtime.ClientRequest(ctx, embeddedRPCRequest("inv-prompt-b", "session/prompt", map[string]any{
			"sessionId": sessionB,
			"prompt":    "hello from session B",
		}))
		promptBCh <- embeddedPromptCallResult{response: resp, err: err}
	}()

	sessionSet := map[string]bool{
		sessionA: true,
		sessionB: true,
	}
	sessionUpdates := map[string]int{
		sessionA: 0,
		sessionB: 0,
	}
	turnToSession := make(map[string]string)

	gotPromptA := false
	gotPromptB := false
	deadline := time.After(2 * responseTimeout)

	for !(gotPromptA && gotPromptB) {
		select {
		case result := <-promptACh:
			if result.err != nil {
				t.Fatalf("session A prompt failed: %v", result.err)
			}
			stopReason := embeddedDecodeStopReason(t, "session A prompt", result.response)
			if stopReason != "end_turn" {
				t.Fatalf("session A stopReason=%q, want end_turn", stopReason)
			}
			gotPromptA = true
		case result := <-promptBCh:
			if result.err != nil {
				t.Fatalf("session B prompt failed: %v", result.err)
			}
			stopReason := embeddedDecodeStopReason(t, "session B prompt", result.response)
			if stopReason != "end_turn" {
				t.Fatalf("session B stopReason=%q, want end_turn", stopReason)
			}
			gotPromptB = true
		case msg, ok := <-updates:
			if !ok {
				t.Fatalf("embedded updates channel closed unexpectedly")
			}
			if msg.Method != "session/update" {
				continue
			}
			update := embeddedDecodeSessionUpdate(t, msg)
			if !sessionSet[update.SessionID] {
				t.Fatalf("unexpected session/update sessionId=%q", update.SessionID)
			}
			sessionUpdates[update.SessionID]++
			if strings.TrimSpace(update.TurnID) != "" {
				if owner, exists := turnToSession[update.TurnID]; exists && owner != update.SessionID {
					t.Fatalf(
						"turn %q crossed sessions: first=%q then=%q",
						update.TurnID,
						owner,
						update.SessionID,
					)
				}
				turnToSession[update.TurnID] = update.SessionID
			}
		case <-deadline:
			t.Fatalf(
				"embedded invariant timeout: promptA=%v promptB=%v sessionUpdates=%v",
				gotPromptA,
				gotPromptB,
				sessionUpdates,
			)
		}
	}

	if sessionUpdates[sessionA] == 0 || sessionUpdates[sessionB] == 0 {
		t.Fatalf("both sessions must receive updates, got=%v", sessionUpdates)
	}
}

func runContractScriptStandalone(
	t *testing.T,
	scenarios []contractPromptScenario,
) contractTranscript {
	t.Helper()

	h := startAdapter(t)
	initResp := contractInitializeStandalone(t, h)
	sessionID := contractSessionNewStandalone(t, h)

	out := contractTranscript{
		Initialize: initResp,
		Prompts:    make([]contractPromptOutcome, 0, len(scenarios)),
	}
	for idx, scenario := range scenarios {
		promptID := fmt.Sprintf("contract-standalone-prompt-%d", idx+1)
		out.Prompts = append(out.Prompts, runContractPromptStandalone(t, h, sessionID, promptID, scenario))
	}

	h.assertStdoutPureJSONRPC()
	return out
}

func runContractScriptEmbedded(
	t *testing.T,
	scenarios []contractPromptScenario,
) contractTranscript {
	t.Helper()

	rootDir := repoRoot(t)
	fakeServerBin := buildBinary(t, rootDir, "./testdata/fake_codex_app_server")

	runtime := codexacp.NewEmbeddedRuntime(codexacp.RuntimeConfig{
		AppServerCommand: fakeServerBin,
		LogLevel:         "debug",
		PatchApplyMode:   "appserver",
		RetryTurnOnCrash: true,
		InitialAuthMode:  "chatgpt_subscription",
	})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := runtime.Start(ctx); err != nil {
		t.Fatalf("start embedded runtime: %v", err)
	}
	defer func() {
		_ = runtime.Close()
	}()

	updates, unsubscribe := runtime.SubscribeUpdates(512)
	defer unsubscribe()

	initResp := contractInitializeEmbedded(t, ctx, runtime)
	sessionID := embeddedNewSession(t, ctx, runtime, "contract-embedded-new", rootDir)

	out := contractTranscript{
		Initialize: initResp,
		Prompts:    make([]contractPromptOutcome, 0, len(scenarios)),
	}
	for idx, scenario := range scenarios {
		promptID := fmt.Sprintf("contract-embedded-prompt-%d", idx+1)
		out.Prompts = append(out.Prompts, runContractPromptEmbedded(t, ctx, runtime, updates, sessionID, promptID, scenario))
	}

	return out
}

func contractInitializeStandalone(t *testing.T, h *adapterHarness) contractInitializeSnapshot {
	t.Helper()
	h.sendRequest("contract-init", "initialize", map[string]any{
		"protocolVersion": 1,
	})
	initResp := h.waitResponse("contract-init", responseTimeout)
	return decodeInitializeSnapshot(t, "standalone initialize", initResp.Result, initResp.Error)
}

func contractInitializeEmbedded(
	t *testing.T,
	ctx context.Context,
	runtime *codexacp.EmbeddedRuntime,
) contractInitializeSnapshot {
	t.Helper()
	initResp := embeddedRequest(t, ctx, runtime, "contract-init", "initialize", map[string]any{
		"protocolVersion": 1,
	})
	return decodeInitializeSnapshot(t, "embedded initialize", initResp.Result, initResp.Error)
}

func contractSessionNewStandalone(t *testing.T, h *adapterHarness) string {
	t.Helper()
	h.sendRequest("contract-new", "session/new", map[string]any{
		"cwd": repoRoot(t),
	})
	newResp := h.waitResponse("contract-new", responseTimeout)
	var result struct {
		SessionID string `json:"sessionId"`
	}
	unmarshalResult(t, newResp, &result)
	if strings.TrimSpace(result.SessionID) == "" {
		t.Fatalf("session/new returned empty sessionId")
	}
	return result.SessionID
}

func embeddedNewSession(
	t *testing.T,
	ctx context.Context,
	runtime *codexacp.EmbeddedRuntime,
	requestID string,
	cwd string,
) string {
	t.Helper()
	newResp := embeddedRequest(t, ctx, runtime, requestID, "session/new", map[string]any{
		"cwd": cwd,
	})
	embeddedEnsureNoRPCError(t, "session/new", newResp)
	var result struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(newResp.Result, &result); err != nil {
		t.Fatalf("decode session/new result: %v", err)
	}
	if strings.TrimSpace(result.SessionID) == "" {
		t.Fatalf("session/new returned empty sessionId")
	}
	return result.SessionID
}

func runContractPromptStandalone(
	t *testing.T,
	h *adapterHarness,
	sessionID string,
	promptID string,
	scenario contractPromptScenario,
) contractPromptOutcome {
	t.Helper()

	outcome := contractPromptOutcome{
		Name:      scenario.Name,
		SessionID: sessionID,
	}
	cancelID := promptID + "-cancel"
	cancelSent := false

	h.sendRequest(promptID, "session/prompt", map[string]any{
		"sessionId": sessionID,
		"prompt":    scenario.Prompt,
	})

	deadline := time.Now().Add(2 * responseTimeout)
	for {
		if time.Now().After(deadline) {
			t.Fatalf("standalone prompt %q timeout", scenario.Name)
		}

		msg := h.nextMessage(time.Until(deadline))
		switch msg.Method {
		case "session/update":
			update := decodeSessionUpdate(t, msg)
			if !shouldTrackPromptUpdate(&outcome, update) {
				continue
			}
			recordPromptUpdate(t, &outcome, sessionID, update)
			if scenario.CancelAfterUpdate && !cancelSent {
				h.sendRequest(cancelID, "session/cancel", map[string]any{
					"sessionId": sessionID,
				})
				cancelSent = true
			}
		case "session/request_permission":
			if msg.ID == nil {
				t.Fatalf("permission request missing id")
			}
			req := decodeSessionRequestPermission(t, msg)
			if req.SessionID != sessionID {
				t.Fatalf("permission session mismatch got=%q want=%q", req.SessionID, sessionID)
			}
			outcome.PermissionRequests = append(outcome.PermissionRequests, req.Approval)
			if !scenario.ExpectPermission {
				t.Fatalf("unexpected permission request in scenario %q", scenario.Name)
			}
			h.sendResultResponse(messageID(msg), map[string]any{
				"outcome": scenario.PermissionOutcome,
			})
		default:
			switch messageID(msg) {
			case cancelID:
				if !scenario.CancelAfterUpdate {
					t.Fatalf("unexpected session/cancel response in scenario %q", scenario.Name)
				}
				var cancelResult struct {
					Cancelled bool `json:"cancelled"`
				}
				unmarshalResult(t, msg, &cancelResult)
				outcome.CancelAcknowledged = cancelResult.Cancelled
			case promptID:
				var promptResult struct {
					StopReason string `json:"stopReason"`
				}
				unmarshalResult(t, msg, &promptResult)
				outcome.StopReason = promptResult.StopReason
				if scenario.CancelAfterUpdate && !outcome.CancelAcknowledged {
					cancelResp := waitForResponse(t, h.reader, cancelID, responseTimeout, nil)
					var cancelResult struct {
						Cancelled bool `json:"cancelled"`
					}
					unmarshalResult(t, cancelResp, &cancelResult)
					outcome.CancelAcknowledged = cancelResult.Cancelled
				}
				drainStandalonePromptTail(t, h, &outcome, sessionID, scenario.ExpectPermission)
				return outcome
			}
		}
	}
}

func runContractPromptEmbedded(
	t *testing.T,
	ctx context.Context,
	runtime *codexacp.EmbeddedRuntime,
	updates <-chan codexacp.RPCMessage,
	sessionID string,
	promptID string,
	scenario contractPromptScenario,
) contractPromptOutcome {
	t.Helper()

	outcome := contractPromptOutcome{
		Name:      scenario.Name,
		SessionID: sessionID,
	}
	cancelSent := false

	promptDone := make(chan embeddedPromptCallResult, 1)
	go func() {
		resp, err := runtime.ClientRequest(ctx, embeddedRPCRequest(promptID, "session/prompt", map[string]any{
			"sessionId": sessionID,
			"prompt":    scenario.Prompt,
		}))
		promptDone <- embeddedPromptCallResult{response: resp, err: err}
	}()

	deadline := time.After(2 * responseTimeout)
	for {
		select {
		case result := <-promptDone:
			if result.err != nil {
				t.Fatalf("embedded prompt %q request failed: %v", scenario.Name, result.err)
			}
			outcome.StopReason = embeddedDecodeStopReason(t, scenario.Name, result.response)
			drainEmbeddedPromptTail(t, updates, &outcome, sessionID)
			return outcome
		case msg, ok := <-updates:
			if !ok {
				t.Fatalf("embedded updates channel closed while running scenario %q", scenario.Name)
			}
			switch msg.Method {
			case "session/update":
				update := embeddedDecodeSessionUpdate(t, msg)
				if !shouldTrackPromptUpdate(&outcome, update) {
					continue
				}
				recordPromptUpdate(t, &outcome, sessionID, update)
				if scenario.CancelAfterUpdate && !cancelSent {
					cancelCtx, cancel := context.WithTimeout(ctx, responseTimeout)
					cancelResp, err := runtime.ClientRequest(cancelCtx, embeddedRPCRequest(
						promptID+"-cancel",
						"session/cancel",
						map[string]any{"sessionId": sessionID},
					))
					cancel()
					if err != nil {
						t.Fatalf("embedded session/cancel failed: %v", err)
					}
					var cancelResult struct {
						Cancelled bool `json:"cancelled"`
					}
					embeddedUnmarshalResult(t, "session/cancel", cancelResp, &cancelResult)
					outcome.CancelAcknowledged = cancelResult.Cancelled
					cancelSent = true
				}
			case "session/request_permission":
				if msg.ID == nil {
					t.Fatalf("embedded permission request missing id")
				}
				req := embeddedDecodeSessionRequestPermission(t, msg)
				if req.SessionID != sessionID {
					t.Fatalf("embedded permission session mismatch got=%q want=%q", req.SessionID, sessionID)
				}
				outcome.PermissionRequests = append(outcome.PermissionRequests, req.Approval)
				if !scenario.ExpectPermission {
					t.Fatalf("unexpected embedded permission request in scenario %q", scenario.Name)
				}
				if err := runtime.RespondPermission(
					ctx,
					*msg.ID,
					codexacp.PermissionDecision{Outcome: scenario.PermissionOutcome},
				); err != nil {
					t.Fatalf("embedded respond permission: %v", err)
				}
			}
		case <-deadline:
			t.Fatalf("embedded prompt %q timeout", scenario.Name)
		}
	}
}

func decodeInitializeSnapshot(
	t *testing.T,
	label string,
	rawResult json.RawMessage,
	errObj any,
) contractInitializeSnapshot {
	t.Helper()

	switch v := errObj.(type) {
	case *rpcError:
		if v != nil {
			t.Fatalf("%s returned rpc error: code=%d message=%s", label, v.Code, v.Message)
		}
	case *codexacp.RPCError:
		if v != nil {
			t.Fatalf("%s returned rpc error: code=%d message=%s", label, v.Code, v.Message)
		}
	}

	if len(rawResult) == 0 {
		t.Fatalf("%s returned empty result", label)
	}

	var payload struct {
		ProtocolVersion   int `json:"protocolVersion"`
		AgentCapabilities struct {
			Sessions      *bool `json:"sessions"`
			Images        *bool `json:"images"`
			ToolCalls     *bool `json:"toolCalls"`
			SlashCommands *bool `json:"slashCommands"`
			Permissions   *bool `json:"permissions"`

			PromptCapabilities struct {
				Image           *bool `json:"image"`
				EmbeddedContext *bool `json:"embeddedContext"`
			} `json:"promptCapabilities"`

			MCPCapabilities struct {
				HTTP *bool `json:"http"`
				SSE  *bool `json:"sse"`
			} `json:"mcpCapabilities"`

			SessionCapabilities struct {
				List   json.RawMessage `json:"list"`
				Fork   json.RawMessage `json:"fork"`
				Resume json.RawMessage `json:"resume"`
			} `json:"sessionCapabilities"`
		} `json:"agentCapabilities"`
	}

	if err := json.Unmarshal(rawResult, &payload); err != nil {
		t.Fatalf("decode %s: %v", label, err)
	}

	return contractInitializeSnapshot{
		ProtocolVersion: payload.ProtocolVersion,

		HasLegacySessions:      payload.AgentCapabilities.Sessions != nil,
		HasLegacyImages:        payload.AgentCapabilities.Images != nil,
		HasLegacyToolCalls:     payload.AgentCapabilities.ToolCalls != nil,
		HasLegacySlashCommands: payload.AgentCapabilities.SlashCommands != nil,
		HasLegacyPermissions:   payload.AgentCapabilities.Permissions != nil,

		LegacySessions:      boolPointerValue(payload.AgentCapabilities.Sessions),
		LegacyImages:        boolPointerValue(payload.AgentCapabilities.Images),
		LegacyToolCalls:     boolPointerValue(payload.AgentCapabilities.ToolCalls),
		LegacySlashCommands: boolPointerValue(payload.AgentCapabilities.SlashCommands),
		LegacyPermissions:   boolPointerValue(payload.AgentCapabilities.Permissions),

		HasPromptImage:           payload.AgentCapabilities.PromptCapabilities.Image != nil,
		HasPromptEmbeddedContext: payload.AgentCapabilities.PromptCapabilities.EmbeddedContext != nil,
		PromptImage:              boolPointerValue(payload.AgentCapabilities.PromptCapabilities.Image),
		PromptEmbeddedContext:    boolPointerValue(payload.AgentCapabilities.PromptCapabilities.EmbeddedContext),

		HasMCPHTTP: payload.AgentCapabilities.MCPCapabilities.HTTP != nil,
		HasMCPSSE:  payload.AgentCapabilities.MCPCapabilities.SSE != nil,
		MCPHTTP:    boolPointerValue(payload.AgentCapabilities.MCPCapabilities.HTTP),
		MCPSSE:     boolPointerValue(payload.AgentCapabilities.MCPCapabilities.SSE),

		HasSessionList:   len(payload.AgentCapabilities.SessionCapabilities.List) > 0,
		HasSessionFork:   len(payload.AgentCapabilities.SessionCapabilities.Fork) > 0,
		HasSessionResume: len(payload.AgentCapabilities.SessionCapabilities.Resume) > 0,
	}
}

func assertInitializeCompleteness(
	t *testing.T,
	mode string,
	snapshot contractInitializeSnapshot,
) {
	t.Helper()

	if snapshot.ProtocolVersion != 1 {
		t.Fatalf("%s initialize protocolVersion=%d, want 1", mode, snapshot.ProtocolVersion)
	}
	if !snapshot.HasLegacySessions || !snapshot.LegacySessions {
		t.Fatalf("%s initialize missing legacy sessions capability", mode)
	}
	if !snapshot.HasLegacyImages || !snapshot.LegacyImages {
		t.Fatalf("%s initialize missing legacy images capability", mode)
	}
	if !snapshot.HasLegacyToolCalls || !snapshot.LegacyToolCalls {
		t.Fatalf("%s initialize missing legacy toolCalls capability", mode)
	}
	if !snapshot.HasLegacySlashCommands || !snapshot.LegacySlashCommands {
		t.Fatalf("%s initialize missing legacy slashCommands capability", mode)
	}
	if !snapshot.HasLegacyPermissions || !snapshot.LegacyPermissions {
		t.Fatalf("%s initialize missing legacy permissions capability", mode)
	}
	if !snapshot.HasPromptImage || !snapshot.PromptImage {
		t.Fatalf("%s initialize missing promptCapabilities.image=true", mode)
	}
	if !snapshot.HasPromptEmbeddedContext || !snapshot.PromptEmbeddedContext {
		t.Fatalf("%s initialize missing promptCapabilities.embeddedContext=true", mode)
	}
}

func assertPromptExpectations(
	t *testing.T,
	mode string,
	scenario contractPromptScenario,
	outcome contractPromptOutcome,
) {
	t.Helper()

	if outcome.StopReason != scenario.ExpectStopReason {
		t.Fatalf(
			"%s %s stopReason=%q, want %q",
			mode,
			scenario.Name,
			outcome.StopReason,
			scenario.ExpectStopReason,
		)
	}
	if scenario.ExpectMessageChunk && !outcome.SawMessageChunk {
		t.Fatalf("%s %s expected streaming message chunk", mode, scenario.Name)
	}
	if scenario.CancelAfterUpdate && !outcome.CancelAcknowledged {
		t.Fatalf("%s %s expected session/cancel acknowledged=true", mode, scenario.Name)
	}
	if scenario.ExpectPermission {
		if len(outcome.PermissionRequests) == 0 {
			t.Fatalf("%s %s expected permission request", mode, scenario.Name)
		}
		if len(outcome.ToolTransitions) == 0 {
			t.Fatalf("%s %s expected tool_call_update transitions", mode, scenario.Name)
		}
		if !strings.Contains(
			outcome.ToolTransitions[len(outcome.ToolTransitions)-1],
			":"+scenario.PermissionOutcome,
		) {
			t.Fatalf(
				"%s %s final tool transition=%q, want decision=%q",
				mode,
				scenario.Name,
				outcome.ToolTransitions[len(outcome.ToolTransitions)-1],
				scenario.PermissionOutcome,
			)
		}
	} else if len(outcome.PermissionRequests) > 0 {
		t.Fatalf("%s %s expected no permission request, got=%v", mode, scenario.Name, outcome.PermissionRequests)
	}
}

func assertPromptContractEquivalent(
	t *testing.T,
	scenarioName string,
	standalone contractPromptOutcome,
	embedded contractPromptOutcome,
) {
	t.Helper()

	if standalone.StopReason != embedded.StopReason {
		t.Fatalf(
			"%s stopReason mismatch standalone=%q embedded=%q",
			scenarioName,
			standalone.StopReason,
			embedded.StopReason,
		)
	}
	if standalone.SawMessageChunk != embedded.SawMessageChunk {
		t.Fatalf(
			"%s message-chunk mismatch standalone=%v embedded=%v",
			scenarioName,
			standalone.SawMessageChunk,
			embedded.SawMessageChunk,
		)
	}
	if standalone.CancelAcknowledged != embedded.CancelAcknowledged {
		t.Fatalf(
			"%s cancel-ack mismatch standalone=%v embedded=%v",
			scenarioName,
			standalone.CancelAcknowledged,
			embedded.CancelAcknowledged,
		)
	}
	if standalone.StopReason == "cancelled" {
		if !slices.Contains(standalone.LifecycleSequence, "turn_started") ||
			!slices.Contains(embedded.LifecycleSequence, "turn_started") {
			t.Fatalf(
				"%s cancelled flow must include turn_started lifecycle, standalone=%v embedded=%v",
				scenarioName,
				standalone.LifecycleSequence,
				embedded.LifecycleSequence,
			)
		}
	} else if !reflect.DeepEqual(
		normalizeComparableLifecycle(standalone.LifecycleSequence),
		normalizeComparableLifecycle(embedded.LifecycleSequence),
	) {
		t.Fatalf(
			"%s lifecycle sequence mismatch standalone=%v embedded=%v",
			scenarioName,
			standalone.LifecycleSequence,
			embedded.LifecycleSequence,
		)
	}
	if !reflect.DeepEqual(standalone.PermissionRequests, embedded.PermissionRequests) {
		t.Fatalf(
			"%s permission sequence mismatch standalone=%v embedded=%v",
			scenarioName,
			standalone.PermissionRequests,
			embedded.PermissionRequests,
		)
	}
	if !reflect.DeepEqual(standalone.ToolTransitions, embedded.ToolTransitions) {
		t.Fatalf(
			"%s tool transition mismatch standalone=%v embedded=%v",
			scenarioName,
			standalone.ToolTransitions,
			embedded.ToolTransitions,
		)
	}
}

func recordPromptUpdate(
	t *testing.T,
	outcome *contractPromptOutcome,
	expectedSessionID string,
	update sessionUpdateParams,
) {
	t.Helper()

	if update.SessionID != expectedSessionID {
		t.Fatalf("session/update routed to wrong session: got=%q want=%q", update.SessionID, expectedSessionID)
	}
	if update.Status != "" {
		outcome.LifecycleSequence = appendUniqueTransition(outcome.LifecycleSequence, update.Status)
	}
	if update.Type == "message" && strings.TrimSpace(update.Delta) != "" {
		outcome.SawMessageChunk = true
	}
	if update.Type == "tool_call_update" {
		transition := update.Status
		if update.Approval != "" {
			transition += ":" + update.Approval
		}
		if update.PermissionDecision != "" {
			transition += ":" + update.PermissionDecision
		}
		outcome.ToolTransitions = appendUniqueTransition(outcome.ToolTransitions, transition)
	}
}

func appendUniqueTransition(seq []string, token string) []string {
	if token == "" {
		return seq
	}
	if len(seq) > 0 && seq[len(seq)-1] == token {
		return seq
	}
	return append(seq, token)
}

func normalizeComparableLifecycle(seq []string) []string {
	out := make([]string, 0, len(seq))
	for _, status := range seq {
		if status == "turn_completed" || status == "turn_cancelled" {
			continue
		}
		out = append(out, status)
	}
	return out
}

func drainStandalonePromptTail(
	t *testing.T,
	h *adapterHarness,
	outcome *contractPromptOutcome,
	sessionID string,
	expectPermission bool,
) {
	t.Helper()

	deadline := time.Now().Add(quietWindow)
	for {
		left := time.Until(deadline)
		if left <= 0 {
			return
		}
		msg, ok := h.reader.poll(left)
		if !ok {
			return
		}
		switch msg.Method {
		case "session/update":
			update := decodeSessionUpdate(t, msg)
			if !shouldTrackPromptUpdate(outcome, update) {
				continue
			}
			recordPromptUpdate(t, outcome, sessionID, update)
		case "session/request_permission":
			if !expectPermission || msg.ID == nil {
				continue
			}
			req := decodeSessionRequestPermission(t, msg)
			outcome.PermissionRequests = append(outcome.PermissionRequests, req.Approval)
		}
	}
}

func drainEmbeddedPromptTail(
	t *testing.T,
	updates <-chan codexacp.RPCMessage,
	outcome *contractPromptOutcome,
	sessionID string,
) {
	t.Helper()

	timer := time.NewTimer(quietWindow)
	defer timer.Stop()

	for {
		select {
		case msg, ok := <-updates:
			if !ok {
				return
			}
			if msg.Method != "session/update" {
				continue
			}
			update := embeddedDecodeSessionUpdate(t, msg)
			if !shouldTrackPromptUpdate(outcome, update) {
				continue
			}
			recordPromptUpdate(t, outcome, sessionID, update)
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(quietWindow)
		case <-timer.C:
			return
		}
	}
}

func shouldTrackPromptUpdate(
	outcome *contractPromptOutcome,
	update sessionUpdateParams,
) bool {
	if strings.TrimSpace(update.TurnID) == "" {
		return outcome.TurnID != ""
	}
	if outcome.TurnID == "" {
		if update.Status == "turn_started" {
			outcome.TurnID = update.TurnID
			return true
		}
		return false
	}
	return update.TurnID == outcome.TurnID
}

func embeddedEnsureNoRPCError(t *testing.T, method string, resp codexacp.RPCMessage) {
	t.Helper()
	if resp.Error != nil {
		t.Fatalf("%s rpc error code=%d message=%s", method, resp.Error.Code, resp.Error.Message)
	}
}

func embeddedUnmarshalResult(t *testing.T, method string, resp codexacp.RPCMessage, out any) {
	t.Helper()
	embeddedEnsureNoRPCError(t, method, resp)
	if len(resp.Result) == 0 {
		t.Fatalf("%s empty rpc result", method)
	}
	if err := json.Unmarshal(resp.Result, out); err != nil {
		t.Fatalf("decode %s result: %v", method, err)
	}
}

func embeddedDecodeStopReason(
	t *testing.T,
	label string,
	resp codexacp.RPCMessage,
) string {
	t.Helper()
	var result struct {
		StopReason string `json:"stopReason"`
	}
	embeddedUnmarshalResult(t, label, resp, &result)
	return result.StopReason
}

func embeddedDecodeSessionUpdate(t *testing.T, msg codexacp.RPCMessage) sessionUpdateParams {
	t.Helper()
	var params sessionUpdateParams
	if err := json.Unmarshal(msg.Params, &params); err != nil {
		t.Fatalf("decode embedded session/update params: %v", err)
	}
	return params
}

func embeddedDecodeSessionRequestPermission(
	t *testing.T,
	msg codexacp.RPCMessage,
) sessionRequestPermissionParams {
	t.Helper()
	var params sessionRequestPermissionParams
	if err := json.Unmarshal(msg.Params, &params); err != nil {
		t.Fatalf("decode embedded session/request_permission params: %v", err)
	}
	return params
}

func boolPointerValue(v *bool) bool {
	return v != nil && *v
}
