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

	process, err := appserver.StartProcess(ctx, appserver.ProcessConfig{
		Command: cfg.AppServerCommand,
		Args:    cfg.AppServerArgs,
		Stderr:  os.Stderr,
	})
	if err != nil {
		logger.Error("failed to start app server", slog.String("error", err.Error()))
		os.Exit(1)
	}
	defer func() {
		_ = process.Close()
	}()

	appClient := appserver.NewClient(process, logger)
	defer appClient.Close()

	handshakeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	if err := appClient.Initialize(handshakeCtx); err != nil {
		logger.Error("app server initialize failed", slog.String("error", err.Error()))
		os.Exit(1)
	}
	if err := appClient.Initialized(); err != nil {
		logger.Error("app server initialized notification failed", slog.String("error", err.Error()))
		os.Exit(1)
	}

	server := acp.NewServer(
		acp.NewStdioCodec(os.Stdin, os.Stdout),
		appClient,
		bridge.NewStore(),
		logger,
	)

	if err := server.Serve(ctx); err != nil && !errors.Is(err, io.EOF) {
		logger.Error("acp server stopped with error", slog.String("error", err.Error()))
		os.Exit(1)
	}
}
