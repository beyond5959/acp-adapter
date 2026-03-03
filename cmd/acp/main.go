// Command acp is a unified ACP adapter entry point supporting multiple backends.
//
// Usage:
//
//	acp --adapter codex [codex flags...]
//	acp --adapter claude [claude flags...]
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/beyond5959/acp-adapter/internal/config"
	"github.com/beyond5959/acp-adapter/pkg/claudeacp"
	"github.com/beyond5959/acp-adapter/pkg/acpadapter"
)

const usage = `acp — ACP adapter with multiple backend support

Usage:
  acp [--adapter codex|claude] [flags]

Flags (shared):
  --adapter          Backend adapter: codex (default) or claude
  --log-level        Log level: debug|info|warn|error (default: info)
  --trace-json       Enable raw JSON tracing to file
  --trace-json-file  Trace JSONL output file (default: trace-jsonl.log)
  --patch-apply-mode Patch apply mode: appserver|acp_fs (default: appserver)
  --profiles-file    Profiles config file (JSON)
  --profiles-json    Profiles config JSON string
  --default-profile  Default profile name

Flags (--adapter codex):
  --app-server-cmd   App server command (default: codex, env: CODEX_APP_SERVER_CMD)
  --app-server-args  App server args, space separated (default: app-server)
  --retry-turn-on-crash  Retry current turn once after crash (default: true)

Flags (--adapter claude):
  --claude-bin            Path to claude binary (env: CLAUDE_BIN, default: claude)
  --model                 Default model (env: CLAUDE_MODEL, default: claude-opus-4-6)
  --max-turns             Max agentic turns per invocation (default: 10)
  --skip-perms            Pass --dangerously-skip-permissions to claude (default: true)
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, os.Args[1:]); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "acp: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("acp", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, usage) }

	// ---- shared flags ----
	adapter := fs.String("adapter", firstEnv("ACP_ADAPTER", "codex"), "backend adapter: codex|claude")
	logLevel := fs.String("log-level", firstEnv("LOG_LEVEL", "info"), "log level")
	traceJSON := fs.Bool("trace-json", false, "enable raw JSON tracing")
	traceJSONFile := fs.String("trace-json-file", firstEnv("TRACE_JSON_FILE", "trace-jsonl.log"), "trace JSONL output file")
	patchApplyMode := fs.String("patch-apply-mode", firstEnv("PATCH_APPLY_MODE", "appserver"), "patch apply mode")
	profilesFile := fs.String("profiles-file", os.Getenv("CODEX_ACP_PROFILES_FILE"), "profiles config file")
	profilesJSON := fs.String("profiles-json", os.Getenv("CODEX_ACP_PROFILES_JSON"), "profiles config JSON")
	defaultProfile := fs.String("default-profile", os.Getenv("CODEX_ACP_DEFAULT_PROFILE"), "default profile name")

	// ---- codex-specific flags ----
	appServerCmd := fs.String("app-server-cmd", firstEnv("CODEX_APP_SERVER_CMD", "codex"), "app server command")
	appServerArgs := fs.String("app-server-args", firstEnv("CODEX_APP_SERVER_ARGS", "app-server"), "app server args")
	retryOnCrash := fs.Bool("retry-turn-on-crash", parseBoolDefault(os.Getenv("RETRY_TURN_ON_CRASH"), true), "retry turn on crash")

	// ---- claude-specific flags ----
	claudeBin := fs.String("claude-bin", firstEnv("CLAUDE_BIN", "claude"), "path to claude binary")
	model := fs.String("model", firstEnv("CLAUDE_MODEL", ""), "Claude default model")
	maxTurns := fs.Int("max-turns", 10, "max agentic turns per invocation")
	skipPerms := fs.Bool("skip-perms", parseBoolDefault(os.Getenv("CLAUDE_SKIP_PERMS"), true), "pass --dangerously-skip-permissions to claude")

	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return nil
		}
		return err
	}

	switch strings.ToLower(*adapter) {
	case "codex", "":
		return runCodexAdapter(ctx, runCodexParams{
			appServerCmd:   *appServerCmd,
			appServerArgs:  strings.Fields(*appServerArgs),
			logLevel:       *logLevel,
			traceJSON:      *traceJSON,
			traceJSONFile:  *traceJSONFile,
			patchApplyMode: *patchApplyMode,
			retryOnCrash:   *retryOnCrash,
			profilesFile:   *profilesFile,
			profilesJSON:   *profilesJSON,
			defaultProfile: *defaultProfile,
		})
	case "claude":
		return runClaudeAdapter(ctx, runClaudeParams{
			claudeBin:      *claudeBin,
			model:          *model,
			maxTurns:       *maxTurns,
			skipPerms:      *skipPerms,
			logLevel:       *logLevel,
			traceJSON:      *traceJSON,
			traceJSONFile:  *traceJSONFile,
			patchApplyMode: *patchApplyMode,
			profilesFile:   *profilesFile,
			profilesJSON:   *profilesJSON,
			defaultProfile: *defaultProfile,
		})
	default:
		return fmt.Errorf("unknown adapter %q; choose codex or claude", *adapter)
	}
}

// ---- codex adapter wiring ----

type runCodexParams struct {
	appServerCmd   string
	appServerArgs  []string
	logLevel       string
	traceJSON      bool
	traceJSONFile  string
	patchApplyMode string
	retryOnCrash   bool
	profilesFile   string
	profilesJSON   string
	defaultProfile string
}

func runCodexAdapter(ctx context.Context, p runCodexParams) error {
	profiles := loadProfiles(p.profilesFile, p.profilesJSON)
	initialAuthMode := config.DetectAuthMode(
		os.Getenv("CODEX_API_KEY"),
		os.Getenv("OPENAI_API_KEY"),
		os.Getenv("CHATGPT_SUBSCRIPTION_ACTIVE"),
	)

	cfg := acpadapter.RuntimeConfig{
		AppServerCommand: p.appServerCmd,
		AppServerArgs:    p.appServerArgs,
		TraceJSON:        p.traceJSON,
		TraceJSONFile:    p.traceJSONFile,
		LogLevel:         p.logLevel,
		PatchApplyMode:   p.patchApplyMode,
		RetryTurnOnCrash: p.retryOnCrash,
		Profiles:         mapCodexProfiles(profiles),
		DefaultProfile:   p.defaultProfile,
		InitialAuthMode:  initialAuthMode,
	}
	return acpadapter.RunStdio(ctx, cfg, os.Stdin, os.Stdout, os.Stderr)
}

// ---- claude adapter wiring ----

type runClaudeParams struct {
	claudeBin      string
	model          string
	maxTurns       int
	skipPerms      bool
	logLevel       string
	traceJSON      bool
	traceJSONFile  string
	patchApplyMode string
	profilesFile   string
	profilesJSON   string
	defaultProfile string
}

func runClaudeAdapter(ctx context.Context, p runClaudeParams) error {
	profiles := loadProfiles(p.profilesFile, p.profilesJSON)

	cfg := claudeacp.RuntimeConfig{
		ClaudeBin:      p.claudeBin,
		DefaultModel:   p.model,
		MaxTurns:       p.maxTurns,
		SkipPerms:      p.skipPerms,
		TraceJSON:      p.traceJSON,
		TraceJSONFile:  p.traceJSONFile,
		LogLevel:       p.logLevel,
		PatchApplyMode: p.patchApplyMode,
		Profiles:       mapClaudeProfiles(profiles),
		DefaultProfile: p.defaultProfile,
	}
	return claudeacp.RunStdio(ctx, cfg, os.Stdin, os.Stdout, os.Stderr)
}

// ---- profile loading (shared) ----

type profileEntry struct {
	Name               string `json:"name"`
	Model              string `json:"model,omitempty"`
	ApprovalPolicy     string `json:"approvalPolicy,omitempty"`
	Sandbox            string `json:"sandbox,omitempty"`
	Personality        string `json:"personality,omitempty"`
	SystemInstructions string `json:"systemInstructions,omitempty"`
}

type profileValues struct {
	Model              string
	ApprovalPolicy     string
	Sandbox            string
	Personality        string
	SystemInstructions string
}

func loadProfiles(path, inlineJSON string) map[string]profileValues {
	out := make(map[string]profileValues)
	if strings.TrimSpace(path) != "" {
		data, err := os.ReadFile(path)
		if err == nil {
			mergeProfiles(out, data)
		}
	}
	if strings.TrimSpace(inlineJSON) != "" {
		mergeProfiles(out, []byte(inlineJSON))
	}
	return out
}

func mergeProfiles(dst map[string]profileValues, data []byte) {
	raw := strings.TrimSpace(string(data))
	if raw == "" {
		return
	}
	if strings.HasPrefix(raw, "{") {
		var byName map[string]profileValues
		if err := json.Unmarshal(data, &byName); err == nil {
			for k, v := range byName {
				dst[k] = v
			}
		}
		return
	}
	var entries []profileEntry
	if err := json.Unmarshal(data, &entries); err == nil {
		for _, e := range entries {
			if strings.TrimSpace(e.Name) == "" {
				continue
			}
			dst[e.Name] = profileValues{
				Model:              e.Model,
				ApprovalPolicy:     e.ApprovalPolicy,
				Sandbox:            e.Sandbox,
				Personality:        e.Personality,
				SystemInstructions: e.SystemInstructions,
			}
		}
	}
}

func mapCodexProfiles(profiles map[string]profileValues) map[string]acpadapter.ProfileConfig {
	out := make(map[string]acpadapter.ProfileConfig, len(profiles))
	for name, p := range profiles {
		out[name] = acpadapter.ProfileConfig{
			Model:              p.Model,
			ApprovalPolicy:     p.ApprovalPolicy,
			Sandbox:            p.Sandbox,
			Personality:        p.Personality,
			SystemInstructions: p.SystemInstructions,
		}
	}
	return out
}

func mapClaudeProfiles(profiles map[string]profileValues) map[string]claudeacp.ProfileConfig {
	out := make(map[string]claudeacp.ProfileConfig, len(profiles))
	for name, p := range profiles {
		out[name] = claudeacp.ProfileConfig{
			Model:              p.Model,
			ApprovalPolicy:     p.ApprovalPolicy,
			Sandbox:            p.Sandbox,
			Personality:        p.Personality,
			SystemInstructions: p.SystemInstructions,
		}
	}
	return out
}

// ---- helpers ----

func firstEnv(keys ...string) string {
	for _, k := range keys {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	if len(keys) > 0 {
		return keys[len(keys)-1]
	}
	return ""
}

func parseBoolDefault(raw string, fallback bool) bool {
	switch strings.TrimSpace(strings.ToLower(raw)) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	}
	return fallback
}
