package pi

import (
	"encoding/json"
	"strings"
)

type rpcEnvelope struct {
	ID      string          `json:"id,omitempty"`
	Type    string          `json:"type"`
	Command string          `json:"command,omitempty"`
	Success *bool           `json:"success,omitempty"`
	Error   string          `json:"error,omitempty"`
	Data    json.RawMessage `json:"data,omitempty"`
}

type rpcImageContent struct {
	Type     string `json:"type"`
	Data     string `json:"data"`
	MimeType string `json:"mimeType"`
}

type rpcPromptCommand struct {
	ID                string            `json:"id,omitempty"`
	Type              string            `json:"type"`
	Message           string            `json:"message,omitempty"`
	Images            []rpcImageContent `json:"images,omitempty"`
	StreamingBehavior string            `json:"streamingBehavior,omitempty"`
}

type rpcSetModelCommand struct {
	ID       string `json:"id,omitempty"`
	Type     string `json:"type"`
	Provider string `json:"provider"`
	ModelID  string `json:"modelId"`
}

type rpcSetThinkingCommand struct {
	ID    string `json:"id,omitempty"`
	Type  string `json:"type"`
	Level string `json:"level"`
}

type rpcSwitchSessionCommand struct {
	ID          string `json:"id,omitempty"`
	Type        string `json:"type"`
	SessionPath string `json:"sessionPath"`
}

type rpcExtensionUIResponse struct {
	Type      string `json:"type"`
	ID        string `json:"id"`
	Confirmed *bool  `json:"confirmed,omitempty"`
	Cancelled bool   `json:"cancelled,omitempty"`
}

type rpcState struct {
	Model                 *rpcModel `json:"model,omitempty"`
	ThinkingLevel         string    `json:"thinkingLevel,omitempty"`
	IsStreaming           bool      `json:"isStreaming,omitempty"`
	IsCompacting          bool      `json:"isCompacting,omitempty"`
	SessionFile           string    `json:"sessionFile,omitempty"`
	SessionID             string    `json:"sessionId,omitempty"`
	SessionName           string    `json:"sessionName,omitempty"`
	AutoCompactionEnabled bool      `json:"autoCompactionEnabled,omitempty"`
	MessageCount          int       `json:"messageCount,omitempty"`
	PendingMessageCount   int       `json:"pendingMessageCount,omitempty"`
}

type rpcModel struct {
	ID            string          `json:"id,omitempty"`
	Name          string          `json:"name,omitempty"`
	Provider      string          `json:"provider,omitempty"`
	Reasoning     bool            `json:"reasoning,omitempty"`
	ContextWindow int64           `json:"contextWindow,omitempty"`
	Input         []string        `json:"input,omitempty"`
	Cost          *rpcModelCost   `json:"cost,omitempty"`
	Extra         json.RawMessage `json:"-"`
}

type rpcModelCost struct {
	Input      float64 `json:"input,omitempty"`
	Output     float64 `json:"output,omitempty"`
	CacheRead  float64 `json:"cacheRead,omitempty"`
	CacheWrite float64 `json:"cacheWrite,omitempty"`
}

type rpcSessionStats struct {
	SessionFile   string           `json:"sessionFile,omitempty"`
	SessionID     string           `json:"sessionId,omitempty"`
	UserMessages  int64            `json:"userMessages,omitempty"`
	AssistantMsgs int64            `json:"assistantMessages,omitempty"`
	ToolCalls     int64            `json:"toolCalls,omitempty"`
	ToolResults   int64            `json:"toolResults,omitempty"`
	TotalMessages int64            `json:"totalMessages,omitempty"`
	Tokens        rpcUsageTokens   `json:"tokens,omitempty"`
	Cost          float64          `json:"cost,omitempty"`
	ContextUsage  *rpcContextUsage `json:"contextUsage,omitempty"`
}

type rpcUsageTokens struct {
	Input      int64 `json:"input,omitempty"`
	Output     int64 `json:"output,omitempty"`
	CacheRead  int64 `json:"cacheRead,omitempty"`
	CacheWrite int64 `json:"cacheWrite,omitempty"`
	Total      int64 `json:"total,omitempty"`
}

type rpcContextUsage struct {
	Tokens        *int64   `json:"tokens"`
	ContextWindow int64    `json:"contextWindow,omitempty"`
	Percent       *float64 `json:"percent"`
}

type rpcCommandResult struct {
	Cancelled bool `json:"cancelled,omitempty"`
}

type rpcForkResult struct {
	Text      string `json:"text,omitempty"`
	Cancelled bool   `json:"cancelled,omitempty"`
}

type rpcGetCommandsResult struct {
	Commands []rpcSlashCommand `json:"commands,omitempty"`
}

type rpcSlashCommand struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Source      string         `json:"source,omitempty"`
	SourceInfo  *rpcSourceInfo `json:"sourceInfo,omitempty"`
}

type rpcSourceInfo struct {
	Path     string `json:"path,omitempty"`
	Location string `json:"location,omitempty"`
}

type rpcGetMessagesResult struct {
	Messages []rpcAgentMessage `json:"messages,omitempty"`
}

type rpcAgentEndEvent struct {
	Type     string            `json:"type"`
	Messages []rpcAgentMessage `json:"messages,omitempty"`
}

type rpcTurnEndEvent struct {
	Type        string            `json:"type"`
	Message     *rpcAgentMessage  `json:"message,omitempty"`
	ToolResults []rpcAgentMessage `json:"toolResults,omitempty"`
}

type rpcMessageUpdateEvent struct {
	Type                  string                    `json:"type"`
	Message               *rpcAgentMessage          `json:"message,omitempty"`
	AssistantMessageEvent *rpcAssistantMessageEvent `json:"assistantMessageEvent,omitempty"`
}

type rpcAssistantMessageEvent struct {
	Type         string            `json:"type"`
	ContentIndex int               `json:"contentIndex,omitempty"`
	Delta        string            `json:"delta,omitempty"`
	Reason       string            `json:"reason,omitempty"`
	ToolCall     *rpcToolCallBlock `json:"toolCall,omitempty"`
}

type rpcToolExecutionStartEvent struct {
	Type       string          `json:"type"`
	ToolCallID string          `json:"toolCallId"`
	ToolName   string          `json:"toolName"`
	Args       json.RawMessage `json:"args,omitempty"`
}

type rpcToolExecutionUpdateEvent struct {
	Type          string          `json:"type"`
	ToolCallID    string          `json:"toolCallId"`
	ToolName      string          `json:"toolName"`
	Args          json.RawMessage `json:"args,omitempty"`
	PartialResult *rpcToolResult  `json:"partialResult,omitempty"`
}

type rpcToolExecutionEndEvent struct {
	Type       string         `json:"type"`
	ToolCallID string         `json:"toolCallId"`
	ToolName   string         `json:"toolName"`
	Result     *rpcToolResult `json:"result,omitempty"`
	IsError    bool           `json:"isError,omitempty"`
}

type rpcExtensionUIRequest struct {
	Type       string   `json:"type"`
	ID         string   `json:"id"`
	Method     string   `json:"method"`
	Title      string   `json:"title,omitempty"`
	Message    string   `json:"message,omitempty"`
	Options    []string `json:"options,omitempty"`
	NotifyType string   `json:"notifyType,omitempty"`
}

type rpcCompactionEndEvent struct {
	Type         string               `json:"type"`
	Reason       string               `json:"reason,omitempty"`
	Result       *rpcCompactionResult `json:"result,omitempty"`
	Aborted      bool                 `json:"aborted,omitempty"`
	WillRetry    bool                 `json:"willRetry,omitempty"`
	ErrorMessage string               `json:"errorMessage,omitempty"`
}

type rpcCompactionResult struct {
	Summary          string          `json:"summary,omitempty"`
	FirstKeptEntryID string          `json:"firstKeptEntryId,omitempty"`
	TokensBefore     int64           `json:"tokensBefore,omitempty"`
	Details          json.RawMessage `json:"details,omitempty"`
}

type rpcAutoRetryStartEvent struct {
	Type         string `json:"type"`
	Attempt      int    `json:"attempt,omitempty"`
	MaxAttempts  int    `json:"maxAttempts,omitempty"`
	DelayMS      int64  `json:"delayMs,omitempty"`
	ErrorMessage string `json:"errorMessage,omitempty"`
}

type rpcAutoRetryEndEvent struct {
	Type       string `json:"type"`
	Success    bool   `json:"success,omitempty"`
	Attempt    int    `json:"attempt,omitempty"`
	FinalError string `json:"finalError,omitempty"`
}

type rpcAgentMessage struct {
	Role         string             `json:"role"`
	Content      json.RawMessage    `json:"content,omitempty"`
	Provider     string             `json:"provider,omitempty"`
	Model        string             `json:"model,omitempty"`
	StopReason   string             `json:"stopReason,omitempty"`
	ErrorMessage string             `json:"errorMessage,omitempty"`
	Usage        *rpcAssistantUsage `json:"usage,omitempty"`
	Timestamp    int64              `json:"timestamp,omitempty"`
	ToolCallID   string             `json:"toolCallId,omitempty"`
	ToolName     string             `json:"toolName,omitempty"`
	IsError      bool               `json:"isError,omitempty"`
	Details      json.RawMessage    `json:"details,omitempty"`
}

type rpcAssistantUsage struct {
	Input      int64         `json:"input,omitempty"`
	Output     int64         `json:"output,omitempty"`
	CacheRead  int64         `json:"cacheRead,omitempty"`
	CacheWrite int64         `json:"cacheWrite,omitempty"`
	Cost       *rpcUsageCost `json:"cost,omitempty"`
}

type rpcUsageCost struct {
	Input      float64 `json:"input,omitempty"`
	Output     float64 `json:"output,omitempty"`
	CacheRead  float64 `json:"cacheRead,omitempty"`
	CacheWrite float64 `json:"cacheWrite,omitempty"`
	Total      float64 `json:"total,omitempty"`
}

type rpcContentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	Thinking  string          `json:"thinking,omitempty"`
	Data      string          `json:"data,omitempty"`
	MimeType  string          `json:"mimeType,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

type rpcToolCallBlock struct {
	Type      string          `json:"type"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

type rpcToolResult struct {
	Content []rpcContentBlock `json:"content,omitempty"`
	Details json.RawMessage   `json:"details,omitempty"`
}

type gateRequestPayload struct {
	Gate       string   `json:"gate"`
	Version    int      `json:"version"`
	ToolCallID string   `json:"toolCallId"`
	ToolName   string   `json:"toolName"`
	Approval   string   `json:"approval"`
	Command    string   `json:"command,omitempty"`
	Files      []string `json:"files,omitempty"`
	Message    string   `json:"message,omitempty"`
}

type diskSessionHeader struct {
	Type          string `json:"type"`
	ID            string `json:"id"`
	Timestamp     string `json:"timestamp,omitempty"`
	CWD           string `json:"cwd,omitempty"`
	ParentSession string `json:"parentSession,omitempty"`
}

type diskSessionInfoEntry struct {
	Type string `json:"type"`
	Name string `json:"name,omitempty"`
}

type diskSessionMessageEntry struct {
	Type     string          `json:"type"`
	ID       string          `json:"id,omitempty"`
	ParentID string          `json:"parentId,omitempty"`
	Message  rpcAgentMessage `json:"message"`
}

func (m *rpcModel) FullID() string {
	if m == nil {
		return ""
	}
	modelID := strings.TrimSpace(m.ID)
	provider := strings.TrimSpace(m.Provider)
	switch {
	case provider != "" && modelID != "":
		return provider + "/" + modelID
	case modelID != "":
		return modelID
	default:
		return ""
	}
}
