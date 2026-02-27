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
