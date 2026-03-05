package codex

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

// RunOptions carries per-thread/per-turn runtime overrides.
type RunOptions struct {
	Model              string `json:"model,omitempty"`
	ApprovalPolicy     string `json:"approvalPolicy,omitempty"`
	Sandbox            string `json:"sandbox,omitempty"`
	Personality        string `json:"personality,omitempty"`
	SystemInstructions string `json:"systemInstructions,omitempty"`
}

// ModelOption is one selectable model exposed by the downstream backend.
type ModelOption struct {
	ID          string
	Name        string
	Description string
	Hidden      bool
	IsDefault   bool
}

// UserInput is one structured item inside turn/start input.
type UserInput struct {
	Type string `json:"type"`

	// Text input payload.
	Text string `json:"text,omitempty"`

	// Remote image URL payload.
	URL string `json:"url,omitempty"`

	// Local image or mention payload.
	Path string `json:"path,omitempty"`
	Name string `json:"name,omitempty"`
}

// ThreadStartParams starts a new conversation thread.
type ThreadStartParams struct {
	CWD string `json:"cwd,omitempty"`
	RunOptions
}

// ThreadStartResult carries new thread id.
type ThreadStartResult struct {
	ThreadID string     `json:"threadId,omitempty"`
	Thread   *ThreadRef `json:"thread,omitempty"`
}

// TurnStartParams starts a turn under one thread.
type TurnStartParams struct {
	ThreadID string      `json:"threadId"`
	Input    []UserInput `json:"input"`
	RunOptions
}

// TurnStartResult carries new turn id.
type TurnStartResult struct {
	TurnID string   `json:"turnId,omitempty"`
	Turn   *TurnRef `json:"turn,omitempty"`
}

// ReviewStartParams starts a review workflow in one thread.
type ReviewStartParams struct {
	ThreadID     string `json:"threadId"`
	Instructions string `json:"instructions,omitempty"`
	RunOptions
}

// ReviewStartResult returns review turn id.
type ReviewStartResult struct {
	TurnID       string   `json:"turnId,omitempty"`
	Turn         *TurnRef `json:"turn,omitempty"`
	ReviewThread string   `json:"reviewThreadId,omitempty"`
}

// CompactStartParams starts one compact operation under one thread.
type CompactStartParams struct {
	ThreadID string `json:"threadId"`
}

// CompactStartResult returns compact turn id.
type CompactStartResult struct {
	TurnID string   `json:"turnId,omitempty"`
	Turn   *TurnRef `json:"turn,omitempty"`
}

// TurnInterruptParams interrupts an active turn.
type TurnInterruptParams struct {
	ThreadID string `json:"threadId"`
	TurnID   string `json:"turnId"`
}

// ModelListParams requests available model list from app-server.
type ModelListParams struct {
	IncludeHidden *bool `json:"includeHidden,omitempty"`
}

// ModelListResult carries available models.
type ModelListResult struct {
	Data []ModelListItem `json:"data"`
}

// ModelListItem is one app-server model/list entry.
type ModelListItem struct {
	ID          string `json:"id,omitempty"`
	Model       string `json:"model,omitempty"`
	DisplayName string `json:"displayName,omitempty"`
	Description string `json:"description,omitempty"`
	Hidden      bool   `json:"hidden,omitempty"`
	IsDefault   bool   `json:"isDefault,omitempty"`
}

// TurnStartedNotification notifies that one turn has entered running state.
type TurnStartedNotification struct {
	ThreadID string   `json:"threadId"`
	TurnID   string   `json:"turnId,omitempty"`
	Turn     *TurnRef `json:"turn,omitempty"`
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
	ThreadID string         `json:"threadId"`
	TurnID   string         `json:"turnId"`
	ItemID   string         `json:"itemId,omitempty"`
	ItemType string         `json:"itemType,omitempty"`
	Item     *ThreadItemRef `json:"item,omitempty"`
}

// ItemCompletedNotification marks one streamed item as completed.
type ItemCompletedNotification struct {
	ThreadID string         `json:"threadId"`
	TurnID   string         `json:"turnId"`
	ItemID   string         `json:"itemId,omitempty"`
	ItemType string         `json:"itemType,omitempty"`
	Item     *ThreadItemRef `json:"item,omitempty"`
}

// TurnCompletedNotification finalizes a turn.
type TurnCompletedNotification struct {
	ThreadID   string   `json:"threadId"`
	TurnID     string   `json:"turnId,omitempty"`
	StopReason string   `json:"stopReason,omitempty"`
	Turn       *TurnRef `json:"turn,omitempty"`
}

// ThreadRef is a minimal thread object shape used by newer app-server payloads.
type ThreadRef struct {
	ID string `json:"id,omitempty"`
}

// TurnRef is a minimal turn object shape used by newer app-server payloads.
type TurnRef struct {
	ID     string `json:"id,omitempty"`
	Status string `json:"status,omitempty"`
}

// ThreadItemRef is a minimal item object shape used by item started/completed notifications.
type ThreadItemRef struct {
	ID   string `json:"id,omitempty"`
	Type string `json:"type,omitempty"`
}

// ReviewModeNotification indicates review mode lifecycle transitions.
type ReviewModeNotification struct {
	ThreadID string `json:"threadId"`
	TurnID   string `json:"turnId"`
}

// ApprovalKind describes which side-effect type requires permission.
type ApprovalKind string

const (
	ApprovalKindCommand ApprovalKind = "command"
	ApprovalKindFile    ApprovalKind = "file"
	ApprovalKindNetwork ApprovalKind = "network"
	ApprovalKindMCP     ApprovalKind = "mcp"
)

// ApprovalDecision is the final user decision sent back to app-server.
type ApprovalDecision string

const (
	ApprovalDecisionApproved  ApprovalDecision = "approved"
	ApprovalDecisionDeclined  ApprovalDecision = "declined"
	ApprovalDecisionCancelled ApprovalDecision = "cancelled"
)

// ApprovalRequest is server-initiated permission payload.
type ApprovalRequest struct {
	ThreadID   string       `json:"threadId"`
	TurnID     string       `json:"turnId"`
	ApprovalID string       `json:"approvalId,omitempty"`
	ToolCallID string       `json:"toolCallId,omitempty"`
	Kind       ApprovalKind `json:"kind"`
	Command    string       `json:"command,omitempty"`
	Files      []string     `json:"files,omitempty"`
	Host       string       `json:"host,omitempty"`
	Protocol   string       `json:"protocol,omitempty"`
	Port       int          `json:"port,omitempty"`
	MCPServer  string       `json:"mcpServer,omitempty"`
	MCPTool    string       `json:"mcpTool,omitempty"`
	Message    string       `json:"message,omitempty"`
	WritePath  string       `json:"writePath,omitempty"`
	WriteText  string       `json:"writeText,omitempty"`
	Patch      string       `json:"patch,omitempty"`
}

// ApprovalDecisionResult is sent as JSON-RPC result for approval request.
type ApprovalDecisionResult struct {
	Outcome string `json:"outcome"`
}

// MCPServer describes one MCP server capability snapshot.
type MCPServer struct {
	Name          string   `json:"name"`
	OAuthRequired bool     `json:"oauthRequired,omitempty"`
	Tools         []string `json:"tools,omitempty"`
}

// MCPServerListResult is returned by mcpServer/list.
type MCPServerListResult struct {
	Servers []MCPServer `json:"servers"`
}

// MCPToolCallParams invokes one MCP tool.
type MCPToolCallParams struct {
	Server    string `json:"server"`
	Tool      string `json:"tool"`
	Arguments string `json:"arguments,omitempty"`
}

// MCPToolCallResult returns MCP tool output payload.
type MCPToolCallResult struct {
	Output string `json:"output,omitempty"`
}

// MCPOAuthLoginParams starts one MCP OAuth flow for one server.
type MCPOAuthLoginParams struct {
	Server string `json:"server"`
}

// MCPOAuthLoginResult reports MCP OAuth login bootstrap state.
type MCPOAuthLoginResult struct {
	Status  string `json:"status,omitempty"`
	URL     string `json:"url,omitempty"`
	Message string `json:"message,omitempty"`
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
	// TurnEventTypeApprovalRequired indicates turn is blocked on permission.
	TurnEventTypeApprovalRequired TurnEventType = "approval_required"
	// TurnEventTypeReviewModeEntered indicates review mode entered.
	TurnEventTypeReviewModeEntered TurnEventType = "review_mode_entered"
	// TurnEventTypeReviewModeExited indicates review mode exited.
	TurnEventTypeReviewModeExited TurnEventType = "review_mode_exited"
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
	Approval   ApprovalRequest
}

const (
	methodInitialized   = "initialized"
	methodThreadStart   = "thread/start"
	methodThreadCompact = "thread/compact/start"
	methodTurnStart     = "turn/start"
	methodReviewStart   = "review/start"
	methodTurnInterrupt = "turn/interrupt"
	methodModelList     = "model/list"
	methodApprovalReq   = "approval/request"
	methodMCPServerList = "mcpServer/list"
	methodMCPServerCall = "mcpServer/call"
	methodMCPOAuthLogin = "mcpServer/oauth/login"
	methodAuthLogout    = "auth/logout"
	methodAccountLogout = "account/logout"

	notificationTurnStarted           = "turn/started"
	notificationTurnUpdate            = "turn/update"
	notificationItemStarted           = "item/started"
	notificationItemCompleted         = "item/completed"
	notificationItemAgentMessageDelta = "item/agentMessage/delta"
	notificationTurnCompleted         = "turn/completed"
	notificationReviewModeEntered     = "review/mode_entered"
	notificationReviewModeExited      = "review/mode_exited"
)
