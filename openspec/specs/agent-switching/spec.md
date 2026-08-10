# agent-switching Specification

## Purpose
让用户在不重启 cc-connect、不修改配置文件的前提下，于对话过程中运行时查看并切换 opencode 会话的 agent（如 build、plan 及自定义 primary/all 模式 agent），并将切换结果持久化。
## Requirements
### Requirement: User can view and switch the current agent at runtime

系统 SHALL 提供运行时查看与切换当前 opencode agent 的命令（`/agent`）。执行该命令时，系统 SHALL 展示当前生效的 agent，以及所有可切换的 agent 列表。用户选择某个 agent 后，系统 SHALL 将其设为后续消息使用的 agent，且 SHALL 将该选择持久化到项目配置，使 cc-connect 重启后仍保持。

切换为全局语义：切换后所有会话（含当前会话与其他会话）的下一条消息 SHALL 使用新 agent。切换 SHALL 在用户下一条消息时生效，不中断当前正在进行的会话。

#### Scenario: View current agent and list
- **WHEN** 用户发送 `/agent`（无参数）
- **THEN** 系统展示当前 agent 名称与可切换 agent 列表

#### Scenario: Switch agent by name
- **WHEN** 用户发送 `/agent switch brainstorm`
- **THEN** 系统确认切换到 brainstorm，且该会话下一条消息以 brainstorm agent 运行

#### Scenario: Switch agent persists across restart
- **WHEN** 用户切换到 brainstorm 后重启 cc-connect
- **THEN** 会话仍以 brainstorm agent 运行

#### Scenario: Switch affects other sessions
- **WHEN** 用户在会话 A 切换到 brainstorm，随后在会话 B 发送消息
- **THEN** 会话 B 的消息同样以 brainstorm agent 运行

### Requirement: Only switchable agents are offered

系统 SHALL 只向用户展示可作为主 agent 运行的 agent（primary 与 all 模式），SHALL NOT 展示 subagent 模式 agent（如 explore、general）或内部 hidden agent（如 compaction、title、summary）。`--agent` 传入 subagent 时 opencode 会静默回退默认 agent，因此列表 SHALL 排除此类 agent 以避免误导。

#### Scenario: Subagent not offered
- **WHEN** 用户查看可用 agent 列表
- **THEN** 列表中不包含 explore、general 等 subagent

#### Scenario: Custom all-mode agent offered
- **WHEN** 用户配置了 `mode: all` 的自定义 agent（如 brainstorm）
- **THEN** 该 agent 出现在列表中且可正常切换

### Requirement: Agent enumeration failure degrades gracefully

当无法枚举可用 agent（如 `opencode agent list` 执行失败或输出无法解析）时，系统 SHALL NOT 中断命令流程，SHALL 至少展示当前 agent 并提示列表不可用；已配置的 agent 值仍 SHALL 可被用户直接以名称切换。

#### Scenario: Enumeration fails
- **WHEN** 枚举可用 agent 失败
- **THEN** 系统仍展示当前 agent，并提示可用列表暂不可用，切换命令仍可按名称使用

### Requirement: Invalid switch targets are rejected

用户显式切换到一个不存在或不可作为主 agent 的名称时，系统 SHALL 拒绝该切换并说明原因，SHALL NOT 静默接受（避免触发 opencode 的静默回退行为）。系统 SHALL 保持原 agent 不变。

#### Scenario: Switch to subagent rejected
- **WHEN** 用户尝试切换到 subagent 名称（如 explore）
- **THEN** 系统拒绝切换并提示该 agent 不可作为主 agent 使用，当前 agent 保持不变

#### Scenario: Switch to unknown name rejected
- **WHEN** 用户尝试切换到列表中不存在的名称
- **THEN** 系统拒绝切换并提示名称无效，当前 agent 保持不变

