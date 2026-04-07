# PROGRESS.md

> 本文件是本项目的“长期记忆入口”。任何时候如果对进度/状态不确定，以本文件为准。  
> 更新频率：每合并一个 PR 必须更新一次；每次发现阻塞也要更新。

## 项目概览
- 项目：acp-adapter（ACP 适配器，当前支持 Codex App Server、Claude Code CLI、Pi RPC 模式）
- 当前阶段：Pi Adapter RPC 初版完成，Library Mode 持续收尾（R5 in progress）
- 最近更新：2026-04-07

## 2026-04-07 新增后端（Pi RPC Adapter 初版）
- 完成点：
  - 新增 `internal/pi` 与 `pkg/piacp`，通过官方 `pi --mode rpc` 子进程接入 ACP，不解析交互式 CLI 文本。
  - `cmd/acp` 新增 `--adapter pi` 与配套参数：`--pi-bin`、`--pi-args`、`--pi-provider`、`--pi-model`、`--pi-session-dir`、`--pi-disable-gate`。
  - 支持 `session/new` / `session/prompt` / `session/cancel` / `session/list` / `session/load` / `session/set_config_option(model,thought_level)` / `authenticate` / `/compact` / `/logout`。
  - `session/list` / `session/load` 基于 Pi session jsonl 文件恢复历史；thread id 以 `sessionFile` 路径为准。
  - 新增 adapter-managed Pi permission gate extension：把 `bash` / `write` / `edit` 工具调用上收为 ACP `session/request_permission`。
  - `authenticate` 现在会下发到后端恢复本地 auth 状态；Pi/Claude 在 `/logout` 后可同进程恢复，Pi 缺失 live session 时会按 `sessionFile` 懒恢复后再继续 turn。
  - `internal/acp/server` 现在支持按后端注入 `authMethods` 与 `availableCommands`，不再默认写死 Codex auth 目录。
- 测试与回归：
  - 新增 `testdata/fake_pi_rpc`
  - 新增 `test/integration/pi_e2e_test.go`
    - `TestPiE2EBasicPromptCancelAndAvailableCommands`
    - `TestPiE2ESessionConfigOptionsModelListAndSwitch`
    - `TestPiE2ESessionListLoadAndPrompt`
    - `TestPiE2EPermissionGate`
    - `TestPiE2ELogoutAuthenticateAndPrompt`
  - 新增 `test/integration/pi_real_e2e_test.go`
    - `TestRealPiBasicPromptCancelAndAvailableCommands`
    - `TestRealPiSessionListLoadAndPrompt`
    - `TestRealPiSessionConfigOptionsModelAndThoughtLevel`
    - `TestRealPiPermissionGateCommandAndWrite`
    - `TestRealPiSlashCommandsAndLogout`
  - 全量通过：`go test ./...`
- 下一步：
  - 继续评估 Pi `get_commands` / 非 gate `extension_ui_request` 的 ACP 桥接策略。
  - 继续补齐 Pi 与 Codex 在 review 语义、archived session、MCP 能力上的差距说明与验收项。

## 2026-03-31 增量修复（ACP `session/prompt` 最终 response 携带 usage 快照）
- 修复点：
  - `internal/acp/server.go` 已先把 `usage_update.used` 从 thread 累计 `tokenUsage.total.totalTokens` 校正为最新输入 token 近似值 `tokenUsage.last.inputTokens`，避免把 lifetime usage 误当成窗口占用。
  - `internal/acp/types.go` 扩展 `SessionPromptResult`，在保留 `stopReason` 的同时新增可选 `used/size/cost` 字段。
  - `internal/acp/server.go` 在 `session/prompt` 路径缓存同一 turn 最近一次 `thread/tokenUsage/updated` 映射后的 usage 快照，并在最终 JSON-RPC result 一并返回。
  - 这样上游除了持续接收 `session/update(type="usage_update")`，也能在 prompt 最终 response 中直接拿到最后一次 usage snapshot。
- 测试与回归：
  - 更新 `test/integration/e2e_test.go::TestE2EACPUsageUpdateMappedFromThreadTokenUsageUpdated`
  - 全量通过：`go test ./...`

## 2026-03-30 增量修复（Codex `thread/tokenUsage/updated` -> ACP `usage_update`）
- 修复点：
  - `internal/codex/types.go` / `internal/codex/client.go` 新增 schema 对齐的 `ThreadTokenUsageUpdatedNotification`、`ThreadTokenUsage`、`TokenUsageBreakdown`，并接收下游 `thread/tokenUsage/updated` notification。
  - `internal/acp/server` / `internal/acp/types` 新增 ACP `session/update(type="usage_update")` 桥接：
    - `used` <- `tokenUsage.total.totalTokens`
    - `size` <- `tokenUsage.modelContextWindow`
  - `session/update` 继续遵循当前 adapter 的“双输出”策略：既保留顶层 `used/size`，也填充标准 `params.update.sessionUpdate="usage_update"` envelope，便于 ACP client 直接消费。
- 测试与回归：
  - 新增 `internal/codex/client_notification_test.go::TestHandleNotification_ThreadTokenUsageUpdated`
  - 新增 `internal/acp/server_stdio_test.go::TestBuildSessionUpdatePayloadUsageUpdate`
  - 新增 `test/integration/e2e_test.go::TestE2EACPUsageUpdateMappedFromThreadTokenUsageUpdated`
  - 全量通过：`go test ./...`

## 2026-03-27 增量修复（Codex turn 失败详情桥接）
- 修复点：
  - `internal/codex/client` 新增对下游 `error` notification 的解析，并把 `willRetry` 与结构化 `TurnError` 透传为内部 turn event。
  - `turn/completed` 现在会读取新版 payload 中 `turn.error.message` / `additionalDetails` / `codexErrorInfo`，不再只根据 `status=failed` 丢掉失败原因。
  - `internal/acp/server` 新增两类上游可见状态：
    - `backend_error_retrying` / `backend_error`：用于下游显式 error notification
    - `turn_error`：用于最终 failed turn，并携带真实失败消息
  - 结果是像 `apply_patch verification failed: Failed to find expected lines ...` 这类真实 Codex 失败原因，会作为 ACP `session/update(status="turn_error")` 暴露给上游，而不再只留在子进程 stderr 日志中。
- 测试与回归：
  - 新增 `internal/codex/client_notification_test.go::TestHandleNotification_TurnCompletedIncludesErrorMessage`
  - 新增 `internal/codex/client_notification_test.go::TestHandleNotification_ErrorNotificationRetrying`
  - 新增 `test/integration/e2e_test.go::TestE2ETurnCompletedFailedErrorDetailsSurfaced`
  - 新增 `test/integration/e2e_test.go::TestE2EErrorNotificationRetryingSurfaced`
  - 回归通过：`go test ./...`

## 2026-03-26 增量修复（Codex `session/list` 立即暴露 live session）
- 修复点：
  - `internal/acp/server.go` 的 `session/list` active page 现在会先并入当前进程里已知且 `cwd` 匹配的 live session，再与 app-server `thread/list` 结果做去重合并。
  - 这样 `session/new` 刚返回后，即使 Codex app-server 的 `thread/list` 还没把新 thread 刷进历史列表，ACP client 也能立刻在 `session/list` 看到当前 session。
  - 当稍后的 `thread/list` 返回了同一个 thread 的正式摘要时，adapter 会用其 `title/updatedAt/_meta` 补齐前面的 live placeholder，而不会改变已分配的 session id。
- 测试与回归：
  - 新增 `internal/acp/server_stdio_test.go::TestServerStdioSessionListIncludesLiveSessionBeforeThreadListHistory`，覆盖 `initialize -> session/new -> session/list` 时 thread history 仍为空的场景。
  - 回归通过：`go test ./...`。

## 2026-03-26 增量修复（Codex `fileChange.kind` schema 兼容）
- 修复点：
  - `internal/codex/types.go` 中 `FileUpdateChange.Kind` 改为按 schema 解析 `PatchChangeKind`，兼容当前 app-server 的对象形态 `{"type":"add|delete|update"}`。
  - 同时保留对历史字符串形态（如 `"update"`）的兼容，避免 mixed-version trace 或旧录制样例回归失败。
  - 修复后，包含 `fileChange` item 的 `item/started` / `item/completed` 不再因为反序列化失败被整条忽略。
- 测试与回归：
  - 新增 `internal/codex/client_notification_test.go::TestHandleNotification_FileChangePatchKindCompatibility`，覆盖 started/completed 两条通知以及 object/string 两种 `kind` 形态。
  - 回归通过：`go test ./...`。

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
- [x] Pi RPC backend 初版
  - 状态：Done
  - 说明：已新增 `--adapter pi`、Pi RPC 子进程 client、session/list/load、permission gate 与基础 e2e 覆盖

## 2026-03-13 增量修复（ACP `available_commands_update` 主动发布）
- 修复点：
  - ACP `session/update` 新增标准 `update.sessionUpdate="available_commands_update"` 映射，并同步输出 `availableCommands` 列表。
  - `session/new` / `session/load` 成功后，adapter 会主动向上游发布当前 session 的 slash command 目录。
  - 认证态变化时刷新命令目录：
    - `/logout` 后将当前已知 session 的命令表收敛为最小集（仅保留 `/logout`）
    - `authenticate` 成功后向当前已知 session 重新发布完整命令表
  - 命令目录按后端裁剪：
    - Codex：`/review`、`/review-branch`、`/review-commit`、`/init`、`/compact`、`/logout`、`/mcp`
    - Claude：`/review`、`/review-branch`、`/review-commit`、`/init`、`/compact`、`/logout`
- 测试与回归：
  - 新增 `internal/acp/server_stdio_test.go::TestBuildSessionUpdatePayloadAvailableCommands`，覆盖标准 payload 序列化。
  - 新增 `test/integration/e2e_test.go::TestE2EAvailableCommandsPublishedAndRefreshedAfterLogout`，覆盖 Codex 新 session 发布、`/logout` 缩表、`authenticate` 恢复。
  - 新增 `test/integration/claude_e2e_test.go::TestClaudeE2EAvailableCommandsPublishedOnSessionNew`，覆盖 Claude 新 session 主动发布且不误广告 `/mcp`。
  - 回归通过：`go test ./...`。

## 2026-03-15 增量修复（默认开启 Codex reasoning summary）
- 修复点：
  - Codex adapter 默认子进程启动参数改为 `codex app-server -c 'model_reasoning_summary="detailed"'`。
  - 统一对齐两条入口：
    - `internal/config.Parse()` 的默认 `CODEX_APP_SERVER_ARGS`
    - `pkg/codexacp.DefaultRuntimeConfig()` 的库模式默认值
  - `cmd/acp --help` 的默认参数说明同步更新。
- 测试与回归：
  - 新增 `internal/config/config_test.go::TestDefaultCodexAppServerArgs`
  - 新增 `pkg/codexacp/defaults_test.go::TestDefaultRuntimeConfigEnablesDetailedReasoningSummary`
  - 回归通过：`go test ./...`

## 2026-03-21 增量修复（Codex runtime `commandExecution` -> ACP `tool_call_update`）
- 修复点：
  - `internal/codex/client` 现在会保留 `item/started` / `item/completed` 中 `commandExecution` item 的结构化字段：`command`、`commandActions`、`cwd`、`status`、`exitCode`、`aggregatedOutput`。
  - `internal/acp/server` 新增 runtime command tool-call 桥接：把 `commandExecution` item 的 started/completed/failed 生命周期映射成 ACP `session/update(type="tool_call_update")`。
  - ACP `toolCallId` 直接复用 app-server `commandExecution` item id，`title/message` 使用真实命令字符串；标准 `update.content.text` 现在也会填充：
    - `in_progress`：命令字符串
    - `item/commandExecution/outputDelta`：逐块命令输出文本
    - `completed/failed`：优先 `aggregatedOutput`，否则回退到命令/exit code 摘要
  - runtime command output 透传时不再对 `outputDelta` / `aggregatedOutput` 做 `TrimSpace`，避免尾部换行和纯空白 chunk 在 ACP `tool_call_update.content.text` 中丢失。
  - 当已经发出结构化 command tool call update 时，不再退回普通 `status item_started/item_completed`。
  - 对 approval 驱动的 command tool call 增加去重，避免与后续 runtime `commandExecution` item 重复发送相同状态。
- 测试与回归：
  - 新增 `test/integration/e2e_test.go::TestE2ECommandExecutionItemsMappedToToolCallUpdates`，覆盖 fake app-server 的 commandExecution item -> ACP tool_call_update 映射。
  - 新增 `test/integration/e2e_test.go::TestE2ECommandExecutionOutputDeltaMappedToToolCallContent`，覆盖 fake app-server 的 `item/commandExecution/outputDelta` -> ACP `tool_call_update.content` 流式桥接。
  - 新增 `test/integration/e2e_test.go::TestE2ERealCodexCommandExecutionMappedToToolCalls`，覆盖真实 codex trace 中 `commandExecution` / `aggregatedOutput` 与 ACP `tool_call_update` 的 id 对齐。
  - 全量通过：`go test ./...`

## 2026-03-22 增量修复（Codex/MCP 工具图片输出 -> ACP image block）
- 修复点：
  - `internal/acp/server` 现在会为 `session/update(type="tool_call_update")` 构造标准 ACP `update.content[]`，每项使用 `{"type":"content","content":<ContentBlock>}` 形态，而不再把 tool output 限死为单个纯文本对象。
  - runtime `dynamicToolCall` / `mcpToolCall` item 新增结构化解析：
    - `dynamicToolCall.contentItems[].type=inputText` -> ACP text block
    - `dynamicToolCall.contentItems[].type=inputImage,imageUrl=data:...` -> ACP image block
    - `mcpToolCall.result.content[]` 中的 MCP `text` / `image` content item -> 直接桥接为 ACP text / image block
  - `/mcp call` 直连路径也同步升级：
    - text 结果继续作为普通 message chunk 可见
    - image 结果会进入 terminal `tool_call_update.content[]`，供 ACP client 直接渲染图片
  - 对只返回普通 URL 的图片结果，当前会保守降级为文本提示 `image available at ...`，避免伪造不完整的 ACP image block。
- 测试与回归：
  - 新增 `internal/acp/server_stdio_test.go::TestBuildSessionUpdatePayloadToolCallContent`，覆盖标准 `tool_call_update.content[]` payload 序列化。
  - 新增 `test/integration/e2e_test.go::TestE2EToolImageItemsMappedToACPImageBlock`，覆盖 fake app-server `dynamicToolCall` item -> ACP image block。
  - 扩展 `test/integration/e2e_test.go::TestE2EAcceptanceG1ToG6SlashCommandsAndMCP`，覆盖 `/mcp call ... render-image` -> completed `tool_call_update` image content。
  - 全量通过：`go test ./...`

## 2026-03-22 增量修复（Codex `turn/diff/updated` -> ACP tool-call diffs）
- 修复点：
  - `internal/codex/client` 新增 `turn/diff/updated` 事件接收与内部 `TurnEventTypeDiffUpdated` 分发，保留 app-server 的聚合 unified diff 字符串。
  - `internal/acp/server` 新增 turn diff 桥接：
    - 为每个 turn 维护稳定的 `toolCallId`（`turn-diff-<turnId>`）
    - 收到 `turn/diff/updated` 时，优先把 unified diff 解析成 ACP `tool_call_update.content[type="diff"]`
    - `path` 基于 ACP session `cwd` 解析为绝对路径，并通过客户端 `fs/read_text_file` 读取旧文本后回放 patch，生成 `oldText/newText`
    - turn 正常结束时补发 `completed`，异常结束时补发 `failed`，保证 tool-call 生命周期闭环
  - 当 diff 无法结构化重建时，adapter 保守降级为 fenced `diff` 文本块，而不是伪造不完整的 ACP diff item。
  - 修复 `fs/read_text_file` 读取结果时错误 `TrimSpace` 的问题，避免尾部换行丢失导致 patch 回放失败。
- 测试与回归：
  - 扩展 `internal/acp/server_stdio_test.go::TestBuildSessionUpdatePayloadToolCallContent`，覆盖 ACP `tool_call_update.content[]` 中 `diff` item 的序列化。
  - 新增 `test/integration/e2e_test.go::TestE2ETurnDiffUpdatedMappedToToolCallDiffs`，覆盖 fake app-server `turn/diff/updated` -> ACP `tool_call_update.content[type=diff]`，并验证 `fs/read_text_file` 请求路径、`oldText/newText` 与稳定 `toolCallId`。
  - 全量通过：`go test ./...`

## Library Embedding Program（R0-R6）
- Current：R5 server 集成（In Progress）
- Next：R6 收尾验收
- [x] R0 文档立项
  - 状态：Done
  - 说明：完成库化目标建档（里程碑、ADR、风险、初版验收项），未改动运行时行为。
- [x] R1 外观库化（零行为变化）
  - 状态：Done
  - 说明：新增 `pkg/codexacp` 外观 API（`RunStdio`）并将 `cmd` 启动委托到库入口，协议行为保持不变。
- [x] R2 传输层抽象
  - 状态：Done
  - 说明：引入 ACP 传输接口并新增 inproc channel transport，Server 改为依赖接口，stdio 行为保持兼容。
- [x] R3 嵌入 API
  - 状态：Done
  - 说明：新增 `EmbeddedRuntime`（Start/ClientRequest/SubscribeUpdates/RespondPermission/Close），复用同一套 ACP server 逻辑并跑通核心流程。
- [x] R4 契约对照测试
  - 状态：Done
  - 说明：同一输入脚本双跑 standalone(stdio)/embedded(inproc)，对照 initialize、streaming prompt、cancel、permission approve/decline 的关键行为与终态。
- [ ] R5 server 集成
  - 状态：In Progress
  - 说明：已在 `go-acp-server` 完成本地 `go mod replace` 联调并跑通真实 prompt/SSE/cancel/permission 回路；库侧仍需继续收敛真实 app-server 版本兼容差异（见 KI-0015/KI-0016）。
- [ ] R6 收尾验收
  - 状态：Todo
  - 说明：完成 Library Mode 验收闭环与文档收敛。

## 2026-03-06 增量修复（Codex app-server server-request 兼容）
- 修复点：
  - `internal/codex/client` 新增对新版 app-server server request 的兼容：
    - `item/commandExecution/requestApproval`
    - `item/fileChange/requestApproval`
  - 兼容回写审批结果格式：
    - 新版请求回 `{"decision":"accept|decline|cancel"}`
    - 旧版 `approval/request` 继续回 `{"outcome":"approved|declined|cancelled"}`
  - 对当前未实现的 server request（`item/tool/requestUserInput`、`item/tool/call`、`account/chatgptAuthTokens/refresh`、legacy `execCommandApproval`/`applyPatchApproval`）改为显式 fail-closed `-32000`，避免 `-32601 method not found` 误导性错误。
- 测试与回归：
  - 新增 `internal/codex/client_server_request_test.go`（请求映射与回写格式单测）。
  - 新增 `pkg/codexacp/runtime_test.go::TestRunStdio_CommandApprovalRequestCompatibility`（fake app-server 发新版 command approval，验证 ACP permission 闭环）。
  - 更新 fake app-server：command approval 默认走新版 `item/commandExecution/requestApproval`，其余 approval 路径保持现有行为以保证 review/patch 回归稳定。
  - 全量通过：`go test ./...`。

## 2026-03-09 增量修复（ACP agent-plan 映射）
- 修复点：
  - 新增 Codex app-server `turn/plan/updated` -> ACP `session/update` 标准 `update.sessionUpdate="plan"` 映射。
  - `session/update` 新增 plan entries 输出；每次计划更新都按 ACP 语义发送完整 entries 列表，供客户端整体替换当前 plan。
  - app-server `inProgress` 状态统一映射为 ACP `in_progress`；由于下游计划项无优先级字段，当前固定回填 `priority=medium`。
  - 新增 `item/plan/delta` fallback 桥接：当下游未发送 `turn/plan/updated` 时，适配器使用 plan item delta 流与 `item/completed(type=plan)` 文本生成草稿/完成态 ACP `plan` update。
- 测试与回归：
  - 新增 `TestE2EACPPlanUpdateMappedFromTurnPlanUpdated`，覆盖 fake app-server 发两次 `turn/plan/updated` 时 ACP plan 全量替换语义。
  - 新增 `TestE2EACPPlanUpdateMappedFromPlanDeltaFallback`，覆盖仅有 `item/plan/delta + item/completed(plan)` 时的 fallback plan streaming。
  - 新增 `TestBuildSessionUpdatePayloadPlan`，覆盖 `session/update` 标准 envelope 序列化。
  - fake app-server 新增 structured plan 场景，便于回归 `turn/plan/updated` 桥接。

## 2026-03-11 增量修复（ACP `session/list` -> Codex `thread/list`）
- 修复点：
  - ACP server 新增 `session/list` handler，并在 Codex adapter 初始化能力里声明 `agentCapabilities.sessionCapabilities.list`。
  - Codex app-server client/supervisor 新增 `thread/list` 调用，桥接历史线程到 ACP session 摘要。
  - `session/list` 结果映射字段：
    - `sessionId`：复用当前进程内已知 session 映射；历史 thread 首次发现时分配稳定的 adapter session id。
    - `cwd` / `title` / `updatedAt`：分别来自 thread cwd、`name|preview`、UTC RFC3339 时间。
    - `_meta`：补充 `threadId`、`archived`、`createdAt`、`modelProvider`、`preview`、`source`、`status`。
  - 为满足“历史会话”语义，adapter 在内部串接 app-server 的 active / archived 两段 `thread/list` 分页，并对 ACP 暴露单一 opaque cursor。
- 测试与回归：
  - fake app-server 新增 `thread/list`、cwd 过滤、active/archived 分页，以及新建 thread 自动进入 history 列表。
  - 新增 `TestE2ESessionListMappedFromThreadList`，覆盖 capability 广告、当前 sessionId 复用、RFC3339 时间、cwd 过滤、active→archived 分页。
  - 全量通过：`go test ./...`。

## 2026-03-11 增量修复（ACP `session/load` -> Codex `thread/resume`）
- 修复点：
  - ACP server 新增 `session/load` handler，并在 Codex adapter 初始化能力里声明 `agentCapabilities.loadSession=true`。
  - Codex app-server client/supervisor 新增 `thread/resume` 调用，使用 persisted thread history 恢复会话到内存。
  - `session/load` 成功后会先把历史 turn 中的 `userMessage` / `agentMessage` 通过 `session/update` 回放给上游，再返回 `configOptions`。
  - 历史消息映射：
    - `userMessage` -> `update.sessionUpdate="user_message_chunk"`
    - `agentMessage` -> `update.sessionUpdate="agent_message_chunk"`
  - load 后会用 `thread/resume` 返回的 `model` / `reasoningEffort` 刷新 session config，使后续 `session/prompt` 沿用恢复出的运行参数。
- 测试与回归：
  - fake app-server 新增 `thread/resume` 与 seeded thread history。
  - 新增 `TestE2ESessionLoadReplaysHistoryAndAllowsPrompt`，覆盖 `session/list -> session/load -> session/prompt` 链路、历史回放、TODO 回放与 resumed config 复用。
  - 全量通过：`go test ./...`。

## 2026-03-11 增量修复（Claude CLI `session/list` 占位 + `session/load` 部分恢复）
- 修复点：
  - Claude adapter 新增 ACP `session/list` / `session/load` 方法入口，并在初始化能力中声明 `agentCapabilities.sessionCapabilities.list` 与 `agentCapabilities.loadSession=true`。
  - `session/list` 当前返回空页占位；原因是 Claude CLI 只有 `--resume` / `--continue` 恢复入口，没有稳定的 machine-readable 历史会话枚举接口。
  - `session/load` 当前支持“已知 Claude native session ID”的部分恢复：adapter 直接把该 ID 绑定为 ACP `sessionId`，后续 `session/prompt` 使用 `claude --resume <session-id>` 续聊。
  - ACP server 新增 external session loader 旁路，允许非 Codex 后端在 adapter 尚未见过该 session 时，通过外部 session ID 先绑定 thread。
  - `bridge.Store` 新增显式 `Bind(sessionID, threadID)`，用于把 caller-supplied session id 绑定到运行时 thread。
- 测试与回归：
  - 新增 `TestClaudeE2ESessionListEmptyAndLoadAllowsPrompt`，覆盖 capability 广告、空 `session/list`、以及 `session/load -> session/prompt` 可继续。
  - Claude 相关回归通过：`go test ./test/integration -run 'TestClaudeE2E(BasicPromptAndCancel|SessionConfigOptionsModelListAndSwitch|SessionListEmptyAndLoadAllowsPrompt|InitializeContainsStandardFields|ApprovalAutoApproved|NoAuthRequiredWithCLI|ContractStandaloneVsEmbedded|UnifiedCmdAdapterFlag)$' -count=1`。
  - 全量通过：`go test ./...`。

## Claude Adapter Program（C-R0 ~ C-R5）
- [x] C-R0 文档立项
  - 状态：Done
  - 说明：建立 Claude Mode 验收条目（L1-L9），新增 ADR-0033。
- [x] C-R1 internal/claude/ 核心客户端（claude -p CLI 子进程）
  - 状态：Done
  - 说明：config.go/client.go/stream.go 实现 appClient 接口，驱动 `claude -p` 子进程；会话历史由 CLI 持久化（--session-id/--resume）；取消通过 kill 子进程实现；启动时过滤 CLAUDECODE 环境变量。
- [x] C-R2 pkg/claudeacp/ 库入口
  - 状态：Done
  - 说明：runtime.go/runtime_runner.go 提供 RunStdio/NewEmbeddedRuntime 公共 API；配置字段：ClaudeBin/DefaultModel/MaxTurns/SkipPerms/AllowedTools。
- [x] C-R3 统一 cmd/acp 入口
  - 状态：Done
  - 说明：`cmd/acp --adapter codex|claude`；Claude 侧 flag：--claude-bin/--max-turns/--skip-perms；Codex/Claude 统一由 `cmd/acp` 启动。
- [x] C-R4 测试基础设施
  - 状态：Done
  - 说明：testdata/fake_claude_cli（fake `claude` 二进制，支持 stream-json 输出）；claude_e2e_test.go 使用 CLAUDE_BIN + buildFakeClaudeCLI；go test ./... 全通过。
- [x] C-R5 验收运行 + 文档收尾
  - 状态：Done
  - 说明：go test ./... 全通过；L9 Codex 零回退；go.mod 零外部依赖；文档已更新。

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

### K. Library Mode（初版）
- [x] K1 双入口可启动（cmd + pkg）
- [x] K2 R1 零行为变化
- [x] K3 传输层抽象可替换（R2）
- [x] K4 嵌入 API 生命周期（R3）
- [x] K5 独立模式与库模式契约对照（R4）
- [ ] K6 server 集成（R5）
- [ ] K7 收尾验收（R6）

### L. Claude Mode（Anthropic API 适配器）
- [x] L1 协议合规（initialize/session/new/session/prompt/session/cancel）
- [x] L2 Anthropic API 后端对接（无需 codex app-server 子进程）
- [x] L3 内容能力（@mentions + images base64）
- [x] L4 工具审批（tool_use → permission → approve/decline/cancel）
- [x] L5 Slash commands（/review/compact/logout 等）
- [x] L6 Auth 方法（claude CLI 自身认证；无 token 配置；/logout 清空）
- [x] L7 可靠性（stdout 纯净；cancel 生效；CLAUDECODE 环境变量过滤）
- [x] L8 库模式（RunStdio + EmbeddedRuntime；契约对照通过）
- [x] L9 Codex 零回退（go test ./... 全通过）

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
6. 改进崩溃恢复（遗留问题 #1）：
   - `session/prompt` 在 turn 流中检测到 app-server 崩溃时，默认自动重启后“同次请求内部重试一次”
   - 增加开关 `RETRY_TURN_ON_CRASH` / `--retry-turn-on-crash`（默认开启）
   - 重试失败时返回明确 `turn_error` 并附“可重试一次 prompt”提示
7. 增强 `/logout`（遗留问题 #2）：
   - `/logout` 输出按认证方式区分的“可复制粘贴下一步指令”（API key / subscription）
   - 无认证错误增加 `nextStepCommand` 与更清晰的恢复 hint
   - app-server 侧 auth 清理支持 `account/logout`（优先）并兼容回退 `auth/logout`
8. 补齐 README（面向用户）：
   - 重写根目录 `README.md` 为精简使用版：仅保留 What/Features/Quickstart/Usage tips
   - 明确 ACP stdio 约束（newline JSON-RPC、stdout 仅协议、stderr 日志）与下游 Codex App Server 链路
   - 保留最小 Zed external agent 配置模板与实际生效的环境变量名
9. 补充 ACP 认证元数据与认证选择入口：
   - `initialize.authMethods` 增加 `id/name/description` 字段，并保留 `type/label` 兼容已有客户端
   - 新增 `authenticate` RPC（`methodId`）用于 client 选择认证方式并刷新 adapter 认证态
   - 崩溃重试窗口增强：`file already closed` 等错误纳入 supervisor 重启后二次重试判定，降低中途崩溃误失败
10. 修复 ACP `session/prompt` 参数形态与 `session/update` 渲染协议兼容：
   - `session/prompt` 新增对 `prompt` 为 `string | ContentBlock | ContentBlock[]` 的统一解码
   - `session/update` 保留现有扁平字段，同时补充标准 `update.sessionUpdate` 结构（如 `agent_message_chunk`）
   - 适配下游新版 app-server 响应结构：`thread.id` / `turn.id` / `turn.status`
11. 修复 ACP `initialize` 标准字段缺失（Zed 兼容）：
   - 增加 `protocolVersion=1`
   - 增加标准能力树 `agentCapabilities.promptCapabilities/mcpCapabilities/sessionCapabilities`
   - 增加 `agentInfo(name/version/title)`，并保留 legacy capabilities 字段兼容旧客户端
12. 新增 Session Config Options（模型列表展示 + 模型切换）：
   - `session/new` 返回 `configOptions`（当前实现：`model`，`type=select`，`currentValue` + `options`）
   - 新增 `session/set_config_option`（当前支持 `configId=model`，严格校验 value 必须来自 options）
   - 新增 `session/update` `config_options_update` 映射（扁平字段 + `update.sessionUpdate` 标准 envelope）
   - codex 后端接入 `model/list`；claude 后端新增 `ModelsList`（来源：`CLAUDE_MODELS` + `--model` + profile models）
13. 新增 reasoning 展示与切换（ACP `thought_level`）：
   - `session/new` 返回 `thought_level` 配置项，并随 `model` 变更动态刷新候选值与默认值
   - `session/set_config_option` 新增 `configId=thought_level` 校验与持久化
   - codex 后端：`model/list` 解析 `defaultReasoningEffort/supportedReasoningEfforts`，`turn/start` 发送 `effort`
   - claude 后端：`ModelsList` 暴露 effort 候选并在 turn 命令行传递 `--effort`

## 影响范围是什么
1. `internal/acp`：slash 路由矩阵、inline MCP command 执行、auth gate、profile 解析与运行参数透传。
2. `internal/codex`：新增 `thread/compact/start`、`mcpServer/*`、`auth/logout` client/supervisor 方法。
3. `internal/config`/`cmd`：新增 profiles 加载、初始 auth 模式识别、配置到 server options 的映射。
4. `testdata/fake_codex_app_server`：新增 compact/mcp/oauth/logout 伪实现与 runtime options 回显（profile probe）。
5. `test/integration`：新增 G2-G6、H1、I1-I3、MCP 覆盖与 J1 压力测试入口。
6. `scripts`/`Makefile`：新增 `scripts/j1_stress.sh` 与 `make stress-j1`。
7. `internal/bridge`：新增 active turn 替换能力，支持内部重试后把 cancel 目标切换到新 turnId。
8. `internal/codex`：logout 方法改为 `account/logout -> auth/logout` 兼容回退。
9. 文档：新增并精简根目录 `README.md`（面向终端用户使用说明）。
10. `internal/acp`：认证元数据输出与 `authenticate` 请求处理；增强重试窗口错误判定。
11. `test/integration`：新增 `TestE2EAuthMethodsAndAuthenticateFlow`，覆盖 authMethods 字段与 `authenticate` 基本链路。
12. `internal/acp`：`session/prompt` 支持 `prompt` 数组/对象输入；`session/update` 增加标准 `update.sessionUpdate` 映射。
13. `internal/codex`：兼容新版 `thread/start`、`turn/start` 与 `turn/completed` 返回结构。
14. `internal/acp`：`initialize` 输出补齐 ACP 标准字段（`protocolVersion`、标准 capability 结构、`agentInfo`）。
15. `test/integration`：新增 `TestE2EInitializeIncludesACPStandardFields`，防止 `initialize` 协议字段回退。
16. `internal/acp` / `internal/codex` / `internal/claude`：新增模型配置选项链路（model/list → configOptions → session/set_config_option）。
17. `test/integration`：新增 codex/claude 的模型列表与模型切换 e2e 覆盖。
18. `testdata/fake_codex_app_server` / `testdata/fake_claude_cli`：新增模型列表与模型探针回显，支持回归测试。
19. `internal/acp` / `internal/codex` / `internal/claude` / `cmd/acp`：新增 thought_level 配置链路（reasoning 列表展示 + effort 切换落地）。
20. `internal/codex` / `internal/acp`：新增 `turn/plan/updated` -> ACP `plan` session/update 标准映射。
21. `testdata/fake_codex_app_server` / `test/integration`：新增 structured plan 测试场景与端到端回归。
22. `internal/codex` / `internal/acp`：新增 `item/plan/delta` + `item/completed(plan)` fallback plan 桥接。

## 如何验证
1. 执行：
   - `go test ./...`
   - 真实 app-server e2e：`E2E_REAL_CODEX=1 go test ./... -run TestE2EReal -count=1`
     - 前置：本机 `codex app-server` 可用；无可用认证会在 real e2e 中给出 skip 原因
2. 预期：
   - `test/integration` 通过，包含：
     - `TestE2EAcceptanceA1ToA5AndB1`
     - `TestE2EAcceptanceB1AppServerCrashReturnsClearError`
     - `TestE2EAcceptanceB1AppServerCrashDuringTurnAutoRetry`
     - `TestE2EAcceptanceB1AppServerCrashDuringTurnRetryFailureHasHint`
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
     - `TestE2EAcceptanceG6LogoutGuidanceWithAPIKeysAndRecoveryAfterRestart`
     - `TestE2EAcceptanceH1ProfilesAffectRuntime`
     - `TestE2ESessionConfigOptionsModelListAndSwitch`
     - `TestE2EAcceptanceMCPListCallAndOAuth`
     - `TestE2EAcceptanceI1ToI3AuthMethods`
     - `TestE2EAuthRequiredWithoutConfiguredMethod`
     - `TestE2EPromptArrayContentBlocksAccepted`
     - `TestE2EMessageUpdateIncludesACPUpdateEnvelope`
     - `TestE2EInitializeIncludesACPStandardFields`
     - `TestE2ERealCodexAppServer_BasicPromptAndCancel`（`E2E_REAL_CODEX=1`）
     - `TestE2ERealCodexAppServer_AuthMissingReturnsClearError`（`E2E_REAL_CODEX=1`）
     - `TestE2ERealCodexAppServer_AuthInjectedKeyRecovers`（需注入 key，`E2E_REAL_CODEX=1`）
     - `TestE2ERealCodexAppServer_MCPListAndOptionalCall`（`E2E_REAL_CODEX=1`）
     - `TestE2ERealCodexAppServer_CompactProducesVisibleUpdates`（`E2E_REAL_CODEX=1`）
     - `TestE2ERealCodexPromptInteractions`（含真实 prompt：`What is this project?`）
     - `TestE2ERealCodexContentBlocksMentionsImagesAndTODO`（`E2E_REAL_CODEX=1`）
     - `TestRPCReaderDetectsInvalidStdoutLine`
     - `TestClaudeE2ESessionConfigOptionsModelListAndSwitch`
   - `Session Config Options` 用例新增覆盖 `thought_level`（展示 + 切换 + prompt 生效）。
   - PR5 相关验收由 e2e 自动覆盖：G/H/I + MCP；J1 由脚本触发专项回归。
   - 测试中持续校验 adapter stdout 每行均为合法 JSON-RPC。
   - 真实 e2e 启用时会先执行 `make schema`（generate + schema-check + hash）再启动测试。
3. J1 压测专项：
   - `make stress-j1` 或 `scripts/j1_stress.sh`
   - 预期：100 turns（含 approve/deny/cancel）完成，无崩溃、stdout 仍纯 JSON-RPC

## 遗留问题是什么
1. 当前“当次请求自动重试”仅在未发出不可重放内容时启用（幂等边界）；若已进入不可安全重放阶段，仍会 fail-closed 并提示用户重试一次 prompt。
2. `/logout` 已提供明确可复制恢复指引并清理 app-server/client 认证态；但仍缺少“同进程无重启 re-auth RPC”。
3. 已补充真实 codex app-server 的 mcp/auth/compact 基本存在性回归；但复杂行为（多版本兼容、MCP 工具结果语义、compact 实际压缩质量）仍需持续联调。
4. `session/update` 现已对所有更新携带标准 `update.sessionUpdate`（非 message/tool 更新回退为 `agent_thought_chunk` 文本），但低频事件语义仍较粗粒度，跨客户端 UI 展示可能存在差异。

## 当前阻塞（Blockers）
- 无

## Done / In Progress / Next（Library Embedding Program）
### Done
1. R0 文档立项：补充里程碑、ADR、风险与 Library Mode 初版验收项。
2. R1 外观库化：新增 `pkg/codexacp`，`cmd` 入口委托库启动，新增最小参数映射测试。
3. R2 传输层抽象：新增 ACP 传输接口与 inproc transport，Server 改为基于接口，补充传输/stdio 基线测试。
4. R3 嵌入 API：新增进程内调用 API 与 permission 回写能力，并补充嵌入模式 integration tests。
5. R4 契约对照测试：新增同脚本双驱动对照框架，覆盖 initialize/new/prompt/cancel/permission（approve+decline）并补充嵌入模式并发不变量测试。

### In Progress
- 无

### Next
1. R5 server 集成。

## 变更摘要（每 PR 一条）
### 2026-03-04 — 移除 `cmd/acp-adapter`，统一 `cmd/acp` 单入口
- Done:
  - 删除 `cmd/acp-adapter/main.go`。
  - `test/integration/e2e_test.go` 改为构建 `./cmd/acp`（Codex 默认后端），并更新真实 e2e 提示文案为 `cmd/acp`。
  - `npm/scripts/build-binaries.mjs` 改为构建 `./cmd/acp`，保持产物文件名不变。
  - 更新 README / ACCEPTANCE / CLAUDE 文档中的启动与配置示例到 `cmd/acp --adapter codex|claude`。

### 2026-03-05 — 新增 Session Config Options（模型 + reasoning/thought_level）
- Done:
  - `session/new` 返回 `configOptions`（`model` + `thought_level`）。
  - 新增 `session/set_config_option`（`configId=model|thought_level`）并输出 `config_options_update`。
  - codex 接入 `model/list` 的 reasoning 元数据并把 `thought_level` 映射到 `turn/start.effort`。
  - claude 接入可配置模型列表与 effort 列表（`CLAUDE_MODELS`/`--models` + `CLAUDE_EFFORTS`/`--efforts`），并传递 `--effort`。
  - 新增 e2e：`TestE2ESessionConfigOptionsModelListAndSwitch`、`TestClaudeE2ESessionConfigOptionsModelListAndSwitch`。
  - `go test ./...` 全通过。
  - 更新 `docs/KNOWN_ISSUES.md`：记录入口迁移（KI-0034）并修正旧构建/安装命令。
- Tests:
  - `go test ./...` 通过
- Notes/Follow-ups:
  - 外部仍依赖 `cmd/acp-adapter` 的脚本需迁移到 `cmd/acp`。

### 2026-03-03 — 项目统一重命名：acp-adapter
- Done:
  - Go module 路径统一为 `github.com/beyond5959/acp-adapter`。
  - 包路径从 `pkg/acpadapter` 重命名为 `pkg/codexacp`，并同步所有导入。
  - 入口命令从 `cmd/codex-acp-go` 重命名为 `cmd/acp-adapter`。
  - npm workspace 与发布包统一改名为 `acp-adapter` 系列（含平台子包与构建脚本）。
  - 主文档与工程文档中的项目名/路径同步为 `acp-adapter`。
- Tests:
  - `go test ./...` 通过
- Notes/Follow-ups:
  - 外部依赖旧路径（module/import/cmd/npm）的脚本需同步迁移到新命名。

### 2026-03-03 — Claude 适配器后端：claude -p CLI 子进程
- Done:
  - `internal/claude/` 重写为 `claude -p` 子进程驱动；config.go/client.go/stream.go 实现 appClient 接口。
  - 会话连续性：首次 turn `--session-id <uuid>`，后续 turn `--resume <uuid>`；历史由 CLI 持久化到磁盘。
  - 取消：`TurnInterrupt` 调用 `cmd.Process.Kill()`。
  - 启动子进程时过滤 `CLAUDECODE` 环境变量，防止嵌套 session 保护报错（KI-0031）。
  - `ApprovalRespond` 为 no-op；工具以 `--dangerously-skip-permissions` 自动执行（默认，可关闭，见 KI-0032）。
  - `pkg/claudeacp/runtime.go`：配置字段 ClaudeBin/DefaultModel/MaxTurns/SkipPerms/AllowedTools。
  - `cmd/acp/main.go`：Claude 侧 flag --claude-bin/--max-turns/--skip-perms。
  - `testdata/fake_claude_cli/main.go`：fake `claude` 二进制，输出 stream-json 格式。
  - `test/integration/claude_e2e_test.go`：改用 `buildFakeClaudeCLI` + `CLAUDE_BIN`；auth 测试改为无需 token 验证。
  - `go.mod`：零外部依赖（纯标准库）。
  - `internal/claudecli/` 目录重命名为 `internal/claude/`（包名 `claude`）。
- Tests:
  - `go test ./...` 通过（Claude 6 个 e2e 测试全通过；Codex 零回退）
- Notes/Follow-ups:
  - KI-0031（CLAUDECODE 过滤）已在 `client.go:buildCmd` 中修复。
  - KI-0032（skip-perms 默认开启）需用户知晓；后续可评估 approval 事件桥接。
  - 真实 claude CLI 冒烟测试因嵌套 session 限制无法在 Claude Code 内直接执行；需在独立终端验证。
### 2026-02-28 — R4 契约对照测试（standalone vs embedded）
- Done:
  - 新增对照测试 `test/integration/r4_contract_test.go`，同一输入脚本分别驱动：
    - standalone：`cmd/acp-adapter` + stdio JSON-RPC
    - embedded：`pkg/codexacp.EmbeddedRuntime` + inproc transport
  - 对照范围覆盖：
    - initialize 字段完整性（`protocolVersion` + capabilities）
    - `session/new` + `session/prompt`（流式 chunk）
    - `session/cancel`（`stopReason=cancelled`）
    - permission approve/decline 双路径
  - 明确并验证不变量：
    - standalone：stdout 持续满足纯 JSON-RPC 约束
    - embedded：并发双 session 无阻塞/死锁、无跨 session 串扰（turn 不跨 session）
- Tests:
  - `go test ./test/integration -run 'TestR4ContractStandaloneEqualsEmbedded|TestR4EmbeddedInvariants_NoDeadlock_NoCrossSessionCrosstalk' -count=1` 通过
  - `go test ./...` 通过
- Notes/Follow-ups:
  - R4 完成，下一阶段进入 R5（server 集成）

### 2026-02-28 — R3 嵌入 API（进程内调用）
- Done:
  - 在 `pkg/codexacp` 新增嵌入模式 API：
    - `NewEmbeddedRuntime(...)`
    - `Start(ctx)`
    - `ClientRequest(ctx, msg)`
    - `SubscribeUpdates(...)`
    - `RespondPermission(...)`
    - `Close()`
  - 嵌入模式复用同一套 `internal/acp` server 逻辑与 R2 传输抽象（inproc transport），未复制业务分支。
  - 跑通嵌入模式关键链路：`initialize`、`session/new`、`session/prompt` 流式 `session/update`、`session/cancel`、permission 往返。
  - 新增 integration tests：
    - `TestEmbeddedInitializeNewPromptCancel`
    - `TestEmbeddedPermissionRoundTrip`
- Tests:
  - `go test ./test/integration -run 'TestEmbeddedInitializeNewPromptCancel|TestEmbeddedPermissionRoundTrip' -count=1` 通过
  - `go test ./...` 通过
- Notes/Follow-ups:
  - R3 完成，下一阶段进入 R4（契约对照测试）

### 2026-02-28 — R2 传输层抽象（stdio 兼容保持不变）
- Done:
  - 在 `internal/acp` 引入最小传输接口：`ReadMessage/WriteMessage/WriteResult/WriteError/WriteNotification`。
  - 保留 `StdioCodec` 作为 stdio 传输实现（协议行为不变）。
  - 新增 inproc transport（内存通道双端，支持 request/response/notification 双向、并发写、关闭语义）。
  - `Server` 从依赖具体 `StdioCodec` 改为依赖传输接口，默认路径仍走 stdio。
  - 新增测试：
    - `internal/acp/transport_inproc_test.go`（基本收发、并发写、关闭语义）
    - `internal/acp/server_stdio_test.go`（initialize/new/prompt stdio 基线）
- Tests:
  - `go test ./internal/acp -count=1` 通过
  - `go test ./...` 通过
- Notes/Follow-ups:
  - R2 完成，下一阶段进入 R3（嵌入 API）

### 2026-02-28 — R1 外观库化（零行为变化）
- Done:
  - 新增 `pkg/codexacp`，导出运行时配置与 `RunStdio(ctx, cfg, stdin, stdout, stderr)`。
  - `cmd/acp-adapter/main.go` 仅保留参数解析与信号处理，核心启动逻辑委托 `pkg/codexacp`。
  - 保持协议约束：stdout 仅 ACP JSON-RPC；stderr 仅日志。
  - 新增最小单测：`TestRunStdio_ProfileMappingWithFakeAppServer`，验证库入口参数映射（profile/run options）路径。
- Tests:
  - `go test ./pkg/codexacp -run TestRunStdio_ProfileMappingWithFakeAppServer -count=1` 通过
  - `go test ./...` 通过（含 `test/integration`）
- Notes/Follow-ups:
  - R1 完成，下一阶段进入 R2（传输层抽象）

### 2026-02-28 — R0 文档立项（Library Embedding Program）
- Done:
  - 新增 `Library Embedding Program（R0-R6）` 里程碑，并设置 `Current=R1`、`Next=R2`
  - 在 `docs/DECISIONS.md` 增加 ADR：双入口单内核（独立模式 + 库模式）
  - 在 `docs/KNOWN_ISSUES.md` 增加库化风险：行为回归、嵌入并发/阻塞、permission 回写超时
  - 在 `docs/ACCEPTANCE.md` 增加 `Library Mode` 初版验收条目
- Tests:
  - `go test ./...` 通过（含 `test/integration`，本次约 42s）
- Notes/Follow-ups:
  - R0 仅文档与计划，不包含行为改造

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

### 2026-02-27 — Real Codex e2e + trace-json + schema 前置校验
- A. 范围与目标:
  - 增加真实 `codex app-server` 子进程 e2e（`E2E_REAL_CODEX=1`），覆盖 initialize/new/prompt/cancel 基线
  - 提供 trace-json 脱敏落盘调试能力并保持 stdout 纯 ACP JSON-RPC
- B. 实现:
  - 新增真实 app-server 基线路径用例（后续演进为 `TestE2ERealCodexAppServer_BasicPromptAndCancel`）
  - e2e 真实模式前置 `make schema`（生成 + 校验 + hash）
  - 新增 `--trace-json` + `--trace-json-file`，记录 ACP/AppServer 双向脱敏 JSONL
  - 强化 rpcReader：stdout 每行必须是严格 JSON-RPC envelope
- C. 验证:
  - `go test ./...` 通过
  - `E2E_REAL_CODEX=1 go test ./... -run TestE2EReal -count=1`（本机具备 codex/auth 环境时）

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

### 2026-02-27 — 崩溃恢复增强：当次 turn 自动重试一次（默认开启）
- A. 范围与目标:
  - 处理遗留问题 #1：减少 app-server 中途崩溃时“需要客户端手动重试”的场景
  - 在不重复输出已发文本的前提下，对当前 `session/prompt` 做一次内部重试
- B. 实现:
  - `session/prompt` turn 流在检测到可恢复崩溃错误后，发出 `backend_restarted_retrying` 并重启后重试一次
  - 新增 `RETRY_TURN_ON_CRASH` / `--retry-turn-on-crash`（默认 `true`）
  - 新增 session active turn 替换逻辑，确保重试后 `session/cancel` 命中新 turn
  - 重试仍失败时返回清晰 `turn_error`，包含“可重试一次 prompt”提示
- C. 验证:
  - 新增：
    - `TestE2EAcceptanceB1AppServerCrashDuringTurnAutoRetry`
    - `TestE2EAcceptanceB1AppServerCrashDuringTurnRetryFailureHasHint`
  - `go test ./...` 通过

### 2026-02-27 — `/logout` 增强：可复制恢复指引 + app-server auth 清理兼容
- A. 范围与目标:
  - 处理遗留问题 #2：`/logout` 后给出明确恢复路径，而非仅提示“未认证”
  - 保持 fail-closed，同时降低用户恢复成本
- B. 实现:
  - `/logout` 输出按 auth 模式区分的“可复制粘贴”下一步指令（`CODEX_API_KEY` / `OPENAI_API_KEY` / `codex login`）
  - 未认证错误附带 `nextStepCommand` 与模式化恢复 hint
  - app-server logout 兼容调用：优先 `account/logout`，回退 `auth/logout`
- C. 验证:
  - 新增：
    - `TestE2EAcceptanceG6LogoutGuidanceWithAPIKeysAndRecoveryAfterRestart`
  - 回归：
    - `TestE2EAcceptanceG6LogoutRequiresReauth`
    - `TestE2EAcceptanceI1ToI3AuthMethods`
    - `TestE2EAuthRequiredWithoutConfiguredMethod`
  - `go test ./...` 通过


### 2026-02-27 — Real app-server 存在性回归补齐（mcp/auth/compact）
- A. 范围与目标:
  - 处理遗留问题 #3：补充真实 `codex app-server` 的 mcp/auth/compact 基本路径回归
  - 保持默认 CI 不跑 real，用 `E2E_REAL_CODEX=1` 显式开启
- B. 实现:
  - 新增 `TestE2ERealCodexAppServer_BasicPromptAndCancel`：
    - 覆盖 `initialize -> session/new -> session/prompt(>=1 update,end_turn)` + cancel(`stopReason=cancelled`)
    - 增加 trace 断言：真实 app-server 链路包含 `initialize/initialized/thread/start/turn/start`
  - 新增 `TestE2ERealCodexAppServer_AuthMissingReturnsClearError` 与 `TestE2ERealCodexAppServer_AuthInjectedKeyRecovers`
  - 新增 `TestE2ERealCodexAppServer_MCPListAndOptionalCall` 与 `TestE2ERealCodexAppServer_CompactProducesVisibleUpdates`
  - 所有新增 real 用例维持 stdout 严格 JSON-RPC 校验（`rpcReader` + `assertStdoutPureJSONRPC`）
- C. 验证:
  - 已执行：`go test ./...`
  - real 回归入口：`E2E_REAL_CODEX=1 go test ./... -run TestE2EReal -count=1`

### 2026-02-27 — go module 路径与 GitHub 仓库地址对齐
- A. 范围与目标:
  - 修正 `go.mod` 的 `module`，避免外部 `go get/go install` 出现模块路径不匹配
- B. 实现:
  - `go.mod` 从 `module codex-acp` 调整为 `module github.com/beyond5959/acp-adapter`
  - 同步替换仓库内 Go 代码中的内部导入路径为 `github.com/beyond5959/acp-adapter/...`
- C. 验证:
  - 执行 `go test ./...` 通过
  - 全仓检查无残留 `\"codex-acp/...\"` 导入

### 2026-02-27 — 协议形态兼容修复（prompt 多形态 + session/update 标准 envelope）
- A. 范围与目标:
  - 解决 ACP 客户端使用 `prompt: ContentBlock[]` 时 `session/prompt` 直接 `invalid params` 的问题
  - 解决仅有 protocol traffic 可见、聊天面板不渲染 `message delta` 的问题
  - 对齐新版 app-server 的嵌套返回结构（`thread.id` / `turn.id` / `turn.status`）
- B. 实现:
  - `session/prompt` 参数解码升级为 `prompt: string | ContentBlock | ContentBlock[]` 统一入口
  - `session/update` 输出升级为超集：
    - 保留既有扁平字段（`type/delta/status/...`）
    - 同步输出标准 `update.sessionUpdate` 结构（当前 message/tool 为语义映射，其余更新回退 `agent_thought_chunk` 保证严格客户端可反序列化）
  - app-server client 同时兼容旧/新返回形态：
    - `thread/start`: `threadId` 或 `thread.id`
    - `turn/start/review/start/thread/compact/start`: `turnId` 或 `turn.id`
    - `turn/completed`: `stopReason` 或 `turn.status`
- C. 验证:
  - 新增：
    - `TestE2EPromptArrayContentBlocksAccepted`
    - `TestE2EMessageUpdateIncludesACPUpdateEnvelope`
  - 回归：
    - `TestE2EAcceptanceA1ToA5AndB1`
    - `TestE2EAuthMethodsAndAuthenticateFlow`
  - `go test ./...` 通过（偶发 integration 超时重跑后可过）

### 2026-03-06 — Tool server-request 兼容修复（`item/tool/requestUserInput` + `item/tool/call`）
- A. 范围与目标:
  - 修复 MCP/tool 流程触发 `item/tool/requestUserInput` 时返回 `-32000` 导致中断的问题。
  - 避免 `item/tool/call` 使用 method error 硬中断。
- B. 实现:
  - `internal/codex/client` 对 `item/tool/requestUserInput` 返回 schema-compatible `answers`（按 question 默认取首个 option label）。
  - `item/tool/call` 返回 `DynamicToolCallResponse{success:false}` 结构化失败结果，不再返回 RPC method error。
  - `internal/codex/types` 新增 `ToolRequestUserInput*` 与 `DynamicToolCall*` 类型。
- C. 验证:
  - `go test ./...` 通过。
