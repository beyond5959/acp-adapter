# acp-adapter

![acp-adapter promo](docs/assets/acp-adapter.png)

`acp-adapter` is a Go ACP adapter that lets ACP clients drive Codex and Claude Code over the [ACP protocol](https://agentclientprotocol.com/).

## 1) Positioning
This component supports two integration models:

| Model | Best for | Entry point |
|---|---|---|
| **Standalone (process mode)** | Configure a binary directly in Zed or other ACP clients | `cmd/acp` (Codex + Claude) |
| **Library (embedded mode)** | Host ACP runtime directly inside your Go service | `pkg/codexacp` / `pkg/claudeacp` |

Supported backends:

| Backend | Flag | Description |
|---|---|---|
| **Codex** (default) | `--adapter codex` | Drives Codex via `codex app-server` subprocess |
| **Claude** | `--adapter claude` | Drives Claude Code CLI (`claude -p`) as a subprocess |

ACP transport rules are strict: `stdout` must contain protocol messages only, and logs must go to `stderr`.

## 2) Features
- Context `@`-mentions and images in prompts
- Tool calls with permission requests (command/file/network/MCP side-effects)
- Edit review flow (`/review`, `/review-branch`, `/review-commit`)
- Structured TODO updates
- Slash commands: `/review`, `/review-branch`, `/review-commit`, `/init`, `/compact`, `/logout`
- Client MCP servers via `/mcp list`, `/mcp call`, `/mcp oauth`
- Library mode APIs for in-process integration (`RunStdio` + `NewEmbeddedRuntime`)

## 3) Standalone Usage (Binary)
### Option A: Codex backend
Prerequisite: Codex CLI installed and logged in.

```bash
go build -o ./bin/acp ./cmd/acp
./bin/acp --adapter codex
```

Auth (one of):
- Existing `codex login` session
- `CODEX_API_KEY`
- `OPENAI_API_KEY`

### Option B: Claude backend
Prerequisite: Claude Code CLI installed and logged in (`claude auth login`).

```bash
go build -o ./bin/acp ./cmd/acp
./bin/acp --adapter claude
```

Optional overrides:
- `CLAUDE_BIN` — path to the `claude` binary (default: `claude` in `$PATH`)
- `--model` — model name (default: `claude-opus-4-6`)
- `--max-turns` — max agentic turns per invocation (default: `10`)
- `--skip-perms` — pass `--dangerously-skip-permissions` to claude (default: `true`)

### Option C: Unified binary (both backends)
```bash
go build -o ./bin/acp ./cmd/acp
./bin/acp --adapter codex
./bin/acp --adapter claude
```

### Zed External Agent config (minimal)
Codex backend:

```json
{
  "agent_servers": {
    "codex-acp": {
      "command": "/absolute/path/to/bin/acp",
      "args": ["--adapter", "codex"],
      "env": {
        "CODEX_APP_SERVER_CMD": "codex",
        "CODEX_APP_SERVER_ARGS": "app-server"
      }
    }
  }
}
```

Claude backend:

```json
{
  "agent_servers": {
    "claude-acp": {
      "command": "/absolute/path/to/bin/acp",
      "args": ["--adapter", "claude"],
      "env": {
        "CLAUDE_BIN": "/usr/local/bin/claude"
      }
    }
  }
}
```

## 4) Use as a Library (Go)
### 4.1 Stdio Mode (Wrap in your own `main`)

```go
package main

import (
	"context"
	"log"
	"os"

	"github.com/beyond5959/acp-adapter/pkg/codexacp"
)

func main() {
	cfg := codexacp.DefaultRuntimeConfig()
	if err := codexacp.RunStdio(context.Background(), cfg, os.Stdin, os.Stdout, os.Stderr); err != nil {
		log.Fatal(err)
	}
}
```

### 4.2 Embedded Mode (In-Process RPC)

```go
cfg := codexacp.DefaultRuntimeConfig()
rt := codexacp.NewEmbeddedRuntime(cfg)
if err := rt.Start(ctx); err != nil {
	return err
}
defer rt.Close()

updates, unsubscribe := rt.SubscribeUpdates(64)
defer unsubscribe()

rawID := json.RawMessage(`1`)
rawParams := json.RawMessage(`{"protocolVersion":1}`)

// Send ACP JSON-RPC initialize request.
resp, err := rt.ClientRequest(ctx, codexacp.RPCMessage{
	ID:     &rawID,
	Method: "initialize",
	Params: rawParams,
})
_ = resp
_ = updates
```

For Claude in library mode, use `github.com/beyond5959/acp-adapter/pkg/claudeacp`; the API shape is aligned with `codexacp` (`RunStdio` / `NewEmbeddedRuntime`).

## 5) Usage tips
- Use your client's mention UX (for example `@file`) to pass file context.
- Common slash commands: `/review`, `/review-branch`, `/review-commit`, `/init`, `/compact`, `/logout`.
- MCP helpers (Codex backend): `/mcp list`, `/mcp call <server> <tool> [args]`, `/mcp oauth <server>`.
- For side-effect actions, handle the permission prompt: approve to proceed, decline to block safely.
- Default policy is fail-closed: without permission, write/command/network/MCP side-effects are not executed.
- If you wrap this binary, never write logs to `stdout`; use `stderr` only, or ACP parsing will fail.
- Claude backend: conversation history is managed by the Claude Code CLI (`~/.claude/projects/`); the adapter itself is stateless across restarts.

For protocol details and development/testing docs, see [`docs/SPEC.md`](docs/SPEC.md), [`docs/ACCEPTANCE.md`](docs/ACCEPTANCE.md), and [`PROGRESS.md`](PROGRESS.md).
