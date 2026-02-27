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
- 现象：app-server 异常退出时，进行中的 turn 会以错误结束。
- 影响：
  - 当次请求会失败（返回可读错误）。
  - 后续请求可在 supervisor 重建后恢复。
- 复现：
  - 设置 `FAKE_APP_SERVER_CRASH_ON_THREAD_START_ONCE_FILE` 并触发 `session/new`。
- Workaround：
  - 客户端在收到 `thread/start` 或 `turn/start` 失败后重试一次请求。
- 后续计划：
  - 在 PR4/PR5 评估“自动重放当次请求”的安全边界与幂等性要求。

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
  - 满足“logout 后需重新认证”，但当前只能通过外部重新配置环境变量或重启进程恢复。
- 复现：
  - 先正常对话，再发送 `/logout`，随后发送任意 prompt。
- Workaround：
  - 重新设置认证环境（`CODEX_API_KEY`/`OPENAI_API_KEY`/subscription）并重启 adapter。
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
- 现象：当前实现依赖 `thread/compact/start`、`mcpServer/*`、`auth/logout` 方法名。
- 影响：
  - 若真实 app-server 不同版本方法名/参数变更，PR5 相关能力会出现 `method not found` 或参数不兼容。
- 复现：
  - 连接不支持上述 endpoint 的 app-server 版本执行相应 slash 命令。
- Workaround：
  - 通过兼容错误处理回退（例如 `auth/logout` 不支持时仅清理本地状态），并优先使用对齐版本联调。
- 后续计划：
  - 在 B2 schema 锁定后引入 endpoint capability 检测与版本门控。

## KI-0016：真实 codex e2e 依赖本机 codex 命令与认证态
- 现象：`E2E_REAL_CODEX=1` 时测试会执行 `make schema` 并启动真实 `codex app-server`。
- 影响：
  - 若本机未安装 `codex` 或认证不可用，真实 e2e 会跳过（带原因），不会覆盖真实链路。
- 复现：
  - 执行 `E2E_REAL_CODEX=1 go test ./... -run TestE2E -count=1`，且环境缺少 codex/auth。
  - 例如 `TestE2ERealCodexInitializePromptAndCancel` / `TestE2ERealCodexPromptInteractions` 会因 `thread/start failed` skip。
- Workaround：
  - 安装并确保 `codex app-server` 可运行；准备可用认证态（API key 或 subscription）以让 real e2e 实际执行。
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
