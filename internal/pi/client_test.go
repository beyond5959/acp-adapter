package pi

import (
	"testing"

	"github.com/beyond5959/acp-adapter/internal/codex"
)

func TestSessionApprovalRuleFromApproval(t *testing.T) {
	t.Parallel()

	rule, ok := sessionApprovalRuleFromApproval(codex.ApprovalRequest{
		Kind:    codex.ApprovalKindCommand,
		Command: "go test ./...",
	})
	if !ok {
		t.Fatalf("expected command approval rule")
	}
	if rule.Kind != codex.ApprovalKindCommand || rule.Command != "go test ./..." {
		t.Fatalf("unexpected command rule: %+v", rule)
	}

	fileRule, ok := sessionApprovalRuleFromApproval(codex.ApprovalRequest{
		Kind:  codex.ApprovalKindFile,
		Files: []string{"b.txt", "a.txt", "a.txt", "  "},
	})
	if !ok {
		t.Fatalf("expected file approval rule")
	}
	if len(fileRule.Files) != 2 || fileRule.Files[0] != "a.txt" || fileRule.Files[1] != "b.txt" {
		t.Fatalf("unexpected file rule: %+v", fileRule)
	}
}

func TestClientRememberSessionApproval(t *testing.T) {
	t.Parallel()

	client := NewClient(Config{})
	threadID := "session-1"
	approval := codex.ApprovalRequest{
		ThreadID: threadID,
		Kind:     codex.ApprovalKindCommand,
		Command:  "echo approved by fake pi",
	}

	if client.shouldAutoApprove(threadID, approval) {
		t.Fatalf("approval should not be cached before remember")
	}

	client.rememberSessionApproval(threadID, approval)

	if !client.shouldAutoApprove(threadID, approval) {
		t.Fatalf("approval should be cached after remember")
	}

	client.removeSession(threadID)
	if client.shouldAutoApprove(threadID, approval) {
		t.Fatalf("approval cache should be cleared with session removal")
	}
}
