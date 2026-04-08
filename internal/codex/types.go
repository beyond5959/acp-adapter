package codex

import (
	"bytes"
	"encoding/json"
	"strings"
)

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
	// Effort is turn-level reasoning effort; serialized explicitly by TurnStartParams.
	Effort string `json:"-"`
}

// ModelOption is one selectable model exposed by the downstream backend.
type ModelOption struct {
	ID          string
	Name        string
	Description string
	Hidden      bool
	IsDefault   bool
	// DefaultReasoningEffort is the model default effort (e.g. medium).
	DefaultReasoningEffort string
	// SupportedReasoningEfforts are selectable effort options for this model.
	SupportedReasoningEfforts []ReasoningEffortOption
}

// ReasoningEffortOption is one selectable reasoning effort level.
type ReasoningEffortOption struct {
	Value       string
	Description string
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

// ThreadListParams requests one page of thread history.
type ThreadListParams struct {
	Archived *bool   `json:"archived,omitempty"`
	Cursor   string  `json:"cursor,omitempty"`
	CWD      string  `json:"cwd,omitempty"`
	Limit    *uint32 `json:"limit,omitempty"`
}

// ThreadListResult carries one page of thread history.
type ThreadListResult struct {
	Data       []Thread `json:"data"`
	NextCursor string   `json:"nextCursor,omitempty"`
}

// Thread is the minimal app-server history shape used by ACP session/list.
type Thread struct {
	ID            string `json:"id"`
	CWD           string `json:"cwd"`
	Name          string `json:"name,omitempty"`
	Preview       string `json:"preview,omitempty"`
	Path          string `json:"path,omitempty"`
	ModelProvider string `json:"modelProvider,omitempty"`
	CreatedAt     int64  `json:"createdAt"`
	UpdatedAt     int64  `json:"updatedAt"`
	Source        any    `json:"source,omitempty"`
	Status        any    `json:"status,omitempty"`
	Turns         []Turn `json:"turns,omitempty"`
}

// Turn is one historical turn returned by thread/resume.
type Turn struct {
	ID     string       `json:"id"`
	Status string       `json:"status"`
	Items  []ThreadItem `json:"items,omitempty"`
}

// ThreadItem is one persisted thread item returned by thread/resume.
type ThreadItem struct {
	ID      string      `json:"id"`
	Type    string      `json:"type"`
	Text    string      `json:"text,omitempty"`
	Content []UserInput `json:"content,omitempty"`
}

// ThreadResumeParams resumes a persisted thread into memory.
type ThreadResumeParams struct {
	ThreadID       string `json:"threadId"`
	CWD            string `json:"cwd,omitempty"`
	Model          string `json:"model,omitempty"`
	ApprovalPolicy string `json:"approvalPolicy,omitempty"`
	Sandbox        string `json:"sandbox,omitempty"`
	Personality    string `json:"personality,omitempty"`
}

// ThreadResumeResult carries the resumed thread and effective runtime settings.
type ThreadResumeResult struct {
	ApprovalPolicy  any    `json:"approvalPolicy,omitempty"`
	CWD             string `json:"cwd,omitempty"`
	Model           string `json:"model,omitempty"`
	ModelProvider   string `json:"modelProvider,omitempty"`
	ReasoningEffort string `json:"reasoningEffort,omitempty"`
	Sandbox         any    `json:"sandbox,omitempty"`
	Thread          Thread `json:"thread"`
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
	Effort   string      `json:"effort,omitempty"`
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
	ID                        string                          `json:"id,omitempty"`
	Model                     string                          `json:"model,omitempty"`
	DisplayName               string                          `json:"displayName,omitempty"`
	Description               string                          `json:"description,omitempty"`
	Hidden                    bool                            `json:"hidden,omitempty"`
	IsDefault                 bool                            `json:"isDefault,omitempty"`
	DefaultReasoningEffort    string                          `json:"defaultReasoningEffort,omitempty"`
	SupportedReasoningEfforts []ModelReasoningEffortListEntry `json:"supportedReasoningEfforts,omitempty"`
}

// ModelReasoningEffortListEntry is one model/list reasoning effort option shape.
type ModelReasoningEffortListEntry struct {
	ReasoningEffort string `json:"reasoningEffort,omitempty"`
	Description     string `json:"description,omitempty"`
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

// ErrorNotification reports one downstream turn error before completion.
type ErrorNotification struct {
	ThreadID  string    `json:"threadId"`
	TurnID    string    `json:"turnId"`
	Error     TurnError `json:"error"`
	WillRetry bool      `json:"willRetry"`
}

// TurnError carries structured downstream failure details.
type TurnError struct {
	AdditionalDetails string          `json:"additionalDetails,omitempty"`
	CodexErrorInfo    json.RawMessage `json:"codexErrorInfo,omitempty"`
	Message           string          `json:"message"`
}

// ThreadRef is a minimal thread object shape used by newer app-server payloads.
type ThreadRef struct {
	ID string `json:"id,omitempty"`
}

// TurnRef is a minimal turn object shape used by newer app-server payloads.
type TurnRef struct {
	ID     string     `json:"id,omitempty"`
	Status string     `json:"status,omitempty"`
	Error  *TurnError `json:"error,omitempty"`
}

// ThreadItemRef is a minimal item object shape used by item started/completed notifications.
type ThreadItemRef struct {
	ID               string                             `json:"id,omitempty"`
	Type             string                             `json:"type,omitempty"`
	Text             string                             `json:"text,omitempty"`
	Tool             string                             `json:"tool,omitempty"`
	Server           string                             `json:"server,omitempty"`
	Changes          []FileUpdateChange                 `json:"changes,omitempty"`
	Command          string                             `json:"command,omitempty"`
	CommandActions   []CommandAction                    `json:"commandActions,omitempty"`
	ContentItems     []DynamicToolCallOutputContentItem `json:"contentItems,omitempty"`
	Result           *MCPToolCallResult                 `json:"result,omitempty"`
	Success          *bool                              `json:"success,omitempty"`
	CWD              string                             `json:"cwd,omitempty"`
	AggregatedOutput *string                            `json:"aggregatedOutput,omitempty"`
	DurationMs       *int64                             `json:"durationMs,omitempty"`
	ExitCode         *int                               `json:"exitCode,omitempty"`
	ProcessID        string                             `json:"processId,omitempty"`
	Status           string                             `json:"status,omitempty"`
}

// CommandAction is app-server's best-effort structured interpretation of one shell command segment.
type CommandAction struct {
	Type    string  `json:"type,omitempty"`
	Command string  `json:"command,omitempty"`
	Name    string  `json:"name,omitempty"`
	Path    *string `json:"path,omitempty"`
	Query   *string `json:"query,omitempty"`
}

// FileUpdateChange is one file diff inside a fileChange item.
type FileUpdateChange struct {
	Diff string          `json:"diff,omitempty"`
	Kind PatchChangeKind `json:"kind,omitempty"`
	Path string          `json:"path,omitempty"`
}

// PatchChangeKind is the schema-backed file diff kind.
// Newer app-server versions encode it as {"type":"add|delete|update"},
// while some older traces used a plain string.
type PatchChangeKind struct {
	Type string `json:"type,omitempty"`
}

// UnmarshalJSON accepts both the current object form and the legacy string form.
func (k *PatchChangeKind) UnmarshalJSON(data []byte) error {
	payload := bytes.TrimSpace(data)
	if len(payload) == 0 || bytes.Equal(payload, []byte("null")) {
		*k = PatchChangeKind{}
		return nil
	}

	if payload[0] == '"' {
		var legacy string
		if err := json.Unmarshal(payload, &legacy); err != nil {
			return err
		}
		k.Type = strings.TrimSpace(legacy)
		return nil
	}

	var current struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(payload, &current); err != nil {
		return err
	}
	k.Type = strings.TrimSpace(current.Type)
	return nil
}

// CommandExecution describes one runtime command tool call emitted by app-server items.
type CommandExecution struct {
	ID               string
	Command          string
	CommandActions   []CommandAction
	CWD              string
	AggregatedOutput string
	DurationMs       *int64
	ExitCode         *int
	ProcessID        string
	Status           string
}

// ReviewModeNotification indicates review mode lifecycle transitions.
type ReviewModeNotification struct {
	ThreadID string `json:"threadId"`
	TurnID   string `json:"turnId"`
}

// PlanDeltaNotification carries streamed text for one plan item.
type PlanDeltaNotification struct {
	ThreadID string `json:"threadId"`
	TurnID   string `json:"turnId"`
	ItemID   string `json:"itemId"`
	Delta    string `json:"delta"`
}

// ReasoningSummaryTextDeltaNotification carries reasoning summary text chunks.
type ReasoningSummaryTextDeltaNotification struct {
	ThreadID     string `json:"threadId"`
	TurnID       string `json:"turnId"`
	ItemID       string `json:"itemId"`
	SummaryIndex int64  `json:"summaryIndex"`
	Delta        string `json:"delta"`
}

// ReasoningSummaryPartAddedNotification marks a new reasoning summary section.
type ReasoningSummaryPartAddedNotification struct {
	ThreadID     string `json:"threadId"`
	TurnID       string `json:"turnId"`
	ItemID       string `json:"itemId"`
	SummaryIndex int64  `json:"summaryIndex"`
}

// ReasoningTextDeltaNotification carries raw reasoning text chunks.
type ReasoningTextDeltaNotification struct {
	ThreadID string `json:"threadId"`
	TurnID   string `json:"turnId"`
	ItemID   string `json:"itemId"`
	Delta    string `json:"delta"`
}

// CommandExecutionOutputDeltaNotification carries streamed command output chunks.
type CommandExecutionOutputDeltaNotification struct {
	ThreadID string `json:"threadId"`
	TurnID   string `json:"turnId"`
	ItemID   string `json:"itemId"`
	Delta    string `json:"delta"`
}

// TurnPlanUpdatedNotification carries the latest full turn plan snapshot.
type TurnPlanUpdatedNotification struct {
	ThreadID    string         `json:"threadId"`
	TurnID      string         `json:"turnId"`
	Explanation string         `json:"explanation,omitempty"`
	Plan        []TurnPlanStep `json:"plan"`
}

// TurnDiffUpdatedNotification carries the latest aggregated turn diff.
type TurnDiffUpdatedNotification struct {
	ThreadID string `json:"threadId"`
	TurnID   string `json:"turnId"`
	Diff     string `json:"diff"`
}

// ThreadTokenUsageUpdatedNotification carries the latest thread token usage snapshot.
type ThreadTokenUsageUpdatedNotification struct {
	ThreadID   string           `json:"threadId"`
	TurnID     string           `json:"turnId"`
	TokenUsage ThreadTokenUsage `json:"tokenUsage"`
}

// ThreadTokenUsage is the schema-backed token-usage snapshot for one thread.
type ThreadTokenUsage struct {
	Last               TokenUsageBreakdown `json:"last"`
	ModelContextWindow *int64              `json:"modelContextWindow"`
	Total              TokenUsageBreakdown `json:"total"`
}

// TokenUsageBreakdown is the schema-backed token usage breakdown.
type TokenUsageBreakdown struct {
	CachedInputTokens     int64 `json:"cachedInputTokens"`
	InputTokens           int64 `json:"inputTokens"`
	OutputTokens          int64 `json:"outputTokens"`
	ReasoningOutputTokens int64 `json:"reasoningOutputTokens"`
	TotalTokens           int64 `json:"totalTokens"`
}

// TurnPlanStep is one app-server plan step entry.
type TurnPlanStep struct {
	Status string `json:"status"`
	Step   string `json:"step"`
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
	ApprovalDecisionApproved           ApprovalDecision = "approved"
	ApprovalDecisionApprovedForSession ApprovalDecision = "approved_for_session"
	ApprovalDecisionDeclined           ApprovalDecision = "declined"
	ApprovalDecisionCancelled          ApprovalDecision = "cancelled"
)

// ApprovalRequest is server-initiated permission payload.
type ApprovalRequest struct {
	ThreadID                        string                   `json:"threadId"`
	TurnID                          string                   `json:"turnId"`
	ApprovalID                      string                   `json:"approvalId,omitempty"`
	ToolCallID                      string                   `json:"toolCallId,omitempty"`
	Kind                            ApprovalKind             `json:"kind"`
	Command                         string                   `json:"command,omitempty"`
	CommandActions                  []CommandAction          `json:"commandActions,omitempty"`
	CWD                             string                   `json:"cwd,omitempty"`
	Files                           []string                 `json:"files,omitempty"`
	Host                            string                   `json:"host,omitempty"`
	Protocol                        string                   `json:"protocol,omitempty"`
	Port                            int                      `json:"port,omitempty"`
	MCPServer                       string                   `json:"mcpServer,omitempty"`
	MCPTool                         string                   `json:"mcpTool,omitempty"`
	Message                         string                   `json:"message,omitempty"`
	ProposedExecpolicyAmendment     []string                 `json:"proposedExecpolicyAmendment,omitempty"`
	ProposedNetworkPolicyAmendments []NetworkPolicyAmendment `json:"proposedNetworkPolicyAmendments,omitempty"`
	WritePath                       string                   `json:"writePath,omitempty"`
	WriteText                       string                   `json:"writeText,omitempty"`
	Patch                           string                   `json:"patch,omitempty"`
}

// ApprovalDecisionResult is sent as JSON-RPC result for approval request.
type ApprovalDecisionResult struct {
	Outcome string `json:"outcome"`
}

// CommandExecutionRequestApprovalParams are server params for one command approval.
type CommandExecutionRequestApprovalParams struct {
	ThreadID                        string                   `json:"threadId"`
	TurnID                          string                   `json:"turnId"`
	ItemID                          string                   `json:"itemId"`
	ApprovalID                      string                   `json:"approvalId,omitempty"`
	Command                         string                   `json:"command,omitempty"`
	CommandActions                  []CommandAction          `json:"commandActions,omitempty"`
	CWD                             string                   `json:"cwd,omitempty"`
	Reason                          string                   `json:"reason,omitempty"`
	NetworkApprovalContext          *NetworkApprovalContext  `json:"networkApprovalContext,omitempty"`
	ProposedExecpolicyAmendment     []string                 `json:"proposedExecpolicyAmendment,omitempty"`
	ProposedNetworkPolicyAmendments []NetworkPolicyAmendment `json:"proposedNetworkPolicyAmendments,omitempty"`
}

// NetworkApprovalContext carries optional host/protocol hints for network approvals.
type NetworkApprovalContext struct {
	Host     string `json:"host"`
	Protocol string `json:"protocol"`
}

// NetworkPolicyAmendment carries one proposed host allow/deny rule.
type NetworkPolicyAmendment struct {
	Action string `json:"action"`
	Host   string `json:"host"`
}

// FileChangeRequestApprovalParams are server params for one file-change approval.
type FileChangeRequestApprovalParams struct {
	ThreadID   string `json:"threadId"`
	TurnID     string `json:"turnId"`
	ItemID     string `json:"itemId"`
	ApprovalID string `json:"approvalId,omitempty"`
	GrantRoot  string `json:"grantRoot,omitempty"`
	Reason     string `json:"reason,omitempty"`
}

// ExecCommandApprovalParams are legacy server params for command approval.
type ExecCommandApprovalParams struct {
	ApprovalID     string   `json:"approvalId,omitempty"`
	CallID         string   `json:"callId"`
	ConversationID string   `json:"conversationId"`
	Command        []string `json:"command"`
	CWD            string   `json:"cwd"`
	Reason         string   `json:"reason,omitempty"`
}

// ApplyPatchApprovalParams are legacy server params for patch approval.
type ApplyPatchApprovalParams struct {
	ApprovalID     string                     `json:"approvalId,omitempty"`
	CallID         string                     `json:"callId"`
	ConversationID string                     `json:"conversationId"`
	FileChanges    map[string]json.RawMessage `json:"fileChanges"`
	GrantRoot      string                     `json:"grantRoot,omitempty"`
	Reason         string                     `json:"reason,omitempty"`
}

// ChatgptAuthTokensRefreshParams are server params for client-managed token refresh.
type ChatgptAuthTokensRefreshParams struct {
	PreviousAccountID string `json:"previousAccountId,omitempty"`
	Reason            string `json:"reason"`
}

// ToolRequestUserInputParams are server params for request_user_input.
type ToolRequestUserInputParams struct {
	ItemID    string                         `json:"itemId"`
	Questions []ToolRequestUserInputQuestion `json:"questions"`
	ThreadID  string                         `json:"threadId"`
	TurnID    string                         `json:"turnId"`
}

// ToolRequestUserInputQuestion is one question presented to the user.
type ToolRequestUserInputQuestion struct {
	Header   string                       `json:"header"`
	ID       string                       `json:"id"`
	IsOther  bool                         `json:"isOther,omitempty"`
	IsSecret bool                         `json:"isSecret,omitempty"`
	Options  []ToolRequestUserInputOption `json:"options,omitempty"`
	Question string                       `json:"question"`
}

// ToolRequestUserInputOption is one selectable option for a question.
type ToolRequestUserInputOption struct {
	Description string `json:"description"`
	Label       string `json:"label"`
}

// ToolRequestUserInputResponse returns chosen answers by question id.
type ToolRequestUserInputResponse struct {
	Answers map[string]ToolRequestUserInputAnswer `json:"answers"`
}

// ToolRequestUserInputAnswer is one selected answer set.
type ToolRequestUserInputAnswer struct {
	Answers []string `json:"answers"`
}

// DynamicToolCallParams are server params for client-side dynamic tool invocation.
type DynamicToolCallParams struct {
	Arguments json.RawMessage `json:"arguments"`
	CallID    string          `json:"callId"`
	ThreadID  string          `json:"threadId"`
	Tool      string          `json:"tool"`
	TurnID    string          `json:"turnId"`
}

// DynamicToolCallResponse returns one dynamic tool invocation result.
type DynamicToolCallResponse struct {
	ContentItems []DynamicToolCallOutputContentItem `json:"contentItems"`
	Success      bool                               `json:"success"`
}

// DynamicToolCallOutputContentItem carries text or image output for one dynamic tool call.
type DynamicToolCallOutputContentItem struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	ImageURL string `json:"imageUrl,omitempty"`
}

// ToolOutputContentItem is one normalized downstream tool output entry.
type ToolOutputContentItem struct {
	Type     string
	Text     string
	Data     string
	MimeType string
	URI      string
}

// ToolExecution describes one runtime tool call emitted by app-server items.
type ToolExecution struct {
	ID           string
	Kind         string
	Tool         string
	Server       string
	Status       string
	Success      *bool
	ContentItems []ToolOutputContentItem
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
	Output            string            `json:"output,omitempty"`
	Content           []json.RawMessage `json:"content,omitempty"`
	StructuredContent any               `json:"structuredContent,omitempty"`
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
	// TurnEventTypePlanDelta indicates downstream streamed one plan item delta.
	TurnEventTypePlanDelta TurnEventType = "plan_delta"
	// TurnEventTypePlanUpdated indicates the downstream plan snapshot changed.
	TurnEventTypePlanUpdated TurnEventType = "plan_updated"
	// TurnEventTypeReasoningDelta indicates downstream streamed reasoning text.
	TurnEventTypeReasoningDelta TurnEventType = "reasoning_delta"
	// TurnEventTypeCommandExecutionDelta indicates downstream streamed command output text.
	TurnEventTypeCommandExecutionDelta TurnEventType = "command_execution_delta"
	// TurnEventTypeDiffUpdated indicates downstream streamed aggregated turn diff.
	TurnEventTypeDiffUpdated TurnEventType = "diff_updated"
	// TurnEventTypeTokenUsageUpdated indicates downstream reported thread token usage.
	TurnEventTypeTokenUsageUpdated TurnEventType = "token_usage_updated"
	// TurnEventTypeBackendError indicates downstream reported a turn error notification.
	TurnEventTypeBackendError TurnEventType = "backend_error"
)

// TurnEvent is emitted to ACP session/prompt handler.
type TurnEvent struct {
	Type       TurnEventType
	ThreadID   string
	TurnID     string
	ItemID     string
	ItemType   string
	ItemText   string
	Command    *CommandExecution
	Tool       *ToolExecution
	TokenUsage *ThreadTokenUsage
	Diff       string
	Delta      string
	StopReason string
	Message    string
	Approval   ApprovalRequest
	Plan       []TurnPlanStep
	WillRetry  bool
}

const (
	methodInitialized                         = "initialized"
	methodThreadList                          = "thread/list"
	methodThreadResume                        = "thread/resume"
	methodThreadStart                         = "thread/start"
	methodThreadCompact                       = "thread/compact/start"
	methodTurnStart                           = "turn/start"
	methodReviewStart                         = "review/start"
	methodTurnInterrupt                       = "turn/interrupt"
	methodModelList                           = "model/list"
	methodApprovalReq                         = "approval/request" // legacy approval request.
	methodItemCommandExecutionRequestApproval = "item/commandExecution/requestApproval"
	methodItemFileChangeRequestApproval       = "item/fileChange/requestApproval"
	methodItemToolRequestUserInput            = "item/tool/requestUserInput"
	methodItemToolCall                        = "item/tool/call"
	methodAccountChatgptAuthTokensRefresh     = "account/chatgptAuthTokens/refresh"
	methodApplyPatchApproval                  = "applyPatchApproval"
	methodExecCommandApproval                 = "execCommandApproval"
	methodMCPServerList                       = "mcpServer/list"
	methodMCPServerCall                       = "mcpServer/call"
	methodMCPOAuthLogin                       = "mcpServer/oauth/login"
	methodAuthLogout                          = "auth/logout"
	methodAccountLogout                       = "account/logout"

	notificationTurnStarted                     = "turn/started"
	notificationTurnUpdate                      = "turn/update"
	notificationThreadTokenUsageUpdated         = "thread/tokenUsage/updated"
	notificationError                           = "error"
	notificationItemStarted                     = "item/started"
	notificationItemCompleted                   = "item/completed"
	notificationItemAgentMessageDelta           = "item/agentMessage/delta"
	notificationItemPlanDelta                   = "item/plan/delta"
	notificationItemReasoningSummaryTextDelta   = "item/reasoning/summaryTextDelta"
	notificationItemReasoningSummaryPartAdded   = "item/reasoning/summaryPartAdded"
	notificationItemReasoningTextDelta          = "item/reasoning/textDelta"
	notificationItemCommandExecutionOutputDelta = "item/commandExecution/outputDelta"
	notificationTurnDiffUpdated                 = "turn/diff/updated"
	notificationTurnCompleted                   = "turn/completed"
	notificationReviewModeEntered               = "review/mode_entered"
	notificationReviewModeExited                = "review/mode_exited"
	notificationTurnPlanUpdated                 = "turn/plan/updated"
)
