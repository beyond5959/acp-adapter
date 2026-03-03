# PR 计划

> 目标：把 “全功能 spec” 拆成可合并、可回归的 PR。每个 PR 必须能跑 `go test ./...`。

## PR1：工程骨架 + 双 codec + 最小端到端 harness
**目标**：可运行、可测试地完成 initialize/new/prompt/cancel 的“空心链路”。

- 新增文件
  - `cmd/acp-adapter/main.go`
  - `internal/acp/codec_stdio.go`（newline JSON-RPC）
  - `internal/acp/server.go`（handlers：initialize/session/new/session/prompt/session/cancel）
  - `internal/appserver/process.go`（spawn `codex app-server`）
  - `internal/appserver/codec_jsonl.go`
  - `internal/appserver/client.go`（initialize/initialized + thread/start + turn/start 的最小调用）
  - `internal/bridge/session_state.go`
  - `test/integration/e2e_test.go`（启动 adapter 进程，模拟 ACP client）
- 验收（自动化）
  - A1-A5、B1（最小）、B2（命令占位）

## PR2：完整流式映射与 turn 生命周期
**目标**：App Server notifications → ACP `session/update`，并正确结束 stopReason。

- 完成：
  - turn state machine：started → streaming → completed/cancelled
  - 事件分发：turnId/reqId 绑定
  - cancel：`session/cancel` → `turn/interrupt`
- 新增测试：
  - A4（必须看到 >=1 update）
  - A5（取消必须 stopReason=cancelled）

## PR3：Approvals → ACP permission（命令/文件/网络/MCP）
**目标**：所有敏感动作都能在上游弹 permission，并按用户选择推进/回滚。

- 完成：
  - approvals broker：下游请求 → 上游 `session/request_permission` → outcome → 下游回传
  - tool_call_update：in_progress/completed/failed
- 新增测试：
  - D1-D5（可用 mock app-server 或可控脚本）

## PR4：Edit review + Patch 落盘两模式
**目标**：review/start 与 patch 可审阅可应用，支持两种落盘策略。

- 完成：
  - `/review`：调用 app-server review 工作流（或等价）并流式映射
  - patch：解析 diff、展示、permission、应用
  - 写盘：AppServer 模式 + ACP fs 模式（若 client 支持）
- 新增测试：
  - E1/E2

## PR5：Slash commands + Custom prompts + MCP + Auth 收尾
**目标**：实现所有剩余“全功能项”，并把 ACCEPTANCE 全部跑通。

- 完成：
  - slash commands：/review-branch /review-commit /init /compact /logout
  - custom profiles（config.toml）
  - MCP servers：列出/调用/（如需要）oauth flow
  - auth：CODEX_API_KEY / OPENAI_API_KEY / subscription 登录态 + /logout
- 新增测试：
  - G1-G6、H1、I1-I3
  - J1/J2（至少提供脚本跑压力回归）
