package codex

import (
	"encoding/json"
	"io"
	"log/slog"
	"strconv"
	"testing"
)

func TestHandleServerRequest_CommandExecutionApproval(t *testing.T) {
	t.Parallel()

	client := &Client{
		logger:      slog.New(slog.NewJSONHandler(io.Discard, nil)),
		approvals:   make(map[string]pendingApproval),
		turnStreams: make(map[string]chan TurnEvent),
		queuedTurns: make(map[string][]TurnEvent),
	}

	rawID := json.RawMessage(strconv.Quote("server-1"))
	params := CommandExecutionRequestApprovalParams{
		ThreadID: "thread-1",
		TurnID:   "turn-1",
		ItemID:   "item-1",
		Command:  "go test ./...",
	}
	paramsRaw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}

	client.handleServerRequest(RPCMessage{
		JSONRPC: "2.0",
		ID:      &rawID,
		Method:  methodItemCommandExecutionRequestApproval,
		Params:  paramsRaw,
	})

	approval, ok := client.approvals["server-1"]
	if !ok {
		t.Fatalf("approval not stored")
	}
	if approval.requestMethod != methodItemCommandExecutionRequestApproval {
		t.Fatalf("requestMethod=%q, want %q", approval.requestMethod, methodItemCommandExecutionRequestApproval)
	}

	queued := client.queuedTurns["turn-1"]
	if len(queued) != 1 {
		t.Fatalf("queued events=%d, want 1", len(queued))
	}
	event := queued[0]
	if event.Type != TurnEventTypeApprovalRequired {
		t.Fatalf("event type=%q, want %q", event.Type, TurnEventTypeApprovalRequired)
	}
	if event.Approval.Kind != ApprovalKindCommand {
		t.Fatalf("approval kind=%q, want %q", event.Approval.Kind, ApprovalKindCommand)
	}
	if event.Approval.Command != "go test ./..." {
		t.Fatalf("approval command=%q", event.Approval.Command)
	}
	if event.Approval.ApprovalID != "server-1" {
		t.Fatalf("approval id=%q, want %q", event.Approval.ApprovalID, "server-1")
	}
}

func TestApprovalResponsePayload_ItemApprovalDecision(t *testing.T) {
	t.Parallel()

	raw, err := approvalResponsePayload(methodItemFileChangeRequestApproval, ApprovalDecisionDeclined)
	if err != nil {
		t.Fatalf("approvalResponsePayload returned error: %v", err)
	}

	var payload struct {
		Decision string `json:"decision"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if payload.Decision != "decline" {
		t.Fatalf("decision=%q, want %q", payload.Decision, "decline")
	}
}

func TestApprovalResponsePayload_ItemApprovalDecisionAcceptForSession(t *testing.T) {
	t.Parallel()

	raw, err := approvalResponsePayload(methodItemCommandExecutionRequestApproval, ApprovalDecisionApprovedForSession)
	if err != nil {
		t.Fatalf("approvalResponsePayload returned error: %v", err)
	}

	var payload struct {
		Decision string `json:"decision"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if payload.Decision != "acceptForSession" {
		t.Fatalf("decision=%q, want %q", payload.Decision, "acceptForSession")
	}
}

func TestApprovalResponsePayload_LegacyOutcome(t *testing.T) {
	t.Parallel()

	raw, err := approvalResponsePayload(methodApprovalReq, ApprovalDecisionApproved)
	if err != nil {
		t.Fatalf("approvalResponsePayload returned error: %v", err)
	}

	var payload ApprovalDecisionResult
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if payload.Outcome != string(ApprovalDecisionApproved) {
		t.Fatalf("outcome=%q, want %q", payload.Outcome, string(ApprovalDecisionApproved))
	}
}

func TestApprovalFromCommandExecution_NetworkContext(t *testing.T) {
	t.Parallel()

	approval := approvalFromCommandExecution(CommandExecutionRequestApprovalParams{
		ThreadID: "thread-1",
		TurnID:   "turn-1",
		ItemID:   "item-1",
		Command:  "curl -I https://example.com",
		NetworkApprovalContext: &NetworkApprovalContext{
			Host:     "example.com",
			Protocol: "https",
		},
	})

	if approval.Kind != ApprovalKindNetwork {
		t.Fatalf("kind=%q, want %q", approval.Kind, ApprovalKindNetwork)
	}
	if approval.Host != "example.com" {
		t.Fatalf("host=%q, want %q", approval.Host, "example.com")
	}
	if approval.Protocol != "https" {
		t.Fatalf("protocol=%q, want %q", approval.Protocol, "https")
	}
}

func TestApprovalFromCommandExecution_KeepExtendedFields(t *testing.T) {
	t.Parallel()

	actionPath := "docs"
	approval := approvalFromCommandExecution(CommandExecutionRequestApprovalParams{
		ThreadID:                    "thread-1",
		TurnID:                      "turn-1",
		ItemID:                      "item-1",
		Command:                     "rg permission .",
		CWD:                         "/workspace/project",
		CommandActions:              []CommandAction{{Type: "search", Command: "rg permission .", Path: &actionPath}},
		ProposedExecpolicyAmendment: []string{"allow rg permission ."},
	})

	if approval.CWD != "/workspace/project" {
		t.Fatalf("cwd=%q, want %q", approval.CWD, "/workspace/project")
	}
	if len(approval.CommandActions) != 1 {
		t.Fatalf("commandActions=%d, want 1", len(approval.CommandActions))
	}
	if len(approval.ProposedExecpolicyAmendment) != 1 || approval.ProposedExecpolicyAmendment[0] != "allow rg permission ." {
		t.Fatalf("proposedExecpolicyAmendment=%v", approval.ProposedExecpolicyAmendment)
	}
}
