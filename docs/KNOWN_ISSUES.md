# KNOWN_ISSUES.md

> 记录已知问题、限制、坑位与规避方式。  
> 规则：发现问题必须补充“复现步骤 + 影响范围 + workaround + 后续计划”。

## 目录
- KI-0001：ACP stdio 输出被日志污染（stdout 非纯协议）
- KI-0002：bufio.Scanner 默认 token 限制导致大消息截断
- KI-0003：App Server 子进程崩溃/退出后的恢复策略
- KI-0004：取消（cancel）导致 goroutine/进程泄漏
- KI-0005：审批等待导致 turn 卡死（permission 未响应）
- KI-0006：终端交互/PTY 导致死锁风险（尤其 git rebase 等）
- KI-0007：Schema 版本漂移（codex 升级后字段变化）
- KI-0008：不同 ACP client 的 capabilities 差异导致功能不可用
- KI-0009：真实 App Server 与 fake server 事件形态可能不完全一致
- KI-0010：审批超时默认取消可能影响长时间人工确认场景
- KI-0011：Mode B（ACP fs 落盘）依赖客户端 `fs/write_text_file` 契约
- KI-0012：`/logout` 后缺少同进程重新登录入口
- KI-0013：profiles 配置目前仅支持 JSON（未实现 toml）
- KI-0014：J1 压测默认不在 `go test ./...` 中执行
- KI-0015：MCP/compact/auth/model-list 方法名对真实 app-server 版本敏感
- KI-0016：真实 codex e2e 依赖本机 codex 命令与认证态
- KI-0017：trace-json 脱敏规则为启发式，可能存在漏网字段
- KI-0018：mentions/images 输入有大小与能力门槛
- KI-0019：TODO 结构化仅覆盖 markdown checklist 形态
- KI-0020：go module 路径与仓库地址不一致会导致外部安装失败
- KI-0021：`session/update` 的标准 `update.sessionUpdate` 在低频事件上仍是回退语义
- KI-0022：并发请求下 `authenticate` 与后续请求存在时序依赖
- KI-0023：旧二进制缺少 `initialize.protocolVersion`，严格 ACP 客户端会在连接阶段失败
- KI-0024：库化改造的行为回归风险（独立模式与库模式偏差）
- KI-0025：嵌入模式并发/阻塞风险（宿主线程模型差异）
- KI-0026：permission 回写超时风险（嵌入链路）
- KI-0027：inproc transport 背压风险（宿主未及时消费导致写阻塞）
- KI-0028：Claude 适配器依赖本机 Claude Code CLI（claude 命令）
- KI-0029：Claude /review 命令通过 system prompt 模拟，非原生 review/start 语义
- KI-0030：Claude /compact 仅做摘要 prompt 压缩，不跨 CLI session 持久化
- KI-0031：Claude 子进程 CLAUDECODE 环境变量需过滤，否则触发嵌套 session 保护
- KI-0032：Claude 适配器 --dangerously-skip-permissions 默认开启
- KI-0033：项目重命名后的兼容路径变更（module/import/cmd/npm）
- KI-0034：`cmd/acp-adapter` 入口已移除（统一为 `cmd/acp`）
- KI-0035：Claude 模型列表依赖本地配置（非动态探测）
- KI-0036：`thought_level` 候选值在旧 codex 版本上可能退化为 fallback 列表
- KI-0037：部分新版 app-server server request 仍未桥接（显式 `-32000` fail-closed）
- KI-0038：`item/tool/requestUserInput` 目前为兼容自动选项，不是完整交互输入
- KI-0039：ACP `agent-plan` 仍依赖 `turn/plan/updated`，且 priority 只能启发式回填
- KI-0040：`session/load` 已实现，但历史 session 标识与回放仍有局限
- KI-0041：Claude CLI `session/list` / `session/load` 仅做占位与部分恢复
- KI-0042：`available_commands_update` 目前仍是 adapter 级命令目录
- KI-0043：默认开启 detailed reasoning summary 会增加输出与 token 消耗
- KI-0044：runtime `commandExecution` 仍无法区分 stdout/stderr 通道

---

## KI-0001：stdout 被日志污染
- 现象：ACP client 无法解析消息、进程被认为“协议错误”
- 复现：启用 debug 时把 log 打到 stdout
- 影响：致命（协议层）
- Workaround：所有日志强制 stderr；trace 文件落盘
- 修复计划：AGENTS.md 强制约束 + 测试 A1/J2 检查 stdout 纯净

## KI-0002：Scanner token 限制
- 现象：长 JSON 行被截断导致 json.Unmarshal 失败
- Workaround：使用 `bufio.Reader.ReadBytes('\n')` 或增大 buffer；并对消息大小做上限保护
- 影响：大文件引用、图片、长 tool 输出时高概率触发

## KI-0007：Schema 漂移
- 现象：升级 codex 版本后 app-server 协议字段变化导致运行时失败
- Workaround：固定 codex 版本；每次升级必须执行 `make schema` 并更新 types/校验；CI 检查 schema 变更

## KI-0005：审批等待导致 turn 卡死（permission 未响应）
- 现象：上游 ACP client 若不回 `session/request_permission`，turn 可能长期等待。
- 影响：
  - 当前实现在超时前无法继续该 tool call。
  - 超时后会走默认 `cancelled`，副作用工具不执行（fail-closed）。
- 复现：
  - 触发 approval 场景后，不返回 `session/request_permission` 响应。
- Workaround：
  - 客户端必须保证 permission UI 有超时或显式拒绝路径。
  - 适配器侧保底超时（当前默认 30s）后自动取消。
- 后续计划：
  - 在 PR4 评估把超时时间暴露为可配置项，并补充更友好的 timeout 提示。

## KI-0003：App Server 子进程崩溃/退出后的恢复策略
- 现象：
  - app-server 异常退出时，适配器会自动重启并尝试“当次 turn 内部重试一次”（默认开启）。
  - 若内部重试仍失败，turn 以 `turn_error` 结束并提示客户端“可重试一次 prompt”。
- 影响：
  - 常见“中途崩溃”场景不再必须由客户端手动重试。
  - 在不可安全重放边界（已发出不可回放内容）仍会 fail-closed，避免重复输出/副作用。
- 复现：
  - 成功恢复路径：设置 `FAKE_APP_SERVER_CRASH_DURING_TURN_ONCE_FILE` 并触发 `session/prompt`。
  - 重试失败路径：设置 `FAKE_APP_SERVER_CRASH_DURING_TURN=1` 并触发 `session/prompt`。
- Workaround：
  - 默认无需额外操作；若收到 `turn_error` 且消息含 retry hint，客户端可重试一次 `session/prompt`。
  - 可通过 `RETRY_TURN_ON_CRASH=0` 或 `--retry-turn-on-crash=false` 关闭内部重试。
- 后续计划：
  - 评估“已发出部分输出后的安全重放”策略（可选缓冲提交），进一步缩小人工重试窗口。

## KI-0009：真实 App Server 与 fake server 事件形态可能不完全一致
- 现象：当前 e2e 主要依赖 fake app-server，事件字段形态由测试替身控制。
- 影响：
  - 在真实 codex app-server 新字段/兼容字段出现时，可能出现映射遗漏。
- Workaround：
  - 通过 `codex/client` 对未知 notifications 保持忽略且不崩溃。
  - 在真实环境补充集成回归并同步 schema。
- 后续计划：
  - PR4 开始增加真实 app-server 的回归脚本与录制样例（脱敏）。

## KI-0010：审批超时默认取消可能影响长时间人工确认场景
- 现象：审批链路采用 fail-closed，超时会自动返回 `cancelled`。
- 影响：
  - 安全性满足 D5，但在人机审批较慢时可能出现“误取消”。
- Workaround：
  - 客户端在超时前提供提醒并允许用户快速 approve/decline。
  - 需要长审批窗口时，分批拆分任务降低单次审批等待。
- 后续计划：
  - 后续增加 timeout 配置与审计字段，便于按场景调优。

## KI-0011：Mode B（ACP fs 落盘）依赖客户端 `fs/write_text_file` 契约
- 现象：PR4 的 ACP fs 落盘模式依赖上游实现 `fs/write_text_file` 并支持 path/text/冲突结果语义。
- 影响：
  - 不同 ACP client 若方法名或参数不一致，Mode B 会失败并触发 `review_apply_failed`。
  - E2 在 fake harness 可通过，但真实客户端需联调确认。
- 复现：
  - 启用 `PATCH_APPLY_MODE=acp_fs`，让上游不实现 `fs/write_text_file` 或返回不兼容 payload。
- Workaround：
  - 默认使用 Mode A（`appserver`）以保证可用性。
  - 在对接特定 ACP client 时适配其 fs RPC 形状。
- 后续计划：
  - PR5 增加 fs 方法适配层与 capability 检测，按客户端能力动态选择模式。

## KI-0012：`/logout` 后缺少同进程重新登录入口
- 现象：执行 `/logout` 后，适配器进入未认证状态，后续 `session/new`/`session/prompt` 会被 auth gate 拒绝。
- 影响：
  - 现在会提供按 auth 模式区分的可复制恢复指令（API key / `codex login`），但仍需重启 adapter 恢复可用。
  - subscription 模式在无浏览器或本地回调不可用环境下，`codex login` 可能无法完成。
- 复现：
  - 先正常对话，再发送 `/logout`，随后发送任意 prompt。
- Workaround：
  - 按 `/logout` 输出的 next-step 指令执行：
    - API key：设置 `CODEX_API_KEY` 或 `OPENAI_API_KEY` 后重启 adapter。
    - subscription：运行 `codex login` 完成浏览器登录/本地回调后重启 adapter。
- 后续计划：
  - 评估增加显式 re-auth RPC 或与下游 auth 流程对接，实现无重启恢复。

## KI-0013：profiles 配置目前仅支持 JSON（未实现 toml）
- 现象：PR5 新增 profile 配置读取仅支持 `CODEX_ACP_PROFILES_JSON`/`CODEX_ACP_PROFILES_FILE(JSON)`。
- 影响：
  - 与 SPEC 中提及的 `config.toml` 形态暂未完全对齐。
- 复现：
  - 提供 toml profile 文件并通过 `CODEX_ACP_PROFILES_FILE` 指向，profiles 不生效。
- Workaround：
  - 使用 JSON profile 配置（内联或文件）。
- 后续计划：
  - 补充 toml 解析与 schema 校验，保持与文档示例一致。

## KI-0014：J1 压测默认不在 `go test ./...` 中执行
- 现象：`TestE2EAcceptanceJ1Stress100Turns` 需要 `RUN_STRESS_J1=1` 才运行。
- 影响：
  - 常规 CI 只覆盖功能回归，不会自动覆盖 100 turns 压力路径。
- 复现：
  - 直接执行 `go test ./...`，J1 用例显示 skipped。
- Workaround：
  - 执行 `make stress-j1` 或 `scripts/j1_stress.sh`。
- 后续计划：
  - 在 CI 增加定时/夜间压力作业，独立于常规 PR 快速回归。

## KI-0015：MCP/compact/auth/model-list 方法名对真实 app-server 版本敏感
- 现象：
  - 当前实现依赖 `thread/compact/start`、`mcpServer/*`、`account/logout|auth/logout`、`model/list` 方法名。
  - 已新增 real 存在性回归：`TestE2ERealCodexAppServer_MCPListAndOptionalCall`、`TestE2ERealCodexAppServer_CompactProducesVisibleUpdates`；当 endpoint 不兼容时会暴露为 `method not found` 或兼容降级。
- 影响：
  - 若真实 app-server 不同版本方法名/参数变更，PR5 相关能力会出现 `method not found` 或参数不兼容。
- 复现：
  - 连接不支持上述 endpoint 的 app-server 版本执行相应 slash 命令。
- Workaround：
  - 通过兼容错误处理回退（logout 优先 `account/logout`，回退 `auth/logout`），并优先使用对齐版本联调。
  - real 回归里若 `/compact` 返回 endpoint 不支持，会给出明确 skip 原因；需升级本机 codex 版本后再执行 full path 断言。
- 后续计划：
  - 在 B2 schema 锁定后引入 endpoint capability 检测与版本门控。

## KI-0016：真实 codex e2e 依赖本机 codex 命令与认证态
- 现象：`E2E_REAL_CODEX=1` 时测试会执行 `make schema` 并启动真实 `codex app-server`。
- 影响：
  - 若本机未安装 `codex` 或认证不可用，真实 e2e 会跳过（带原因），不会覆盖真实链路。
  - `TestE2ERealCodexAppServer_AuthInjectedKeyRecovers` 需要可注入 key；未提供时会 skip。
  - `TestE2ERealCodexAppServer_MCPListAndOptionalCall` 在无 MCP server 配置时仅验证 list 路径，call 分支不会执行。
  - 本机 `~/.codex/state_*.sqlite` 迁移漂移时，可能出现 `migration ... missing` 警告并伴随部分真实能力异常（联调日志可见）。
- 复现：
  - 执行 `E2E_REAL_CODEX=1 go test ./... -run TestE2EReal -count=1`，且环境缺少 codex/auth。
  - 例如 `TestE2ERealCodexAppServer_BasicPromptAndCancel` / `TestE2ERealCodexPromptInteractions` 会因 `thread/start failed` skip。
- Workaround：
  - 安装并确保 `codex app-server` 可运行；准备可用认证态（API key 或 subscription）以让 real e2e 实际执行。
  - 若出现 `state_*.sqlite migration ... missing`，先修复/清理本机 codex state 后再重跑 real e2e。
  - 若要覆盖 auth 注入恢复路径，设置：
    - `E2E_REAL_CODEX_RECOVERY_CODEX_API_KEY=<key>` 或
    - `E2E_REAL_CODEX_RECOVERY_OPENAI_API_KEY=<key>`
- 后续计划：
  - 在 CI 增加可选 real-e2e job，并明确环境先决条件。

## KI-0017：trace-json 脱敏规则为启发式，可能存在漏网字段
- 现象：当前 trace 脱敏按 key/token 模式匹配，不是全量语义理解。
- 影响：
  - 非标准字段名承载敏感信息时，理论上可能未被识别并脱敏。
- 复现：
  - 在自定义字段中写入敏感值且字段名不含敏感关键字。
- Workaround：
  - 生产环境谨慎开启 trace；优先在本地调试使用并定期审查脱敏规则。
- 后续计划：
  - 基于真实日志样本扩充脱敏词典，并支持可配置的自定义脱敏 key 列表。

## KI-0018：mentions/images 输入有大小与能力门槛
- 现象：
  - mentions 无内联文本时，适配器仅在检测到 `fs/read_text_file` capability 后才会补读。
  - 图片输入要求 mime 白名单（png/jpeg/webp/gif）且 base64/data-uri payload 不超过 4MiB。
- 影响：
  - client 未声明 fs 读能力时，mentions 会降级为“仅路径引用 + 缺上下文告警”。
  - 超限或非法图片会在 `session/prompt` 返回 `invalid params`。
- 复现：
  - 发送无 text 的 mention block 且 initialize 不声明 fs/read capability。
  - 发送 mime 不在白名单或超 4MiB 的 image block。
- Workaround：
  - 对 mentions：优先由 client 提供内联 `resource.text`，或显式声明并实现 `fs/read_text_file`。
  - 对 images：在客户端先做压缩/重采样并保证 mime 合法。
- 后续计划：
  - 支持可配置的图片大小上限与 mime 白名单；补充更细粒度 capability 探测。

## KI-0040：`session/load` 已实现，但历史 session 标识与回放仍有局限
- 现象：
  - Codex adapter 已支持 ACP `session/list` 与 `session/load`，可通过 app-server `thread/list` / `thread/resume` 发现并恢复历史 thread。
  - 但 `session/list` 返回的 `sessionId` 仍是 adapter 进程内映射；重启 adapter 后，同一历史 thread 可能分配新的 `sessionId`。
  - `thread/resume` 返回的 `thread.turns` 本身是 lossy history；app-server schema 明确说明不会完整持久化所有 agent 交互，例如部分 command/tool 细节。
- 影响：
  - 当前 `session/load` 适合恢复“当前 adapter 进程里已发现过的历史 session”；跨重启稳定恢复仍不保证。
  - load 回放主要覆盖 `userMessage` / `agentMessage`；对未持久化的 tool/reasoning 细节，客户端只能接受降级后的历史视图。
- 复现：
  - 执行 `initialize` 后调用 `session/list`，可收到历史会话分页结果。
  - 在同一进程内对 list 出来的 session 调用 `session/load`，可恢复历史并继续 prompt。
  - 重启 adapter 后再次 `session/list`，同一历史 thread 的 `sessionId` 可能变化。
- Workaround：
  - 当前把 `sessionId` 视为当前 adapter 进程的会话句柄；若客户端需要更稳定的外部标识，可同时缓存 `_meta.threadId`。
  - 对需要完整 tool/reasoning 历史的场景，仍应以原生 Codex 客户端的 thread 视图为准。
- 后续计划：
  - 将 `session/list` / `session/load` 的 id 稳定性统一到 thread id 或持久化映射。
  - 评估是否把更多 persisted item（如 plan/review）映射为 ACP 标准历史回放事件。

## KI-0041：Claude CLI `session/list` / `session/load` 仅做占位与部分恢复
- 现象：
  - Claude adapter 现已暴露 ACP `session/list` 与 `session/load` 方法，但能力不对称：
    - `session/list` 恒返回空页。
    - `session/load` 仅支持“调用方已知 Claude native session id”的恢复占位，并把该 id 作为后续 ACP `sessionId` 使用。
  - 当前 Claude CLI 只有 `--resume <session-id>` / `--continue`，没有稳定的 machine-readable 会话枚举与历史消息回放接口。
- 影响：
  - ACP client 目前无法通过 Claude adapter 浏览真实的 Claude 历史会话列表。
  - `session/load` 后不会收到历史 `user_message_chunk` / `agent_message_chunk` 回放；它只保证后续 `session/prompt` 能继续使用该 native session id。
  - `configOptions` 仅按 adapter 当前默认 model / effort 重建，不代表原会话真实历史配置。
- 复现：
  - Claude 模式下执行 `initialize`，会看到 `sessionCapabilities.list` 与 `loadSession=true`。
  - 调用 `session/list`，结果始终为空页。
  - 调用 `session/load(sessionId=<Claude native session id>)` 后，再 `session/prompt`，请求会走 `claude --resume <session-id>`。
- Workaround：
  - 若上游需要恢复 Claude 会话，必须自行缓存 Claude native session id，再把该 id 传给 `session/load`。
  - 不要把 Claude `session/list` 结果视为真实历史索引；当前它只是协议占位。
- 后续计划：
  - 若 Claude CLI/SDK 后续提供稳定的会话列表或 transcript 读取接口，再升级为真正的 `session/list` / `session/load` 历史回放实现。
  - 评估是否在 adapter 内持久化“ACP session id -> Claude native session id”映射，减少上游自行缓存的负担。

## KI-0042：`available_commands_update` 目前仍是 adapter 级命令目录
- 现象：
  - adapter 已支持在 `session/new` / `session/load` 后主动发布 ACP `available_commands_update`，并在 `/logout` / `authenticate` 后刷新命令表。
  - 但命令目录当前仍由 adapter runtime 静态定义，不会实时探测更细粒度的后端能力变化。
- 影响：
  - Codex 真实后端如果因版本/配置差异暂时失去某个 endpoint，可广告命令与实际可执行能力短时间不一致。
  - `authenticate` 只会刷新当前进程内“已知 session”；外部客户端若缓存了旧 session 且未重新连上，命令表可能仍旧。
- 复现：
  - 启动 session 后更新底层 codex app-server 版本或外部配置，再观察客户端 slash popup。
  - `/logout` 后在另一端恢复认证，但不重新连接旧 session。
- Workaround：
  - 在能力明显变化后，重新创建或重新加载 session，让 adapter 重新发布命令目录。
  - 对 Codex 后端版本敏感命令，仍以实际调用结果为准，并结合现有错误提示处理。
- 后续计划：
  - 评估把命令目录与更细粒度 capability 探测绑定，例如按后端 endpoint 可用性或客户端功能开关动态裁剪。
  - 评估在更多全局状态变化时广播命令目录刷新事件。

## KI-0019：TODO 结构化仅覆盖 markdown checklist 形态
- 现象：当前 TODO 结构化解析依赖 `- [ ]` / `- [x]`（含数字序号变体）markdown checklist。
- 影响：
  - 模型若返回自然语言 checklist、表格或其它非 markdown checklist 形态，不会填充 `session/update.todo`，仅保留原文 delta。
  - 新增的 ACP `plan` update 与旧 `todo` 字段是两条独立链路；未消费 `plan` 的旧客户端仍只看到 checklist TODO。
- 复现：
  - 让模型输出“Step 1/Step 2”但不使用 checklist 语法。
- Workaround：
  - 在提示词中显式要求 markdown checklist 输出。
- 后续计划：
  - 扩展 TODO 多格式解析器，并评估在 plan update 到达时是否同步生成更丰富的 TODO 视图。

## KI-0020：go module 路径与仓库地址不一致会导致外部安装失败
- 现象：
  - `go.mod` 若使用短 module（如 `acp-adapter`）而仓库地址为 `github.com/beyond5959/acp-adapter`，外部使用仓库地址安装会报模块路径不匹配。
- 影响：
  - `go get` / `go install` 失败，第三方集成与 CI 拉取依赖不稳定。
- 复现：
  - 保持短 module 路径后执行：`go install github.com/beyond5959/acp-adapter/cmd/acp@latest`。
- Workaround：
  - 使用 canonical module：`module github.com/beyond5959/acp-adapter`。
  - 变更后同步替换仓库内 `acp-adapter/...` 导入路径。
- 后续计划：
  - 仓库地址若变更（迁移/重命名），同一 PR 内同步更新 `go.mod` 和全部内部导入。

## KI-0033：项目重命名后的兼容路径变更（module/import/cmd/npm）
- 现象：
  - 项目从 `codex-acp-go` 重命名为 `acp-adapter` 后，旧路径（如 `cmd/codex-acp-go`、`pkg/acpadapter`、`github.com/beyond5959/codex-acp`、`@beyond5959/codex-acp-go`）已不可用。
- 影响：
  - 外部脚本、CI 配置、第三方导入若仍依赖旧命名，会出现构建失败或命令找不到。
- 复现：
  - 继续执行旧命令：`go build ./cmd/codex-acp-go` 或 `go test` 时导入 `github.com/beyond5959/codex-acp/...`。
- Workaround：
  - 统一切换到新路径：
    - Go module/import：`github.com/beyond5959/acp-adapter`
    - cmd 入口：`cmd/acp`（使用 `--adapter codex|claude`）
    - 包路径：`pkg/codexacp`
    - npm 包：`@beyond5959/acp-adapter` 及其平台子包
- 后续计划：
  - 当前仓库内已完成替换并通过 `go test ./...`；对外使用方需同步升级配置。

## KI-0034：`cmd/acp-adapter` 入口已移除（统一为 `cmd/acp`）
- 现象：
  - 新版本删除了 `cmd/acp-adapter`，统一使用 `cmd/acp` 并通过 `--adapter codex|claude` 选择后端。
- 影响：
  - 仍执行 `go build ./cmd/acp-adapter`、或在外部配置中直接引用旧入口路径的脚本会失败。
- 复现：
  - 执行：`go build ./cmd/acp-adapter`
- Workaround：
  - 构建：`go build -o ./bin/acp ./cmd/acp`
  - 运行 Codex 后端：`./bin/acp --adapter codex`
  - 运行 Claude 后端：`./bin/acp --adapter claude`
- 后续计划：
  - 保持文档、测试和发布脚本全部围绕 `cmd/acp` 维护，避免双入口漂移。

## KI-0021：`session/update` 的标准 `update.sessionUpdate` 在低频事件上仍是回退语义
- 现象：
  - 适配器已支持“扁平字段 + 标准 envelope”双输出，并保证每条 `session/update` 都带 `update.sessionUpdate`。
  - 已补齐 `plan` 标准映射，但对部分非 message/tool 的低频更新，当前仍用 `agent_thought_chunk` 文本回退承载。
- 影响：
  - 严格 ACP 客户端可稳定反序列化，但在 thought/模式切换等低频更新上仍可能出现“语义被弱化”的展示差异。
- 复现：
  - 使用仅消费 `params.update.sessionUpdate` 的 ACP client，观察非 message/tool 的 session/update 呈现为通用 thought chunk。
- Workaround：
  - 客户端同时消费扁平字段（`type/status/delta/message/...`）与标准 envelope，以保留更多语义。
- 后续计划：
  - 继续扩展标准映射覆盖：thought、mode/model update、permission/tool 生命周期等，减少通用回退路径。

## KI-0022：并发请求下 `authenticate` 与后续请求存在时序依赖
- 现象：
  - server 请求处理是并发的。若客户端并发发送 `authenticate` 与 `session/new`/`session/prompt`，后者可能先到达 auth gate 并被拒绝。
- 影响：
  - 个别会“抢发请求”的客户端可能出现“刚认证成功但第一次 session/new 仍报 requires authentication”。
- 复现：
  - 在未认证状态下，几乎同时发送：
    - `authenticate(methodId=...)`
    - `session/new`
- Workaround：
  - 客户端必须等待 `authenticate` 的成功响应后，再发送 `session/new` 或 `session/prompt`。
  - 若出现该错误，重试一次 `session/new` 通常可恢复。
- 后续计划：
  - 评估在 adapter 侧为“认证切换窗口”增加短暂排队或重试，降低时序敏感度。

## KI-0023：旧二进制缺少 `initialize.protocolVersion`，严格 ACP 客户端会在连接阶段失败
- 现象：
  - 使用旧版 `acp-adapter` 二进制连接严格 ACP 客户端（如 Zed）时，可能在连接阶段报错：`failed to deserialize response`。
- 影响：
  - 初始化握手失败，无法进入认证和会话创建流程。
- 复现：
  - 使用未包含本次修复的旧二进制启动 agent，并让客户端发 `initialize(protocolVersion=1)`。
- Workaround：
  - 重新构建并替换二进制：
    - `go build -o ./bin/acp ./cmd/acp`
  - 重启 ACP 客户端后重连。
- 后续计划：
  - 保持 `TestE2EInitializeIncludesACPStandardFields` 回归，防止该字段再次回退。

## KI-0024：库化改造的行为回归风险（独立模式与库模式偏差）
- 现象：
  - 引入库入口后，若 `cmd` 与 `pkg` 装配参数不一致，可能出现独立模式可用但嵌入模式行为漂移（或反之）。
- 影响：
  - 直接影响协议兼容性（A1-A5）与现网客户端稳定性，属于高风险回归点。
- 复现：
  - 在同一输入下分别运行独立模式与库模式，对比 `initialize/session/update` 输出，出现字段/时序差异。
- Workaround：
  - 已新增 R4 契约对照回归：同一脚本双跑 standalone/embedded，并对照 initialize/new/prompt/cancel/permission 关键行为。
- 后续计划：
  - R4 已完成；R5/R6 持续扩展对照覆盖面（review/compact/mcp/auth 等）并维护差异白名单。

## KI-0025：嵌入模式并发/阻塞风险（宿主线程模型差异）
- 现象：
  - 作为库被宿主调用时，宿主可能使用与 CLI 不同的 goroutine/锁模型，导致 turn 处理阻塞或 cancel 延迟。
- 影响：
  - 可能破坏“每 session 单 active turn”约束与 `session/cancel` 及时性，严重时触发 goroutine 泄漏。
- 复现：
  - 在嵌入 API 中并发触发 prompt + cancel + permission 回调，观察是否出现死锁或超时。
- Workaround：
  - 嵌入 API 明确 async/阻塞语义，要求回调不可重入阻塞主读写循环。
- 后续计划：
  - R4 已补充并发双 session 不变量测试（无阻塞/无串扰）；R5 继续增加高并发 cancel 与 permission 回调竞争压测。

## KI-0026：permission 回写超时风险（嵌入链路）
- 现象：
  - 嵌入模式下 permission 由宿主 UI 或业务层回传；若宿主未及时回写，turn 会长时间等待并最终超时取消。
- 影响：
  - 用户侧表现为“工具调用卡住后失败”，且副作用工具按 fail-closed 被拒绝，影响任务完成率。
- 复现：
  - 触发 `session/request_permission` 后不回写或延迟回写超过超时阈值。
- Workaround：
  - 宿主必须实现超时前显式 approve/decline/cancel，并在 UI 提示倒计时。
- 后续计划：
  - R3 增加可配置 timeout 与回调观测字段；R4 补超时与迟到回写回归测试。

## KI-0027：inproc transport 背压风险（宿主未及时消费导致写阻塞）
- 现象：
  - R2 新增的 inproc channel transport 在宿主不及时读取 outbound 消息时，会触发写端背压并阻塞后续事件发送。
- 影响：
  - 在高频 `session/update` 场景下，可能放大延迟并影响 cancel/permission 等控制消息的处理时效。
- 复现：
  - 使用小 buffer inproc transport，持续写入通知且宿主不消费（或消费速度显著低于生产速度）。
- Workaround：
  - 嵌入宿主必须持续消费 transport 输出；必要时提高 channel buffer，避免长时间无消费。
- 后续计划：
  - R3 评估引入可观测指标（队列深度/阻塞时长）与可配置背压策略，R4 增加压力回归。

## KI-0028：Claude 适配器依赖本机 Claude Code CLI（claude 命令）
- 现象：
  - `cmd/acp --adapter claude` 依赖 `$CLAUDE_BIN`（默认 `claude`）可执行文件存在并已登录认证。
  - 若 `claude` 未安装或未登录，`TurnStart` 时子进程启动失败，`session/prompt` 返回错误。
- 影响：
  - 在未安装 Claude Code 的 CI / Docker 环境中，Claude 适配器完全不可用。
- 复现：
  - 不安装 claude 直接运行 `cmd/acp --adapter claude` 并发 `session/prompt`。
- Workaround：
  - 确保运行环境已安装 Claude Code 并完成 `claude auth login`。
  - 在测试环境设置 `CLAUDE_BIN=<path/to/fake_claude_cli>` 使用 fake 替身。
- 后续计划：
  - 评估内嵌 claude API 调用作为无 CLI 降级路径。

## KI-0029：Claude /review 命令通过 system prompt 模拟，非原生 review/start 语义
- 现象：
  - Claude 适配器的 `/review`、`/review-branch`、`/review-commit` 通过特殊 system prompt 触发 Claude 做代码审阅，无法产生与 Codex `review/start` 完全相同的 entered/exited review mode 通知序列。
- 影响：
  - 严格依赖 `review_mode_entered`/`review_mode_exited` 事件的 ACP 客户端在 Claude 模式下展示可能不完整。
- 复现：
  - 用 Claude 适配器执行 `/review` 并断言 review mode 事件序列。
- Workaround：
  - 适配器会在 turn 开始/结束时发送等价 status 事件（`review_started`/`review_completed`），客户端可消费扁平 status 字段。
- 后续计划：
  - 若 Anthropic API 未来支持原生 review 能力，替换为原生实现。

## KI-0030：Claude /compact 仅做摘要 prompt 压缩，不跨 CLI session 持久化
- 现象：
  - `/compact` 向 claude CLI 发送压缩 prompt；由于 `--resume <uuid>` 机制，CLI 本身已持久化历史，但 compact 后的新摘要仍依赖 Claude Code 磁盘上的 session 文件。
  - 若 CLI session 文件被清理，历史不可恢复。
- 影响：
  - compact 后若 claude 清理旧 session，后续 `--resume` 会失败并触发错误。
- Workaround：
  - 避免在 compact 后清理 claude 的 session 存储（`~/.claude/projects/`）。
- 后续计划：
  - 检测 `--resume` 失败时自动回退到新 `--session-id` 重新建立会话。

## KI-0031：Claude 子进程 CLAUDECODE 环境变量需过滤，否则触发嵌套 session 保护
- 现象：
  - 在 Claude Code 交互会话内运行 `cmd/acp --adapter claude` 时，`claude` 子进程检测到 `CLAUDECODE` 环境变量并拒绝启动（报 `Claude Code cannot be launched inside another Claude Code session`）。
- 影响：
  - 在 Claude Code 内部调试/测试 Claude 适配器时，所有 `session/prompt` 均失败。
- 复现：
  - 在 Claude Code 终端中执行 `cmd/acp --adapter claude` 并发 `session/prompt`。
- Workaround：
  - 适配器已在 `buildCmd` 中自动过滤 `CLAUDECODE` 变量（`filterEnv`），正常使用无需手动处理。
  - 若手动测试需要在 Claude Code 内运行 `claude -p`，可先执行 `unset CLAUDECODE`。
- 后续计划：
  - 当前已在 `client.go:buildCmd` 中修复，保持回归测试覆盖。

## KI-0032：Claude 适配器 --dangerously-skip-permissions 默认开启
- 现象：
  - `SkipPerms: true` 为默认配置，`claude -p` 子进程以 `--dangerously-skip-permissions` 启动，工具调用不经过用户审批自动执行。
- 影响：
  - 模型若调用文件写入、命令执行等副作用工具，会在无用户介入的情况下执行。
  - ACP `session/request_permission` 不会被触发（`ApprovalRespond` 为 no-op）。
- 复现：
  - 默认配置下发送含工具调用触发词的 prompt，观察无 permission 请求。
- Workaround：
  - 若需要审批，通过 `--skip-perms=false` 关闭，并配置 `--allowed-tools` 显式白名单。
  - 或在受信任的 CI/自动化环境中维持默认（skip）以减少交互。
- 后续计划：
  - 评估将 tool approval 事件从 `claude -p` 的 stream-json 输出中解析并桥接到 ACP `session/request_permission`，恢复 approval 往返语义。

## KI-0035：Claude 模型列表依赖本地配置（非动态探测）
- 现象：
  - Claude 适配器的 `Session Config Options` 模型列表来源于本地配置聚合（`CLAUDE_MODELS` / `--models` / `--model` / profiles）。
  - 当前未接入 Claude CLI 的“官方模型目录”查询 API（CLI 侧目前也无稳定 JSON-RPC 端点可复用）。
- 影响：
  - 若配置缺失，模型列表可能只包含单个默认模型。
  - 若配置与实际 CLI 支持模型不一致，`session/set_config_option` 可选项可能包含不可用模型，随后 turn 执行会失败。
- 复现：
  - 不设置 `CLAUDE_MODELS` 且无 profile model，仅设置 `--model` 启动，`session/new.configOptions` 只返回一个 model 选项。
- Workaround：
  - 显式设置 `CLAUDE_MODELS`（或 `--models`）并保持与本机 Claude CLI 实际可用模型一致。
  - 通过 profile 配置补充团队标准模型列表，统一客户端展示。
- 后续计划：
  - 若 Claude CLI 未来提供稳定模型列表接口，切换到动态探测并保留本地配置作为兜底。

## KI-0036：`thought_level` 候选值在旧 codex 版本上可能退化为 fallback 列表
- 现象：
  - 新增 `thought_level` 后，adapter 优先使用 `model/list` 返回的 `supportedReasoningEfforts/defaultReasoningEffort` 构建候选值。
  - 若真实 codex app-server 版本较旧、未返回 reasoning 元数据，adapter 会回退到内置 effort 列表（`none/minimal/low/medium/high/xhigh`）。
- 影响：
  - 客户端 UI 仍可展示与切换 `thought_level`，但候选值可能与后端真实支持集不完全一致。
  - 当用户选择了后端不支持的 effort，可能在真实 turn 执行时收到后端参数错误。
- 复现：
  - 连接不含 `supportedReasoningEfforts/defaultReasoningEffort` 的旧版 app-server，执行 `session/new` 并查看 `configOptions` 中的 `thought_level`。
- Workaround：
  - 升级本机 codex 到支持 reasoning 元数据的版本。
  - 在出现后端参数错误时，切回 `medium` 或模型默认 effort 再重试。
- 后续计划：
  - 在 adapter 中增加 downstream capability 探测与值白名单回写，进一步减少 fallback 与真实能力不一致的窗口。

## KI-0037：部分新版 app-server server request 仍未桥接（显式 `-32000` fail-closed）
- 现象：
  - 已兼容：
    - `item/commandExecution/requestApproval`
    - `item/fileChange/requestApproval`
    - `item/tool/requestUserInput`（兼容回包）
    - `item/tool/call`（兼容失败回包）
  - 但以下 server request 仍未实现完整桥接：
    - `account/chatgptAuthTokens/refresh`
    - legacy `execCommandApproval` / `applyPatchApproval`
  - 对未实现项当前仍为显式 `-32000`（fail-closed），不再返回 `-32601 method not found`。
- 影响：
  - `requestUserInput`/`tool/call` 不再 hard-fail，但仍是兼容策略（非完整交互式能力）。
  - 未实现项在真实链路仍会被拒绝，可能导致 turn 失败或降级。
- 复现：
  - 连接触发 `account/chatgptAuthTokens/refresh` 或 legacy approval request 的 app-server 版本。
- Workaround：
  - 优先使用当前已覆盖的 command/file/tool 兼容路径。
  - 对 `chatgptAuthTokens/refresh` 场景，使用 managed auth（由 codex 自管刷新）或在宿主侧避免 external-auth-only 配置。
- 后续计划：
  - 逐项补齐 `chatgptAuthTokens/refresh` 与 legacy approval 桥接能力，并补充端到端验收用例。

## KI-0038：`item/tool/requestUserInput` 目前为兼容自动选项，不是完整交互输入
- 现象：
  - 当前对 `item/tool/requestUserInput` 使用兼容回包：每题默认选择首个 option label。
  - 尚未支持多选策略、自由文本输入、secret 输入等完整交互能力。
- 影响：
  - 可避免 hard error，但答案语义可能与真实用户意图不一致。
- 复现：
  - 触发包含复杂问题（多选/自由输入）的 `requestUserInput` 请求，观察回包为默认首选项。
- Workaround：
  - 优先在不依赖复杂交互问题的 MCP/tool 场景使用该链路。
  - 对关键操作，使用原生 codex app 交互界面执行。
- 后续计划：
  - 在 ACP/hub 侧补齐通用 user-input 请求桥接与 UI 交互闭环。

## KI-0039：ACP `agent-plan` 仍依赖 `turn/plan/updated`，且 priority 只能启发式回填
- 现象：
  - 当前 adapter 已把 codex app-server `turn/plan/updated` 映射为 ACP `session/update(plan)`，并新增 `item/plan/delta + item/completed(plan)` 的 fallback 桥接。
  - 但下游 schema 未提供 priority 字段，adapter 只能固定回填 `priority=medium`。
  - fallback delta 路径缺少结构化 step status，adapter 只能保守回填 `status=pending`。
- 影响：
  - ACP client 已可消费标准 `plan` entries，但无法区分高/中/低优先级。
  - 在仅靠 delta fallback 的版本上，客户端能看到计划文本流，但无法反映真实 completed/in_progress 状态。
- 复现：
  - 连接只发 `item/plan/delta` 不发 `turn/plan/updated`，或对 priority / status 有更细粒度需求的 app-server/client 组合。
- Workaround：
  - 优先使用会发 `turn/plan/updated` 的 codex app-server 版本，以获得真实 step status。
  - 客户端若需要优先级差异，可暂时结合普通文本说明做展示降级。
- 后续计划：
  - 若下游后续补充 priority 元数据，再升级 ACP plan 映射以透传真实优先级。
  - 若下游后续为 delta/item-completed 提供结构化状态，再升级 fallback 路径以透传真实状态。

## KI-0043：默认开启 detailed reasoning summary 会增加输出与 token 消耗
- 现象：
  - 适配器现在默认以 `codex app-server -c 'model_reasoning_summary="detailed"'` 启动真实 Codex 后端。
  - 这会让真实 app-server 更积极地输出 `item/reasoning/summaryTextDelta`，从而在 ACP 上游看到 `agent_thought_chunk`。
- 影响：
  - reasoning summary 会增加流式输出体积，也可能增加模型侧 reasoning summary 相关 token 消耗。
  - 对只关心最终答案的客户端，thought UI 可能显得更“吵”。
- 复现：
  - 使用默认 `cmd/acp --adapter codex` 或 `pkg/codexacp.DefaultRuntimeConfig()` 启动，观察 trace 中 reasoning summary delta。
- Workaround：
  - 若不想默认输出 thought summary，可显式覆盖：
    - `CODEX_APP_SERVER_ARGS='app-server -c model_reasoning_summary="none"'`
    - 或 `--app-server-args 'app-server -c model_reasoning_summary="none"'`
- 后续计划：
  - 评估把 reasoning summary detail 暴露为适配器一级显式配置，而不是仅靠透传 app-server args。

## KI-0044：runtime `commandExecution` 仍无法区分 stdout/stderr 通道
- 现象：
  - 适配器现在已经把 `item/started`、`item/commandExecution/outputDelta`、`item/completed(type=commandExecution)` 全部桥接成 ACP `tool_call_update`，`update.content.text` 可携带命令文本、逐块输出和最终 `aggregatedOutput`。
  - 但 app-server 当前 `item/commandExecution/outputDelta` 事件只提供纯文本 `delta`，没有 `stdout` / `stderr` / `channel` 之类的字段。
- 影响：
  - ACP client 已能实时展示命令输出，但如果 UI 需要分别渲染 stdout 与 stderr，当前协议形态不足以支持。
  - 终端样式回放只能按单一文本流追加，而不能忠实复刻多通道终端语义。
- 复现：
  - 使用真实 `codex app-server` 运行项目问答（例如「这是什么项目？」），观察 trace：
    - app-server 会发送 `item/commandExecution/outputDelta` / `item/completed.commandExecution.aggregatedOutput`
    - 其中 `outputDelta` payload 只有 `delta` 文本，没有通道标签
- Workaround：
  - 若 UI 只需要顺序展示命令输出，直接追加 `tool_call_update.content.text` 即可。
  - 若必须区分 stdout/stderr，只能依赖未来 app-server 增补字段，或让命令本身在输出里带自定义前缀。
- 后续计划：
  - 持续跟踪 app-server schema；如果后续 `commandExecution/outputDelta` 增加通道元数据，再把它映射到 ACP 更细粒度的输出语义。
