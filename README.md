# ACP adapter for Codex, Claude Code, and Pi

[![CI](https://github.com/beyond5959/acp-adapter/actions/workflows/go.yml/badge.svg)](https://github.com/beyond5959/acp-adapter/actions)
[![License](https://img.shields.io/github/license/beyond5959/acp-adapter)](LICENSE)
[![Go Version](https://img.shields.io/badge/Go-1.24+-blue)](https://go.dev)
[![Go Report Card](https://goreportcard.com/badge/github.com/beyond5959/acp-adapter)](https://goreportcard.com/report/github.com/beyond5959/acp-adapter)

<img src="docs/assets/acp-adapter.png" alt="acp-adapter promo" width="400">

`acp-adapter` is a Go ACP (Agent Client Protocol) adapter that lets ACP clients drive multiple downstream agent backends over the [ACP protocol](https://agentclientprotocol.com/):

| Backend | Downstream official channel | Standalone flag | Library package | Notes |
|------|------|------|------|------|
| Codex | `codex app-server` over stdio JSON-RPC | `--adapter codex` | [`pkg/codexacp`](./pkg/codexacp) | Most complete backend, including MCP routing. |
| Claude Code | `claude -p ... --output-format stream-json` | `--adapter claude` | [`pkg/claudeacp`](./pkg/claudeacp) | Machine-readable Claude CLI bridge. |
| Pi | `pi --mode rpc` over official RPC JSON lines | `--adapter pi` | [`pkg/piacp`](./pkg/piacp) | Official Pi RPC bridge with session load/list and ACP permission gate. |

## Usage Modes

This component supports two integration models:

| Mode | Use Case | Entry Point |
|------|----------|-------------|
| **Standalone** (process) | Configure a binary in Zed or other ACP clients | [`cmd/acp`](./cmd/acp) |
| **Library** (embedded) | Host ACP runtime inside your Go service | [`pkg/codexacp`](./pkg/codexacp), [`pkg/claudeacp`](./pkg/claudeacp), [`pkg/piacp`](./pkg/piacp) |

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

# Pi backend
acp-adapter --adapter pi --pi-provider openai-codex --pi-model gpt-5.4-mini
```

Pi mode expects a working `pi` CLI on `PATH` or `--pi-bin`. Useful Pi-specific flags:

- `--pi-provider` / `PI_PROVIDER`
- `--pi-model` / `PI_MODEL`
- `--pi-session-dir` / `PI_SESSION_DIR`
- `--pi-disable-gate` / `PI_DISABLE_GATE`

### ACP Client Config

```json
{
  "agent_servers": {
    "acp-adapter": {
      "command": "/usr/local/bin/acp-adapter",
      "args": ["--adapter", "pi", "--pi-provider", "openai-codex", "--pi-model", "gpt-5.4-mini"]
    }
  }
}
```

Swap `pi` for `codex` or `claude` if you want a different backend.

## Library Usage

The three runtime packages expose aligned entry points: `DefaultRuntimeConfig`, `RunStdio`, and `NewEmbeddedRuntime`.

```go
import "github.com/beyond5959/acp-adapter/pkg/piacp"

// Stdio mode
cfg := piacp.DefaultRuntimeConfig()
cfg.DefaultProvider = "openai-codex"
cfg.DefaultModel = "gpt-5.4-mini"
err := piacp.RunStdio(ctx, cfg, os.Stdin, os.Stdout, os.Stderr)

// Embedded mode
rt := piacp.NewEmbeddedRuntime(cfg)
rt.Start(ctx)
defer rt.Close()
resp, err := rt.ClientRequest(ctx, msg)
```

Use [`pkg/codexacp`](./pkg/codexacp) for Codex and [`pkg/claudeacp`](./pkg/claudeacp) for Claude; the runtime shape is the same, while config fields stay backend-specific.

## Codex ACP Support

This section is intentionally based on the current code in [`internal/acp/server.go`](./internal/acp/server.go) and [`internal/acp/types.go`](./internal/acp/types.go).

| ACP surface | Support | Notes |
|------|------|------|
| `initialize` | Yes | Returns `protocolVersion=1` and ACP capability flags. |
| `authenticate` | Yes | Supports `codex_api_key`, `openai_api_key`, `chatgpt_subscription`. |
| `session/new` | Yes | Creates a Codex-backed session/thread. |
| `session/prompt` | Yes | Main turn entry; slash commands are routed here too. |
| `session/cancel` | Yes | Cancels the active Codex turn. |
| `session/list` | Yes | Lists Codex-backed sessions. |
| `session/load` | Yes | Loads a historical Codex session. |
| `session/set_config_option` | Yes | Currently supports `model` and `thought_level`. |
| `session/update` | Yes | Streams prompt execution updates back to the ACP client. |
| `session/request_permission` | Yes | Used for command/file/network/MCP approvals. |
| `fs/read_text_file` | Partial | Used only when the client exposes it, for mentions and diff reconstruction. |
| `fs/write_text_file` | Partial | Used only when `PATCH_APPLY_MODE=acp_fs`. |

| `session/update.type` | Standard `update.sessionUpdate` | Support | Notes |
|------|------|------|------|
| `message` | `agent_message_chunk` / `user_message_chunk` | Yes | Main text streaming path. |
| `tool_call_update` | `tool_call_update` | Yes | Tool lifecycle, approvals, command output, diff, text, and image content. |
| `usage_update` | `usage_update` | Yes | Emitted from Codex `thread/tokenUsage/updated`; currently maps `used=tokenUsage.last.inputTokens` and `size=modelContextWindow`. |
| `config_options_update` | `config_options_update` | Yes | Emitted after `session/set_config_option`. |
| `plan` | `plan` | Yes | Emitted from Codex plan events. |
| `available_commands_update` | `available_commands_update` | Yes | Publishes the Codex slash-command directory. |
| `reasoning` | `agent_thought_chunk` | Yes | Reasoning output is exposed as ACP thought chunks. |
| `status` | no dedicated standard kind | Partial | Present on the wire, but the standard envelope currently falls back to `agent_thought_chunk`. |

| ACP capability flag | Support | Notes |
|------|------|------|
| `loadSession` | Yes | Advertised from `initialize`. |
| `sessionCapabilities.list` | Yes | Advertised when the Codex backend supports session listing. |
| `promptCapabilities.image` | Yes | Advertised. |
| `promptCapabilities.audio` | No | Advertised as unsupported. |
| `promptCapabilities.embeddedContext` | Yes | Advertised. |
| `mcpCapabilities.http` | No | MCP is bridged through Codex command/tool paths, not ACP HTTP transport. |
| `mcpCapabilities.sse` | No | MCP is bridged through Codex command/tool paths, not ACP SSE transport. |

## Pi ACP Support

This section is intentionally based on the current code in [`internal/acp/server.go`](./internal/acp/server.go), [`internal/pi/client.go`](./internal/pi/client.go), and [`internal/acp/types.go`](./internal/acp/types.go).

Pi mode currently publishes these slash commands: `/review`, `/review-branch`, `/review-commit`, `/init`, `/compact`, `/logout`.

| ACP surface | Support | Notes |
|------|------|------|
| `initialize` | Yes | Returns `protocolVersion=1` and ACP capability flags. |
| `authenticate` | Yes | Supports `pi`; also restores adapter-side Pi availability after `/logout`. |
| `session/new` | Yes | Creates a Pi-backed session through official `pi --mode rpc`. |
| `session/prompt` | Yes | Main turn entry; slash commands are routed here too. |
| `session/cancel` | Yes | Cancels the active Pi turn. |
| `session/list` | Yes | Lists Pi session files from the configured or default Pi session directory. |
| `session/load` | Yes | Loads a historical Pi session file, replays history, and allows continued prompting. |
| `session/set_config_option` | Yes | Currently supports `model` and `thought_level`. |
| `session/update` | Yes | Streams Pi prompt execution updates back to the ACP client. |
| `session/request_permission` | Yes | Used for Pi `bash` / `write` / `edit` permission gate requests. |
| `fs/read_text_file` | Partial | Used only when the client exposes it, for mentions and embedded context. |
| `fs/write_text_file` | No | Current Pi bridge does not use ACP fs patch apply; writes stay on Pi tool paths behind permission gate. |

| `session/update.type` | Standard `update.sessionUpdate` | Support | Notes |
|------|------|------|------|
| `message` | `agent_message_chunk` / `user_message_chunk` | Yes | Main text streaming path. |
| `tool_call_update` | `tool_call_update` | Yes | Tool lifecycle, approvals, command output, and tool text output from Pi RPC events. |
| `usage_update` | `usage_update` | Yes | Best-effort mapping from Pi `get_session_stats`; emitted when stats are available in time. |
| `config_options_update` | `config_options_update` | Yes | Emitted after `session/set_config_option`. |
| `plan` | `plan` | No | Current Pi bridge does not emit ACP plan snapshots. |
| `available_commands_update` | `available_commands_update` | Yes | Publishes the Pi slash-command directory and refreshes it on auth changes. |
| `reasoning` | `agent_thought_chunk` | Yes | Pi `thinking_delta` is exposed as ACP thought chunks. |
| `status` | no dedicated standard kind | Partial | Used for review lifecycle and backend/gate diagnostics; no dedicated standard envelope kind exists yet. |

| ACP capability flag | Support | Notes |
|------|------|------|
| `loadSession` | Yes | Advertised from `initialize`. |
| `sessionCapabilities.list` | Yes | Advertised in Pi mode. |
| `promptCapabilities.image` | Yes | Advertised. |
| `promptCapabilities.audio` | No | Advertised as unsupported. |
| `promptCapabilities.embeddedContext` | Yes | Advertised. |
| `mcpCapabilities.http` | No | Pi mode does not bridge ACP HTTP MCP transport. |
| `mcpCapabilities.sse` | No | Pi mode does not bridge ACP SSE MCP transport. |

Current Pi-specific gaps relative to Codex:

- No `/mcp` command or MCP list/call/oauth routing.
- No archived session pagination in `session/list`.
- `/review` is simulated through normal Pi prompts plus synthetic review lifecycle events, not a native Pi review RPC.
- Pi custom `get_commands` and generic `extension_ui_request` flows are not yet bridged to ACP.
