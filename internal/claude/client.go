// Package claude provides an appClient implementation backed by the Anthropic API.
package claude

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"


	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/beyond5959/codex-acp/internal/appserver"
)

// approvalRegistry tracks in-flight approval channels keyed by approvalID.
type approvalRegistry struct {
	mu        sync.Mutex
	pending   map[string]chan appserver.ApprovalDecision
	decisions map[string]toolDecision // last resolved decision per approvalID
}

type toolDecision struct {
	decision  appserver.ApprovalDecision
	toolUseID string
	toolName  string
	input     []byte
}

func newApprovalRegistry() *approvalRegistry {
	return &approvalRegistry{
		pending:   make(map[string]chan appserver.ApprovalDecision),
		decisions: make(map[string]toolDecision),
	}
}

func (r *approvalRegistry) register(approvalID string, ch chan appserver.ApprovalDecision) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pending[approvalID] = ch
}

func (r *approvalRegistry) remove(approvalID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.pending, approvalID)
}

func (r *approvalRegistry) respond(approvalID string, decision appserver.ApprovalDecision) error {
	r.mu.Lock()
	ch, ok := r.pending[approvalID]
	r.mu.Unlock()
	if !ok {
		return fmt.Errorf("claude: unknown approval id %q", approvalID)
	}
	select {
	case ch <- decision:
		return nil
	default:
		return fmt.Errorf("claude: approval channel full for %q", approvalID)
	}
}

func (r *approvalRegistry) storeDecision(approvalID string, d appserver.ApprovalDecision, toolUseID, toolName string, input []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.decisions[approvalID] = toolDecision{decision: d, toolUseID: toolUseID, toolName: toolName, input: input}
}

// turnCancel holds a cancel function for an active turn, keyed by turnID.
type turnCancel struct {
	mu      sync.Mutex
	cancels map[string]context.CancelFunc
}

func newTurnCancel() *turnCancel {
	return &turnCancel{cancels: make(map[string]context.CancelFunc)}
}

func (tc *turnCancel) register(turnID string, cancel context.CancelFunc) {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	tc.cancels[turnID] = cancel
}

func (tc *turnCancel) cancel(turnID string) {
	tc.mu.Lock()
	cancel, ok := tc.cancels[turnID]
	tc.mu.Unlock()
	if ok {
		cancel()
	}
}

func (tc *turnCancel) remove(turnID string) {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	delete(tc.cancels, turnID)
}

// Client implements the internal acp.appClient interface using Anthropic API.
type Client struct {
	cfg       Config
	api       anthropic.Client
	sessions  *sessionStore
	approvals *approvalRegistry
	turns     *turnCancel
	nextID    atomic.Int64
	// loggedOut tracks whether auth has been cleared via /logout.
	loggedOut atomic.Bool
}

// NewClient creates a new Claude API-backed client.
func NewClient(cfg Config) *Client {
	opts := []option.RequestOption{
		option.WithAuthToken(cfg.AuthToken),
	}
	if cfg.BaseURL != "" {
		opts = append(opts, option.WithBaseURL(cfg.BaseURL))
	}
	return &Client{
		cfg:       cfg,
		api:       anthropic.NewClient(opts...),
		sessions:  newSessionStore(),
		approvals: newApprovalRegistry(),
		turns:     newTurnCancel(),
	}
}

func (c *Client) genID(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, c.nextID.Add(1))
}

// ThreadStart creates a new in-memory conversation thread.
func (c *Client) ThreadStart(ctx context.Context, cwd string, _ appserver.RunOptions) (string, error) {
	if c.loggedOut.Load() {
		return "", fmt.Errorf("claude: not authenticated — use ANTHROPIC_AUTH_TOKEN or restart adapter")
	}
	threadID := c.genID("thread")
	c.sessions.Create(threadID, cwd)
	return threadID, nil
}

// TurnStart begins a streaming turn against the Anthropic API.
func (c *Client) TurnStart(
	ctx context.Context,
	threadID string,
	input []appserver.UserInput,
	options appserver.RunOptions,
) (string, <-chan appserver.TurnEvent, error) {
	if c.loggedOut.Load() {
		return "", nil, fmt.Errorf("claude: not authenticated")
	}
	sess, ok := c.sessions.Get(threadID)
	if !ok {
		return "", nil, fmt.Errorf("claude: unknown thread %q", threadID)
	}

	// Build user message content from UserInput items.
	userContent, err := buildUserContent(input)
	if err != nil {
		return "", nil, fmt.Errorf("claude: build user content: %w", err)
	}

	// Append user message to history before streaming.
	sess.AppendUser(userContent)

	turnID := c.genID("turn")
	turnCtx, cancel := context.WithCancel(ctx)
	c.turns.register(turnID, cancel)

	model := resolveModel(options.Model, c.cfg.DefaultModel)
	system := options.SystemInstructions
	if options.Personality != "" && system == "" {
		system = options.Personality
	}

	params := anthropic.MessageNewParams{
		Model:     anthropic.Model(model),
		MaxTokens: c.cfg.MaxTokens,
		Messages:  sess.Messages(),
	}
	if system != "" {
		params.System = []anthropic.TextBlockParam{{Text: system}}
	}

	stream := c.api.Messages.NewStreaming(turnCtx, params)

	out := make(chan appserver.TurnEvent, 64)
	go func() {
		defer cancel()
		defer c.turns.remove(turnID)
		streamToEvents(turnCtx, stream, threadID, turnID, c.approvals, out)
		// Persist the accumulated response to history.
		var acc anthropic.Message
		sess.AppendAssistant(acc)
	}()

	return turnID, out, nil
}

// ReviewStart starts a review workflow using a specialised system prompt.
func (c *Client) ReviewStart(
	ctx context.Context,
	threadID string,
	instructions string,
	options appserver.RunOptions,
) (string, <-chan appserver.TurnEvent, error) {
	if c.loggedOut.Load() {
		return "", nil, fmt.Errorf("claude: not authenticated")
	}
	sess, ok := c.sessions.Get(threadID)
	if !ok {
		return "", nil, fmt.Errorf("claude: unknown thread %q", threadID)
	}

	reviewPrompt := buildReviewPrompt(instructions)
	sess.AppendUser([]anthropic.ContentBlockParamUnion{
		anthropicTextBlock(reviewPrompt),
	})

	turnID := c.genID("review-turn")
	turnCtx, cancel := context.WithCancel(ctx)
	c.turns.register(turnID, cancel)

	model := resolveModel(options.Model, c.cfg.DefaultModel)
	params := anthropic.MessageNewParams{
		Model:     anthropic.Model(model),
		MaxTokens: c.cfg.MaxTokens,
		Messages:  sess.Messages(),
		System: []anthropic.TextBlockParam{{
			Text: reviewSystemPrompt,
		}},
	}

	stream := c.api.Messages.NewStreaming(turnCtx, params)

	outWrapped := make(chan appserver.TurnEvent, 64)
	go func() {
		defer cancel()
		defer c.turns.remove(turnID)
		defer close(outWrapped)

		// Emit review mode entered.
		outWrapped <- appserver.TurnEvent{
			Type:     appserver.TurnEventTypeReviewModeEntered,
			ThreadID: threadID,
			TurnID:   turnID,
		}

		// Use a pipe goroutine to forward from the inner stream channel to outWrapped.
		inner := make(chan appserver.TurnEvent, 64)
		done := make(chan struct{})
		go func() {
			defer close(done)
			for ev := range inner {
				outWrapped <- ev
			}
		}()
		streamToEvents(turnCtx, stream, threadID, turnID, c.approvals, inner)
		<-done // wait until all events forwarded

		// Emit review mode exited.
		outWrapped <- appserver.TurnEvent{
			Type:     appserver.TurnEventTypeReviewModeExited,
			ThreadID: threadID,
			TurnID:   turnID,
		}
	}()

	return turnID, outWrapped, nil
}

// CompactStart compresses the thread history by asking Claude to summarise it.
func (c *Client) CompactStart(ctx context.Context, threadID string) (string, <-chan appserver.TurnEvent, error) {
	if c.loggedOut.Load() {
		return "", nil, fmt.Errorf("claude: not authenticated")
	}
	sess, ok := c.sessions.Get(threadID)
	if !ok {
		return "", nil, fmt.Errorf("claude: unknown thread %q", threadID)
	}

	turnID := c.genID("compact-turn")
	turnCtx, cancel := context.WithCancel(ctx)
	c.turns.register(turnID, cancel)

	params := anthropic.MessageNewParams{
		Model:     anthropic.Model(c.cfg.DefaultModel),
		MaxTokens: c.cfg.MaxTokens,
		Messages:  sess.Messages(),
		System: []anthropic.TextBlockParam{{
			Text: compactSystemPrompt,
		}},
	}

	stream := c.api.Messages.NewStreaming(turnCtx, params)

	out := make(chan appserver.TurnEvent, 64)
	go func() {
		defer cancel()
		defer c.turns.remove(turnID)

		// Collect the summary text while emitting deltas.
		var sb strings.Builder
		proxy := make(chan appserver.TurnEvent, 64)
		go func() {
			for ev := range proxy {
				if ev.Type == appserver.TurnEventTypeAgentMessageDelta {
					sb.WriteString(ev.Delta)
				}
				out <- ev
			}
			// Replace history with a single-message summary.
			summary := sb.String()
			if summary != "" {
				sess.Replace([]anthropic.MessageParam{
					{
						Role: anthropic.MessageParamRoleUser,
						Content: []anthropic.ContentBlockParamUnion{
							anthropicTextBlock("[Previous conversation summary]\n" + summary),
						},
					},
				})
			}
			close(out)
		}()
		streamToEvents(turnCtx, stream, threadID, turnID, c.approvals, proxy)
	}()

	return turnID, out, nil
}

// TurnInterrupt cancels an active turn.
func (c *Client) TurnInterrupt(_ context.Context, _, turnID string) error {
	c.turns.cancel(turnID)
	return nil
}

// ApprovalRespond routes a permission decision to the waiting stream goroutine.
func (c *Client) ApprovalRespond(_ context.Context, approvalID string, decision appserver.ApprovalDecision) error {
	return c.approvals.respond(approvalID, decision)
}

// MCPServersList returns an empty list; MCP servers are managed externally.
func (c *Client) MCPServersList(_ context.Context) ([]appserver.MCPServer, error) {
	return []appserver.MCPServer{}, nil
}

// MCPToolCall is not supported in direct Anthropic API mode.
func (c *Client) MCPToolCall(_ context.Context, params appserver.MCPToolCallParams) (appserver.MCPToolCallResult, error) {
	return appserver.MCPToolCallResult{}, fmt.Errorf("claude: MCP tool call not supported in direct API mode; use claude tools instead")
}

// MCPOAuthLogin is not applicable in direct API mode.
func (c *Client) MCPOAuthLogin(_ context.Context, server string) (appserver.MCPOAuthLoginResult, error) {
	return appserver.MCPOAuthLoginResult{
		Status:  "not_supported",
		Message: "Claude direct API mode does not support MCP OAuth; configure ANTHROPIC_AUTH_TOKEN instead",
	}, nil
}

// Logout clears the local auth state.
func (c *Client) Logout(_ context.Context) error {
	c.loggedOut.Store(true)
	c.cfg.AuthToken = ""
	return nil
}

// ---- helpers ----

func resolveModel(sessionModel, defaultModel string) string {
	if sessionModel != "" {
		return sessionModel
	}
	if defaultModel != "" {
		return defaultModel
	}
	return DefaultModel
}

func buildUserContent(inputs []appserver.UserInput) ([]anthropic.ContentBlockParamUnion, error) {
	var blocks []anthropic.ContentBlockParamUnion
	for _, inp := range inputs {
		switch inp.Type {
		case "text":
			blocks = append(blocks, anthropicTextBlock(inp.Text))
		case "mention":
			// Mention: embed as text with path prefix.
			content := fmt.Sprintf("[File: %s]\n%s", inp.Path, inp.Text)
			blocks = append(blocks, anthropicTextBlock(content))
		case "image":
			block, err := buildImageBlock(inp)
			if err != nil {
				return nil, err
			}
			blocks = append(blocks, block)
		case "localimage":
			block, err := buildImageBlock(inp)
			if err != nil {
				return nil, err
			}
			blocks = append(blocks, block)
		default:
			// Unknown type: pass text if present.
			if inp.Text != "" {
				blocks = append(blocks, anthropicTextBlock(inp.Text))
			}
		}
	}
	if len(blocks) == 0 {
		blocks = []anthropic.ContentBlockParamUnion{anthropicTextBlock("")}
	}
	return blocks, nil
}

func buildImageBlock(inp appserver.UserInput) (anthropic.ContentBlockParamUnion, error) {
	// inp.Text may carry base64 data (for image type), inp.Path for localimage.
	data := inp.Text
	if data == "" {
		data = inp.URL
	}
	if data == "" {
		return anthropic.ContentBlockParamUnion{}, fmt.Errorf("claude: image input has no data")
	}
	// Strip data: URI prefix if present.
	mimeType := "image/png"
	if idx := strings.Index(data, ";"); idx > 0 {
		if after, ok := strings.CutPrefix(data[:idx], "data:"); ok {
			mimeType = after
		}
		if rest, ok := strings.CutPrefix(data[idx+1:], "base64,"); ok {
			data = rest
		}
	}
	// Validate base64.
	if _, err := base64.StdEncoding.DecodeString(data); err != nil {
		if _, err2 := base64.RawStdEncoding.DecodeString(data); err2 != nil {
			return anthropic.ContentBlockParamUnion{}, fmt.Errorf("claude: invalid base64 image data")
		}
	}
	return anthropic.NewImageBlockBase64(mimeType, data), nil
}

func anthropicTextBlock(text string) anthropic.ContentBlockParamUnion {
	return anthropic.NewTextBlock(text)
}

func buildReviewPrompt(instructions string) string {
	if instructions == "" {
		return "Please review the code changes in this conversation. Provide a structured review with:\n" +
			"1. Summary of changes\n" +
			"2. Issues found (if any)\n" +
			"3. Suggestions for improvement\n" +
			"4. Overall assessment"
	}
	return fmt.Sprintf("Please review according to these instructions: %s\n\n"+
		"Provide a structured review with:\n"+
		"1. Summary of changes\n"+
		"2. Issues found (if any)\n"+
		"3. Suggestions for improvement\n"+
		"4. Overall assessment", instructions)
}

const reviewSystemPrompt = `You are a code reviewer. When asked to review code, provide a thorough, constructive review.
Format your review with clear sections. Be specific about line numbers and file names when relevant.
After producing your review, end with a brief summary of whether changes are recommended.`

const compactSystemPrompt = `You are a conversation summarizer. Summarize the conversation history provided in the messages.
Produce a concise summary that preserves:
- Key decisions made
- Important context established
- Current task state
- Any pending action items

The summary will replace the full history to reduce context length.`

// Ensure Client satisfies the appClient interface at compile time.
// This import-cycle-safe check is done via a blank var in the acp package;
// here we just declare the interface inline to keep the package standalone.
var _ interface {
	ThreadStart(ctx context.Context, cwd string, options appserver.RunOptions) (string, error)
	TurnStart(ctx context.Context, threadID string, input []appserver.UserInput, options appserver.RunOptions) (string, <-chan appserver.TurnEvent, error)
	ReviewStart(ctx context.Context, threadID string, instructions string, options appserver.RunOptions) (string, <-chan appserver.TurnEvent, error)
	CompactStart(ctx context.Context, threadID string) (string, <-chan appserver.TurnEvent, error)
	TurnInterrupt(ctx context.Context, threadID, turnID string) error
	ApprovalRespond(ctx context.Context, approvalID string, decision appserver.ApprovalDecision) error
	MCPServersList(ctx context.Context) ([]appserver.MCPServer, error)
	MCPToolCall(ctx context.Context, params appserver.MCPToolCallParams) (appserver.MCPToolCallResult, error)
	MCPOAuthLogin(ctx context.Context, server string) (appserver.MCPOAuthLoginResult, error)
	Logout(ctx context.Context) error
} = (*Client)(nil)

