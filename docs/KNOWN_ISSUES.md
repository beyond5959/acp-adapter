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
- KI-0015：MCP/compact/auth 方法名对真实 app-server 版本敏感
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
  - 通过 `appserver/client` 对未知 notifications 保持忽略且不崩溃。
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

## KI-0015：MCP/compact/auth 方法名对真实 app-server 版本敏感
- 现象：
  - 当前实现依赖 `thread/compact/start`、`mcpServer/*`、`account/logout|auth/logout` 方法名。
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

## KI-0019：TODO 结构化仅覆盖 markdown checklist 形态
- 现象：当前 TODO 结构化解析依赖 `- [ ]` / `- [x]`（含数字序号变体）markdown checklist。
- 影响：
  - 模型若返回自然语言计划、表格或其它格式，不会填充 `session/update.todo`，仅保留原文 delta。
- 复现：
  - 让模型输出“Step 1/Step 2”但不使用 checklist 语法。
- Workaround：
  - 在提示词中显式要求 markdown checklist 输出。
- 后续计划：
  - 评估接入 app-server 原生 plan/todo 事件（若可用）并扩展多格式解析器。

## KI-0020：go module 路径与仓库地址不一致会导致外部安装失败
- 现象：
  - `go.mod` 若使用短 module（如 `codex-acp`）而仓库地址为 `github.com/beyond5959/codex-acp`，外部使用仓库地址安装会报模块路径不匹配。
- 影响：
  - `go get` / `go install` 失败，第三方集成与 CI 拉取依赖不稳定。
- 复现：
  - 保持短 module 路径后执行：`go install github.com/beyond5959/codex-acp/cmd/codex-acp-go@latest`。
- Workaround：
  - 使用 canonical module：`module github.com/beyond5959/codex-acp`。
  - 变更后同步替换仓库内 `codex-acp/...` 导入路径。
- 后续计划：
  - 仓库地址若变更（迁移/重命名），同一 PR 内同步更新 `go.mod` 和全部内部导入。

## KI-0021：`session/update` 的标准 `update.sessionUpdate` 在低频事件上仍是回退语义
- 现象：
  - 适配器已支持“扁平字段 + 标准 envelope”双输出，并保证每条 `session/update` 都带 `update.sessionUpdate`。
  - 对非 message/tool 的低频更新，当前用 `agent_thought_chunk` 文本回退承载，尚未做细粒度类型映射。
- 影响：
  - 严格 ACP 客户端可稳定反序列化，但在 plan/thought/模式切换等低频更新上可能出现“语义被弱化”的展示差异。
- 复现：
  - 使用仅消费 `params.update.sessionUpdate` 的 ACP client，观察非 message/tool 的 session/update 呈现为通用 thought chunk。
- Workaround：
  - 客户端同时消费扁平字段（`type/status/delta/message/...`）与标准 envelope，以保留更多语义。
- 后续计划：
  - 扩展标准映射覆盖：plan、thought、mode/model update、permission/tool 生命周期等，减少通用回退路径。

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
  - 使用旧版 `codex-acp-go` 二进制连接严格 ACP 客户端（如 Zed）时，可能在连接阶段报错：`failed to deserialize response`。
- 影响：
  - 初始化握手失败，无法进入认证和会话创建流程。
- 复现：
  - 使用未包含本次修复的旧二进制启动 agent，并让客户端发 `initialize(protocolVersion=1)`。
- Workaround：
  - 重新构建并替换二进制：
    - `go build -o ./bin/codex-acp-go ./cmd/codex-acp-go`
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
