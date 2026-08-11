# design: agent-footer-indicator

## Context

动机见 proposal.md。关键事实：

- `composeRichStatusFooter`（engine.go:7126）组装富卡片底部文本：Line 1 耗时、Line 2 model·effort·token（`buildClaudeStatusLineFooter` 或 `replyFooterUsageText` 回退）、Line 3 workdir。
- `replyFooterModel`（engine.go:7295）用类型断言模式取值：先断言 `session.(interface{ GetModel() string })`，再断言 `agent.(interface{ GetModel() string })`。
- `AgentSwitcher.GetAgent() string` 已在 `agent-switch-command` 变更实现，opencode 的 `Agent` 支持。
- footer 是纯文本通道（statusFooter 字符串 → note 元素），不支持按钮（切换沿用 `/agent` 命令卡片）。

## Goals / Non-Goals

Goals:
- footer Line 2 显示 `agent · model`
- 兼容所有 agent 类型（无 AgentSwitcher 时静默省略）

Non-Goals:
- 不在 footer 提供切换交互（按钮需要新通道，属于未来工作）
- 不做思考强度（variant）显示：cc-connect 无 variant 状态（不传 `--variant`）、无 CLI 枚举入口、当前默认模型 deepseek 无变体；做了恒为空，记为已知限制
- 不改动非富卡片（纯文本消息）路径

## Decisions

### D1: 复用类型断言模式，新增 `replyFooterAgent`

照抄 `replyFooterModel` 结构：先 `session.(interface{ GetAgent() string })`（opencodeSession 当前无此方法，会话层空转），再 `agent.(AgentSwitcher)` 取 `GetAgent()`。无能力或空值返回 `""`。

备选：直接在 `AgentSwitcher` 断言——等同，但保留 session 优先层为将来 per-session 覆盖留位（与 `replyFooterModel` 对称）。

### D2: 拼接位置为 Line 2 model 前，带 ` · ` 分隔

`buildClaudeStatusLineFooter` 已接收 model/effort/usage 参数并拼 `model · effort · out ...`。agent 作为 model 前缀传入（`build · deepseek/deepseek-v4-pro · ...`）。实现方式：在 `composeRichStatusFooter` 里组合 `agentName + " · " + model` 再传给既有函数，或扩展 `buildClaudeStatusLineFooter` 签名加 agent 参数——倾向后者（保持单点拼装，测试友好）。

### D3: 空值聚合规则

agent 为空则仅传 model（现状）；model 与 agent 都空则整行跳过（现状逻辑不变）。不引入 i18n 文案（纯标识符，无本地化需求）。

## Risks / Trade-offs

- [footer 行变长] → 与 model 共享一行，不新增行数；长 agent 名截断策略与 model 一致（无截断，平台自动换行）
- [其他 agent 误实现 GetAgent] → 类型断言天然约束为 AgentSwitcher，无歧义
- [与 /agent 切换值不同步] → GetAgent 即 cc-connect 侧当前值，与每消息 `--agent` 透传一致；TUI 侧改动会被 cc-connect 覆盖（既有架构行为，非本变更引入）

## Migration Plan

无部署步骤；纯 footer 显示变更，回滚即移除拼接。
