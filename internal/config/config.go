package config

import (
	"flag"
	"os"
	"strings"
)

// Config contains runtime settings for the ACP adapter.
type Config struct {
	AppServerCommand string
	AppServerArgs    []string
	TraceJSON        bool
	LogLevel         string
}

// Parse reads command-line flags with environment variable defaults.
func Parse() Config {
	defaultCommand := firstNonEmpty(os.Getenv("CODEX_APP_SERVER_CMD"), "codex")
	defaultArgsRaw := os.Getenv("CODEX_APP_SERVER_ARGS")
	if defaultArgsRaw == "" && defaultCommand == "codex" {
		defaultArgsRaw = "app-server"
	}

	defaultLogLevel := firstNonEmpty(os.Getenv("LOG_LEVEL"), "info")

	var cfg Config
	var argsRaw string
	flag.StringVar(&cfg.AppServerCommand, "app-server-cmd", defaultCommand, "app server command")
	flag.StringVar(&argsRaw, "app-server-args", defaultArgsRaw, "app server args, space separated")
	flag.BoolVar(&cfg.TraceJSON, "trace-json", false, "enable raw json tracing")
	flag.StringVar(&cfg.LogLevel, "log-level", defaultLogLevel, "log level: debug|info|warn|error")
	flag.Parse()

	cfg.AppServerArgs = splitArgs(argsRaw)
	return cfg
}

func splitArgs(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	return strings.Fields(raw)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
