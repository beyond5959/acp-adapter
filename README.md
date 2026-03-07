# ACP adapter for Codex & Claude Code

[![CI](https://github.com/beyond5959/acp-adapter/actions/workflows/go.yml/badge.svg)](https://github.com/beyond5959/acp-adapter/actions)
[![License](https://img.shields.io/github/license/beyond5959/acp-adapter)](LICENSE)
[![Go Version](https://img.shields.io/badge/Go-1.24+-blue)](https://go.dev)
[![Go Report Card](https://goreportcard.com/badge/github.com/beyond5959/acp-adapter)](https://goreportcard.com/report/github.com/beyond5959/acp-adapter)

<img src="docs/assets/acp-adapter.png" alt="acp-adapter promo" width="400">

`acp-adapter` is a Go ACP(Agent Client Protocol) adapter that lets ACP clients drive **Codex** and **Claude Code** over the [ACP protocol](https://agentclientprotocol.com/).

## Usage Modes

This component supports two integration models:

| Mode | Use Case | Entry Point |
|------|----------|-------------|
| **Standalone** (process) | Configure a binary in Zed or other ACP clients | [`cmd/acp`](./cmd/acp) |
| **Library** (embedded) | Host ACP runtime inside your Go service | [`pkg/codexacp`](./pkg/codexacp) [`pkg/claudeacp`](./pkg/claudeacp) |

## Standalone Usage

### Installation

```bash
curl -sSL https://raw.githubusercontent.com/beyond5959/acp-adapter/master/install.sh | sh
```

### Quick Start

```bash
# Codex backend (default)
acp-adapter --adapter codex

# Claude backend
acp-adapter --adapter claude
```

### ACP Client Config

```json
{
  "agent_servers": {
    "acp-adapter": {
      "command": "/usr/local/bin/acp-adapter",
      "args": ["--adapter", "codex"]
    }
  }
}
```

## Library Usage

```go
import "github.com/beyond5959/acp-adapter/pkg/codexacp"

// Stdio mode
cfg := codexacp.DefaultRuntimeConfig()
err := codexacp.RunStdio(ctx, cfg, os.Stdin, os.Stdout, os.Stderr)

// Embedded mode
rt := codexacp.NewEmbeddedRuntime(cfg)
rt.Start(ctx)
defer rt.Close()
resp, err := rt.ClientRequest(ctx, msg)
```

Use `pkg/claudeacp` for Claude backend—the API is identical.

