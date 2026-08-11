# agent-footer-indicator

## Why

用户在飞书富卡片底部（statusFooter）看不到当前会话的 agent——foot（卡片下方状态区）只显示耗时、model、workdir。opencode TUI 的 footer 是 `agent · model · variant` 三段式，IM 侧缺失同样信息，用户在对话中无法确认当前跑的是 build 还是 brainstorm。agent 显示能力已经存在（`AgentSwitcher.GetAgent`，`agent-switch-command` 变更已合入），此变更只是把它接进 footer。

## What Changes

- 新增 `replyFooterAgent` 辅助函数（仿 `replyFooterModel` 的类型断言模式：session/agent 上断言 `AgentSwitcher.GetAgent()`）
- `composeRichStatusFooter` 的 Line 2 在 model 前拼入 agent 名称（如 `build · deepseek/deepseek-v4-pro`）；无 `AgentSwitcher` 的 agent 或值为空时不显示
- 非目标：不在 footer 放切换按钮（footer 是纯文本 note 通道，切换沿用 `/agent` 命令卡片）；不做思考强度（variant）显示——cc-connect 无 variant 状态、无 CLI 枚举入口、当前默认模型 deepseek 无变体，做了也是恒为空，记为已知限制

## Capabilities

### New Capabilities

- `agent-footer-indicator`: 富卡片底部状态区显示当前 agent 名称（作为 model 行前缀，仅在有 `AgentSwitcher` 能力时显示）

### Modified Capabilities

无。

## Impact

- `core/engine.go`：新增 `replyFooterAgent`，修改 `composeRichStatusFooter`（Line 2 组装）
- 无平台改动（footer 通道已有）、无 i18n 新增（纯标识符拼接，不涉及文案）、无配置新增
