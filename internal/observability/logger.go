package observability

import (
	"io"
	"log/slog"
	"os"
	"strings"
)

// NewJSONLogger returns a stderr JSON logger.
func NewJSONLogger(levelText string) *slog.Logger {
	return NewJSONLoggerWithWriter(levelText, os.Stderr)
}

// NewJSONLoggerWithWriter returns a JSON logger writing to one writer.
func NewJSONLoggerWithWriter(levelText string, writer io.Writer) *slog.Logger {
	if writer == nil {
		writer = os.Stderr
	}

	opts := &slog.HandlerOptions{
		Level: parseLevel(levelText),
	}
	return slog.New(slog.NewJSONHandler(writer, opts))
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
