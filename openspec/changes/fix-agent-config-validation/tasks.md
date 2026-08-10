# tasks: fix-agent-config-validation

## 1. 枚举与校验实现

- [ ] 1.1 在 `agent/opencode` 内实现共享枚举 `listAgents(ctx)`（`opencode agent list` 执行 + `^(\S+) \((primary|subagent|all)\)$` 逐行解析 + 跳过缩进权限 JSON；零匹配视为失败）——与 `agent-switch-command` 变更的 1.1-1.3 为同一实现，此处完成后复用
- [ ] 1.2 实现 `ValidateConfiguredAgent(configured string) (problem string, available []string)`：配置为空 → 跳过；存在且 primary/all → 无警告；不存在或 subagent → 返回问题描述与可用列表；枚举失败 → 返回"校验跳过"标记
- [ ] 1.3 单元测试：有效 primary/all 无警告、subagent 报错、未知名报错、空值跳过、枚举失败不误报（不产生"agent 无效"结论）

## 2. 启动接线

- [ ] 2.1 在 `agent/opencode` 的 agent 构造流程（`New()` 之后）调用校验，配置无效时 `slog.Warn` 输出配置值、原因与可用 agent 列表；枚举失败时 `slog.Warn` 输出"校验跳过"
- [ ] 2.2 验证不阻塞启动、不修改配置（构造返回不受校验影响）

## 3. 验证

- [ ] 3.1 全量验证：`go build ./...`、`go test ./...`
- [ ] 3.2 手动冒烟（可选）：配置 `agent = "explore"` 启动观察警告日志；配置 `agent = "brainstorm"` 启动无警告
