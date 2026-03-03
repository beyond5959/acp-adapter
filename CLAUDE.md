# CLAUDE.md

本文件是 Claude Code 在此仓库工作的行为规范。所有约束优先于默认行为。

## 0. 最高优先级约束（MUST）

1. **stdout 严格纯净**
   - 适配器 stdout 只能输出 ACP JSON-RPC 消息。任何日志/调试输出必须写 stderr。
   - 不得在任何代码路径中把非 ACP 消息写入 stdout。

2. **下游必须走 Codex App Server**
   - 不得直接解析 `codex` CLI 文本输出。必须通过子进程 `codex app-server`（stdio JSONL）通信。

3. **Schema 驱动**
   - `internal/appserver/schema/` 由 `make schema` 生成，不得手写并提交。
   - 每次改动涉及 App Server 协议字段时，必须重新运行 `make schema`。

4. **安全 fail-closed**
   - 未通过 `session/request_permission` 的情况下，禁止执行：写盘、命令执行、网络访问、MCP 副作用工具。

5. **并发约束**
   - 每个 session 同时最多 1 个 active turn。
   - `session/cancel` 必须快速生效且不泄漏 goroutine。

6. **测试门槛**
   - 每次代码改动后必须确保 `go test ./...` 通过，不得跳过。

## 1. 开工前必读

在开始任何实现或修改前，必须先阅读以下文件（以这些文件为准，不依赖上下文记忆）：

- `PROGRESS.md`：当前进度、已完成/未完成验收条目、下一步计划
- `docs/DECISIONS.md`：关键技术决策与取舍（为什么这么做）
- `docs/KNOWN_ISSUES.md`：已知问题/坑位/规避方式
- `docs/SPEC.md`：完整技术方案（权威来源）
- `docs/ACCEPTANCE.md`：逐条验收清单

## 2. 收尾时必更

完成一个功能块后，必须更新：
1. `PROGRESS.md`：勾选已完成的验收条目，记录下一步
2. `docs/DECISIONS.md`：记录新增/改变的关键决策（ADR 格式，含替代方案与理由）
3. `docs/KNOWN_ISSUES.md`：记录新发现的坑、限制、复现/规避步骤

## 3. 工程规范

- **Go 版本**：Go 1.24+
- **Module 路径**：`github.com/beyond5959/acp-adapter`（所有内部导入必须用此前缀）
- **目录分层**：`internal/acp`、`internal/appserver`、`internal/bridge`、`internal/config`、`internal/observability`，不得随意新增顶层目录
- **错误处理**：优先显式处理；不得吞错；对外错误必须带上下文（sessionId/threadId/turnId）
- **日志**：结构化 JSON 写 stderr；不得用 `fmt.Println` 等打到 stdout

## 4. 禁止事项（MUST NOT）

- 不得把任何日志/调试文本写到 stdout
- 不得引入 LSP 的 Content-Length framing
- 不得在未通过 permission 的情况下执行写盘/命令/网络/副作用工具
- 不得修改 `go.mod` 的 module 路径
- 不得绕过 `go test ./...`（除非用户明确要求）

## 5. 运行与调试

```bash
# 常规测试
go test ./...

# 真实 app-server e2e（需本机有 codex 及认证）
E2E_REAL_CODEX=1 go test ./... -run TestE2EReal -count=1

# 压力回归（100 turns）
make stress-j1

# Schema 生成与校验
make schema

# 构建二进制
go build -o ./bin/acp-adapter ./cmd/acp-adapter
```

## 6. 关键架构速查

```
ACP Client (Zed/etc)
       ↕  ACP stdio newline-delimited JSON-RPC
  acp-adapter  ← cmd/ 入口委托 pkg/acpadapter
       ↕  App Server stdio JSONL JSON-RPC
  codex app-server (子进程)
```

- `pkg/acpadapter`：独立模式（`RunStdio`）+ 嵌入模式（`EmbeddedRuntime`）
- `internal/acp`：ACP server、传输层抽象（stdio / inproc）
- `internal/appserver`：App Server 子进程生命周期、supervisor、client
- `internal/bridge`：ACP ↔ App Server 协议映射、session 状态机
- `test/integration/`：端到端回归（fake harness + real codex 开关）
- `testdata/fake_codex_app_server/`：测试替身（不得用于生产）

## 7. 当前状态（2026-02-28）

- PR1–PR5 全部完成，验收清单 A1–J2 全部通过
- Library Embedding Program：R0–R4 完成，**R5 进行中**，R6 待开始
- 无阻塞项；已知风险见 `docs/KNOWN_ISSUES.md`
