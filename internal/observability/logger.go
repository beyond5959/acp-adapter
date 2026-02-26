package observability

import (
	"log/slog"
	"os"
	"strings"
)

// NewJSONLogger returns a stderr JSON logger.
func NewJSONLogger(levelText string) *slog.Logger {
	opts := &slog.HandlerOptions{
		Level: parseLevel(levelText),
	}
	return slog.New(slog.NewJSONHandler(os.Stderr, opts))
}

func parseLevel(levelText string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(levelText)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
