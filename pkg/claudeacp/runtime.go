// Package claudeacp provides an ACP adapter backed by the claude CLI subprocess.
package claudeacp

import (
	"context"
	"io"
	"os"
	"strings"

	"github.com/beyond5959/acp-adapter/internal/acp"
	"github.com/beyond5959/acp-adapter/internal/claude"
)

const (
	defaultTraceJSONFile  = "trace-jsonl.log"
	defaultPatchApplyMode = "appserver"
)

// ProfileConfig defines one named runtime profile (mirrors codexacp.ProfileConfig).
type ProfileConfig struct {
	Model              string
	ThoughtLevel       string
	ApprovalPolicy     string
	Sandbox            string
	Personality        string
	SystemInstructions string
}

// RuntimeConfig configures the Claude adapter runtime.
type RuntimeConfig struct {
	// Claude CLI-specific settings.
	ClaudeBin    string
	DefaultModel string
	// AvailableModels are exposed to ACP model picker UI.
	AvailableModels []string
	// DefaultEffort is exposed as ACP thought_level current value.
	DefaultEffort string
	// AvailableEfforts are exposed to ACP thought_level picker UI.
	AvailableEfforts []string
	MaxTurns         int
	SkipPerms        bool
	AllowedTools     string

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
		ClaudeBin:       claude.DefaultBin(),
		DefaultModel:    claude.DefaultModel,
		AvailableModels: []string{claude.DefaultModel},
		DefaultEffort:   claude.DefaultEffort,
		AvailableEfforts: []string{
			"low",
			claude.DefaultEffort,
			"high",
		},
		MaxTurns:       claude.DefaultMaxTurns,
		SkipPerms:      true,
		TraceJSONFile:  defaultTraceJSONFile,
		LogLevel:       "info",
		PatchApplyMode: defaultPatchApplyMode,
		Profiles:       map[string]ProfileConfig{},
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
	if strings.TrimSpace(cfg.ClaudeBin) == "" {
		cfg.ClaudeBin = claude.DefaultBin()
	}
	if strings.TrimSpace(cfg.DefaultModel) == "" {
		cfg.DefaultModel = claude.DefaultModel
	}
	if strings.TrimSpace(cfg.DefaultEffort) == "" {
		cfg.DefaultEffort = claude.DefaultEffort
	}
	cfg.AvailableModels = append(uniqueValues(cfg.AvailableModels), cfg.DefaultModel)
	cfg.AvailableModels = uniqueValues(cfg.AvailableModels)
	cfg.AvailableEfforts = append(uniqueValues(cfg.AvailableEfforts), cfg.DefaultEffort)
	cfg.AvailableEfforts = uniqueValues(cfg.AvailableEfforts)
	if cfg.MaxTurns <= 0 {
		cfg.MaxTurns = claude.DefaultMaxTurns
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
			ThoughtLevel:       p.ThoughtLevel,
			ApprovalPolicy:     p.ApprovalPolicy,
			Sandbox:            p.Sandbox,
			Personality:        p.Personality,
			SystemInstructions: p.SystemInstructions,
		}
	}
	return out
}

func toClaudeCliConfig(cfg RuntimeConfig) claude.Config {
	return claude.Config{
		ClaudeBin:       cfg.ClaudeBin,
		DefaultModel:    cfg.DefaultModel,
		AvailableModels: uniqueValues(cfg.AvailableModels),
		DefaultEffort:   cfg.DefaultEffort,
		AvailableEfforts: uniqueValues(
			cfg.AvailableEfforts,
		),
		MaxTurns:     cfg.MaxTurns,
		SkipPerms:    cfg.SkipPerms,
		AllowedTools: cfg.AllowedTools,
	}
}

func uniqueValues(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, raw := range values {
		value := strings.TrimSpace(raw)
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
