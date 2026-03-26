package codex

import (
	"encoding/json"
	"io"
	"log/slog"
	"testing"
)

func TestHandleNotification_FileChangePatchKindCompatibility(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		method string
		params string
		want   TurnEventType
	}{
		{
			name:   "started supports object kind",
			method: notificationItemStarted,
			params: `{"threadId":"thread-1","turnId":"turn-1","item":{"id":"item-1","type":"fileChange","changes":[{"diff":"@@ -0,0 +1 @@\n+hello\n","kind":{"type":"add"},"path":"README.md"}]}}`,
			want:   TurnEventTypeItemStarted,
		},
		{
			name:   "completed supports legacy string kind",
			method: notificationItemCompleted,
			params: `{"threadId":"thread-1","turnId":"turn-1","item":{"id":"item-1","type":"fileChange","changes":[{"diff":"@@ -1 +1 @@\n-old\n+new\n","kind":"update","path":"README.md"}]}}`,
			want:   TurnEventTypeItemCompleted,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			client := &Client{
				logger:      slog.New(slog.NewJSONHandler(io.Discard, nil)),
				approvals:   make(map[string]pendingApproval),
				turnStreams: make(map[string]chan TurnEvent),
				queuedTurns: make(map[string][]TurnEvent),
			}

			client.handleNotification(RPCMessage{
				JSONRPC: "2.0",
				Method:  tc.method,
				Params:  json.RawMessage(tc.params),
			})

			queued := client.queuedTurns["turn-1"]
			if len(queued) != 1 {
				t.Fatalf("queued events=%d, want 1", len(queued))
			}
			event := queued[0]
			if event.Type != tc.want {
				t.Fatalf("event type=%q, want %q", event.Type, tc.want)
			}
			if event.ItemID != "item-1" {
				t.Fatalf("item id=%q, want %q", event.ItemID, "item-1")
			}
			if event.ItemType != "fileChange" {
				t.Fatalf("item type=%q, want %q", event.ItemType, "fileChange")
			}
		})
	}
}
