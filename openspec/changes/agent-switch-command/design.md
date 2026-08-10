# design: agent-switch-command

## Context

动机见 proposal.md。关键事实（源码 + 本机 1.18.15 实测验证）：

- opencode 的 `--agent` 是逐消息绑定：`createUserMessage` 将消息绑定到指定 agent，与 session 当前 agent 不同时 `setAgentModel` 持久化切换；`runLoop` 用最后一条用户消息的 agent 驱动。
- `mode: subagent` 的 agent 传入 `--agent` 会被拒绝并静默回退默认（实测：`--agent explore` → session.agent 变回 build）；`mode: all` / `mode: primary` 正常（实测：`--agent brainstorm` → session.agent = brainstorm）。
- **resume 不传 `--agent` 会把会话 agent 重置回默认**（`input.agent` 为空时用 `defaultInfo()`，非会话当前值）。因此 cc-connect 必须每消息显式传 `--agent`，切换只是改变传入的值。
- `opencode agent list` 输出：`<name> (<mode>)` 行 + 下一行起缩进的权限 JSON 块；模式标签为 `primary` / `subagent` / `all`；compaction/title/summary 以 primary 出现但为内部 hidden。
- `/model` 的生效生命周期：`switchModelOnAgent`（Agent 级 `SetModel` + 配置持久化）→ `cleanupInteractiveState`（关闭当前交互会话）→ 下一条消息 `StartSession` 重建（`--resume <id>` 续接，不重放历史）。
- `config.SaveAgentModel(project, model)` 已存在，写 `[projects.agent.options]`；`SetModelSaveFunc` 钩子由 cmd 接线。

## Goals / Non-Goals

Goals:
- 提供与 `/model` 同构的 `/agent` 命令（查看、列表、切换、持久化）
- 枚举与校验以 `opencode agent list` 为唯一数据源
- 切换与启动校验共享同一份枚举逻辑（与 `fix-agent-config-validation` 变更协同）

Non-Goals:
- 不实现会话级（session-scoped）切换语义（与全局的 /model 家族一致）
- 不实现"尊重 TUI 切换"（需读 session 实际状态，记为已知限制）
- 不处理 variant 切换（见 proposal 讨论，独立问题）

## Decisions

### D1: 全局 Agent 级语义（而非会话级）

切换值存于 Agent 实例（`agentName` 字段），与 `/model`、`/mode`、`/provider` 家族一致。所有会话下一条消息使用新值。

备选：会话级 override（#193 PR 风格）——需 engine 新增会话级机制，与既有命令语义不一致，成本高。opencode 的 per-session 状态在 cc-connect"每消息强覆盖"架构下本就被抹平，会话级价值有限。

### D2: 枚举数据源为 `opencode agent list` 子进程

权威、无额外依赖、与 opencode 内部加载逻辑同源（config 目录扫描 + 内置）。

备选：读 sqlite `session.agent`——能读"实际当前 agent"但依赖 sqlite3 CLI（本机未安装，cc-connect 消息计数已因此降级）；且仅对单会话有读价值，违背 D1 的全局语义。备选：解析 agent 配置目录文件——与 opencode 加载规则耦合（frontmatter 默认 mode、glob 规则），脆弱。

### D3: 解析与过滤

逐行匹配 `^(\S+) \((primary|subagent|all)\)$`，跳过缩进开头的行（权限 JSON）。过滤：`subagent` 模式剔除；`compaction` / `title` / `summary` 按名称剔除（list 输出无 hidden 标记）。

风险：跨版本输出格式变化。缓解：解析容错——零匹配时视为枚举失败走降级路径（spec: Enumeration failure degrades gracefully）。

### D4: 持久化走既有 SaveAgentModel 模式

新增 `config.SaveAgentName(project, name)`（写 `[projects.agent.options].agent`），cmd 接线 `SetAgentSaveFunc` 钩子，与 `SetModelSaveFunc` 同构。空值允许（清除配置，opencode 回默认）。

### D5: 切换校验与降级

`SetAgent` 前用 D2 的枚举结果校验：subagent 或未知名称 → 拒绝并报错（spec: Invalid switch targets are rejected）。配置值本身在启动时校验（见 `fix-agent-config-validation`）。切换持久化失败时回滚内存值并报错。

## Risks / Trade-offs

- [agent list 格式跨版本漂移] → 解析容错 + 零匹配降级（D3）；切换失败时状态不变
- [其他 agent 不实现 AgentSwitcher] → `/agent` 显示"不支持"（仿 `/reasoning` 对非 Pi 的行为），非 opencode 平台零影响
- [枚举子进程阻塞] → 命令路径使用带超时的 context（仿 `AvailableModels` 的 10s）
- [静默回退残余风险] → 切换路径双重防线：列表过滤 + 显式校验（D5）；但配置值被外部改动的并发窗口仍可能触发 opencode 静默回退，日志侧由 fix 变更的启动校验覆盖

## Migration Plan

无部署步骤；切换即持久化。回滚：`/agent switch <原名>` 或直接清配置（`/agent` 支持空值语义）。

## Open Questions

- 1.18.15 的 `opencode agent list` 输出是否与 main 分支一致（权限块格式）——实现时以真实输出为准做一次解析冒烟测试。
- 插件注册的 agent（如重新启用 oh-my-openagent 后）是否出现在 `opencode agent list` 中——插件当前禁用，不影响首版；若启用后发现不在列表，则插件 agent 无法通过卡片切换（可回退到名称直切），记为后续验证项。
