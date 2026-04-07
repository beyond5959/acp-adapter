package piacp

import (
	"context"
	"io"
	"os"
	"strings"

	"github.com/beyond5959/acp-adapter/internal/acp"
	"github.com/beyond5959/acp-adapter/internal/pi"
)

const (
	defaultTraceJSONFile  = "trace-jsonl.log"
	defaultPatchApplyMode = "appserver"
)

// ProfileConfig defines one named runtime profile.
type ProfileConfig struct {
	Model              string
	ThoughtLevel       string
	ApprovalPolicy     string
	Sandbox            string
	Personality        string
	SystemInstructions string
}

// RuntimeConfig configures the Pi adapter runtime.
type RuntimeConfig struct {
	PiBin           string
	PiArgs          []string
	DefaultProvider string
	DefaultModel    string
	SessionDir      string
	EnableGate      bool

	TraceJSON      bool
	TraceJSONFile  string
	LogLevel       string
	PatchApplyMode string
	Profiles       map[string]ProfileConfig
	DefaultProfile string
}

// DefaultRuntimeConfig returns the default Pi runtime settings.
func DefaultRuntimeConfig() RuntimeConfig {
	return RuntimeConfig{
		PiBin:          pi.DefaultBin,
		EnableGate:     true,
		TraceJSONFile:  defaultTraceJSONFile,
		LogLevel:       "info",
		PatchApplyMode: defaultPatchApplyMode,
		Profiles:       map[string]ProfileConfig{},
	}
}

// RunStdio runs the Pi ACP adapter over newline-delimited JSON-RPC stdio.
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
	defaults := DefaultRuntimeConfig()
	if strings.TrimSpace(cfg.PiBin) == "" {
		cfg.PiBin = defaults.PiBin
	}
	if strings.TrimSpace(cfg.TraceJSONFile) == "" {
		cfg.TraceJSONFile = defaults.TraceJSONFile
	}
	if strings.TrimSpace(cfg.LogLevel) == "" {
		cfg.LogLevel = defaults.LogLevel
	}
	if strings.TrimSpace(cfg.PatchApplyMode) == "" {
		cfg.PatchApplyMode = defaults.PatchApplyMode
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
	for name, profile := range profiles {
		out[name] = acp.ProfileConfig{
			Model:              profile.Model,
			ThoughtLevel:       profile.ThoughtLevel,
			ApprovalPolicy:     profile.ApprovalPolicy,
			Sandbox:            profile.Sandbox,
			Personality:        profile.Personality,
			SystemInstructions: profile.SystemInstructions,
		}
	}
	return out
}
