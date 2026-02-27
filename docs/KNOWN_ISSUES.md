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
  - 在 PR3/PR4 评估“自动重放当次请求”的安全边界与幂等性要求。

## KI-0009：真实 App Server 与 fake server 事件形态可能不完全一致
- 现象：当前 e2e 主要依赖 fake app-server，事件字段形态由测试替身控制。
- 影响：
  - 在真实 codex app-server 新字段/兼容字段出现时，可能出现映射遗漏。
- Workaround：
  - 通过 `appserver/client` 对未知 notifications 保持忽略且不崩溃。
  - 在真实环境补充集成回归并同步 schema。
- 后续计划：
  - PR3 开始增加真实 app-server 的回归脚本与录制样例（脱敏）。
