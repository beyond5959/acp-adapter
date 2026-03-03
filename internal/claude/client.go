// Package claudecli provides an appClient implementation backed by the claude CLI subprocess.
package claude

import (
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/beyond5959/codex-acp/internal/appserver"
)

// session holds per-thread state for the claude CLI adapter.
type session struct {
	mu        sync.Mutex
	cwd       string
	sessionID string // claude CLI --session-id / --resume value
	turnCount int    // number of turns completed; 0 = first turn
	loggedOut bool
}

func (s *session) init(cwd string) {
	s.cwd = cwd
	s.sessionID = newRandomID()
}

func newRandomID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	// Format as UUID v4: xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// sessionStore manages per-thread sessions.
type sessionStore struct {
	mu       sync.Mutex
	sessions map[string]*session
}

func newSessionStore() *sessionStore {
	return &sessionStore{sessions: make(map[string]*session)}
}

func (ss *sessionStore) Create(threadID, cwd string) *session {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	s := &session{}
	s.init(cwd)
	ss.sessions[threadID] = s
	return s
}

func (ss *sessionStore) Get(threadID string) (*session, bool) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	s, ok := ss.sessions[threadID]
	return s, ok
}

// turnRegistry tracks active subprocess handles for cancellation.
type turnRegistry struct {
	mu    sync.Mutex
	cmds  map[string]*exec.Cmd
}

func newTurnRegistry() *turnRegistry {
	return &turnRegistry{cmds: make(map[string]*exec.Cmd)}
}

func (tr *turnRegistry) register(turnID string, cmd *exec.Cmd) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	tr.cmds[turnID] = cmd
}

func (tr *turnRegistry) remove(turnID string) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	delete(tr.cmds, turnID)
}

func (tr *turnRegistry) kill(turnID string) {
	tr.mu.Lock()
	cmd, ok := tr.cmds[turnID]
	tr.mu.Unlock()
	if ok && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

// Client implements the appClient interface using the claude CLI subprocess.
type Client struct {
	cfg       Config
	sessions  *sessionStore
	turns     *turnRegistry
	nextID    atomic.Int64
	loggedOut atomic.Bool
}

// NewClient creates a new claude CLI-backed client.
func NewClient(cfg Config) *Client {
	if cfg.ClaudeBin == "" {
		cfg.ClaudeBin = "claude"
	}
	if cfg.DefaultModel == "" {
		cfg.DefaultModel = DefaultModel
	}
	if cfg.MaxTurns <= 0 {
		cfg.MaxTurns = DefaultMaxTurns
	}
	return &Client{
		cfg:      cfg,
		sessions: newSessionStore(),
		turns:    newTurnRegistry(),
	}
}

func (c *Client) genID(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, c.nextID.Add(1))
}

// ThreadStart creates a new conversation thread.
func (c *Client) ThreadStart(_ context.Context, cwd string, _ appserver.RunOptions) (string, error) {
	if c.loggedOut.Load() {
		return "", fmt.Errorf("claudecli: not authenticated — run 'claude auth login' or set CLAUDE_BIN")
	}
	threadID := c.genID("thread")
	c.sessions.Create(threadID, cwd)
	return threadID, nil
}

// TurnStart begins a streaming turn via a claude -p subprocess.
func (c *Client) TurnStart(
	ctx context.Context,
	threadID string,
	input []appserver.UserInput,
	options appserver.RunOptions,
) (string, <-chan appserver.TurnEvent, error) {
	if c.loggedOut.Load() {
		return "", nil, fmt.Errorf("claudecli: not authenticated")
	}
	sess, ok := c.sessions.Get(threadID)
	if !ok {
		return "", nil, fmt.Errorf("claudecli: unknown thread %q", threadID)
	}

	prompt := buildPrompt(input)
	turnID := c.genID("turn")

	system := options.SystemInstructions
	if options.Personality != "" && system == "" {
		system = options.Personality
	}

	cmd, err := c.buildCmd(ctx, sess, options.Model, system, prompt, "")
	if err != nil {
		return "", nil, fmt.Errorf("claudecli: build cmd: %w", err)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", nil, fmt.Errorf("claudecli: stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return "", nil, fmt.Errorf("claudecli: start: %w", err)
	}

	sess.mu.Lock()
	sess.turnCount++
	sess.mu.Unlock()

	c.turns.register(turnID, cmd)

	out := make(chan appserver.TurnEvent, 64)
	go func() {
		defer c.turns.remove(turnID)
		streamToEvents(ctx, stdout, threadID, turnID, out)
		_ = cmd.Wait()
	}()

	return turnID, out, nil
}

// ReviewStart starts a review workflow using an appended system prompt.
func (c *Client) ReviewStart(
	ctx context.Context,
	threadID string,
	instructions string,
	options appserver.RunOptions,
) (string, <-chan appserver.TurnEvent, error) {
	if c.loggedOut.Load() {
		return "", nil, fmt.Errorf("claudecli: not authenticated")
	}
	sess, ok := c.sessions.Get(threadID)
	if !ok {
		return "", nil, fmt.Errorf("claudecli: unknown thread %q", threadID)
	}

	prompt := buildReviewPrompt(instructions)
	turnID := c.genID("review-turn")

	system := reviewSystemPrompt
	if options.SystemInstructions != "" {
		system = options.SystemInstructions + "\n\n" + reviewSystemPrompt
	}

	cmd, err := c.buildCmd(ctx, sess, options.Model, system, prompt, "")
	if err != nil {
		return "", nil, fmt.Errorf("claudecli: build review cmd: %w", err)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", nil, fmt.Errorf("claudecli: stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return "", nil, fmt.Errorf("claudecli: start: %w", err)
	}

	sess.mu.Lock()
	sess.turnCount++
	sess.mu.Unlock()

	c.turns.register(turnID, cmd)

	outWrapped := make(chan appserver.TurnEvent, 64)
	go func() {
		defer c.turns.remove(turnID)
		defer close(outWrapped)

		outWrapped <- appserver.TurnEvent{
			Type:     appserver.TurnEventTypeReviewModeEntered,
			ThreadID: threadID,
			TurnID:   turnID,
		}

		inner := make(chan appserver.TurnEvent, 64)
		done := make(chan struct{})
		go func() {
			defer close(done)
			for ev := range inner {
				outWrapped <- ev
			}
		}()
		streamToEvents(ctx, stdout, threadID, turnID, inner)
		<-done

		outWrapped <- appserver.TurnEvent{
			Type:     appserver.TurnEventTypeReviewModeExited,
			ThreadID: threadID,
			TurnID:   turnID,
		}
		_ = cmd.Wait()
	}()

	return turnID, outWrapped, nil
}

// CompactStart compresses the thread history by running a summarisation prompt.
func (c *Client) CompactStart(ctx context.Context, threadID string) (string, <-chan appserver.TurnEvent, error) {
	if c.loggedOut.Load() {
		return "", nil, fmt.Errorf("claudecli: not authenticated")
	}
	sess, ok := c.sessions.Get(threadID)
	if !ok {
		return "", nil, fmt.Errorf("claudecli: unknown thread %q", threadID)
	}

	turnID := c.genID("compact-turn")

	cmd, err := c.buildCmd(ctx, sess, "", compactSystemPrompt,
		"Please summarize the conversation history concisely.", "")
	if err != nil {
		return "", nil, fmt.Errorf("claudecli: build compact cmd: %w", err)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", nil, fmt.Errorf("claudecli: stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return "", nil, fmt.Errorf("claudecli: start: %w", err)
	}

	sess.mu.Lock()
	sess.turnCount++
	sess.mu.Unlock()

	c.turns.register(turnID, cmd)

	out := make(chan appserver.TurnEvent, 64)
	go func() {
		defer c.turns.remove(turnID)
		// After compact, reset turnCount so next real turn uses --session-id fresh.
		// (Claude CLI persists history internally; we just restart our counter tracking.)
		streamToEvents(ctx, stdout, threadID, turnID, out)
		_ = cmd.Wait()
	}()

	return turnID, out, nil
}

// TurnInterrupt kills the subprocess for the given turn.
func (c *Client) TurnInterrupt(_ context.Context, _, turnID string) error {
	c.turns.kill(turnID)
	return nil
}

// ApprovalRespond is a no-op: the claude CLI handles tool approval internally.
func (c *Client) ApprovalRespond(_ context.Context, _ string, _ appserver.ApprovalDecision) error {
	return nil
}

// MCPServersList returns empty; MCP is managed by claude CLI itself.
func (c *Client) MCPServersList(_ context.Context) ([]appserver.MCPServer, error) {
	return []appserver.MCPServer{}, nil
}

// MCPToolCall is not supported in CLI mode.
func (c *Client) MCPToolCall(_ context.Context, _ appserver.MCPToolCallParams) (appserver.MCPToolCallResult, error) {
	return appserver.MCPToolCallResult{}, fmt.Errorf("claudecli: MCP tool call routing not supported in CLI mode")
}

// MCPOAuthLogin is not applicable in CLI mode.
func (c *Client) MCPOAuthLogin(_ context.Context, _ string) (appserver.MCPOAuthLoginResult, error) {
	return appserver.MCPOAuthLoginResult{
		Status:  "not_supported",
		Message: "Claude CLI mode does not support MCP OAuth routing; configure via claude CLI settings",
	}, nil
}

// Logout marks the client as logged out.
func (c *Client) Logout(_ context.Context) error {
	c.loggedOut.Store(true)
	return nil
}

// ---- helpers ----

// buildCmd constructs the claude -p subprocess for the given session turn.
func (c *Client) buildCmd(
	ctx context.Context,
	sess *session,
	model, systemPrompt, prompt, _ string,
) (*exec.Cmd, error) {
	sess.mu.Lock()
	cwd := sess.cwd
	sessionID := sess.sessionID
	turnCount := sess.turnCount
	sess.mu.Unlock()

	args := []string{"-p", prompt,
		"--output-format", "stream-json",
		"--verbose",
		"--include-partial-messages",
	}

	if turnCount == 0 {
		args = append(args, "--session-id", sessionID)
	} else {
		args = append(args, "--resume", sessionID)
	}

	m := resolveModel(model, c.cfg.DefaultModel)
	if m != "" {
		args = append(args, "--model", m)
	}

	if c.cfg.MaxTurns > 0 {
		args = append(args, "--max-turns", fmt.Sprintf("%d", c.cfg.MaxTurns))
	}

	if c.cfg.SkipPerms {
		args = append(args, "--dangerously-skip-permissions")
	} else if c.cfg.AllowedTools != "" {
		args = append(args, "--allowedTools", c.cfg.AllowedTools)
	}

	if systemPrompt != "" {
		args = append(args, "--append-system-prompt", systemPrompt)
	}

	cmd := exec.CommandContext(ctx, c.cfg.ClaudeBin, args...)
	if cwd != "" {
		cmd.Dir = cwd
	} else if c.cfg.WorkDir != "" {
		cmd.Dir = c.cfg.WorkDir
	}
	// Strip CLAUDECODE so the subprocess is not blocked by the nested-session guard.
	cmd.Env = filterEnv(os.Environ(), "CLAUDECODE")
	return cmd, nil
}

// filterEnv returns a copy of env with all entries whose key equals key removed.
func filterEnv(env []string, key string) []string {
	prefix := key + "="
	out := make([]string, 0, len(env))
	for _, e := range env {
		if !strings.HasPrefix(e, prefix) {
			out = append(out, e)
		}
	}
	return out
}

func resolveModel(sessionModel, defaultModel string) string {
	if sessionModel != "" {
		return sessionModel
	}
	return defaultModel
}

func buildPrompt(inputs []appserver.UserInput) string {
	var sb strings.Builder
	for _, inp := range inputs {
		switch inp.Type {
		case "text":
			sb.WriteString(inp.Text)
		case "mention":
			sb.WriteString(fmt.Sprintf("[File: %s]\n%s", inp.Path, inp.Text))
		case "image", "localimage":
			// Images are not trivially passable as CLI args; embed base64 hint.
			if inp.URL != "" {
				sb.WriteString(fmt.Sprintf("[Image: %s]", inp.URL))
			} else if inp.Text != "" {
				sb.WriteString("[Image: <base64 data>]")
			}
		default:
			if inp.Text != "" {
				sb.WriteString(inp.Text)
			}
		}
		sb.WriteString(" ")
	}
	return strings.TrimSpace(sb.String())
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

// Compile-time interface check.
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
