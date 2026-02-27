# PROGRESS.md

> 本文件是本项目的“长期记忆入口”。任何时候如果对进度/状态不确定，以本文件为准。  
> 更新频率：每合并一个 PR 必须更新一次；每次发现阻塞也要更新。

## 项目概览
- 项目：codex-acp-go（基于 Codex App Server 的 ACP 适配器）
- 当前阶段：PR3
- 最近更新：2026-02-27

## 关键链接/文档
- docs/SPEC.md：技术方案（权威）
- docs/ACCEPTANCE.md：验收清单（必须逐条通过）
- docs/PR_PLAN.md：实施分 PR 计划
- docs/DECISIONS.md：关键决策记录
- docs/KNOWN_ISSUES.md：已知问题与规避

## 当前里程碑状态（按 PR）
- [x] PR1：工程骨架 + 双 codec + 最小 e2e harness（initialize/new/prompt/cancel）
  - 状态：Done
  - 说明：已完成可运行骨架、ACP stdio codec、App Server 子进程 client、e2e harness（A1-A5 + B1 自动化覆盖）
- [x] PR2：流式映射与 turn 生命周期（notifications -> session/update; cancel 语义）
  - 状态：Done
  - 说明：已实现 turn 状态机、notification 路由、cancel 语义强化、子进程异常退出恢复与错误可读化
- [x] PR3：Approvals -> ACP permission（command/file/network/mcp）
  - 状态：Done
  - 说明：已实现 approvals broker、permission 三分支映射与 tool_call_update 状态闭环
- [ ] PR4：Edit review + patch 落盘两模式
  - 状态：Not started
  - 说明：待实现 review 模式和落盘双策略
- [ ] PR5：Slash commands + Custom prompts + MCP + Auth 收尾
  - 状态：Not started
  - 说明：待完成功能收尾与全量验收

## 验收进度（从 docs/ACCEPTANCE.md 勾选）
### A. 协议合规（ACP）
- [x] A1 stdio 合规（stdout 纯协议，stderr 仅日志）
- [x] A2 initialize
- [x] A3 session/new
- [x] A4 session/prompt（流式）
- [x] A5 session/cancel

### B. App Server 对接
- [x] B1 App Server 初始化
- [ ] B2 Schema 锁定（make schema）

### C. 内容能力
- [ ] C1 @-mentions
- [ ] C2 Images

### D. 工具、审批与安全
- [x] D1 命令执行审批
- [x] D2 文件改动审批
- [x] D3 网络审批
- [x] D4 MCP side-effect 审批
- [x] D5 默认安全策略

### E. Edit review
- [ ] E1 Review 模式输出
- [ ] E2 Patch 落盘两模式

### F. TODO lists
- [ ] F1 结构化 TODO

### G. Slash commands
- [ ] G1 /review
- [ ] G2 /review-branch
- [ ] G3 /review-commit
- [ ] G4 /init
- [ ] G5 /compact
- [ ] G6 /logout

### H. Custom Prompts
- [ ] H1 profiles 生效

### I. Auth methods
- [ ] I1 CODEX_API_KEY
- [ ] I2 OPENAI_API_KEY
- [ ] I3 subscription 登录态（如环境支持）

### J. 可靠性
- [ ] J1 压力回归（100 turns 含 approve/deny/cancel）
- [x] J2 stdout 纯净（trace 脱敏）

## 本 PR 做了什么
1. 实现 Approvals 桥：App Server `approval/request` → ACP `session/request_permission` → App Server approval response。
2. 补齐 `tool_call_update` 状态闭环：`in_progress -> completed/failed`，并带 `toolCallId`、审批类型、permission decision。
3. 覆盖 command/file/network/MCP side-effect 四类审批字段映射与上游展示。
4. 落实默认安全策略（D5）：permission 获取失败/超时时默认 `cancelled`，不放行副作用执行。
5. 扩展 ACP server 双向 JSON-RPC 能力：支持上游对 `session/request_permission` 的响应路由。
6. 升级 fake app-server + e2e harness，自动验证 accept/decline/cancel 三分支。

## 影响范围是什么
1. `internal/acp`：新增 `session/request_permission` outbound 请求与审批结果处理逻辑。
2. `internal/appserver`：新增 server-initiated `approval/request` 处理与 `ApprovalRespond` 回传通道。
3. `testdata/fake_codex_app_server`：新增审批请求/响应机制与四类 side-effect 场景。
4. `test/integration`：新增 D1-D5 自动化覆盖（accept/decline/cancel）。

## 如何验证
1. 执行：
   - `go test ./...`
2. 预期：
   - `test/integration` 通过，包含：
     - `TestE2EAcceptanceA1ToA5AndB1`
     - `TestE2EAcceptanceB1AppServerCrashReturnsClearError`
     - `TestE2ENotificationRoutingBySessionAndTurn`
     - `TestE2EAcceptanceD1ToD5ApprovalsBridge`
     - `TestRPCReaderDetectsInvalidStdoutLine`
   - PR3 相关验收由 e2e 自动覆盖：D1、D2、D3、D4、D5（含 accept/decline/cancel）。
   - 测试中持续校验 adapter stdout 每行均为合法 JSON-RPC。
3. 可选手工验证：
   - 启动 `cmd/codex-acp-go`。
   - 发送触发副作用的 prompt（command/file/network/mcp）。
   - 观察 adapter 发出 `session/request_permission`，并在批准/拒绝/取消后看到 `tool_call_update` 状态收敛。

## 遗留问题是什么
1. B2 尚未完成：schema 仍为目录占位，缺少真实产物与 hash 追踪校验。
2. 当前崩溃恢复策略对“当次请求”返回可读失败，需要客户端重试一次（已在 KNOWN_ISSUES 记录）。
3. 审批等待期间若上游长期不响应，会触发默认取消；真实客户端超时体验仍需联调优化。
4. e2e 仍主要依赖 fake app-server，真实 codex app-server 审批事件形态回归需在后续 PR 补齐。

## 当前阻塞（Blockers）
- 无

## 下一步（Next）
1. 进入 PR4：实现 Edit review 输出与 patch 落盘双模式（E1/E2）。
2. 针对审批链路补充真实 codex app-server 联调样例（脱敏录制）。
3. 开始评估审批等待超时策略与客户端侧可配置项。

## 变更摘要（每 PR 一条）
### 2026-02-26 — PR1 工程骨架 + 双 codec + 最小 e2e harness
- Done:
  - 完成 ACP/app-server 双 codec
  - 完成 initialize/new/prompt/cancel 最小链路
  - 完成 app-server 子进程 client 与 session state
  - 完成 fake app-server + e2e 测试
  - e2e 补齐 A1-A5 + B1（含 app-server 崩溃错误路径）
- Tests:
  - `go test ./...` 通过
- Notes/Follow-ups:
  - `make schema` 已提供，真实 schema 锁定与校验在后续 PR 补齐

### 2026-02-27 — PR2 流式映射与 turn 生命周期状态机
- A. 范围与目标:
  - 覆盖 A4、A5（强化）、B1（稳定性）、J2（stdout 纯净可检测）
  - 建立 `started -> streaming -> completed/cancelled/error` 生命周期
- B. 实现:
  - `appserver/client` 支持 `turn/started`、`item/started`、`item/agentMessage/delta`、`item/completed`、`turn/completed` 路由
  - `acp/server` 引入 turn 生命周期状态机并映射为 ACP `session/update`
  - `session/cancel -> turn/interrupt` 后保证 prompt 收敛为 `stopReason=cancelled`
  - 新增 `appserver/supervisor`：子进程异常后重启，向上游返回可读错误并支持后续恢复
- C. 验证:
  - e2e 新增/强化：
    - `TestE2EAcceptanceA1ToA5AndB1`
    - `TestE2EAcceptanceB1AppServerCrashReturnsClearError`
    - `TestE2ENotificationRoutingBySessionAndTurn`
    - `TestRPCReaderDetectsInvalidStdoutLine`
  - `go test ./...` 通过
- D. 文档:
  - 更新 `PROGRESS.md`、`docs/DECISIONS.md`、`docs/KNOWN_ISSUES.md`

### 2026-02-27 — PR3 Approvals -> ACP session/request_permission
- A. 范围与目标:
  - 覆盖 D1-D5（command/file/network/mcp side-effect + 默认安全策略）
  - 打通 approvals 请求/响应桥接和 `tool_call_update` 状态闭环
- B. 实现:
  - `acp/server` 新增 `session/request_permission` outbound 请求与响应路由
  - `appserver/client` 新增 server-initiated `approval/request` 处理与 `ApprovalRespond`
  - 默认拒绝策略：permission 失败/超时时回传 `cancelled`，不执行副作用
  - fake app-server 增加四类审批场景与审批响应等待机制
- C. 验证:
  - 新增 `TestE2EAcceptanceD1ToD5ApprovalsBridge`
  - `go test ./...` 通过
- D. 文档:
  - 更新 `PROGRESS.md`、`docs/DECISIONS.md`、`docs/KNOWN_ISSUES.md`
