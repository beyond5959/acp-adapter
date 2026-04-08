package acp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/beyond5959/acp-adapter/internal/codex"
)

func TestPrepareTurnInputImageResourceLinkUsesLocalImagePath(t *testing.T) {
	t.Parallel()

	server := &Server{}
	const imagePath = "/tmp/ngent-image-991527206.png"

	input, warnings, promptText, err := server.prepareTurnInput(
		context.Background(),
		"session-1",
		SessionPromptParams{
			Content: []PromptContentBlock{
				{
					Type: "text",
					Text: "describe the image briefly",
				},
				{
					Type:     "resource_link",
					URI:      "file://" + imagePath,
					Name:     "ngent-image-991527206.png",
					MimeType: "image/png",
				},
			},
		},
		nil,
	)
	if err != nil {
		t.Fatalf("prepareTurnInput returned error: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("prepareTurnInput warnings = %+v, want none", warnings)
	}
	if promptText != "describe the image briefly" {
		t.Fatalf("promptText = %q, want %q", promptText, "describe the image briefly")
	}
	if len(input) != 2 {
		t.Fatalf("len(input) = %d, want 2", len(input))
	}
	if input[0].Type != "text" || input[0].Text != "describe the image briefly" {
		t.Fatalf("input[0] = %+v, want text prompt", input[0])
	}
	if input[1].Type != "localImage" {
		t.Fatalf("input[1].Type = %q, want localImage", input[1].Type)
	}
	if input[1].Path != imagePath {
		t.Fatalf("input[1].Path = %q, want %q", input[1].Path, imagePath)
	}
}

func TestSessionRequestPermissionResultUnmarshalStandardSelected(t *testing.T) {
	t.Parallel()

	var result SessionRequestPermissionResult
	if err := json.Unmarshal([]byte(`{"outcome":{"outcome":"selected","optionId":"acceptForSession"}}`), &result); err != nil {
		t.Fatalf("unmarshal permission result: %v", err)
	}
	if result.Outcome != "selected" {
		t.Fatalf("outcome=%q, want %q", result.Outcome, "selected")
	}
	if result.SelectedOptionID != "acceptForSession" {
		t.Fatalf("selectedOptionId=%q, want %q", result.SelectedOptionID, "acceptForSession")
	}
}

func TestNormalizePermissionOutcomeSelectedAcceptForSession(t *testing.T) {
	t.Parallel()

	got := normalizePermissionOutcome(SessionRequestPermissionResult{
		Outcome:          "selected",
		SelectedOptionID: "acceptForSession",
	})
	if got != permissionOutcomeApprovedForSession {
		t.Fatalf("normalizePermissionOutcome=%q, want %q", got, permissionOutcomeApprovedForSession)
	}
}

func TestPermissionRequestOptionsCommandIncludeAllowForSession(t *testing.T) {
	t.Parallel()

	options := permissionRequestOptions(codex.ApprovalRequest{Kind: codex.ApprovalKindCommand})
	foundAccept := false
	foundAllowAlways := false
	for _, option := range options {
		if option.OptionID == "accept" && option.Kind == "allow_once" {
			foundAccept = true
		}
		if option.OptionID == "acceptForSession" && option.Kind == "allow_always" {
			foundAllowAlways = true
		}
	}
	if !foundAccept {
		t.Fatalf("permission options missing accept: %+v", options)
	}
	if !foundAllowAlways {
		t.Fatalf("permission options missing acceptForSession: %+v", options)
	}
}
