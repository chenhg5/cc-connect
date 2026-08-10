# tasks: agent-switch-command

## 1. 枚举与接口基础

- [x] 1.1 在 `agent/opencode` 内实现 `listAgents(ctx)`：执行 `opencode agent list`，按 `^(\S+) \((primary|subagent|all)\)$` 逐行解析，跳过缩进权限 JSON 行；零匹配视为失败
- [x] 1.2 实现过滤：剔除 `subagent` 模式与 hidden 名称（compaction/title/summary），保留 primary/all
- [x] 1.3 添加 `listAgents` 单元测试（含真实 1.18.15 输出格式的样例、权限块跳过、空输出、非 JSON 行容错）
- [x] 1.4 在 `core/interfaces.go` 新增 `AgentSwitcher` 接口（`SetAgent` / `GetAgent` / `AvailableAgents`），注释说明全局语义与生效时机

## 2. opencode 适配器

- [x] 2.1 `agent/opencode` 实现 `AgentSwitcher`：`SetAgent`（mutex 保护，校验目标名称存在且非 subagent，无效返回错误）、`GetAgent`、`AvailableAgents`（复用 listAgents，带超时 context）
- [x] 2.2 校验 `StartSession` 快照路径：agentName 从可变字段读取（现有代码已快照，确认无竞态）
- [x] 2.3 单元测试：SetAgent 切换成功/拒绝 subagent/拒绝未知名、GetAgent、AvailableAgents 过滤结果

## 3. 配置持久化

- [x] 3.1 `config/config.go` 新增 `SaveAgentName(projectName, agent string)`（仿 `SaveAgentModel`，写 `[projects.agent.options].agent`；空值清除该键）
- [x] 3.2 `config_test.go` 添加 SaveAgentName 测试（含注释/未知字段保留，仿 `TestSaveAgentModel_PreservesCommentsAndUnknownFields`）
- [x] 3.3 `cmd/cc-connect/main.go` 接线保存钩子（仿 `SetModelSaveFunc`）

## 4. 引擎命令与卡片

- [x] 4.1 `core/engine.go` 新增 `cmdAgent`：无参数时按平台能力渲染卡片或文本+按钮列表（仿 `cmdModel` / `cmdReasoning` 非卡片分支）；非 `AgentSwitcher` agent 回复"不支持"
- [x] 4.2 实现 `renderAgentCard`：当前 agent + 可选列表 + 按钮（数据 `cmd:/agent switch <n>` / `nav:/agent`），当前项标记
- [x] 4.3 实现切换流程（仿 `performModelSwitchAsync`）：解析目标（序号/名称）→ 校验 → `SetAgent` → `SaveAgentName` → 清理交互状态（`cleanupInteractiveState` 使下一条消息生效）→ 结果提示；持久化失败回滚内存值
- [x] 4.4 `handleCardNav` / `handleCardAction` 接入 `act:/agent` 与 `nav:/agent`（仿 `/model` 的接线点）
- [x] 4.5 `/help` 文案与命令注册表新增 `/agent`

## 5. i18n

- [x] 5.1 新增 `MsgKey` 常量：命令描述、当前 agent、列表标题、切换成功/失败、不支持、空列表降级提示等
- [x] 5.2 补齐 EN/ZH/ZH-TW/JA/ES 五种语言翻译（含 `/help` 区块）

## 6. 测试与文档

- [x] 6.1 engine 单元测试：`cmdAgent` 卡片/文本两分支、序号与名称切换、拒绝 subagent/未知名、持久化失败回滚、非 AgentSwitcher 平台提示
- [x] 6.2 CUJ 测试 `TestCUJ_*_AgentSwitch`（`core/cuj_test.go`）：用户视角 ≥3 步——`/agent` 查看 → 切 brainstorm → 发消息验证 agent 生效 → 切回 build
- [x] 6.3 `config.example.toml` 更新 opencode 段注释：`/agent` 命令与 agent 配置说明、mode 约束（primary/all 可切换）
- [x] 6.4 全量验证：`go build ./...`、`go test ./...`、`go test ./core/ -run TestCUJ`
