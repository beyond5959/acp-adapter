package claude

import (
	"os"
	"strings"
)

const (
	// DefaultModel is the default Claude model used when none is specified.
	DefaultModel = "claude-opus-4-6"
	// DefaultMaxTurns is the default --max-turns for claude -p.
	DefaultMaxTurns = 10
)

// Config holds configuration for the claude CLI-backed adapter.
type Config struct {
	// ClaudeBin is the path to the claude binary. Defaults to "claude".
	ClaudeBin string
	// DefaultModel is used when the session/turn does not specify a model.
	DefaultModel string
	// AvailableModels are surfaced to ACP model picker UI.
	AvailableModels []string
	// MaxTurns limits the number of agentic turns per invocation.
	MaxTurns int
	// SkipPerms enables --dangerously-skip-permissions.
	SkipPerms bool
	// AllowedTools is passed to --allowedTools when set.
	AllowedTools string
	// WorkDir is the working directory for subprocesses (overridden per-thread by cwd).
	WorkDir string
}

// DefaultBin returns the default claude binary path from env or "claude".
func DefaultBin() string {
	if v := os.Getenv("CLAUDE_BIN"); v != "" {
		return v
	}
	return "claude"
}

// ConfigFromEnv builds a Config from environment variables, applying defaults.
func ConfigFromEnv() Config {
	bin := os.Getenv("CLAUDE_BIN")
	if bin == "" {
		bin = "claude"
	}
	model := os.Getenv("CLAUDE_MODEL")
	if model == "" {
		model = DefaultModel
	}
	return Config{
		ClaudeBin:       bin,
		DefaultModel:    model,
		AvailableModels: parseModelsEnv(os.Getenv("CLAUDE_MODELS"), model),
		MaxTurns:        DefaultMaxTurns,
		SkipPerms:       true,
	}
}

func parseModelsEnv(raw string, fallback string) []string {
	items := splitModelList(raw)
	items = append(items, strings.TrimSpace(fallback))
	return uniqueNonEmpty(items)
}

func splitModelList(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == ' ' || r == '\n' || r == '\t'
	})
	return parts
}

func uniqueNonEmpty(items []string) []string {
	seen := make(map[string]struct{}, len(items))
	out := make([]string, 0, len(items))
	for _, item := range items {
		value := strings.TrimSpace(item)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}
