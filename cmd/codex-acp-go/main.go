package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"codex-acp/internal/acp"
	"codex-acp/internal/appserver"
	"codex-acp/internal/bridge"
	"codex-acp/internal/config"
	"codex-acp/internal/observability"
)

func main() {
	cfg := config.Parse()
	logger := observability.NewJSONLogger(cfg.LogLevel)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	supervisor, err := appserver.NewSupervisor(ctx, appserver.SupervisorConfig{
		Process: appserver.ProcessConfig{
			Command: cfg.AppServerCommand,
			Args:    cfg.AppServerArgs,
			Stderr:  os.Stderr,
		},
		Logger:            logger,
		InitializeTimeout: 5 * time.Second,
	})
	if err != nil {
		logger.Error("failed to start app server", slog.String("error", err.Error()))
		os.Exit(1)
	}
	defer func() {
		_ = supervisor.Close()
	}()

	server := acp.NewServer(
		acp.NewStdioCodec(os.Stdin, os.Stdout),
		supervisor,
		bridge.NewStore(),
		logger,
	)

	if err := server.Serve(ctx); err != nil && !errors.Is(err, io.EOF) {
		logger.Error("acp server stopped with error", slog.String("error", err.Error()))
		os.Exit(1)
	}
}
