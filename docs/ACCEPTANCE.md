# ACCEPTANCE.md

> 目标：本清单用于“逐条可验证”的验收。实现必须覆盖所有条目（全功能，不是 MVP）。

## A. 协议合规（ACP）
A1. **stdio 合规**
- 操作：启动适配器，向 stdin 发送 ACP JSON-RPC。  
- 预期：stdout 仅出现合法 JSON-RPC 行；stderr 可有日志；任何非 JSON-RPC 输出都视为失败。

A2. **initialize**
- 操作：发 `initialize`。  
- 预期：返回 agentCapabilities（至少声明支持：images、tool calls、slash commands、permissions、sessions）。

A3. **session/new**
- 操作：发 `session/new`。  
- 预期：返回 sessionId；内部创建 thread。

A4. **session/prompt（流式）**
- 操作：发 `session/prompt`（简单文本）。  
- 预期：收到 >=1 条 `session/update`（message chunk 或 status）；最终 `session/prompt` response stopReason=end_turn。

A5. **session/cancel**
- 操作：prompt 进行中调用 `session/cancel`。  
- 预期：很快停止后续输出，最终 stopReason=cancelled；无崩溃、无僵尸子进程。

## B. App Server 对接
B1. **App Server 初始化**
- 操作：适配器启动后应自动启动 `codex app-server` 并完成 initialize/initialized。  
- 预期：能创建 thread 并启动 turn；若 app-server 崩溃，适配器给出明确错误并可重启。

B2. **Schema 锁定**
- 操作：执行 `make schema`（或同等命令）。  
- 预期：产物写入 `internal/appserver/schema/`；CI 检查 schema 与 codex 版本一致（至少校验文件存在+hash 变更可追踪）。

## C. 内容能力
C1. **@-mentions（文件引用）**
- 操作：在 prompt 中包含一个文件引用（由 client 以 resource/content block 或 meta 提供）。  
- 预期：Codex 回复中正确引用该内容；适配器不丢 uri/mimeType/range；大文件有截断策略并在输出中说明。

C2. **Images**
- 操作：发送包含 1 张图片的 prompt（base64）。  
- 预期：Codex 回复能基于图片内容；过程稳定、无解码错误。

## D. 工具、审批与安全
D1. **命令执行审批**
- 操作：触发需要执行命令的任务。  
- 预期：在执行前弹出 permission；accept 执行并把 stdout/stderr 流式展示；decline 不执行并给出替代建议。

D2. **文件改动审批**
- 操作：触发写文件/应用 patch。  
- 预期：写入前 permission；accept 后文件内容与 diff 一致；decline 不落盘；取消不落盘。

D3. **网络审批**
- 操作：触发需要网络访问的动作。  
- 预期：permission 展示 host/protocol/port（如可得）；accept 后继续；decline 后该动作失败且可见。

D4. **MCP tool-call（带副作用）审批**
- 操作：触发 MCP side-effect tool。  
- 预期：必须 permission；拒绝后 tool call 显示 failed/declined，并且 turn 能继续或优雅结束。

D5. **默认安全策略**
- 操作：使用默认配置。  
- 预期：未获 permission 前，绝不执行写盘/命令/网络/副作用工具。

## E. Edit review
E1. **Review 模式输出**
- 操作：触发 `/review` 或生成 patch。  
- 预期：能看到 entered/exited review mode（或等价状态）；review 内容可读且包含可操作建议。

E2. **Patch 落盘两模式**
- 操作：分别启用（1）AppServer 落盘（2）ACP fs 落盘。  
- 预期：两模式都能通过 D2 的落盘验收。

## F. TODO lists
F1. **结构化 TODO 输出**
- 操作：让 agent 生成 TODO。  
- 预期：输出中包含可解析的 TODO（至少 markdown checklist）；跨 turn 可延续并更新。

## G. Slash commands（必须全部支持）
G1. `/review [instructions]`
G2. `/review-branch <branch>`
G3. `/review-commit <sha>`
G4. `/init`
G5. `/compact`
G6. `/logout`

每条命令：
- 操作：输入命令并观察输出/状态。  
- 预期：按 SPEC 描述行为执行；涉及写盘/命令/网络必须走 permission。

## H. Custom Prompts
H1. **profiles 生效**
- 操作：配置 >=2 个 profile（不同 model/approval/sandbox/personality）。  
- 预期：切换 profile 后行为明显不同（至少 model 或 approvalPolicy 变化可观察）。

## I. Auth methods
I1. `CODEX_API_KEY`
I2. `OPENAI_API_KEY`
I3. ChatGPT subscription 登录态（如环境支持）

- 预期：三者均可启动 thread/turn；缺失认证提示明确；`/logout` 后需重新认证。

## J. 可靠性
J1. **压力回归**
- 操作：跑 100 次 turn（包含 approve/deny/cancel）。  
- 预期：无崩溃、无 goroutine 泄漏（至少无明显持续增长）、无僵尸子进程。

J2. **stdout 纯净**
- 操作：打开协议抓取（trace）并运行。  
- 预期：stdout 仅协议 JSON；trace 文件脱敏。
