# agent-config-validation Specification

## Purpose
在 cc-connect 启动时校验 opencode 项目配置中 agent 值的有效性（agent 存在且可作为主 agent），避免配置无效时 opencode 静默回退默认 agent 导致用户无感知地运行在错误的 agent 上。
## Requirements
### Requirement: Invalid configured agent is reported at startup

当项目配置的 opencode `agent` 值无效时（agent 不存在，或为 subagent 模式），系统 SHALL 在启动时输出结构化警告日志，说明配置值、失败原因与可用的 agent 列表。此校验 SHALL NOT 阻止 cc-connect 启动，SHALL NOT 修改配置，SHALL NOT 改变 opencode 的静默回退行为（回退仍发生，但不再无声无息）。

#### Scenario: Configured agent does not exist
- **WHEN** 项目配置 `agent = "nonexistent-agent"` 且启动 cc-connect
- **THEN** 系统输出警告日志，指出 `nonexistent-agent` 不存在并列出可用 agent 名称

#### Scenario: Configured agent is a subagent
- **WHEN** 项目配置 `agent = "explore"`（subagent）且启动 cc-connect
- **THEN** 系统输出警告日志，指出 `explore` 为 subagent、不能作为主 agent 使用

#### Scenario: Startup proceeds despite invalid config
- **WHEN** 上述任一场景发生
- **THEN** cc-connect 正常启动，平台与命令功能不受影响

### Requirement: Valid configurations produce no warning

当配置的 agent 存在且可作为主 agent（primary 或 all 模式）时，系统 SHALL NOT 输出校验警告。当配置值为空时，系统 SHALL 跳过校验（opencode 默认使用默认 agent）。

#### Scenario: Valid agent configured
- **WHEN** 项目配置 `agent = "brainstorm"`（mode: all）且启动 cc-connect
- **THEN** 无 agent 校验警告

#### Scenario: Empty agent configured
- **WHEN** 项目未配置 `agent` 值且启动 cc-connect
- **THEN** 跳过校验，无警告

### Requirement: Enumeration failure is reported but tolerated

当无法枚举可用 agent（如 `opencode agent list` 执行失败或输出不可解析）时，系统 SHALL 记录一条警告说明校验未完成的原因，SHALL NOT 因校验失败而产生误导性的"agent 无效"结论。

#### Scenario: Agent enumeration fails
- **WHEN** `opencode agent list` 执行失败
- **THEN** 系统记录"agent 校验跳过"类警告，不产生 agent 无效的误报

