# acp-adapter

## 1) What is this?
`acp-adapter` is a Go ACP adapter that lets ACP clients (such as Zed External Agents) drive AI assistants over the [ACP protocol](docs/SPEC.md).

Two backends are supported:

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
- Embedded library mode (`pkg/acpadapter` / `pkg/claudeacp`) for in-process use

## 3) Quickstart

### Option A: Codex backend
Prerequisite: Codex CLI installed and logged in.

```bash
go build -o ./bin/acp-adapter ./cmd/acp-adapter
./bin/acp-adapter
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
./bin/acp --adapter codex   # same as acp-adapter
./bin/acp --adapter claude  # Claude Code CLI subprocess
```

### Step 2: Point Zed External Agent config to this binary
Minimal template for Codex backend:

```json
{
  "agent_servers": {
    "acp-adapter": {
      "command": "/absolute/path/to/bin/acp-adapter",
      "args": [],
      "env": {
        "CODEX_APP_SERVER_CMD": "codex",
        "CODEX_APP_SERVER_ARGS": "app-server"
      }
    }
  }
}
```

Minimal template for Claude backend:

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

### Step 3: Start using it in Zed
Open Zed's Agent panel, choose the agent server, and start a new thread.

## 4) Usage tips
- Use your client's mention UX (for example `@file`) to pass file context.
- Common slash commands: `/review`, `/review-branch`, `/review-commit`, `/init`, `/compact`, `/logout`.
- MCP helpers (Codex backend): `/mcp list`, `/mcp call <server> <tool> [args]`, `/mcp oauth <server>`.
- For side-effect actions, handle the permission prompt: approve to proceed, decline to block safely.
- Default policy is fail-closed: without permission, write/command/network/MCP side-effects are not executed.
- If you wrap this binary, never write logs to `stdout`; use `stderr` only, or ACP parsing will fail.
- Claude backend: conversation history is managed by the Claude Code CLI (`~/.claude/projects/`); the adapter itself is stateless across restarts.

For protocol details and development/testing docs, see [`docs/SPEC.md`](docs/SPEC.md), [`docs/ACCEPTANCE.md`](docs/ACCEPTANCE.md), and [`PROGRESS.md`](PROGRESS.md).
