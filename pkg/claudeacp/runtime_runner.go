package claudeacp

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"

	"github.com/beyond5959/acp-adapter/internal/acp"
	"github.com/beyond5959/acp-adapter/internal/bridge"
	"github.com/beyond5959/acp-adapter/internal/claude"
	"github.com/beyond5959/acp-adapter/internal/observability"
)

func runRuntime(
	ctx context.Context,
	cfg RuntimeConfig,
	stderr io.Writer,
	buildTransport func(acp.TraceFunc) acp.Transport,
) error {
	cfg = normalizeRuntimeConfig(cfg)
	if stderr == nil {
		stderr = os.Stderr
	}

	logger := observability.NewJSONLoggerWithWriter(cfg.LogLevel, stderr)

	var traceFile *observability.JSONTraceFile
	if cfg.TraceJSON {
		var err error
		traceFile, err = observability.NewJSONTraceFile(cfg.TraceJSONFile)
		if err != nil {
			logger.Error("failed to open trace-json file", slog.String("error", err.Error()))
			return err
		}
		logger.Info("trace-json enabled", slog.String("path", traceFile.Path()))
		defer func() { _ = traceFile.Close() }()
	}

	transport := buildTransport(func(direction string, payload []byte) {
		if traceFile != nil {
			traceFile.TraceACP(direction, payload)
		}
	})
	if transport == nil {
		return errors.New("acp transport is nil")
	}

	client := claude.NewClient(toClaudeCliConfig(cfg))

	server := acp.NewServer(
		transport,
		client,
		bridge.NewStore(),
		logger,
		acp.ServerOptions{
			PatchApplyMode:  cfg.PatchApplyMode,
			Profiles:        toACPProfiles(cfg.Profiles),
			DefaultProfile:  cfg.DefaultProfile,
			InitialAuthMode: "claude_cli",
		},
	)

	if err := server.Serve(ctx); err != nil && !errors.Is(err, io.EOF) {
		logger.Error("acp server stopped with error", slog.String("error", err.Error()))
		return err
	}
	return nil
}
