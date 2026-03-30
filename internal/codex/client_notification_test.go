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

func TestHandleNotification_TurnCompletedIncludesErrorMessage(t *testing.T) {
	t.Parallel()

	client := &Client{
		logger:      slog.New(slog.NewJSONHandler(io.Discard, nil)),
		approvals:   make(map[string]pendingApproval),
		turnStreams: make(map[string]chan TurnEvent),
		queuedTurns: make(map[string][]TurnEvent),
	}

	client.handleNotification(RPCMessage{
		JSONRPC: "2.0",
		Method:  notificationTurnCompleted,
		Params: json.RawMessage(
			`{"threadId":"thread-1","turn":{"id":"turn-1","status":"failed","error":{"message":"apply_patch verification failed","additionalDetails":"patch no longer matches current file contents","codexErrorInfo":"other"}}}`,
		),
	})

	queued := client.queuedTurns["turn-1"]
	if len(queued) != 1 {
		t.Fatalf("queued events=%d, want 1", len(queued))
	}
	event := queued[0]
	if event.Type != TurnEventTypeCompleted {
		t.Fatalf("event type=%q, want %q", event.Type, TurnEventTypeCompleted)
	}
	if event.StopReason != "error" {
		t.Fatalf("stop reason=%q, want %q", event.StopReason, "error")
	}
	if got, want := event.Message, "apply_patch verification failed: patch no longer matches current file contents [codexErrorInfo=other]"; got != want {
		t.Fatalf("message=%q, want %q", got, want)
	}
}

func TestHandleNotification_ErrorNotificationRetrying(t *testing.T) {
	t.Parallel()

	client := &Client{
		logger:      slog.New(slog.NewJSONHandler(io.Discard, nil)),
		approvals:   make(map[string]pendingApproval),
		turnStreams: make(map[string]chan TurnEvent),
		queuedTurns: make(map[string][]TurnEvent),
	}

	client.handleNotification(RPCMessage{
		JSONRPC: "2.0",
		Method:  notificationError,
		Params: json.RawMessage(
			`{"threadId":"thread-1","turnId":"turn-1","willRetry":true,"error":{"message":"temporary upstream connection drop","codexErrorInfo":{"responseStreamDisconnected":{"httpStatusCode":502}}}}`,
		),
	})

	queued := client.queuedTurns["turn-1"]
	if len(queued) != 1 {
		t.Fatalf("queued events=%d, want 1", len(queued))
	}
	event := queued[0]
	if event.Type != TurnEventTypeBackendError {
		t.Fatalf("event type=%q, want %q", event.Type, TurnEventTypeBackendError)
	}
	if !event.WillRetry {
		t.Fatalf("willRetry=%t, want true", event.WillRetry)
	}
	if got, want := event.Message, "temporary upstream connection drop [codexErrorInfo=responseStreamDisconnected(httpStatusCode=502)]"; got != want {
		t.Fatalf("message=%q, want %q", got, want)
	}
}

func TestHandleNotification_ThreadTokenUsageUpdated(t *testing.T) {
	t.Parallel()

	client := &Client{
		logger:      slog.New(slog.NewJSONHandler(io.Discard, nil)),
		approvals:   make(map[string]pendingApproval),
		turnStreams: make(map[string]chan TurnEvent),
		queuedTurns: make(map[string][]TurnEvent),
	}

	client.handleNotification(RPCMessage{
		JSONRPC: "2.0",
		Method:  notificationThreadTokenUsageUpdated,
		Params: json.RawMessage(
			`{"threadId":"thread-1","turnId":"turn-1","tokenUsage":{"last":{"cachedInputTokens":1000,"inputTokens":4000,"outputTokens":500,"reasoningOutputTokens":250,"totalTokens":5750},"modelContextWindow":200000,"total":{"cachedInputTokens":5000,"inputTokens":35000,"outputTokens":12000,"reasoningOutputTokens":1000,"totalTokens":53000}}}`,
		),
	})

	queued := client.queuedTurns["turn-1"]
	if len(queued) != 1 {
		t.Fatalf("queued events=%d, want 1", len(queued))
	}
	event := queued[0]
	if event.Type != TurnEventTypeTokenUsageUpdated {
		t.Fatalf("event type=%q, want %q", event.Type, TurnEventTypeTokenUsageUpdated)
	}
	if event.TokenUsage == nil {
		t.Fatalf("token usage event missing token usage payload")
	}
	if got, want := event.TokenUsage.Total.TotalTokens, int64(53000); got != want {
		t.Fatalf("total.totalTokens=%d, want %d", got, want)
	}
	if event.TokenUsage.ModelContextWindow == nil || *event.TokenUsage.ModelContextWindow != 200000 {
		t.Fatalf("modelContextWindow=%v, want 200000", event.TokenUsage.ModelContextWindow)
	}
	if got, want := event.TokenUsage.Last.TotalTokens, int64(5750); got != want {
		t.Fatalf("last.totalTokens=%d, want %d", got, want)
	}
}
