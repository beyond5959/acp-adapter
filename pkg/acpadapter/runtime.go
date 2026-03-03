package acpadapter

import (
	"context"
	"io"
	"os"
	"strings"
	"time"

	"github.com/beyond5959/acp-adapter/internal/acp"
)

const (
	defaultAppServerCommand  = "codex"
	defaultTraceJSONFile     = "trace-jsonl.log"
	defaultPatchApplyMode    = "appserver"
	defaultRetryTurnOnCrash  = true
	defaultInitializeTimeout = 5 * time.Second
)

// ProfileConfig defines one named runtime profile.
type ProfileConfig struct {
	Model              string
	ApprovalPolicy     string
	Sandbox            string
	Personality        string
	SystemInstructions string
}

// RuntimeConfig configures adapter runtime behavior for stdio mode.
type RuntimeConfig struct {
	AppServerCommand string
	AppServerArgs    []string
	TraceJSON        bool
	TraceJSONFile    string
	LogLevel         string
	PatchApplyMode   string
	RetryTurnOnCrash bool
	Profiles         map[string]ProfileConfig
	DefaultProfile   string
	InitialAuthMode  string
}

// DefaultRuntimeConfig returns the default runtime settings.
func DefaultRuntimeConfig() RuntimeConfig {
	return RuntimeConfig{
		AppServerCommand: defaultAppServerCommand,
		AppServerArgs:    []string{"app-server"},
		TraceJSONFile:    defaultTraceJSONFile,
		LogLevel:         "info",
		PatchApplyMode:   defaultPatchApplyMode,
		RetryTurnOnCrash: defaultRetryTurnOnCrash,
		Profiles:         map[string]ProfileConfig{},
	}
}

// RunStdio runs the ACP adapter over newline-delimited JSON-RPC stdio streams.
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

	if strings.TrimSpace(cfg.AppServerCommand) == "" {
		cfg.AppServerCommand = defaults.AppServerCommand
	}
	if len(cfg.AppServerArgs) == 0 && cfg.AppServerCommand == defaultAppServerCommand {
		cfg.AppServerArgs = append([]string(nil), defaults.AppServerArgs...)
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
			ApprovalPolicy:     profile.ApprovalPolicy,
			Sandbox:            profile.Sandbox,
			Personality:        profile.Personality,
			SystemInstructions: profile.SystemInstructions,
		}
	}
	return out
}
