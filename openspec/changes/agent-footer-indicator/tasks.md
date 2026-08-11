# tasks: agent-footer-indicator

## 1. 实现

- [x] 1.1 新增 `replyFooterAgent(session AgentSession, agent Agent) string`（engine.go，仿 `replyFooterModel`）：先断言 `session.(interface{ GetAgent() string })`，再断言 `agent.(AgentSwitcher)` 取 `GetAgent()`；空值返回 ""
- [x] 1.2 扩展 `buildClaudeStatusLineFooter` 签名（加 agent 参数，或选择 design D2 的拼接位置方案），组装 `agent · model · effort · token`；agent 为空时回退现状
- [x] 1.3 `composeRichStatusFooter` 接线：Line 2 传入 agent 值（含 `replyFooterUsageText` 回退分支的同步处理）

## 2. 测试

- [x] 2.1 单元测试：`replyFooterAgent`（AgentSwitcher 非空/空值、非 AgentSwitcher 返回空、session 层优先）
- [x] 2.2 `composeRichStatusFooter` 测试：opencode 风格 agent 显示 `build · deepseek/deepseek-v4-pro`；无 AgentSwitcher agent 不出现 agent 片段；空 agent 值回退现状
- [x] 2.3 全量验证：`go test -tags no_web ./...`、`go test ./core/ -run TestCUJ`

## 3. 实现说明

- [x] 3.1 实现方式：agent 前缀在 `composeRichStatusFooter` 内组合（`displayModel = agent · model`），未扩展 claudecode 专用方法路径 `e.buildClaudeStatusLineFooter` 签名——该路径仅 claudecode 使用（无 AgentSwitcher，agent 恒空），扩展需改动 claude 专用测试且无收益
