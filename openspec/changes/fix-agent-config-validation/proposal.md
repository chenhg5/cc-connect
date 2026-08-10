# fix-agent-config-validation

## Why

cc-connect 配置 `[projects.agent.options].agent` 中的 agent 名无效时（拼写错误、或为 subagent 如 `explore`/`general`），opencode 会**静默回退默认 agent**（build）而不报错——本机实测确认：`--agent explore` 的会话最终以 build 运行。用户以为在跑 brainstorm，实际每轮都在跑 build，且毫无提示。当前唯一察觉手段是事后翻日志或看输出风格。

## What Changes

- 启动时校验 opencode 项目配置的 `agent` 值：执行 `opencode agent list` 枚举可用 agent，检查配置值是否存在且非 subagent
- 校验失败（agent 不存在 / 为 subagent / 枚举命令失败）时输出结构化警告日志（`slog.Warn`），指明配置值、问题原因与可用 agent 列表；不阻塞启动、不修改配置
- 配置值为空或未配置时跳过校验（opencode 默认行为即为 build）
- 非目标（Non-Goals）：不做运行时切换（见 `agent-switch-command` 变更）；不修复 variant 静默重置问题（opencode 无 CLI 枚举 variants 的入口，修复成本高且当前默认模型 deepseek 无变体，记为已知问题）；不改动 opencode 端的回退行为

## Capabilities

### New Capabilities

- `agent-config-validation`: 启动时校验 opencode agent 配置的有效性，配置为不存在的 agent 或 subagent 时给出显式警告

### Modified Capabilities

无（`openspec/specs/` 尚无既有 spec）。

## Impact

- `agent/opencode/opencode.go`：新增配置校验函数（复用 `opencode agent list` 枚举逻辑，与 `agent-switch-command` 的 `AvailableAgents` 同源）
- `cmd/cc-connect/main.go`：agent 初始化后调用校验
- 无接口、无配置格式、无 i18n 变化（仅日志，用户可见文案走既有日志体系）
