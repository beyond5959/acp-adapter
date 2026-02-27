# PROGRESS.md

> 本文件是本项目的“长期记忆入口”。任何时候如果对进度/状态不确定，以本文件为准。  
> 更新频率：每合并一个 PR 必须更新一次；每次发现阻塞也要更新。

## 项目概览
- 项目：codex-acp-go（基于 Codex App Server 的 ACP 适配器）
- 当前阶段：PR4
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
- [x] PR4：Edit review + patch 落盘两模式
  - 状态：Done
  - 说明：已实现 /review 工作流、review mode 状态映射、Mode A/Mode B 落盘与冲突可见
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
- [x] E1 Review 模式输出
- [x] E2 Patch 落盘两模式

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
1. 新增 `/review` 工作流：adapter 识别 review prompt 并走 `review/start`，映射 review mode entered/exited 到 `session/update`。
2. 完成 diff 展示：review 流中输出可读 diff message chunk（markdown diff）。
3. 实现 patch 落盘两模式：
   - Mode A：AppServer 落盘
   - Mode B：ACP fs 落盘（adapter 调用上游 `fs/write_text_file`）
4. 增加 Mode B 冲突/失败可见：落盘失败时输出 `review_apply_failed`，并保持 tool 状态可追踪。
5. 回归并保持 D2：文件修改仍需 permission，拒绝/取消不落盘。

## 影响范围是什么
1. `internal/acp`：新增 patch apply mode、`/review` 路由、Mode B fs 落盘桥、review 状态映射。
2. `internal/appserver`：新增 `review/start` client/supervisor 调用与 review mode notifications 解码。
3. `internal/config`/`cmd`：新增 `PATCH_APPLY_MODE`（`appserver|acp_fs`）配置并传入 server。
4. `testdata/fake_codex_app_server`：新增 review/start 与 review mode 事件、review patch 审批与冲突模拟。
5. `test/integration`：新增 E1/E2 + D2 回归自动化测试。

## 如何验证
1. 执行：
   - `go test ./...`
2. 预期：
   - `test/integration` 通过，包含：
     - `TestE2EAcceptanceA1ToA5AndB1`
     - `TestE2EAcceptanceB1AppServerCrashReturnsClearError`
     - `TestE2ENotificationRoutingBySessionAndTurn`
     - `TestE2EAcceptanceD1ToD5ApprovalsBridge`
     - `TestE2EAcceptanceE1ReviewWorkflow`
     - `TestE2EAcceptanceE2PatchModeAAppServer`
     - `TestE2EAcceptanceE2PatchModeBACPFS`
     - `TestE2EReviewPatchConflictVisibleModeB`
     - `TestRPCReaderDetectsInvalidStdoutLine`
   - PR4 相关验收由 e2e 自动覆盖：E1、E2，并回归 D2。
   - 测试中持续校验 adapter stdout 每行均为合法 JSON-RPC。
3. 可选手工验证：
   - 启动 `cmd/codex-acp-go`。
   - 发送 `/review <instructions>` 触发 review workflow。
   - 在 Mode A/Mode B 下分别批准文件变更，观察 patch 应用路径与状态输出。
   - 构造冲突场景，确认 `review_apply_failed` 与失败原因可见。

## 遗留问题是什么
1. B2 尚未完成：schema 仍为目录占位，缺少真实产物与 hash 追踪校验。
2. 当前崩溃恢复策略对“当次请求”返回可读失败，需要客户端重试一次（已在 KNOWN_ISSUES 记录）。
3. 审批等待期间若上游长期不响应，会触发默认取消；真实客户端超时体验仍需联调优化。
4. Mode B 当前依赖 `fs/write_text_file` 约定，真实 ACP client 兼容性需在后续联调确认。
5. e2e 仍主要依赖 fake app-server，真实 codex app-server review/edit 事件回归需在后续 PR 补齐。

## 当前阻塞（Blockers）
- 无

## 下一步（Next）
1. 进入 PR5：补齐 slash commands（G1-G6）、custom prompts、MCP servers、auth methods。
2. 补充真实 codex app-server review/edit 联调样例（脱敏录制）以降低 fake 偏差风险。
3. 评估并收敛 Mode B 与不同 ACP client 的 fs 协议兼容层。

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

### 2026-02-27 — PR4 Edit review + patch 落盘两模式
- A. 范围与目标:
  - 覆盖 E1、E2，并回归 D2
  - 打通 `/review` workflow、review mode 状态、diff 展示与双落盘模式
- B. 实现:
  - 新增 `review/start` 路由与 review mode entered/exited -> `session/update` 映射
  - 新增 patch apply mode：`appserver` / `acp_fs`（`PATCH_APPLY_MODE`）
  - Mode B 通过 `fs/write_text_file` 执行落盘；冲突/失败输出 `review_apply_failed`
  - 保持 permission gate：未批准或失败不落盘（D2 回归）
- C. 验证:
  - 新增：
    - `TestE2EAcceptanceE1ReviewWorkflow`
    - `TestE2EAcceptanceE2PatchModeAAppServer`
    - `TestE2EAcceptanceE2PatchModeBACPFS`
    - `TestE2EReviewPatchConflictVisibleModeB`
  - `go test ./...` 通过
- D. 文档:
  - 更新 `PROGRESS.md`、`docs/DECISIONS.md`、`docs/KNOWN_ISSUES.md`
