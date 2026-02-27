package appserver

import "encoding/json"

// RPCMessage is the app-server JSON-RPC envelope.
type RPCMessage struct {
	JSONRPC string           `json:"jsonrpc,omitempty"`
	ID      *json.RawMessage `json:"id,omitempty"`
	Method  string           `json:"method,omitempty"`
	Params  json.RawMessage  `json:"params,omitempty"`
	Result  json.RawMessage  `json:"result,omitempty"`
	Error   *RPCError        `json:"error,omitempty"`
}

// RPCError is a JSON-RPC error object.
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// ClientInfo describes caller identity for initialize.
type ClientInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// InitializeParams is sent to app-server initialize request.
type InitializeParams struct {
	ClientInfo   ClientInfo     `json:"clientInfo"`
	Capabilities map[string]any `json:"capabilities,omitempty"`
	Meta         map[string]any `json:"meta,omitempty"`
}

// InitializeResult is minimally parsed initialize result.
type InitializeResult struct {
	ServerInfo *ClientInfo    `json:"serverInfo,omitempty"`
	Raw        map[string]any `json:"-"`
}

// ThreadStartParams starts a new conversation thread.
type ThreadStartParams struct {
	CWD string `json:"cwd,omitempty"`
}

// ThreadStartResult carries new thread id.
type ThreadStartResult struct {
	ThreadID string `json:"threadId"`
}

// TurnStartParams starts a turn under one thread.
type TurnStartParams struct {
	ThreadID string `json:"threadId"`
	Input    string `json:"input"`
}

// TurnStartResult carries new turn id.
type TurnStartResult struct {
	TurnID string `json:"turnId"`
}

// TurnInterruptParams interrupts an active turn.
type TurnInterruptParams struct {
	ThreadID string `json:"threadId"`
	TurnID   string `json:"turnId"`
}

// TurnStartedNotification notifies that one turn has entered running state.
type TurnStartedNotification struct {
	ThreadID string `json:"threadId"`
	TurnID   string `json:"turnId"`
}

// TurnUpdateNotification is streamed while the turn is running.
type TurnUpdateNotification struct {
	ThreadID string `json:"threadId"`
	TurnID   string `json:"turnId"`
	Delta    string `json:"delta,omitempty"`
}

// ItemAgentMessageDeltaNotification carries assistant message chunks.
type ItemAgentMessageDeltaNotification struct {
	ThreadID string `json:"threadId"`
	TurnID   string `json:"turnId"`
	ItemID   string `json:"itemId,omitempty"`
	Delta    string `json:"delta,omitempty"`
}

// ItemStartedNotification marks one streamed item as started.
type ItemStartedNotification struct {
	ThreadID string `json:"threadId"`
	TurnID   string `json:"turnId"`
	ItemID   string `json:"itemId,omitempty"`
	ItemType string `json:"itemType,omitempty"`
}

// ItemCompletedNotification marks one streamed item as completed.
type ItemCompletedNotification struct {
	ThreadID string `json:"threadId"`
	TurnID   string `json:"turnId"`
	ItemID   string `json:"itemId,omitempty"`
	ItemType string `json:"itemType,omitempty"`
}

// TurnCompletedNotification finalizes a turn.
type TurnCompletedNotification struct {
	ThreadID   string `json:"threadId"`
	TurnID     string `json:"turnId"`
	StopReason string `json:"stopReason,omitempty"`
}

// TurnEventType is an internal event kind consumed by ACP bridge.
type TurnEventType string

const (
	// TurnEventTypeStarted indicates turn is running.
	TurnEventTypeStarted TurnEventType = "started"
	// TurnEventTypeUpdate carries model/tool delta text.
	TurnEventTypeUpdate TurnEventType = "update"
	// TurnEventTypeAgentMessageDelta carries assistant message chunk.
	TurnEventTypeAgentMessageDelta TurnEventType = "agent_message_delta"
	// TurnEventTypeItemStarted indicates one item started.
	TurnEventTypeItemStarted TurnEventType = "item_started"
	// TurnEventTypeItemCompleted indicates one item completed.
	TurnEventTypeItemCompleted TurnEventType = "item_completed"
	// TurnEventTypeCompleted indicates turn completion.
	TurnEventTypeCompleted TurnEventType = "completed"
	// TurnEventTypeError indicates stream-level or process-level failure.
	TurnEventTypeError TurnEventType = "error"
)

// TurnEvent is emitted to ACP session/prompt handler.
type TurnEvent struct {
	Type       TurnEventType
	ThreadID   string
	TurnID     string
	ItemID     string
	ItemType   string
	Delta      string
	StopReason string
	Message    string
}

const (
	methodInitialized   = "initialized"
	methodThreadStart   = "thread/start"
	methodTurnStart     = "turn/start"
	methodTurnInterrupt = "turn/interrupt"

	notificationTurnStarted           = "turn/started"
	notificationTurnUpdate            = "turn/update"
	notificationItemStarted           = "item/started"
	notificationItemCompleted         = "item/completed"
	notificationItemAgentMessageDelta = "item/agentMessage/delta"
	notificationTurnCompleted         = "turn/completed"
)
