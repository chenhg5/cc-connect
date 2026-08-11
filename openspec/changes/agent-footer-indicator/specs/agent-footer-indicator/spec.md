# agent-footer-indicator

## Purpose

让用户在 IM 富卡片底部的状态区内直接看到当前会话运行的 agent 名称，无需依赖记忆或额外命令查询。

## ADDED Requirements

### Requirement: Card footer shows current agent

系统 SHALL 在富卡片底部状态区的 model 行中展示当前 agent 名称，格式为 `agent · model`（如 `build · deepseek/deepseek-v4-pro`）。当 agent 支持运行时 agent 查询（实现 `AgentSwitcher`）且当前 agent 名称非空时 SHALL 展示；否则 SHALL NOT 展示该片段，且 SHALL NOT 影响既有 footer 内容（耗时、model、workdir 保持原样）。

#### Scenario: Footer shows agent for AgentSwitcher agents
- **WHEN** 会话使用 opencode agent（支持 `AgentSwitcher`）且当前 agent 为 `build`
- **THEN** footer model 行显示为 `build · deepseek/deepseek-v4-pro`

#### Scenario: Footer omits agent for agents without AgentSwitcher
- **WHEN** 会话使用不支持 `AgentSwitcher` 的 agent（如 claudecode）
- **THEN** footer 保持既有内容，不出现 agent 片段

#### Scenario: Footer omits agent when value is empty
- **WHEN** agent 未配置且 `GetAgent()` 返回空
- **THEN** footer model 行只有 model 名称，无 agent 前缀
