# AGENTS.md

本仓库用于实现 **Go 版 Codex ACP 适配器（基于 Codex App Server）**。请在任何改动前先阅读 `docs/SPEC.md` 与 `docs/ACCEPTANCE.md`。

## 0. 最高优先级约束（MUST）
1. **ACP stdio 严格合规**  
   - 适配器通过 **stdio newline-delimited JSON-RPC** 与 ACP Client 通信。  
   - **stdout 只能输出 ACP 协议 JSON-RPC 消息**；任何日志、调试输出必须写入 **stderr**。  
   - 每条消息以 `\n` 分隔，消息体不得包含未转义的换行。
2. **下游必须使用 Codex App Server（非直接解析 Codex CLI 文本输出）**  
   - 默认以子进程方式启动：`codex app-server`，通过 stdio JSONL/JSON-RPC 与之通信。
3. **版本锁定与 Schema 驱动**  
   - 必须提供脚本/Makefile 目标：用 `codex app-server generate-json-schema --out internal/appserver/schema` 生成并提交 schema 产物。  
   - Go 类型优先通过 schema 生成（或至少进行 runtime 校验），避免手写漂移。
4. **可验证、可回归**  
   - 每个 PR 必须保持 `go test ./...` 通过。  
   - 新增能力必须新增或更新测试/集成用例，或在 `docs/ACCEPTANCE.md` 中记录验证步骤（必须可执行）。

## 1. 工程规范
- Go 版本：**Go 1.24+**（如需调整，请同步更新 CI）。
- 目录结构需保持清晰分层：`internal/acp`、`internal/appserver`、`internal/bridge`、`internal/config`、`internal/observability`。
- 代码风格：优先显式错误处理；避免“吞错”；对外错误应带上下文（sessionId/threadId/turnId）。
- 并发：每个 session 同时只允许 1 个 active turn；`session/cancel` 必须快速生效且不泄漏 goroutine。

## 2. 运行与调试
- 默认日志格式：结构化 JSON 到 stderr（可配置 log level）。
- 提供 `--trace-json` 开关：可选把 ACP 与 App Server 的原始 JSON 流（脱敏）落盘用于调试。

## 3. 实施顺序（强制按阶段）
- **PR1**：可运行的协议骨架 + 端到端测试 harness（initialize/new/prompt/cancel）。
- **PR2**：App Server notifications → ACP `session/update` 的完整流式映射 + cancel 完整语义。
- **PR3**：Approvals → ACP permission（command/file/network/mcp），含 UI 友好展示与状态更新。
- **PR4**：Edit review + patch 落盘（AppServer 落盘与 ACP fs 落盘两种模式）。
- **PR5**：Slash commands、Custom prompts、MCP servers、Auth methods（全功能收尾）。

## 4. 禁止事项（MUST NOT）
- 不得把日志/调试文本写到 stdout。
- 不得引入 LSP 的 Content-Length framing（本项目不是 LSP）。
- 不得在未通过 permission 的情况下执行写盘/命令/网络/有副作用的工具调用（默认策略）。

## Memory & project docs（长期记忆与项目状态）

本项目的“长期记忆”不依赖聊天上下文，而以仓库内文档为准。你在开始实现/修改代码前必须阅读这些文件，并在完成阶段性工作后更新它们：

### 必读（开工前）
- PROGRESS.md：当前进度、已完成/未完成验收条目、下一步计划
- docs/DECISIONS.md：关键技术决策与取舍（为什么这么做）
- docs/KNOWN_ISSUES.md：已知问题/坑位/规避方式（含临时 workaround）

### 必更（收尾时）
- 当你完成一个 PR（或一个可独立验收的功能块）时：
  1) 更新 PROGRESS.md：勾选已完成的 ACCEPTANCE 条目，记录下一步
  2) 更新 docs/DECISIONS.md：记录新增/改变的关键决策（含替代方案与理由）
  3) 更新 docs/KNOWN_ISSUES.md：记录新发现的坑、限制、以及复现/规避步骤

### 何时必须“重新同步”
- 如果你不确定某个结论、接口、验收状态、或觉得上下文可能丢失：
  - 先重新阅读 PROGRESS.md / docs/DECISIONS.md / docs/KNOWN_ISSUES.md
  - 以这些文件为准，再继续实现
