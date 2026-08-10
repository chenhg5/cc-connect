# design: fix-agent-config-validation

## Context

动机见 proposal.md。关键事实（源码 + 实测验证）：

- `opencode run --agent <name>` 中，`localAgent()` 对不存在的 agent 与 `mode: subagent` 的 agent 输出警告后**静默回退默认**（实测：`--agent explore` → session.agent = build；`--agent brainstorm`（all）→ 正常）。
- `opencode agent list` 输出 `<name> (<mode>)` 行 + 缩进的权限 JSON 块，`mode` 取值为 `primary` / `subagent` / `all`；compaction/title/summary 为内部 hidden。
- 本机 1.18.15 实测 `opencode agent list` 可用。
- `agent/opencode` 的 `New()` 在构造时读取 `opts["agent"]`，校验可在此处之后进行；cc-connect 启动流程中 agent 构造发生在 `cmd/cc-connect/main.go`。

## Goals / Non-Goals

Goals:
- 启动时一次性校验 agent 配置，无效即警告
- 枚举逻辑与 `agent-switch-command` 变更的 `AvailableAgents` 同源复用（单一实现，两个消费方）

Non-Goals:
- 不做运行时切换（在 `agent-switch-command` 变更中）
- 不修复 variant 静默重置（opencode 无 variants 的 CLI 枚举入口；当前默认模型 deepseek 无变体；记为已知问题）
- 不改动 opencode 端回退行为；不改配置；不阻止启动

## Decisions

### D1: 校验时机为 agent 构造后、启动继续前

在 `cmd/cc-connect/main.go` 的 agent 构造流程中调用一次校验函数，`slog.Warn` 输出（含配置值、原因、可用列表）。启动路径零阻塞：校验失败仅记日志。

备选：启动后异步校验——存在"用户已发消息但警告未出"的窗口，同步更简单且成本可忽略（一次子进程调用）。

### D2: 枚举与解析逻辑下沉为 opencode 包内共享函数

在 `agent/opencode` 内实现 `listAgents(ctx)`（执行 `opencode agent list` + 解析 + 过滤 subagent/hidden），返回 `[]AgentInfo{Name, Mode}`；校验函数与 `agent-switch-command` 的 `AvailableAgents()` 均调用它。解析容错规则（零匹配视为失败、跳过缩进行）与 `agent-switch-command` design 的 D3 保持一致。

### D3: 校验仅针对 opencode agent

其他 agent 类型（claudecode/codex 等）无 `--agent` 概念，不参与校验；校验入口以 opencode 包内函数形式暴露，由 cmd 在 agent 类型为 opencode 时调用（或由 opencode 自身在构造时校验——二选一，倾向构造时校验，避免 cmd 侧做类型判断；构造失败不影响 agent 创建，仅警告）。

### D4: 不引入 i18n

警告走 `slog` 日志体系（开发者/运维视角），不经 IM 展示，无需 i18n 文案。

## Risks / Trade-offs

- [agent list 格式跨版本漂移导致误判] → 解析容错：零匹配视为"校验跳过"而非"agent 无效"（spec: Enumeration failure is reported but tolerated）
- [校验与 opencode 实际接受范围不一致] → 以实测 1.18.15 行为为准（subagent 拒绝、all/primary 接受）；若未来 opencode 放宽/收紧规则，仅影响警告准确性，不影响功能
- [构造时同步执行子进程拖慢启动] → 单次调用、带超时（10s），可接受；如需进一步可异步化（不引入首版）

## Migration Plan

无部署步骤；纯日志行为，回滚即删除调用点。
