# codex-acp-go

## 1) What is this?
`codex-acp-go` is a Go ACP adapter that lets ACP clients (such as Zed External Agents) use Codex.

Upstream transport is ACP over stdio, using newline-delimited JSON-RPC (one JSON message per line). ACP transport rules are strict: `stdout` must contain protocol messages only, and logs must go to `stderr`.

Downstream, this adapter drives Codex through **Codex App Server** (`codex app-server`) over stdio JSONL/JSON-RPC. It does not parse CLI text output. This preserves App Server capabilities such as authentication, conversation history, approvals, and streamed turn events.

By default, the adapter launches `codex app-server` (configurable via `CODEX_APP_SERVER_CMD` and `CODEX_APP_SERVER_ARGS`).

## 2) Features
- Context `@`-mentions
- Images in prompts
- Tool calls with permission requests (command/file/network/MCP side-effects)
- Edit review flow
- Structured TODO updates
- Slash commands: `/review`, `/review-branch`, `/review-commit`, `/init`, `/compact`, `/logout` (`/compact` behavior may vary by Codex App Server version)
- Client MCP servers via `/mcp list`, `/mcp call`, `/mcp oauth` (limited cross-version real-backend coverage)
- Auth methods: ChatGPT subscription, `CODEX_API_KEY`, `OPENAI_API_KEY`

## 3) Quickstart
Prerequisite: you already have Codex CLI installed and logged in.

### Step 1: Build and run the adapter
From repo root:

```bash
go build -o ./bin/codex-acp-go ./cmd/codex-acp-go
./bin/codex-acp-go
```

Optional process overrides:

- `CODEX_APP_SERVER_CMD` (default `codex`)
- `CODEX_APP_SERVER_ARGS` (default `app-server`)

If you use API key auth instead of existing CLI login, set one of:

- `CODEX_API_KEY`
- `OPENAI_API_KEY`

### Step 2: Point Zed External Agent config to this binary
Minimal template (adjust path/env for your machine):

```json
{
  "agent_servers": {
    "codex-acp-go": {
      "command": "/absolute/path/to/bin/codex-acp-go",
      "args": [],
      "env": {
        "CODEX_APP_SERVER_CMD": "codex",
        "CODEX_APP_SERVER_ARGS": "app-server"
      }
    }
  }
}
```

### Step 3: Start using it in Zed
Open Zed's Agent panel, choose the `codex-acp-go` agent server, and start a new Codex thread.

## 4) Usage tips
- Use your client's mention UX (for example `@file`) to pass file context.
- Common slash commands: `/review`, `/review-branch`, `/review-commit`, `/init`, `/compact`, `/logout`.
- MCP helpers: `/mcp list`, `/mcp call <server> <tool> [args]`, `/mcp oauth <server>`.
- For side-effect actions, handle the permission prompt: approve to proceed, decline to block safely.
- Default policy is fail-closed: without permission, write/command/network/MCP side-effects are not executed.
- If you wrap this binary, never write logs to `stdout`; use `stderr` only, or ACP parsing will fail.

For protocol details and development/testing docs, see [`docs/SPEC.md`](docs/SPEC.md), [`docs/ACCEPTANCE.md`](docs/ACCEPTANCE.md), and [`PROGRESS.md`](PROGRESS.md).
