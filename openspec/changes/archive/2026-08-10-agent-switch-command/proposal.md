# agent-switch-command

## Why

cc-connect 驱动的 opencode 会话中，agent（build / plan / brainstorm 等）目前是配置级静态值：切换只能改 `config.toml` 重启 cc-connect。而 opencode 端 per-message agent 机制原生支持运行时切换（`session.prompt({agent})` 逐消息绑定 + `setAgentModel` 持久化到会话），本机实测已验证 `mode: all` 的自定义 agent（brainstorm）可通过 `--agent` 切换、`mode: subagent` 会被拒绝并静默回退。缺的只是 cc-connect 侧的接线：一个运行时 `/agent` 命令。

## What Changes

- 新增可选接口 `core.AgentSwitcher`（`SetAgent` / `GetAgent` / `AvailableAgents`），仿照既有 `ModelSwitcher` / `ReasoningEffortSwitcher` 模式
- `agent/opencode` 实现该接口：`agentName` 由只读配置值变为 mutex 保护的运行时可变值；`AvailableAgents` 通过执行 `opencode agent list` 枚举，过滤 subagent 与内置 hidden agent（compaction/title/summary）
- 新增 `/agent` 命令：无参数展示当前 agent + 可选列表（卡片或文本+按钮，依平台能力回退），`/agent switch <name>` 或按钮切换，切换后重建会话使下一条消息生效（复用 `/model` 的 `cleanupInteractiveState` 生命周期）
- 切换值持久化到 `config.toml` 的 `[projects.agent.options].agent`（仿 `config.SaveAgentModel` 新增 `SaveAgentName`）
- 切换为全局语义（Agent 级），与 `/model`、`/mode`、`/provider` 家族一致；现有会话在下一条消息重建时生效
- 不传 `--agent` 会静默重置会话 agent 为默认（opencode 源码 + 实测确认），因此首版不实现"尊重 TUI 切换"（需要读能力，记为已知限制）

## Capabilities

### New Capabilities

- `agent-switching`: 对话过程中通过 `/agent` 命令运行时查看/切换 opencode 会话的 agent（含可用 agent 枚举、切换持久化、subagent/hidden 过滤）

### Modified Capabilities

无（`openspec/specs/` 尚无既有 spec）。

## Impact

- `core/interfaces.go`：新增 `AgentSwitcher` 接口
- `core/engine.go`：`/agent` 命令解析、卡片渲染、切换流程（仿 `cmdModel` / `renderModelCard` / `performModelSwitchAsync`）
- `core/i18n.go`：新增命令与卡片文案（EN/ZH/ZH-TW/JA/ES）
- `agent/opencode/opencode.go`：`agentName` 可变 + `AvailableAgents` 枚举与校验
- `agent/opencode/session.go`：`buildRunArgs` 已支持 `--agent`，无改动
- `config/config.go`：新增 `SaveAgentName`（仿 `SaveAgentModel`）
- `cmd/cc-connect/main.go`：接线配置保存钩子（仿 `SetModelSaveFunc`）
- `config.example.toml`：文档化 `/agent` 与 agent 配置说明
