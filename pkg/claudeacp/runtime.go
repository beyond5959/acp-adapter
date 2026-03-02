// Package claudeacp provides an ACP adapter backed by the Anthropic API.
package claudeacp

import (
	"context"
	"io"
	"os"
	"strings"

	"github.com/beyond5959/codex-acp/internal/acp"
	"github.com/beyond5959/codex-acp/internal/claude"
)

const (
	defaultTraceJSONFile    = "trace-jsonl.log"
	defaultPatchApplyMode   = "appserver"
)

// ProfileConfig defines one named runtime profile (mirrors codexacp.ProfileConfig).
type ProfileConfig struct {
	Model              string
	ApprovalPolicy     string
	Sandbox            string
	Personality        string
	SystemInstructions string
}

// RuntimeConfig configures the Claude adapter runtime.
type RuntimeConfig struct {
	// Claude-specific settings.
	AnthropicAuthToken string
	AnthropicBaseURL   string
	DefaultModel       string
	MaxTokens          int64

	// Shared adapter settings (same semantics as codexacp.RuntimeConfig).
	TraceJSON      bool
	TraceJSONFile  string
	LogLevel       string
	PatchApplyMode string
	Profiles       map[string]ProfileConfig
	DefaultProfile string
}

// DefaultRuntimeConfig returns the default Claude adapter runtime settings.
func DefaultRuntimeConfig() RuntimeConfig {
	return RuntimeConfig{
		AnthropicAuthToken: os.Getenv("ANTHROPIC_AUTH_TOKEN"),
		AnthropicBaseURL:   os.Getenv("ANTHROPIC_BASE_URL"),
		DefaultModel:       claude.DefaultModel,
		MaxTokens:          claude.DefaultMaxTokens,
		TraceJSONFile:      defaultTraceJSONFile,
		LogLevel:           "info",
		PatchApplyMode:     defaultPatchApplyMode,
		Profiles:           map[string]ProfileConfig{},
	}
}

// RunStdio runs the Claude ACP adapter over newline-delimited JSON-RPC stdio.
func RunStdio(
	ctx context.Context,
	cfg RuntimeConfig,
	stdin io.Reader,
	stdout io.Writer,
	stderr io.Writer,
) error {
	stdin, stdout, stderr = normalizeIO(stdin, stdout, stderr)
	return runRuntime(ctx, cfg, stderr, func(trace acp.TraceFunc) acp.Transport {
		return acp.NewStdioCodecWithTrace(stdin, stdout, trace)
	})
}

func normalizeRuntimeConfig(cfg RuntimeConfig) RuntimeConfig {
	if strings.TrimSpace(cfg.AnthropicAuthToken) == "" {
		cfg.AnthropicAuthToken = os.Getenv("ANTHROPIC_AUTH_TOKEN")
	}
	if strings.TrimSpace(cfg.AnthropicBaseURL) == "" {
		cfg.AnthropicBaseURL = os.Getenv("ANTHROPIC_BASE_URL")
	}
	if strings.TrimSpace(cfg.DefaultModel) == "" {
		cfg.DefaultModel = claude.DefaultModel
	}
	if cfg.MaxTokens <= 0 {
		cfg.MaxTokens = claude.DefaultMaxTokens
	}
	if strings.TrimSpace(cfg.TraceJSONFile) == "" {
		cfg.TraceJSONFile = defaultTraceJSONFile
	}
	if strings.TrimSpace(cfg.LogLevel) == "" {
		cfg.LogLevel = "info"
	}
	if strings.TrimSpace(cfg.PatchApplyMode) == "" {
		cfg.PatchApplyMode = defaultPatchApplyMode
	}
	if cfg.Profiles == nil {
		cfg.Profiles = map[string]ProfileConfig{}
	}
	return cfg
}

func normalizeIO(stdin io.Reader, stdout io.Writer, stderr io.Writer) (io.Reader, io.Writer, io.Writer) {
	if stdin == nil {
		stdin = os.Stdin
	}
	if stdout == nil {
		stdout = os.Stdout
	}
	if stderr == nil {
		stderr = os.Stderr
	}
	return stdin, stdout, stderr
}

func toACPProfiles(profiles map[string]ProfileConfig) map[string]acp.ProfileConfig {
	out := make(map[string]acp.ProfileConfig, len(profiles))
	for name, p := range profiles {
		out[name] = acp.ProfileConfig{
			Model:              p.Model,
			ApprovalPolicy:     p.ApprovalPolicy,
			Sandbox:            p.Sandbox,
			Personality:        p.Personality,
			SystemInstructions: p.SystemInstructions,
		}
	}
	return out
}

func toClaudeConfig(cfg RuntimeConfig) claude.Config {
	return claude.Config{
		AuthToken:    cfg.AnthropicAuthToken,
		BaseURL:      cfg.AnthropicBaseURL,
		DefaultModel: cfg.DefaultModel,
		MaxTokens:    cfg.MaxTokens,
	}
}
