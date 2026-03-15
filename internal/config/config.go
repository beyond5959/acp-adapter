package config

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
)

const defaultCodexAppServerArgsRaw = `app-server -c model_reasoning_summary="detailed"`

// ProfileConfig describes one named prompt/runtime profile.
type ProfileConfig struct {
	Model              string `json:"model,omitempty"`
	ThoughtLevel       string `json:"thoughtLevel,omitempty"`
	ApprovalPolicy     string `json:"approvalPolicy,omitempty"`
	Sandbox            string `json:"sandbox,omitempty"`
	Personality        string `json:"personality,omitempty"`
	SystemInstructions string `json:"systemInstructions,omitempty"`
}

type profileConfigEntry struct {
	Name               string `json:"name"`
	Model              string `json:"model,omitempty"`
	ThoughtLevel       string `json:"thoughtLevel,omitempty"`
	ApprovalPolicy     string `json:"approvalPolicy,omitempty"`
	Sandbox            string `json:"sandbox,omitempty"`
	Personality        string `json:"personality,omitempty"`
	SystemInstructions string `json:"systemInstructions,omitempty"`
}

// Config contains runtime settings for the ACP adapter.
type Config struct {
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

// Parse reads command-line flags with environment variable defaults.
func Parse() Config {
	defaultCommand := firstNonEmpty(os.Getenv("CODEX_APP_SERVER_CMD"), "codex")
	defaultArgsRaw := os.Getenv("CODEX_APP_SERVER_ARGS")
	if defaultArgsRaw == "" && defaultCommand == "codex" {
		defaultArgsRaw = defaultCodexAppServerArgsRaw
	}

	defaultLogLevel := firstNonEmpty(os.Getenv("LOG_LEVEL"), "info")
	defaultPatchApplyMode := firstNonEmpty(os.Getenv("PATCH_APPLY_MODE"), "appserver")
	defaultTraceFile := firstNonEmpty(os.Getenv("TRACE_JSON_FILE"), "trace-jsonl.log")
	defaultRetryTurnOnCrash := parseBoolWithDefault(os.Getenv("RETRY_TURN_ON_CRASH"), true)
	defaultProfilesFile := strings.TrimSpace(os.Getenv("CODEX_ACP_PROFILES_FILE"))
	defaultProfilesJSON := strings.TrimSpace(os.Getenv("CODEX_ACP_PROFILES_JSON"))
	defaultProfile := strings.TrimSpace(os.Getenv("CODEX_ACP_DEFAULT_PROFILE"))

	var cfg Config
	var argsRaw string
	var profilesFile string
	var profilesJSON string
	flag.StringVar(&cfg.AppServerCommand, "app-server-cmd", defaultCommand, "app server command")
	flag.StringVar(&argsRaw, "app-server-args", defaultArgsRaw, "app server args, space separated")
	flag.BoolVar(&cfg.TraceJSON, "trace-json", false, "enable raw json tracing")
	flag.StringVar(&cfg.TraceJSONFile, "trace-json-file", defaultTraceFile, "trace jsonl output file")
	flag.StringVar(&cfg.LogLevel, "log-level", defaultLogLevel, "log level: debug|info|warn|error")
	flag.StringVar(
		&cfg.PatchApplyMode,
		"patch-apply-mode",
		defaultPatchApplyMode,
		"patch apply mode: appserver|acp_fs",
	)
	flag.BoolVar(
		&cfg.RetryTurnOnCrash,
		"retry-turn-on-crash",
		defaultRetryTurnOnCrash,
		"retry current turn once after app-server crash (default true)",
	)
	flag.StringVar(&profilesFile, "profiles-file", defaultProfilesFile, "profiles config file (JSON)")
	flag.StringVar(&profilesJSON, "profiles-json", defaultProfilesJSON, "profiles config JSON string")
	flag.StringVar(&cfg.DefaultProfile, "default-profile", defaultProfile, "default profile name")
	flag.Parse()

	cfg.AppServerArgs = splitArgs(argsRaw)
	cfg.Profiles = loadProfiles(profilesFile, profilesJSON)
	cfg.InitialAuthMode = DetectAuthMode(
		os.Getenv("CODEX_API_KEY"),
		os.Getenv("OPENAI_API_KEY"),
		os.Getenv("CHATGPT_SUBSCRIPTION_ACTIVE"),
	)
	return cfg
}

func splitArgs(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	return strings.Fields(raw)
}

// DefaultCodexAppServerArgs returns the adapter's default codex app-server args.
func DefaultCodexAppServerArgs() []string {
	return splitArgs(defaultCodexAppServerArgsRaw)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func loadProfiles(path string, inlineJSON string) map[string]ProfileConfig {
	profiles := make(map[string]ProfileConfig)

	if strings.TrimSpace(path) != "" {
		data, err := os.ReadFile(path)
		if err == nil {
			parsed, parseErr := parseProfilesJSON(data)
			if parseErr == nil {
				for name, cfg := range parsed {
					profiles[name] = cfg
				}
			}
		}
	}

	if strings.TrimSpace(inlineJSON) != "" {
		parsed, err := parseProfilesJSON([]byte(inlineJSON))
		if err == nil {
			for name, cfg := range parsed {
				profiles[name] = cfg
			}
		}
	}

	return profiles
}

func parseProfilesJSON(data []byte) (map[string]ProfileConfig, error) {
	raw := strings.TrimSpace(string(data))
	if raw == "" {
		return map[string]ProfileConfig{}, nil
	}

	if strings.HasPrefix(raw, "{") {
		var byName map[string]ProfileConfig
		if err := json.Unmarshal([]byte(raw), &byName); err != nil {
			return nil, fmt.Errorf("decode profile object: %w", err)
		}
		return byName, nil
	}

	var entries []profileConfigEntry
	if err := json.Unmarshal([]byte(raw), &entries); err != nil {
		return nil, fmt.Errorf("decode profile array: %w", err)
	}

	out := make(map[string]ProfileConfig, len(entries))
	for _, entry := range entries {
		name := strings.TrimSpace(entry.Name)
		if name == "" {
			continue
		}
		out[name] = ProfileConfig{
			Model:              entry.Model,
			ThoughtLevel:       entry.ThoughtLevel,
			ApprovalPolicy:     entry.ApprovalPolicy,
			Sandbox:            entry.Sandbox,
			Personality:        entry.Personality,
			SystemInstructions: entry.SystemInstructions,
		}
	}
	return out, nil
}

func DetectAuthMode(codexAPIKey string, openAIAPIKey string, subscriptionRaw string) string {
	if strings.TrimSpace(codexAPIKey) != "" {
		return "codex_api_key"
	}
	if strings.TrimSpace(openAIAPIKey) != "" {
		return "openai_api_key"
	}
	if subscriptionEnabled(subscriptionRaw) {
		return "chatgpt_subscription"
	}
	return ""
}

func subscriptionEnabled(raw string) bool {
	value := strings.TrimSpace(strings.ToLower(raw))
	if value == "" {
		return true
	}

	switch value {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return true
	}
}

func parseBoolWithDefault(raw string, fallback bool) bool {
	value := strings.TrimSpace(strings.ToLower(raw))
	if value == "" {
		return fallback
	}
	switch value {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return fallback
	}
}
