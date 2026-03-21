# AGENTS.md

本仓库用于实现 **Go 版 ACP 适配器**。当前支持两类下游后端：
- **Codex**：基于 Codex App Server
- **Claude**：基于 Claude Code CLI 子进程

请在任何改动前先阅读 `docs/SPEC.md` 与 `docs/ACCEPTANCE.md`。

官方文档入口：
- ACP（Agent Client Protocol）：https://agentclientprotocol.com/
- Codex App Server：https://developers.openai.com/codex/app-server
- Claude Code 约束与仓库内说明：`CLAUDE.md`

## 0. 最高优先级约束（MUST）
1. **ACP stdio 严格合规**  
   - 适配器通过 **stdio newline-delimited JSON-RPC** 与 ACP Client 通信。  
   - **stdout 只能输出 ACP 协议 JSON-RPC 消息**；任何日志、调试输出必须写入 **stderr**。  
   - 每条消息以 `\n` 分隔，消息体不得包含未转义的换行。
2. **下游必须按后端走受支持的官方通道（不得解析交互式文本输出）**  
   - **Codex 后端**：必须以子进程方式启动 `codex app-server`，通过 stdio JSONL/JSON-RPC 与之通信；不得直接解析 Codex CLI 文本输出。  
   - **Claude 后端**：必须通过 Claude Code CLI 子进程的 machine-readable 输出链路（当前实现为 `claude -p ... --output-format stream-json`）集成；不得抓取/解析交互式 TUI 文本。
3. **版本锁定与协议形态驱动**  
   - **Codex 后端**：必须提供脚本/Makefile 目标：用 `codex app-server generate-json-schema --out internal/codex/schema` 生成并提交 schema 产物。  
   - **Codex 后端**：Go 类型优先通过 schema 生成（或至少进行 runtime 校验），避免手写漂移。  
   - **Claude 后端**：涉及 CLI `stream-json` 事件形态的改动，必须补充或更新测试，防止协议漂移。
4. **可验证、可回归**  
   - 每个 PR 必须保持 `go test ./...` 通过。  
   - 新增能力必须新增或更新测试/集成用例，或在 `docs/ACCEPTANCE.md` 中记录验证步骤（必须可执行）。

## 1. 工程规范
- Go 版本：**Go 1.24+**（如需调整，请同步更新 CI）。
- 目录结构需保持清晰分层：`internal/acp`、`internal/codex`、`internal/claude`、`internal/bridge`、`internal/config`、`internal/observability`、`pkg/codexacp`、`pkg/claudeacp`。
- 代码风格：优先显式错误处理；避免“吞错”；对外错误应带上下文（sessionId/threadId/turnId）。
- 并发：每个 session 同时只允许 1 个 active turn；`session/cancel` 必须快速生效且不泄漏 goroutine。

## 2. 运行与调试
- 默认日志格式：结构化 JSON 到 stderr（可配置 log level）。
- 提供 `--trace-json` 开关：可选把 ACP 与下游后端（Codex App Server / Claude CLI）的原始 JSON 流（脱敏）落盘用于调试。

## 3. 当前阶段与实现脉络
- **Codex 主线**：PR1-PR5 已完成，核心能力基于 Codex App Server。
- **Claude 主线**：C-R0-C-R5 已完成，核心能力基于 Claude Code CLI 子进程。
- **Library Mode**：R5 进行中，R6 待完成。
- 任何新增能力或修复，都必须先确认其影响范围是 `codex`、`claude`，还是双后端共享链路，并同步检查 `docs/ACCEPTANCE.md` 对应条目。

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
- CLAUDE.md：Claude Code 路径的额外行为规范与调试约束（涉及 Claude 后端时必读）

### 必更（收尾时）
- 当你完成一个 PR（或一个可独立验收的功能块）时：
  1) 更新 PROGRESS.md：勾选已完成的 ACCEPTANCE 条目，记录下一步
  2) 更新 docs/DECISIONS.md：记录新增/改变的关键决策（含替代方案与理由）
  3) 更新 docs/KNOWN_ISSUES.md：记录新发现的坑、限制、以及复现/规避步骤

### 何时必须“重新同步”
- 如果你不确定某个结论、接口、验收状态、或觉得上下文可能丢失：
  - 先重新阅读 PROGRESS.md / docs/DECISIONS.md / docs/KNOWN_ISSUES.md
  - 以这些文件为准，再继续实现
