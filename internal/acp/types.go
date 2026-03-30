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
//
// It includes ACP standard fields and keeps legacy compatibility fields
// consumed by existing ACP clients.
type InitializeResult struct {
	ProtocolVersion   int                 `json:"protocolVersion"`
	AgentCapabilities AgentCapabilities   `json:"agentCapabilities"`
	AuthMethods       []AuthMethod        `json:"authMethods,omitempty"`
	AgentInfo         *ImplementationInfo `json:"agentInfo,omitempty"`
	ActiveAuthMethod  string              `json:"activeAuthMethod,omitempty"`
}

// ImplementationInfo identifies this ACP adapter for client UI/debugging.
type ImplementationInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Title   string `json:"title,omitempty"`
}

// AgentCapabilities describes ACP abilities.
//
// `loadSession/promptCapabilities/mcpCapabilities/sessionCapabilities` are
// ACP standard fields. Legacy fields are kept for clients that still read
// the pre-standard shape.
type AgentCapabilities struct {
	LoadSession         bool                `json:"loadSession,omitempty"`
	PromptCapabilities  PromptCapabilities  `json:"promptCapabilities,omitempty"`
	MCPCapabilities     MCPCapabilities     `json:"mcpCapabilities,omitempty"`
	SessionCapabilities SessionCapabilities `json:"sessionCapabilities,omitempty"`

	// Legacy capability fields (compatibility).
	Sessions      bool `json:"sessions,omitempty"`
	Images        bool `json:"images,omitempty"`
	ToolCalls     bool `json:"toolCalls,omitempty"`
	SlashCommands bool `json:"slashCommands,omitempty"`
	Permissions   bool `json:"permissions,omitempty"`
}

// SessionCapabilities are ACP session-method capability switches.
type SessionCapabilities struct {
	List   any `json:"list,omitempty"`
	Fork   any `json:"fork,omitempty"`
	Resume any `json:"resume,omitempty"`
}

// PromptCapabilities declares prompt content support.
type PromptCapabilities struct {
	Image           bool `json:"image,omitempty"`
	Audio           bool `json:"audio,omitempty"`
	EmbeddedContext bool `json:"embeddedContext,omitempty"`
}

// MCPCapabilities declares supported MCP transports.
type MCPCapabilities struct {
	HTTP bool `json:"http,omitempty"`
	SSE  bool `json:"sse,omitempty"`
}

// AuthMethod describes one supported auth path.
type AuthMethod struct {
	// Optional method metadata fields used by ACP clients that select auth by id.
	ID          string `json:"id,omitempty"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`

	// Legacy method metadata fields kept for clients that use type/label.
	Type  string `json:"type,omitempty"`
	Label string `json:"label,omitempty"`
}

// AuthenticateParams handles client-initiated auth selection.
type AuthenticateParams struct {
	MethodID string `json:"methodId,omitempty"`
	Type     string `json:"type,omitempty"`
}

// AuthenticateResult reports auth selection state.
type AuthenticateResult struct {
	Authenticated    bool   `json:"authenticated"`
	ActiveAuthMethod string `json:"activeAuthMethod,omitempty"`
}

// SessionLoadParams restores one historical session into memory.
type SessionLoadParams struct {
	SessionID string `json:"sessionId"`
	CWD       string `json:"cwd"`
}

// SessionLoadResult reports the restored runtime config snapshot.
type SessionLoadResult struct {
	ConfigOptions []SessionConfig `json:"configOptions,omitempty"`
}

// SessionNewParams are optional fields for session/new.
type SessionNewParams struct {
	CWD string `json:"cwd,omitempty"`
	PromptConfig
}

// SessionListParams requests one page of historical sessions.
type SessionListParams struct {
	CWD    string `json:"cwd,omitempty"`
	Cursor string `json:"cursor,omitempty"`
}

// SessionInfo is one ACP-discoverable historical session summary.
type SessionInfo struct {
	SessionID string         `json:"sessionId"`
	CWD       string         `json:"cwd"`
	Title     string         `json:"title"`
	UpdatedAt string         `json:"updatedAt"`
	Meta      map[string]any `json:"_meta,omitempty"`
}

// SessionListResult carries one page of historical sessions.
type SessionListResult struct {
	Sessions   []SessionInfo `json:"sessions"`
	NextCursor string        `json:"nextCursor,omitempty"`
}

// SessionNewResult returns new session id.
type SessionNewResult struct {
	SessionID     string          `json:"sessionId"`
	ConfigOptions []SessionConfig `json:"configOptions,omitempty"`
}

// SessionConfig describes one configurable session/runtime option.
//
// For runtime switching, the adapter currently exposes:
// id="model" and id="thought_level".
type SessionConfig struct {
	ID           string               `json:"id"`
	Category     string               `json:"category,omitempty"`
	Name         string               `json:"name,omitempty"`
	Description  string               `json:"description,omitempty"`
	Type         string               `json:"type"`
	CurrentValue string               `json:"currentValue"`
	Options      []SessionConfigValue `json:"options,omitempty"`
}

// SessionConfigValue is one selectable option value.
type SessionConfigValue struct {
	Value       string `json:"value"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
}

// SessionSetConfigOptionParams updates one session config option.
type SessionSetConfigOptionParams struct {
	SessionID string `json:"sessionId"`
	ConfigID  string `json:"configId"`
	Value     string `json:"value"`
}

// SessionSetConfigOptionResult returns the latest config options snapshot.
type SessionSetConfigOptionResult struct {
	ConfigOptions []SessionConfig `json:"configOptions,omitempty"`
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
	ThoughtLevel       string `json:"thoughtLevel,omitempty"`
	ApprovalPolicy     string `json:"approvalPolicy,omitempty"`
	Sandbox            string `json:"sandbox,omitempty"`
	Personality        string `json:"personality,omitempty"`
	SystemInstructions string `json:"systemInstructions,omitempty"`
}

// ProfileConfig is one named profile loaded from adapter configuration.
type ProfileConfig struct {
	Model              string `json:"model,omitempty"`
	ThoughtLevel       string `json:"thoughtLevel,omitempty"`
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

// PromptContentBlock is one ACP content block used in prompts and updates.
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

// ToolCallContentItem is one ACP tool_call_update content entry.
type ToolCallContentItem struct {
	Type    string              `json:"type"`
	Content *PromptContentBlock `json:"content,omitempty"`
	Path    string              `json:"path,omitempty"`
	OldText string              `json:"oldText,omitempty"`
	NewText string              `json:"newText,omitempty"`
}

// TodoItem is one structured TODO line parsed from markdown checklist.
type TodoItem struct {
	Text string `json:"text"`
	Done bool   `json:"done"`
}

// PlanEntry is one ACP agent-plan entry.
type PlanEntry struct {
	Content  string `json:"content"`
	Priority string `json:"priority"`
	Status   string `json:"status"`
}

// AvailableCommandInput describes one slash-command argument shape.
type AvailableCommandInput struct {
	Hint string `json:"hint,omitempty"`
}

// AvailableCommand advertises one slash command to ACP clients.
type AvailableCommand struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description,omitempty"`
	Input       *AvailableCommandInput `json:"input,omitempty"`
}

// SessionUsageCost is the optional ACP session usage cost payload.
type SessionUsageCost struct {
	Amount   float64 `json:"amount"`
	Currency string  `json:"currency"`
}

// SessionUpdateParams is emitted via session/update notification.
type SessionUpdateParams struct {
	SessionID          string                `json:"sessionId"`
	TurnID             string                `json:"turnId"`
	Type               string                `json:"type"`
	Role               string                `json:"role,omitempty"`
	Phase              string                `json:"phase,omitempty"`
	ItemID             string                `json:"itemId,omitempty"`
	ItemType           string                `json:"itemType,omitempty"`
	Delta              string                `json:"delta,omitempty"`
	Status             string                `json:"status,omitempty"`
	Message            string                `json:"message,omitempty"`
	ToolCallID         string                `json:"toolCallId,omitempty"`
	Approval           string                `json:"approval,omitempty"`
	PermissionDecision string                `json:"permissionDecision,omitempty"`
	Content            *PromptContentBlock   `json:"content,omitempty"`
	ToolCallContent    []ToolCallContentItem `json:"toolCallContent,omitempty"`
	Todo               []TodoItem            `json:"todo,omitempty"`
	Plan               []PlanEntry           `json:"plan,omitempty"`
	ConfigOptions      []SessionConfig       `json:"configOptions,omitempty"`
	AvailableCommands  []AvailableCommand    `json:"availableCommands,omitempty"`
	Used               *int64                `json:"used,omitempty"`
	Size               *int64                `json:"size,omitempty"`
	Cost               *SessionUsageCost     `json:"cost,omitempty"`
}
