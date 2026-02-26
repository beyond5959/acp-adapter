# DECISIONS.md

> 记录“关键决策与取舍”，用于防止上下文丢失导致反复争论/返工。  
> 规则：任何影响架构、协议、默认安全策略、接口形状、或与客户端兼容性的改变都必须记录。

## 决策索引（建议从这里开始）
- ADR-0001：stdout/stderr 分离（ACP stdio 合规）
- ADR-0002：下游采用 Codex App Server（stdio JSONL），不解析 CLI 文本
- ADR-0003：Schema 锁定策略（generate-json-schema + 版本钉死）
- ADR-0004：turn 并发策略（每 session 同时 1 个 active turn）
- ADR-0005：审批桥（App Server approvals -> ACP session/request_permission）
- ADR-0006：patch 落盘两模式（AppServer 落盘 / ACP fs 落盘）
- ADR-0007：终端/PTY 策略（默认安全、避免交互死锁）
- ADR-0008：Slash commands 处理策略（命令路由优先于普通 prompt）
- ADR-0009：长期记忆外置（PROGRESS/DECISIONS/KNOWN_ISSUES）

---

## ADR 模板（复制一份填写）
### ADR-000X：<标题>
- 日期：YYYY-MM-DD
- 状态：Proposed / Accepted / Superseded
- 背景：
- 决策：
- 备选方案：
- 取舍（Pros/Cons）：
- 影响范围（文件/模块）：
- 验证方式（测试/验收项）：
