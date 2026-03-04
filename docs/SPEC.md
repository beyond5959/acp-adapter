# 基于 Codex App Server 的 Go 版 ACP 适配器（acp-adapter）：详细可落地技术方案（全功能）

> 目标：用 **Go** 实现一个 **ACP（Agent Client Protocol）Agent 进程**，在上游对接 **ACP-compatible 客户端（例如 Zed）**，在下游通过 **Codex App Server（`codex app-server`）** 驱动 Codex 的完整 IDE 级能力（认证/会话历史/审批/流式事件/Review/Slash commands/MCP 等）。  
> 说明：**App Server 与 ACP 是两套协议**，本项目本质是一个 **协议桥接（bridge）**。Codex App Server 是 Codex 富客户端（如 VS Code 扩展）使用的双向 JSON-RPC 协议，支持 stdio(JSONL) 与 websocket（实验），并提供 approvals、conversation history、streamed agent events 等能力。  
> ACP stdio 传输同样是 **newline-delimited JSON-RPC**：消息以 `\n` 分隔且不得内嵌换行，stdout 只能输出协议消息，日志走 stderr。

---

## 1. 范围与功能清单（必须全部实现）

能力清单：  
- Context **@-mentions**（文件/符号/选区等上下文注入）  
- **Images**（以 ACP content block image 形式传递）  
- **Tool calls**（含 permission requests）  
- **Following**（跟随/关联用户工作区与上下文变化：例如 cwd、选区、打开文件等）  
- **Edit review**（差异审阅流程）  
- **TODO lists**（结构化待办）  
- Slash commands：`/review`（可选指令）、`/review-branch`、`/review-commit`、`/init`、`/compact`、`/logout`  
- **Custom Prompts**  
- **Client MCP servers**（调用客户端已配置的 MCP servers）  
- Auth Methods：ChatGPT subscription、`CODEX_API_KEY`、`OPENAI_API_KEY`   

同时，必须满足 ACP 协议侧要求：  
- `initialize` 握手与 capability 协商  
- `session/new` / `session/prompt` / `session/update` / `session/cancel` 的完整 prompt-turn 生命周期（含工具进度更新、stopReason）  

---

## 2. 核心技术原理（两层协议 + 双向事件流）

### 2.1 上游：ACP Agent（stdio JSON-RPC）
- Zed 等 ACP 客户端以子进程启动适配器，走 stdio。  
- ACP stdio：每条 JSON-RPC 消息 `\n` 分隔、不得嵌入换行；适配器 stdout 只能写 ACP 消息，stderr 用于日志。  

### 2.2 下游：Codex App Server（stdio JSONL JSON-RPC）
- 启动 `codex app-server`（默认 stdio JSONL），并通过 JSON-RPC 2.0 交换消息（线上的 `"jsonrpc":"2.0"` 字段可省略）。  
- 生命周期：`initialize` → `initialized` → `thread/start|resume|fork` → `turn/start` → 读取持续 notifications（`item/*`、`turn/*`） → `turn/completed`。  
- 支持生成与版本匹配的 schema：`codex app-server generate-ts` / `generate-json-schema`。  
- WebSocket 模式有背压与 `-32001` overload 错误，需要指数退避+抖动重试（本方案默认只用 stdio，仍需保留可扩展点）。  

---

## 3. 总体架构

```
┌───────────────────────────────────────────────────────────────────┐
│ ACP Client (Zed / others)                                          │
│   JSON-RPC over stdio: initialize / session/* / fs/* / terminal/*   │
└───────────────▲───────────────────────────────────────────────────┘
                │ ACP stdio(JSON-RPC, newline-delimited)  
┌───────────────┴───────────────────────────────────────────────────┐
│ Go 适配器：acp-adapter                                             │
│  - ACP Server: handlers + session state + permission broker        │
│  - Bridge Core: 映射 ACP <-> App Server events & requests          │
│  - Client-side RPC: 调用 ACP Client 的 fs/terminal/mcp 工具         │
│  - Policy: approvals/sandbox/权限/审计/配置                          │
└───────────────▲───────────────────────────────────────────────────┘
                │ App Server stdio(JSONL)  
┌───────────────┴───────────────────────────────────────────────────┐
│ codex app-server 子进程                                             │
│  initialize / thread/* / turn/* / review/start / mcpServer/* ...    │
│  notifications: item/*, turn/*, approvals flow ...                   │
└───────────────────────────────────────────────────────────────────┘
```

---

## 4. 协议映射设计（ACP ⇄ App Server）

> 目标：让 ACP 客户端看到的行为与“原生 ACP agent”一致，同时最大化利用 App Server 的能力（thread/turn/item、review/start、approvals、mcp 等）。  

### 4.1 连接与初始化（双握手）

**(A) 下游 App Server 初始化（每次启动后必须）**  
1. spawn `codex app-server`（stdio）  
2. 发送 `initialize` request（包含 clientInfo/capabilities；可配置 optOutNotificationMethods 降低噪声）  
3. 发送 `initialized` notification   

**(B) 上游 ACP 初始化**  
- 实现 ACP `initialize`：返回 agentCapabilities（支持 image、tool calls、session config options、mcp forwarding 等）与 authMethods（见 6.1）。  
- 注意：ACP schema 中对 ContentBlock/工具更新/permission 的结构与字段严格对齐。  

### 4.2 Session / Thread 映射

| ACP | App Server | 说明 |
|---|---|---|
| `session/new` | `thread/start` | 返回 ACP sessionId；内部保存 `threadId`。App Server 的 thread 是对话容器。 |
| `session/new`（带 resume/branch） | `thread/resume` / `thread/fork` | 如果 ACP client 支持恢复/分叉语义，可映射；否则保留内部能力。 |
| session config（cwd/approval/sandbox/model/personality） | `thread/start`/`turn/start` params | App Server 支持在 thread/turn 里覆盖 model/cwd/sandbox/approvalPolicy/personality 等。 |

### 4.3 Prompt turn 映射（核心）

**ACP `session/prompt`**：  
- 解析 PromptRequest：用户消息 + content blocks（含 text/image/resources）+ @-mentions 的资源列表。  
- 转换为 App Server `turn/start`：  
  - `threadId`：来自 session state  
  - `input`：把用户文本与结构化上下文（mentions、resources、选区等）序列化成 Codex 侧可理解的“用户输入项”，并尽可能用 App Server 的 Item 原语保留结构化信息（见 5.2）。  

**流式输出（App Server notifications → ACP session/update）**：  
- `item/agentMessage/delta` → `session/update`（message chunk）  
- `item/started` / `item/completed` → `session/update`（状态、阶段、进度）  
- `turn/completed` → 结束本次 `session/prompt` 响应，填充 ACP StopReason（`end_turn` / `cancelled` / `error` 等）。  

**取消（ACP `session/cancel` → App Server `turn/interrupt`）**：  
- 客户端发 `session/cancel` 时：调用 `turn/interrupt(thread_id, turn_id)`，并将最终 stopReason 映射为 `cancelled`。  

---

## 5. 全功能能力实现方案

### 5.1 Context @-mentions（文件/符号/选区/资源）

ACP 的 prompts 支持 ContentBlock（text/image/resource 等）与附带资源，常见实现是把被 @ 的文件/片段作为 `resource` 或 `text` block 放入 prompt context。  

实现策略：
1. **在 ACP 侧解析 @-mentions**  
   - 不同客户端表示方式不同，但最终都会落为 prompt 的 `resources` / `content` blocks 或 meta 字段。
2. **将资源映射为 App Server 的输入 items**  
   - 对文件：优先使用 ACP client 工具（`fs/read_text_file` 或等价）读取，然后将内容以“引用块”注入 `turn/start` 的 input（包含 uri/path、range、hash）。  
   - 对符号/选区：同样通过 client 提供的引用信息（或让 client 提供选区文本）注入。  
3. **缓存与一致性**  
   - 为每个引用计算 content hash，避免重复读取；当 client 发“following”更新（见 5.4）时失效缓存。

验收要点：@ 单文件、@ 多文件、@ 大文件（截断策略）、@ 选区、@ 不存在文件时的错误提示。

---

### 5.2 Images（图片上下文）

ACP ContentBlock 支持 `image`（base64 data + mimeType + 可选 uri），要求 agent 支持 image prompt capability 才能接收。  

实现策略：
- 在 `session/prompt` 中收集 image blocks：  
  - 若 `data` 存在：按 mimeType 传下游；  
  - 若只有 `uri`：通过 client 工具读二进制（若 ACP client 提供），否则提示用户需要内联数据。  
- 转为 App Server input item：  
  - 使用 App Server 支持的“输入 item”类型（按生成的 JSON schema 进行序列化与校验，避免协议漂移）。  

验收要点：png/jpg/webp、大小限制、无效 mimeType、图文混合 prompt 的流式输出。

---

### 5.3 Tool calls（含 permission requests）

ACP 的 prompt-turn 文档明确：Agent 可在执行敏感工具前调用 `session/request_permission`，工具执行期间用 `session/update` 报告 `tool_call_update`（in_progress → completed/failed）。  

App Server 自带 approvals/审批机制，能在 IDE 客户端交互式批准/拒绝命令和文件修改。  

#### 统一策略：把“审批源”归一到 ACP permission 模型
因为上游是 ACP client（Zed），它只理解 ACP 的 permission 交互，而下游 App Server 可能会向“它的客户端”发起 approvals request。  
在桥接中，我们必须做到：  
- **所有需要用户确认的动作**，最终都以 **ACP `session/request_permission`** 呈现给上游（由上游 UI 决策）。  

实现方式（推荐）：
1. **配置 App Server 为需要 approvals 的策略**（例如 approvalPolicy=auto/on-request 等，具体值以 app-server schema 为准；通过 `thread/start`/`turn/start` params 传入）。  
2. **在下游收到 approval 相关的 server-initiated request/notification** 时：  
   - 转换为 ACP `session/request_permission`（包含命令、目标文件、网络访问、工作区外路径等风险信息）。  
3. **等待 ACP client 的 permission outcome**：  
   - 若允许：向 App Server 回应对应的 approval；  
   - 若拒绝：向 App Server 回绝并让 turn 继续（让模型选择替代方案）或中断；  
   - 若 ACP client cancel：按 ACP 要求返回 `Cancelled` outcome。  
4. **工具执行进度**：  
   - 把下游 item 的状态变更映射为 ACP `session/update` 的 `tool_call_update`（status: in_progress/completed/failed）。  

工具覆盖范围（必须支持）：
- shell/command 执行（含 stdout/stderr streaming）  
- 文件编辑/patch 应用  
- 网络访问（如 web search / fetch）  
- MCP tool 调用（见 5.10）  
- review/analysis 类内置工具（如 `review/start`）  

---

### 5.4 Following（跟随工作区状态）

目的：让 Codex 侧“知道你当前在看什么/在哪个目录/哪些文件被改了”，并保持上下文与编辑器同步；同时避免反复读文件。Zed 的外部 agent 是 ACP 驱动，适配器应利用 ACP 的 session config/updates 机制来追踪。  

实现策略：
- 维护 per-session 的 `WorkspaceState`：
  - cwd（当前项目根/工作目录）
  - active file URI
  - selections（range+文本摘要）
  - open files 列表与版本号/mtime
- 在 ACP `session/update`（来自 client 的更新）或 `session/prompt` meta 中捕获变化并同步到下游：  
  - 若 App Server 支持 `turn/steer`：在 turn 进行中插入状态变更；否则在下一 turn 的 `turn/start` params 里体现。  
- 缓存策略：文件内容缓存按 `(uri, version|mtime, range)`；变更时失效。

---

### 5.5 Edit review（差异审阅）

目标：支持“生成 patch → 让用户审阅 → 选择应用/回滚/继续改”体验。

实现策略：
1. **下游 file edit items**：App Server 会把文件编辑作为 items 流出来（例如 patch/change kind），并在 turn 完成前后可追踪。建议以 app-server schema 为准处理。  
2. **上游呈现为 ACP 工具更新 + 资源内容**：
   - 将 patch/diff 作为 ACP `ContentBlock.text`（Markdown diff）或 resource block，配合 tool_call_update 表示“已准备好审阅”。  
3. **应用 patch 的路径**（两种模式，均需实现并由配置选择）：
   - **Mode A（由 App Server 直接落盘）**：当用户批准后，让 App Server 执行写入。  
   - **Mode B（由 ACP client 落盘）**：把 diff 转为 `fs/write_text_file`/patch 工具调用请求，让客户端执行（更符合 ACP 的“client owns workspace”思想）。  
4. **审阅交互**：
   - 在 ACP 侧通过 permission request 或工具调用 UI 做“Apply/Reject/Apply部分”选择；
   - 结果回写到 App Server（继续 turn / 回滚 / fork 新 thread）。  

验收要点：多文件 patch、冲突处理（rebase/merge 冲突）、拒绝后模型继续给替代方案。

---

### 5.6 TODO lists（结构化待办）

目标：让 agent 输出的 TODO 可被客户端以列表形式渲染，并可逐项跟踪。

实现策略：
- 从下游 items 中识别 “todo/plan/checklist” 类型（具体以 app-server schema 或 agentMessage 中的结构标记）。  
- 若下游不提供显式 todo item：在 agentMessage 中解析 `- [ ]` 语法并结构化（但必须保留原文）。  
- 通过 ACP session/update 发送结构化内容（推荐：用 ContentBlock.text + meta 附带 todo 结构，客户端可选择渲染）。  

验收要点：todo 更新（完成/撤销）、跨 turn 保留、与 /compact 后的一致性。

---

### 5.7 Slash commands（/review、/review-branch、/review-commit、/init、/compact、/logout）

这些命令在 `acp-adapter` 中作为用户输入触发特殊行为。  

实现策略（统一命令路由）：
- 在 ACP `session/prompt` 中，如果用户 message 以 `/` 开头：解析为 command + args，并走专用 handler；否则走正常 `turn/start`。

#### /review（可选 instructions）
- 使用 App Server `review/start`，它会像 `turn/start` 一样流式输出 items，并包含 `enteredReviewMode`/`exitedReviewMode` 事件，最终给出 review 文本。  
- 将输出映射回 ACP session/update。

#### /review-branch、/review-commit
- 通过 ACP client 的 terminal 工具运行 `git diff`/`git show`/`git log` 等获取上下文，再调用 `review/start` 或普通 `turn/start` 让 Codex 输出 review（取决于 App Server 是否直接支持目标参数；如无则用“输入上下文 + review/start”实现）。  

#### /init
- 生成项目初始化建议（README、CI、lint 等），允许写入文件时必须走 permission。  

#### /compact
- 映射到 `thread/compact/start`（App Server 支持会话压缩/摘要），并将进度通过 items 转发。  

#### /logout
- 清理本适配器缓存的 auth 状态（见 6.1），必要时调用 App Server 的相关 auth endpoint（如果 schema 中暴露）。  

---

### 5.8 Custom Prompts（自定义提示模板）

目标：支持用户在客户端配置多个 prompt 模板（如 “安全审阅模式”、“只读问答模式”、“重构模式”），并在对话中选择或自动应用。

实现策略：
- 本适配器读取 `~/.config/acp-adapter/config.toml`（或 JSON）中的 prompt profiles：  
  - `name` / `developer_instructions` / `preferred_model` / `approvalPolicy` / `sandbox` / `personality`  
- profile 的应用点：
  - thread 级：`thread/start` params  
  - turn 级：`turn/start` params  
- 与 App Server 的 collaborationMode（若启用 experimental）兼容：可将 profile 映射到 collaboration mode preset（需要 schema 支持）。  

验收要点：热加载、profile 缺失/非法时回退默认、不同 profile 的权限差异确实生效。

---

### 5.9 Auth Methods（ChatGPT subscription / CODEX_API_KEY / OPENAI_API_KEY）

`acp-adapter` 支持三种认证方式。  

实现策略：
1. **环境变量优先级**：  
   - 若 `CODEX_API_KEY` 存在：优先使用  
   - 否则若 `OPENAI_API_KEY` 存在：使用  
   - 否则走“ChatGPT subscription 登录流程”（需要 Codex CLI/App Server 支持的登录交互）  
2. **与 App Server 对接**：  
   - 将 key 注入 `codex app-server` 子进程环境（spawn env）  
   - 如需 OAuth/login：通过 `mcpServer/oauth/login` 类流程示例可参考 App Server 的 oauth 方法（见 schema）。  
3. **/logout**：清理本地 token 缓存并提示用户重新登录。  
4. **远程项目限制**：ChatGPT subscription 在 remote projects 不工作（保持与 acp-adapter 行为一致，检测条件以客户端传入的 workspace 类型/路径为准）。  

验收要点：三种方式都能成功启动 thread/turn；缺失 auth 时给出明确错误；/logout 后确实重新走认证。

---

### 5.10 Client MCP servers（客户端 MCP 工具）

ACP 的 content/tool 结构与 MCP 兼容，且 `acp-adapter` 宣称支持“Client MCP servers”。  

实现策略：
- 在 ACP initialize 阶段读取 client capabilities：若 client 暴露 MCP tools 列表/调用方法，则注册为“可用工具集合”。  
- 当下游（Codex）提出需要调用某个 MCP tool：  
  1) 适配器将其映射成 ACP 工具调用请求（或直接调用 ACP client 的 MCP 工具接口）  
  2) 工具返回结果再封装成 App Server 的 tool result item（或作为 turn/steer 输入喂回）  
- 同时支持 App Server 自身对 MCP servers 的管理接口：如 `config/mcpServer/reload`、`mcpServerStatus/list`、`mcpServer/oauth/login` 等，适配器可以将其暴露为 slash command（如 `/mcp status`），并将输出转成 ACP message。  

验收要点：能列出工具、能调用、能处理 oauth 登录完成事件、失败能回传错误并不崩溃。

---

## 6. 运行时策略：审批、沙箱、网络与安全

### 6.1 approvals / sandbox 统一配置面

Codex（CLI/App Server）提供审批与沙箱机制；App Server 的 thread/turn 支持设置 `approvalPolicy`、`sandbox` 等参数。  

本适配器需要提供一个统一配置层：
- 默认：安全模式（工作区写入受限、网络默认关、危险操作需要 permission）  
- 可选：只读模式  
- 可选：全自动模式（仍建议保留审计与安全开关）

**强制要求**：即使下游可配置“永不审批”，上游 ACP 仍必须能显示风险提示与审计日志；生产默认不得开启危险模式。

### 6.2 审计与可观测性
- stderr 结构化日志：JSON 格式，包含 sessionId/threadId/turnId、method、latency、result code。  
- App Server 支持 `LOG_FORMAT=json` 且日志走 stderr；适配器需将其与自身日志区分并可开关。  
- 记录每次 permission decision、每次文件写入/命令执行摘要（可选落盘到 `~/.cache/acp-adapter/audit/`）。

---

## 7. Go 工程实现细节

### 7.1 目录结构建议

```
acp-adapter/
  cmd/
    acp-adapter/              # main: ACP stdio server
  internal/
    acp/
      codec_stdio.go           # newline-delimited JSON-RPC codec 
      server.go                # method router: initialize/session/*
      types/                   # ACP schema models (生成+手写补丁) 
      client_rpc.go            # 调用 ACP Client 提供的 fs/terminal/request_permission
    appserver/
      process.go               # spawn codex app-server, env/auth
      codec_jsonl.go           # JSONL read/write 
      client.go                # call initialize/thread/turn/review/...
      types/                   # 通过 generate-json-schema 生成的 models
      approvals_bridge.go      # server->client approvals 转 ACP permission
      events.go                # notifications fanout
    bridge/
      mapper.go                # ACP<->AppServer 映射
      session_state.go
      following.go
      mentions.go
      images.go
      tools.go
      review.go
      slashcmd.go
    config/
      config.go                # profiles/prompt/sandbox/approval defaults
    observability/
      logger.go
      metrics.go               # 可选：prometheus/statsd
  test/
    integration/               # 端到端协议回归
```

### 7.2 协议模型生成策略（避免手写漂移）
- App Server：运行 `codex app-server generate-json-schema --out ./schemas`，再用 jsonschema→Go 的生成工具生成 types（每次升级 codex 版本即重新生成）。  
- ACP：基于 ACP 官方 schema（网页/仓库）生成 Go structs；关键 union（ContentBlock/Update）需手工封装以保持兼容。  

### 7.3 并发与背压
- 每个 ACP session 同时最多 1 个 active turn（与大多数 client 期待一致），并支持 `session/cancel` 中断。  
- App Server notifications 读取单 goroutine → 解码 → 分发到 turn-specific channel。  
- 若 websocket 模式将来启用：处理 `-32001` overload 的重试策略（指数退避+抖动）。  

### 7.4 严格 stdout/stderr 分离
- ACP 侧 stdout **只能**写 ACP JSON-RPC；  
- App Server 子进程 stderr（含其日志）与本适配器日志都写到本进程 stderr。  

---

## 8. 端到端流程（关键序列）

### 8.1 启动 & 初始化
1. ACP client 启动 `acp-adapter`（stdio）  
2. `acp-adapter` spawn `codex app-server`（stdio JSONL）  
3. `acp-adapter` → app-server: `initialize` + `initialized`   
4. ACP client → `acp-adapter`: `initialize`（返回 capabilities & auth methods）  

### 8.2 新建会话
1. ACP client → `session/new`  
2. adapter → app-server: `thread/start`  
3. adapter → ACP client: sessionId（内部映射 threadId）  

### 8.3 发起 turn（含 approvals）
1. ACP client → `session/prompt`（含文本、@mentions、images）  
2. adapter 读取所需文件/选区（通过 ACP client fs 工具）  
3. adapter → app-server: `turn/start`  
4. app-server → adapter: `turn/started`、`item/*`、`item/agentMessage/delta`…  
5. adapter → ACP client: `session/update` 流式转发  
6. 如遇审批：adapter → ACP client `session/request_permission` → outcome → adapter 回 app-server  
7. app-server → `turn/completed`  
8. adapter 完成 `session/prompt` response（stopReason 映射）  

---

## 9. 验收标准（Definition of Done）

> 验收以“可在 Zed External Agents 中稳定使用”为主，同时保证对其它 ACP client 不做 Zed 特化假设。Zed 支持通过 ACP 使用外部 agents。  

### 9.1 协议合规
- [ ] ACP stdio framing 完全符合：每条 JSON-RPC 以 `\n` 分隔、stdout 无杂音、日志只写 stderr。  
- [ ] ACP `initialize` / `session/new` / `session/prompt` / `session/update` / `session/cancel` 全流程通过协议测试。  
- [ ] App Server `initialize`→`initialized`→`thread/start|resume|fork`→`turn/start`→notifications→`turn/completed` 流程稳定。  

### 9.2 功能验收（与 acp-adapter 对齐）
- [ ] Context @-mentions：@文件、@多文件、@选区、@符号（若 client 支持）均能被 Codex 正确引用并在回答中体现。  
- [ ] Images：ACP image blocks 能正确传递到 Codex，并产生与图片相关的有效输出。  
- [ ] Tool calls + permission：  
  - 对命令执行、文件写入、网络访问、MCP 工具调用等敏感动作，必须先弹出 permission；  
  - permission 允许/拒绝/取消三种路径都正确；  
  - 工具执行期间 `session/update` 能持续反映 in_progress→completed/failed。  
- [ ] Following：切换 cwd/active file/selection 后，下一次 turn 能感知并利用这些上下文；文件缓存正确失效。  
- [ ] Edit review：  
  - 生成多文件 patch 后能在 ACP UI 中看到 diff；  
  - Apply/Reject/Partial apply 能工作；  
  - 冲突时能给出可操作的提示与恢复方案。  
- [ ] TODO lists：能输出结构化 todo（列表渲染或可解析），跨 turn 保留与更新一致。  
- [ ] Slash commands：  
  - `/review` 触发 App Server `review/start`，并能看到 entered/exited review mode 相关事件与最终 review 文本；  
  - `/review-branch`、`/review-commit` 可在 git 仓库中正常工作；  
  - `/init` 能生成初始化文件并遵守权限；  
  - `/compact` 触发 `thread/compact/start` 并完成；  
  - `/logout` 能清理认证并要求重新认证。  
- [ ] Custom Prompts：可配置多个 profile，并在新 thread/turn 中正确生效（model/approval/sandbox/personality/instructions）。  
- [ ] Client MCP servers：可列出与调用客户端 MCP tools；支持 oauth 登录完成事件与失败路径。  
- [ ] Auth methods：ChatGPT subscription、`CODEX_API_KEY`、`OPENAI_API_KEY` 三种方式均可完成对话；无认证时提示明确。  

### 9.3 稳定性与可观测性
- [ ] 连续 100 次 prompt-turn（包含工具与审批）无崩溃、无死锁；`session/cancel` 在 1s 内生效（以 UI 取消动作起算）。  
- [ ] stderr 日志包含 sessionId/threadId/turnId，可用于定位问题；stdout 永远是合法 ACP 消息。  
- [ ] 支持“协议录制”调试：可选开启将双向 JSON-RPC 原始流落盘（脱敏）。  

---

## 10. 交付物

- `acp-adapter` 可执行文件（macOS/Linux/Windows）  
- 配置文档：认证、profiles、审批/沙箱策略  
- 协议回归测试：ACP 端（mock client）+ App Server 端（mock server）+ 端到端  
- 验收用例脚本（覆盖第 9 节的所有条目）

---

## 参考资料（权威来源优先）
- Codex App Server 官方文档（协议/传输/示例/背压/生成 schema）  
- App Server 开源 README（thread/turn/item、review/start、API 列表、initialize 规则、opt-out 通知）  
- ACP 协议：Transports（stdio newline-delimited JSON）  
- ACP 协议：Prompt Turn（permission、tool_call_update、stopReason 等）  
- ACP schema（ContentBlock 等核心类型）    
- Zed External Agents（ACP 在 Zed 的集成入口）  
