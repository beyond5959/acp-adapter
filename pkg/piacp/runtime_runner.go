package piacp

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"

	"github.com/beyond5959/acp-adapter/internal/acp"
	"github.com/beyond5959/acp-adapter/internal/bridge"
	"github.com/beyond5959/acp-adapter/internal/observability"
	"github.com/beyond5959/acp-adapter/internal/pi"
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

	client := pi.NewClient(pi.Config{
		PiBin:           cfg.PiBin,
		ExtraArgs:       append([]string(nil), cfg.PiArgs...),
		DefaultProvider: cfg.DefaultProvider,
		DefaultModel:    cfg.DefaultModel,
		SessionDir:      cfg.SessionDir,
		EnableGate:      cfg.EnableGate,
		Stderr:          stderr,
		Trace: func(direction string, payload []byte) {
			if traceFile != nil {
				traceFile.TraceAppServer(direction, payload)
			}
		},
	})

	server := acp.NewServer(
		transport,
		client,
		bridge.NewStore(),
		logger,
		acp.ServerOptions{
			PatchApplyMode:  cfg.PatchApplyMode,
			Profiles:        toACPProfiles(cfg.Profiles),
			DefaultProfile:  cfg.DefaultProfile,
			InitialAuthMode: "pi",
			AuthMethods: []acp.AuthMethod{
				{
					ID:          "pi",
					Name:        "Pi credentials",
					Description: "Authenticate with configured Pi provider credentials or existing login state.",
					Type:        "pi",
					Label:       "Pi credentials",
				},
			},
			AvailableCommands: acp.PiAvailableCommands(),
		},
	)

	if err := server.Serve(ctx); err != nil && !errors.Is(err, io.EOF) {
		logger.Error("acp server stopped with error", slog.String("error", err.Error()))
		return err
	}
	return nil
}
