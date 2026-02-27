# PROGRESS.md

> 本文件是本项目的“长期记忆入口”。任何时候如果对进度/状态不确定，以本文件为准。  
> 更新频率：每合并一个 PR 必须更新一次；每次发现阻塞也要更新。

## 项目概览
- 项目：codex-acp-go（基于 Codex App Server 的 ACP 适配器）
- 当前阶段：PR1
- 最近更新：2026-02-27

## 关键链接/文档
- docs/SPEC.md：技术方案（权威）
- docs/ACCEPTANCE.md：验收清单（必须逐条通过）
- docs/PR_PLAN.md：实施分 PR 计划
- docs/DECISIONS.md：关键决策记录
- docs/KNOWN_ISSUES.md：已知问题与规避

## 当前里程碑状态（按 PR）
- [ ] PR1：工程骨架 + 双 codec + 最小 e2e harness（initialize/new/prompt/cancel）
  - 状态：Done
  - 说明：已完成可运行骨架、ACP stdio codec、App Server 子进程 client、e2e harness（A1-A5 + B1 自动化覆盖）
- [ ] PR2：流式映射与 turn 生命周期（notifications -> session/update; cancel 语义）
  - 状态：Not started
  - 说明：待实现完整 notifications 映射与更细粒度 turn state machine
- [ ] PR3：Approvals -> ACP permission（command/file/network/mcp）
  - 状态：Not started
  - 说明：待实现 approval broker 与 permission 回传
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
- [ ] D1 命令执行审批
- [ ] D2 文件改动审批
- [ ] D3 网络审批
- [ ] D4 MCP side-effect 审批
- [ ] D5 默认安全策略

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
- [ ] J2 stdout 纯净（trace 脱敏）

## 本 PR 做了什么
1. 初始化 Go 工程与执行入口：新增 `go.mod`、`cmd/codex-acp-go/main.go`。
2. 实现 ACP stdio newline-delimited JSON-RPC codec 与方法路由：
   - `initialize`
   - `session/new`
   - `session/prompt`
   - `session/cancel`
3. 实现 App Server 子进程与最小 client：
   - 启动 `codex app-server`（支持通过环境变量覆写命令）
   - 完成 `initialize`、`initialized`、`thread/start`、`turn/start`、`turn/interrupt`。
4. 实现会话状态管理（session-thread-turn 绑定、单 session 单 active turn）。
5. 增加 e2e 测试基建：
   - fake app-server（可控 turn/update/completed/cancel）
   - 启动真实 adapter 进程进行协议级测试。
6. 补齐 PR1 自动化验收到 A1-A5 + B1：
   - 验证 stdout 纯 JSON-RPC
   - 验证 initialize/new/prompt/cancel
   - 验证 app-server 初始化握手约束（initialize/initialized）
   - 验证 app-server thread/start 崩溃时错误可见
7. 增加 `make schema` 目标与 `internal/appserver/schema/` 目录占位。

## 影响范围是什么
1. 运行行为：adapter 启动后会自动拉起下游 app-server 并完成初始化握手。
2. 协议行为：上游 ACP client 可完成最小闭环（initialize/new/prompt/cancel）。
3. 输出约束：stdout 仅输出 ACP JSON-RPC 消息；日志输出到 stderr。
4. 测试基线：引入端到端 harness，后续 PR2+ 能在此基础上扩展回归。

## 如何验证
1. 执行：
   - `go test ./...`
2. 预期：
   - `test/integration` 通过，包含：
     - `TestE2EAcceptanceA1ToA5AndB1`
     - `TestE2EAcceptanceB1AppServerCrashReturnsClearError`
   - A1-A5 + B1 由 e2e 自动验证。
   - 测试中会校验 adapter stdout 每行都是合法 JSON-RPC。
3. 可选手工验证：
   - 启动 `cmd/codex-acp-go`。
   - 通过 stdin 发送 `initialize`、`session/new`、`session/prompt`、`session/cancel`。
   - 观察 stdout 仅协议消息，stderr 为日志。

## 遗留问题是什么
1. B2 尚未完成：当前只有 `make schema` 命令与目录占位，未提交真实 schema 产物与版本锁定校验。
2. PR2~PR5 尚未开始：完整 notifications 流映射、permission、review、slash commands、MCP、auth 仍待实现。
3. 当前 e2e 依赖 fake app-server：真实 codex app-server 的全能力兼容性仍需后续补充验证。

## 当前阻塞（Blockers）
- 无

## 下一步（Next）
1. 进入 PR2：完善 App Server notifications -> ACP `session/update` 全量映射。
2. 强化 cancel 语义：细化 turn state，覆盖竞态与超时场景。
3. 补充更多 e2e case：多 update、错误路径、并发 session。

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
