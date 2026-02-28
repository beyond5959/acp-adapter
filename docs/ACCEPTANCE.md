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

## K. Library Mode（初版）
K1. **双入口可启动（cmd + pkg）**
- 操作：分别通过 `cmd/codex-acp-go` 与库入口创建服务实例并完成 initialize/new/prompt 基线流程。  
- 预期：两种入口都可独立跑通，且不要求修改 ACP client 侧协议调用方式。

K2. **R1 零行为变化**
- 操作：对同一组请求回放，比较 R1 前后输出（`initialize` 字段、`session/update` 事件序、`stopReason`）。  
- 预期：协议可观察行为一致；若有差异必须在变更说明中声明并给出迁移理由。

K3. **传输层抽象可替换（R2）**
- 操作：在测试中注入 mock 传输实现（替代 stdio）并运行核心 turn 流程。  
- 预期：核心状态机不依赖具体 IO 实现，mock 与 stdio 通过同一契约测试。

K4. **嵌入 API 生命周期（R3）**
- 操作：以库模式执行 start/prompt/cancel/shutdown，并模拟宿主 permission 回调。  
- 预期：生命周期清晰、可回收、无 goroutine 泄漏；cancel 语义与独立模式一致。

K5. **独立模式与库模式契约对照（R4）**
- 操作：同一 e2e 用例双跑（standalone vs embedded），比对关键协议输出。  
- 预期：A1-A5、D1-D5、E1-E2 的关键行为一致；差异项需有白名单与说明。
- 对照最小覆盖（必须）：
  - initialize：`protocolVersion` 与关键 capabilities 字段完整且两模式一致。
  - session/new + session/prompt：同脚本下两模式都产出流式 chunk，关键事件序列与 `stopReason` 一致（允许非关键字段差异）。
  - session/cancel：两模式都收敛到 `stopReason=cancelled`。
  - permission：approve / decline 两路径都双跑并保持关键行为一致。
- 不变量（必须）：
  - standalone：stdout 持续满足“仅协议 JSON-RPC”约束。
  - embedded：无阻塞/无死锁，且并发多 session 不发生跨 session 串扰。

K6. **server 集成（R5）**
- 操作：`cmd/codex-acp-go` 改为调用库入口后执行 `go test ./...` 与现有集成测试。  
- 预期：既有 PR1-PR5 能力不回退，CLI 对用户的参数与运行方式保持兼容。

K7. **收尾验收（R6）**
- 操作：按 K1-K6 全量回归并更新文档（PROGRESS/DECISIONS/KNOWN_ISSUES）。  
- 预期：库化改造完成闭环，阻塞项清零或附可执行 workaround。
