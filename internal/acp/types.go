package acp

import "encoding/json"

// RPCMessage is ACP stdio JSON-RPC envelope.
type RPCMessage struct {
	JSONRPC string           `json:"jsonrpc,omitempty"`
	ID      *json.RawMessage `json:"id,omitempty"`
	Method  string           `json:"method,omitempty"`
	Params  json.RawMessage  `json:"params,omitempty"`
	Result  json.RawMessage  `json:"result,omitempty"`
	Error   *RPCError        `json:"error,omitempty"`
}

// RPCError is JSON-RPC error object.
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// InitializeResult is ACP initialize response payload.
type InitializeResult struct {
	AgentCapabilities AgentCapabilities `json:"agentCapabilities"`
	AuthMethods       []AuthMethod      `json:"authMethods,omitempty"`
}

// AgentCapabilities describes top-level ACP abilities.
type AgentCapabilities struct {
	Sessions      bool `json:"sessions"`
	Images        bool `json:"images"`
	ToolCalls     bool `json:"toolCalls"`
	SlashCommands bool `json:"slashCommands"`
	Permissions   bool `json:"permissions"`
}

// AuthMethod describes one supported auth path.
type AuthMethod struct {
	Type  string `json:"type"`
	Label string `json:"label,omitempty"`
}

// SessionNewParams are optional fields for session/new.
type SessionNewParams struct {
	CWD string `json:"cwd,omitempty"`
}

// SessionNewResult returns new session id.
type SessionNewResult struct {
	SessionID string `json:"sessionId"`
}

// SessionPromptParams starts one prompt-turn.
type SessionPromptParams struct {
	SessionID string `json:"sessionId"`
	Prompt    string `json:"prompt,omitempty"`
}

// SessionPromptResult returns final stop reason.
type SessionPromptResult struct {
	StopReason string `json:"stopReason"`
}

// SessionCancelParams requests turn cancellation.
type SessionCancelParams struct {
	SessionID string `json:"sessionId"`
}

// SessionCancelResult reports if active turn was cancelled.
type SessionCancelResult struct {
	Cancelled bool `json:"cancelled"`
}

// SessionUpdateParams is emitted via session/update notification.
type SessionUpdateParams struct {
	SessionID string `json:"sessionId"`
	TurnID    string `json:"turnId"`
	Type      string `json:"type"`
	Delta     string `json:"delta,omitempty"`
	Status    string `json:"status,omitempty"`
}
