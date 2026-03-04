// Command acp-adapter is the ACP adapter binary for the Codex backend.
// It is a thin wrapper around pkg/codexacp.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/beyond5959/acp-adapter/internal/config"
	"github.com/beyond5959/acp-adapter/pkg/codexacp"
)

func main() {
	cfg := config.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	runtimeCfg := codexacp.RuntimeConfig{
		AppServerCommand: cfg.AppServerCommand,
		AppServerArgs:    cfg.AppServerArgs,
		TraceJSON:        cfg.TraceJSON,
		TraceJSONFile:    cfg.TraceJSONFile,
		LogLevel:         cfg.LogLevel,
		PatchApplyMode:   cfg.PatchApplyMode,
		RetryTurnOnCrash: cfg.RetryTurnOnCrash,
		Profiles:         mapProfiles(cfg.Profiles),
		DefaultProfile:   cfg.DefaultProfile,
		InitialAuthMode:  cfg.InitialAuthMode,
	}

	if err := codexacp.RunStdio(ctx, runtimeCfg, os.Stdin, os.Stdout, os.Stderr); err != nil {
		os.Exit(1)
	}
}

func mapProfiles(profiles map[string]config.ProfileConfig) map[string]codexacp.ProfileConfig {
	out := make(map[string]codexacp.ProfileConfig, len(profiles))
	for name, profile := range profiles {
		out[name] = codexacp.ProfileConfig{
			Model:              profile.Model,
			ApprovalPolicy:     profile.ApprovalPolicy,
			Sandbox:            profile.Sandbox,
			Personality:        profile.Personality,
			SystemInstructions: profile.SystemInstructions,
		}
	}
	return out
}
