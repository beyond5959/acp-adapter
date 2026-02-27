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
