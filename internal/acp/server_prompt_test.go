package acp

import (
	"context"
	"testing"
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
