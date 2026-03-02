package claude

import "os"

const (
	// DefaultModel is the default Claude model used when none is specified.
	DefaultModel = "claude-opus-4-6"
	// DefaultMaxTokens is the default max_tokens for each turn.
	DefaultMaxTokens = 8192
)

// Config holds Claude-specific adapter configuration.
type Config struct {
	// AuthToken is the Anthropic API auth token (ANTHROPIC_AUTH_TOKEN).
	AuthToken string
	// BaseURL overrides the default Anthropic API base URL (ANTHROPIC_BASE_URL).
	BaseURL string
	// DefaultModel is used when the session/turn does not specify a model.
	DefaultModel string
	// MaxTokens is the per-turn token limit.
	MaxTokens int64
}

// ConfigFromEnv builds a Config from environment variables, applying
// defaults where env vars are absent.
func ConfigFromEnv() Config {
	cfg := Config{
		AuthToken:    os.Getenv("ANTHROPIC_AUTH_TOKEN"),
		BaseURL:      os.Getenv("ANTHROPIC_BASE_URL"),
		DefaultModel: DefaultModel,
		MaxTokens:    DefaultMaxTokens,
	}
	return cfg
}

// IsAuthenticated reports whether an auth token is present.
func (c Config) IsAuthenticated() bool {
	return c.AuthToken != ""
}
