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
	ActiveAuthMethod  string            `json:"activeAuthMethod,omitempty"`
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
	PromptConfig
}

// SessionNewResult returns new session id.
type SessionNewResult struct {
	SessionID string `json:"sessionId"`
}

// SessionPromptParams starts one prompt-turn.
type SessionPromptParams struct {
	SessionID string               `json:"sessionId"`
	Prompt    string               `json:"prompt,omitempty"`
	Content   []PromptContentBlock `json:"content,omitempty"`
	Resources []PromptResource     `json:"resources,omitempty"`
	PromptConfig
}

// SessionPromptResult returns final stop reason.
type SessionPromptResult struct {
	StopReason string `json:"stopReason"`
}

// SessionCancelParams requests turn cancellation.
type SessionCancelParams struct {
	SessionID string `json:"sessionId"`
}

// PromptConfig carries runtime model/policy/personality overrides.
type PromptConfig struct {
	Profile            string `json:"profile,omitempty"`
	Model              string `json:"model,omitempty"`
	ApprovalPolicy     string `json:"approvalPolicy,omitempty"`
	Sandbox            string `json:"sandbox,omitempty"`
	Personality        string `json:"personality,omitempty"`
	SystemInstructions string `json:"systemInstructions,omitempty"`
}

// ProfileConfig is one named profile loaded from adapter configuration.
type ProfileConfig struct {
	Model              string `json:"model,omitempty"`
	ApprovalPolicy     string `json:"approvalPolicy,omitempty"`
	Sandbox            string `json:"sandbox,omitempty"`
	Personality        string `json:"personality,omitempty"`
	SystemInstructions string `json:"systemInstructions,omitempty"`
}

// SessionCancelResult reports if active turn was cancelled.
type SessionCancelResult struct {
	Cancelled bool `json:"cancelled"`
}

// SessionRequestPermissionParams requests user permission from ACP client.
type SessionRequestPermissionParams struct {
	SessionID  string   `json:"sessionId"`
	TurnID     string   `json:"turnId"`
	Approval   string   `json:"approval"`
	ToolCallID string   `json:"toolCallId,omitempty"`
	Command    string   `json:"command,omitempty"`
	Files      []string `json:"files,omitempty"`
	Host       string   `json:"host,omitempty"`
	Protocol   string   `json:"protocol,omitempty"`
	Port       int      `json:"port,omitempty"`
	MCPServer  string   `json:"mcpServer,omitempty"`
	MCPTool    string   `json:"mcpTool,omitempty"`
	Message    string   `json:"message,omitempty"`
}

// SessionRequestPermissionResult is ACP client decision for one permission prompt.
type SessionRequestPermissionResult struct {
	Outcome  string `json:"outcome,omitempty"`
	Decision string `json:"decision,omitempty"`
	Approved *bool  `json:"approved,omitempty"`
}

// ByteRange marks byte offsets for one referenced resource window.
type ByteRange struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

// PromptResource is one referenced context resource from ACP client.
type PromptResource struct {
	Name     string     `json:"name,omitempty"`
	URI      string     `json:"uri,omitempty"`
	Path     string     `json:"path,omitempty"`
	MimeType string     `json:"mimeType,omitempty"`
	Text     string     `json:"text,omitempty"`
	Data     string     `json:"data,omitempty"`
	Range    *ByteRange `json:"range,omitempty"`
}

// PromptContentBlock is one ACP prompt content block (text/image/resource/mention).
type PromptContentBlock struct {
	Type     string          `json:"type,omitempty"`
	Text     string          `json:"text,omitempty"`
	Data     string          `json:"data,omitempty"`
	URI      string          `json:"uri,omitempty"`
	Path     string          `json:"path,omitempty"`
	Name     string          `json:"name,omitempty"`
	MimeType string          `json:"mimeType,omitempty"`
	Range    *ByteRange      `json:"range,omitempty"`
	Resource *PromptResource `json:"resource,omitempty"`
}

// TodoItem is one structured TODO line parsed from markdown checklist.
type TodoItem struct {
	Text string `json:"text"`
	Done bool   `json:"done"`
}

// SessionUpdateParams is emitted via session/update notification.
type SessionUpdateParams struct {
	SessionID          string     `json:"sessionId"`
	TurnID             string     `json:"turnId"`
	Type               string     `json:"type"`
	Phase              string     `json:"phase,omitempty"`
	ItemID             string     `json:"itemId,omitempty"`
	ItemType           string     `json:"itemType,omitempty"`
	Delta              string     `json:"delta,omitempty"`
	Status             string     `json:"status,omitempty"`
	Message            string     `json:"message,omitempty"`
	ToolCallID         string     `json:"toolCallId,omitempty"`
	Approval           string     `json:"approval,omitempty"`
	PermissionDecision string     `json:"permissionDecision,omitempty"`
	Todo               []TodoItem `json:"todo,omitempty"`
}
