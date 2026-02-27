# DECISIONS.md

> 记录“关键决策与取舍”，用于防止上下文丢失导致反复争论/返工。  
> 规则：任何影响架构、协议、默认安全策略、接口形状、或与客户端兼容性的改变都必须记录。

## 决策索引（建议从这里开始）
- ADR-0001：stdout/stderr 分离（ACP stdio 合规）
- ADR-0002：下游采用 Codex App Server（stdio JSONL），不解析 CLI 文本
- ADR-0003：Schema 锁定策略（generate-json-schema + 版本钉死）
- ADR-0004：turn 并发策略（每 session 同时 1 个 active turn）
- ADR-0005：审批桥（App Server approvals -> ACP session/request_permission）
- ADR-0006：patch 落盘两模式（AppServer 落盘 / ACP fs 落盘）
- ADR-0007：终端/PTY 策略（默认安全、避免交互死锁）
- ADR-0008：Slash commands 处理策略（命令路由优先于普通 prompt）
- ADR-0009：长期记忆外置（PROGRESS/DECISIONS/KNOWN_ISSUES）
- ADR-0010：turn 生命周期状态机与 session/update 映射
- ADR-0011：app-server Supervisor 恢复策略（异常退出后重建）
- ADR-0012：ACP outbound `session/request_permission` 请求通道
- ADR-0013：审批默认拒绝策略与 `tool_call_update` 状态约定
- ADR-0014：`/review` 路由到 `review/start` + review mode 状态映射
- ADR-0015：Patch 落盘双模式（AppServer / ACP fs）与失败可见性

---

## ADR 模板（复制一份填写）
### ADR-000X：<标题>
- 日期：YYYY-MM-DD
- 状态：Proposed / Accepted / Superseded
- 背景：
- 决策：
- 备选方案：
- 取舍（Pros/Cons）：
- 影响范围（文件/模块）：
- 验证方式（测试/验收项）：

### ADR-0010：turn 生命周期状态机与 session/update 映射
- 日期：2026-02-27
- 状态：Accepted
- 背景：
  - PR2 需要把 App Server 流式 notifications 稳定映射到 ACP `session/update`，并保证 turn 有明确终态。
  - 需要满足 A4/A5（强化）和 B1 稳定性目标。
- 决策：
  - 在 ACP server 引入显式 turn 状态机：`started -> streaming -> completed/cancelled/error`。
  - 将 `turn/started`、`item/started`、`item/agentMessage/delta`、`item/completed`、`turn/completed` 统一转换为 `session/update`。
  - `session/prompt` 响应的 `stopReason` 由状态机终态统一归一。
- 备选方案：
  - 方案A：仅做事件透传，不维护显式状态。
  - 方案B：维护独立状态机并统一收敛 stopReason。（采用）
- 取舍（Pros/Cons）：
  - Pros：状态收敛一致、错误路径可控、测试可断言。
  - Cons：实现复杂度上升，需要维护状态与事件兼容。
- 影响范围（文件/模块）：
  - `internal/acp/server.go`
  - `internal/acp/types.go`
  - `internal/appserver/types.go`
  - `internal/appserver/client.go`
- 验证方式（测试/验收项）：
  - `TestE2EAcceptanceA1ToA5AndB1`
  - `TestE2ENotificationRoutingBySessionAndTurn`
  - 对应验收：A4、A5、B1

### ADR-0011：app-server Supervisor 恢复策略（异常退出后重建）
- 日期：2026-02-27
- 状态：Accepted
- 背景：
  - App Server 子进程异常退出会导致 pending turn 失败；PR2 要求“给上游可读错误并可恢复”。
- 决策：
  - 新增 `appserver.Supervisor` 管理子进程生命周期。
  - 对可恢复错误（EOF、read loop、broken pipe 等）执行重建握手。
  - 当前请求返回可读错误；后续请求可在重建后继续处理。
- 备选方案：
  - 方案A：进程异常后直接退出 adapter。
  - 方案B：失败后自动重建 app-server，并保留 adapter 进程。（采用）
- 取舍（Pros/Cons）：
  - Pros：提升稳定性，用户可继续会话；B1 可自动验证崩溃恢复路径。
  - Cons：崩溃当次请求仍会失败，需要客户端重试。
- 影响范围（文件/模块）：
  - `internal/appserver/supervisor.go`
  - `internal/appserver/process.go`
  - `cmd/codex-acp-go/main.go`
  - `test/integration/e2e_test.go`
  - `testdata/fake_codex_app_server/main.go`
- 验证方式（测试/验收项）：
  - `TestE2EAcceptanceB1AppServerCrashReturnsClearError`
  - 对应验收：B1（稳定性/恢复）

### ADR-0012：ACP outbound `session/request_permission` 请求通道
- 日期：2026-02-27
- 状态：Accepted
- 背景：
  - PR3 需要把下游 approval 请求桥接到 ACP `session/request_permission`，并等待上游用户决策。
  - 现有 ACP server 仅支持“上游 -> 适配器”请求处理，不支持“适配器 -> 上游”请求响应匹配。
- 决策：
  - 在 ACP server 引入 pending response map 和 request id 生成器。
  - `Serve` 循环同时处理两类消息：上游请求、上游对 outbound request 的响应。
  - 以 `session/request_permission` 作为唯一审批入口方法。
- 备选方案：
  - 方案A：把 permission 降级为 notification，不等待结果。
  - 方案B：实现完整 JSON-RPC request/response 往返。（采用）
- 取舍（Pros/Cons）：
  - Pros：协议语义完整，可实现 accept/decline/cancel 三分支。
  - Cons：ACP server 状态复杂度增加，需要维护并发安全。
- 影响范围（文件/模块）：
  - `internal/acp/server.go`
  - `internal/acp/types.go`
- 验证方式（测试/验收项）：
  - `TestE2EAcceptanceD1ToD5ApprovalsBridge`
  - 对应验收：D1、D2、D3、D4

### ADR-0013：审批默认拒绝策略与 `tool_call_update` 状态约定
- 日期：2026-02-27
- 状态：Accepted
- 背景：
  - PR3 要求“无 permission 不执行”并把工具状态持续映射到 ACP。
  - 需要统一定义审批失败/取消时的行为和上游可见状态。
- 决策：
  - 审批链路默认安全：permission 失败、超时、解析异常均回传 `cancelled`（不执行副作用）。
  - `tool_call_update` 状态约定：`in_progress -> completed|failed`。
  - 在 `tool_call_update` 中携带 `toolCallId`、审批类型（command/file/network/mcp）和最终 decision。
- 备选方案：
  - 方案A：失败时自动放行（fail-open）。
  - 方案B：失败时默认拒绝（fail-closed）。（采用）
- 取舍（Pros/Cons）：
  - Pros：满足 D5 安全要求，行为可预测，回归断言清晰。
  - Cons：当上游客户端异常时，工具会被保守拦截，可能增加“误拒绝”。
- 影响范围（文件/模块）：
  - `internal/acp/server.go`
  - `internal/acp/types.go`
  - `internal/appserver/client.go`
  - `internal/appserver/types.go`
- 验证方式（测试/验收项）：
  - `TestE2EAcceptanceD1ToD5ApprovalsBridge`
  - 对应验收：D1、D2、D3、D4、D5

### ADR-0014：`/review` 路由到 `review/start` + review mode 状态映射
- 日期：2026-02-27
- 状态：Accepted
- 背景：
  - PR4 要求 `/review` 工作流可见，且需要 entered/exited review mode（或等价）状态。
  - 继续复用 `turn/start` 难以表达 review 专属状态与事件语义。
- 决策：
  - ACP server 在 `session/prompt` 中识别 `/review` 前缀，转调 app-server `review/start`。
  - 新增 review mode notifications（entered/exited）并映射为 `session/update` status。
  - review diff 通过 message delta 透传，保持可读展示。
- 备选方案：
  - 方案A：`/review` 仍走 `turn/start` 并人工拼接状态。
  - 方案B：显式接入 `review/start` 并使用专属状态映射。（采用）
- 取舍（Pros/Cons）：
  - Pros：语义更清晰，E1 可直接断言；便于后续扩展 review 事件。
  - Cons：协议面增大，fake/real app-server 都需要对齐 review 事件。
- 影响范围（文件/模块）：
  - `internal/acp/server.go`
  - `internal/appserver/types.go`
  - `internal/appserver/client.go`
  - `internal/appserver/supervisor.go`
  - `testdata/fake_codex_app_server/main.go`
- 验证方式（测试/验收项）：
  - `TestE2EAcceptanceE1ReviewWorkflow`
  - 对应验收：E1

### ADR-0015：Patch 落盘双模式（AppServer / ACP fs）与失败可见性
- 日期：2026-02-27
- 状态：Accepted
- 背景：
  - PR4 要求 E2：同时支持 AppServer 落盘与 ACP fs 落盘，并在冲突/失败时可见。
  - 需要在保持 D2（permission gate）的同时提供可切换落盘路径。
- 决策：
  - 增加 `PATCH_APPLY_MODE`（`appserver|acp_fs`）配置，默认 `appserver`。
  - Mode A：批准后由 app-server 执行落盘。
  - Mode B：批准后由 adapter 调用上游 `fs/write_text_file` 执行落盘；失败时输出 `review_apply_failed`。
  - Mode B 落盘失败时仍保持 fail-closed，不放行副作用执行。
- 备选方案：
  - 方案A：仅支持单一落盘模式（简化实现）。
  - 方案B：双模式并用配置切换。（采用）
- 取舍（Pros/Cons）：
  - Pros：满足 E2 全量要求，可适配不同客户端 ownership 模型。
  - Cons：Mode B 依赖 ACP fs 方法契约，客户端兼容性风险上升。
- 影响范围（文件/模块）：
  - `internal/config/config.go`
  - `cmd/codex-acp-go/main.go`
  - `internal/acp/server.go`
  - `test/integration/e2e_test.go`
  - `testdata/fake_codex_app_server/main.go`
- 验证方式（测试/验收项）：
  - `TestE2EAcceptanceE2PatchModeAAppServer`
  - `TestE2EAcceptanceE2PatchModeBACPFS`
  - `TestE2EReviewPatchConflictVisibleModeB`
  - 对应验收：E2（并回归 D2）
