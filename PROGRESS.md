# PROGRESS.md

> 本文件是本项目的“长期记忆入口”。任何时候如果对进度/状态不确定，以本文件为准。  
> 更新频率：每合并一个 PR 必须更新一次；每次发现阻塞也要更新。

## 项目概览
- 项目：codex-acp-go（基于 Codex App Server 的 ACP 适配器）
- 当前阶段：PR5
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
- [x] PR5：Slash commands + Custom prompts + MCP + Auth 收尾
  - 状态：Done
  - 说明：已补齐 G1-G6、H1、I1-I3、J1（脚本化压力回归）与 MCP list/call/oauth 主流程

## 验收进度（从 docs/ACCEPTANCE.md 勾选）
### A. 协议合规（ACP）
- [x] A1 stdio 合规（stdout 纯协议，stderr 仅日志）
- [x] A2 initialize
- [x] A3 session/new
- [x] A4 session/prompt（流式）
- [x] A5 session/cancel

### B. App Server 对接
- [x] B1 App Server 初始化
- [x] B2 Schema 锁定（make schema）

### C. 内容能力
- [x] C1 @-mentions
- [x] C2 Images

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
- [x] F1 结构化 TODO

### G. Slash commands
- [x] G1 /review
- [x] G2 /review-branch
- [x] G3 /review-commit
- [x] G4 /init
- [x] G5 /compact
- [x] G6 /logout

### H. Custom Prompts
- [x] H1 profiles 生效

### I. Auth methods
- [x] I1 CODEX_API_KEY
- [x] I2 OPENAI_API_KEY
- [x] I3 subscription 登录态（如环境支持）

### J. 可靠性
- [x] J1 压力回归（100 turns 含 approve/deny/cancel）
- [x] J2 stdout 纯净（trace 脱敏）

## 本 PR 做了什么
1. 补齐 slash commands：
   - `/review-branch`、`/review-commit`：统一路由 `review/start`
   - `/init`：进入文件改动审批路径
   - `/compact`：路由 `thread/compact/start`
   - `/logout`：清理适配器 auth 状态并要求重新认证
2. 增加 MCP commands：
   - `/mcp list`：列出 MCP servers
   - `/mcp call <server> <tool> [args]`：调用前强制 `session/request_permission`（mcp）
   - `/mcp oauth <server>`：触发 OAuth 登录引导
3. 增加 profiles 配置生效链路：
   - 支持从 `CODEX_ACP_PROFILES_JSON` / `CODEX_ACP_PROFILES_FILE` 读取 profile
   - session/new 与 session/prompt 支持 `profile/model/approvalPolicy/sandbox/personality/systemInstructions` 并映射到 app-server runtime options
4. 增加 auth 方法收尾：
   - 初始化返回 `activeAuthMethod`
   - 启动时识别 `CODEX_API_KEY` / `OPENAI_API_KEY` / subscription
   - 无认证时 `session/new` / `session/prompt` 返回明确错误
5. 补充 J1 压力回归：
   - 新增 `TestE2EAcceptanceJ1Stress100Turns`（`RUN_STRESS_J1=1` 启用）
   - 新增 `scripts/j1_stress.sh` 与 `make stress-j1`

## 影响范围是什么
1. `internal/acp`：slash 路由矩阵、inline MCP command 执行、auth gate、profile 解析与运行参数透传。
2. `internal/appserver`：新增 `thread/compact/start`、`mcpServer/*`、`auth/logout` client/supervisor 方法。
3. `internal/config`/`cmd`：新增 profiles 加载、初始 auth 模式识别、配置到 server options 的映射。
4. `testdata/fake_codex_app_server`：新增 compact/mcp/oauth/logout 伪实现与 runtime options 回显（profile probe）。
5. `test/integration`：新增 G2-G6、H1、I1-I3、MCP 覆盖与 J1 压力测试入口。
6. `scripts`/`Makefile`：新增 `scripts/j1_stress.sh` 与 `make stress-j1`。

## 如何验证
1. 执行：
   - `go test ./...`
   - 真实 app-server e2e：`E2E_REAL_CODEX=1 go test ./... -run TestE2E -count=1`
     - 前置：本机 `codex app-server` 可用；无可用认证会在 real e2e 中给出 skip 原因
2. 预期：
   - `test/integration` 通过，包含：
     - `TestE2EAcceptanceA1ToA5AndB1`
     - `TestE2EAcceptanceB1AppServerCrashReturnsClearError`
     - `TestE2ENotificationRoutingBySessionAndTurn`
     - `TestE2EAcceptanceC1MentionsResourcePreserved`
     - `TestE2EEdgeC1MentionWithoutFSCapabilityDegrades`
     - `TestE2EAcceptanceC2ImageContentBlock`
     - `TestE2EEdgeC2InvalidImageBase64Rejected`
     - `TestE2EAcceptanceD1ToD5ApprovalsBridge`
     - `TestE2EAcceptanceF1StructuredTODOAcrossTurns`
     - `TestE2EAcceptanceE1ReviewWorkflow`
     - `TestE2EAcceptanceE2PatchModeAAppServer`
     - `TestE2EAcceptanceE2PatchModeBACPFS`
     - `TestE2EReviewPatchConflictVisibleModeB`
     - `TestE2EAcceptanceG2G3ReviewBranchAndCommit`
     - `TestE2EAcceptanceG4InitRequiresPermission`
     - `TestE2EAcceptanceG5Compact`
     - `TestE2EAcceptanceG6LogoutRequiresReauth`
     - `TestE2EAcceptanceH1ProfilesAffectRuntime`
     - `TestE2EAcceptanceMCPListCallAndOAuth`
     - `TestE2EAcceptanceI1ToI3AuthMethods`
     - `TestE2EAuthRequiredWithoutConfiguredMethod`
     - `TestE2ERealCodexInitializePromptAndCancel`（`E2E_REAL_CODEX=1`）
     - `TestE2ERealCodexPromptInteractions`（含真实 prompt：`What is this project?`）
     - `TestE2ERealCodexContentBlocksMentionsImagesAndTODO`（`E2E_REAL_CODEX=1`）
     - `TestRPCReaderDetectsInvalidStdoutLine`
   - PR5 相关验收由 e2e 自动覆盖：G/H/I + MCP；J1 由脚本触发专项回归。
   - 测试中持续校验 adapter stdout 每行均为合法 JSON-RPC。
   - 真实 e2e 启用时会先执行 `make schema`（generate + schema-check + hash）再启动测试。
3. J1 压测专项：
   - `make stress-j1` 或 `scripts/j1_stress.sh`
   - 预期：100 turns（含 approve/deny/cancel）完成，无崩溃、stdout 仍纯 JSON-RPC

## 遗留问题是什么
1. 当前崩溃恢复策略对“当次请求”返回可读失败，需要客户端重试一次（已在 KNOWN_ISSUES 记录）。
2. `/logout` 当前仅清理适配器侧认证态，不含交互式重新登录入口（需外部重新配置/重启）。
3. e2e 仍主要依赖 fake app-server，真实 codex app-server 的 mcp/auth/compact 行为仍需联调回归。

## 当前阻塞（Blockers）
- 无

## 下一步（Next）
1. 做真实 codex app-server 联调：重点覆盖 `/compact`、`mcpServer/*`、`auth/logout` 与 profile 参数映射。
2. 在 CI 增加可选 `e2e-real` 作业（含 `make schema`）并固化环境前置检查。
3. 评估 `/logout` 的进程内 re-auth RPC，消除重启恢复依赖。

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

### 2026-02-27 — PR5 Slash commands + Profiles + MCP + Auth 收尾
- A. 范围与目标:
  - 覆盖 G1-G6、H1、I1-I3、J1，并补齐 MCP list/call/oauth 主流程
- B. 实现:
  - slash commands：`/review-branch`、`/review-commit`、`/init`、`/compact`、`/logout`
  - profiles：支持 `CODEX_ACP_PROFILES_JSON` / `CODEX_ACP_PROFILES_FILE`，并映射 `model/approvalPolicy/sandbox/personality/systemInstructions`
  - MCP：支持 `/mcp list`、`/mcp call`（ACP permission gate）、`/mcp oauth`
  - auth：初始化暴露 `activeAuthMethod`；无认证 fail-closed；`/logout` 清空认证态
  - J1：新增 `TestE2EAcceptanceJ1Stress100Turns`（环境变量门控）与 `scripts/j1_stress.sh`
- C. 验证:
  - 新增：
    - `TestE2EAcceptanceG2G3ReviewBranchAndCommit`
    - `TestE2EAcceptanceG4InitRequiresPermission`
    - `TestE2EAcceptanceG5Compact`
    - `TestE2EAcceptanceG6LogoutRequiresReauth`
    - `TestE2EAcceptanceH1ProfilesAffectRuntime`
    - `TestE2EAcceptanceMCPListCallAndOAuth`
    - `TestE2EAcceptanceI1ToI3AuthMethods`
    - `TestE2EAuthRequiredWithoutConfiguredMethod`
    - `TestE2EAcceptanceJ1Stress100Turns`（`RUN_STRESS_J1=1`）
  - `go test ./...` 通过
- D. 文档:
  - 更新 `PROGRESS.md`、`docs/DECISIONS.md`、`docs/KNOWN_ISSUES.md`

### 2026-02-27 — Real Codex e2e + trace-json + schema 前置校验
- A. 范围与目标:
  - 增加真实 `codex app-server` 子进程 e2e（`E2E_REAL_CODEX=1`），覆盖 initialize/new/prompt/cancel 基线
  - 提供 trace-json 脱敏落盘调试能力并保持 stdout 纯 ACP JSON-RPC
- B. 实现:
  - 新增 `TestE2ERealCodexInitializePromptAndCancel`（真实 app-server 路径）
  - e2e 真实模式前置 `make schema`（生成 + 校验 + hash）
  - 新增 `--trace-json` + `--trace-json-file`，记录 ACP/AppServer 双向脱敏 JSONL
  - 强化 rpcReader：stdout 每行必须是严格 JSON-RPC envelope
- C. 验证:
  - `go test ./...` 通过
  - `E2E_REAL_CODEX=1 go test ./... -run TestE2E -count=1`（本机具备 codex/auth 环境时）
- D. 文档:
  - 更新 `PROGRESS.md`、`docs/DECISIONS.md`、`docs/KNOWN_ISSUES.md`

### 2026-02-27 — C1/C2/F1 收尾（mentions + images + structured TODO）
- A. 范围与目标:
  - 覆盖 C1、C2、F1，并补齐 real/edge e2e 断言
  - 保持 A1（stdout 纯协议）与 B1（真实 app-server 子进程）不回退
- B. 实现:
  - `session/prompt` 支持 ACP `content/resources`，映射到 app-server `turn/start input[]`
  - mentions：保留 `uri/mimeType/range`，资源缺内容时按 capability 检测尝试 `fs/read_text_file`，失败/缺能力时降级告警
  - images：支持 base64/data-uri/localImage 输入，增加 mime 白名单与 4MiB 大小限制
  - TODO：从 message delta 解析 markdown checklist，并在 `session/update.todo` 返回结构化项（保留原文 delta）
- C. 验证:
  - 新增：
    - `TestE2EAcceptanceC1MentionsResourcePreserved`
    - `TestE2EEdgeC1MentionWithoutFSCapabilityDegrades`
    - `TestE2EAcceptanceC2ImageContentBlock`
    - `TestE2EEdgeC2InvalidImageBase64Rejected`
    - `TestE2EAcceptanceF1StructuredTODOAcrossTurns`
    - `TestE2ERealCodexContentBlocksMentionsImagesAndTODO`（`E2E_REAL_CODEX=1`）
  - `go test ./...` 通过
- D. 文档:
  - 更新 `PROGRESS.md`、`docs/DECISIONS.md`、`docs/KNOWN_ISSUES.md`
