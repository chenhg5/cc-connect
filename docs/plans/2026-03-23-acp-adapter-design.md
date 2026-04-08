# ACP 适配层设计（草案）

本文描述在 cc-connect 中增加 **Agent Client Protocol（ACP）** 适配的可行方案，目标是让 **已实现 ACP Agent 端** 的上游进程（见 [官方 Agents 列表](https://agentclientprotocol.com/get-started/agents)）能通过 **统一协议**接入现有 `core.Engine`，减少为每个 CLI 单独维护解析逻辑的成本。

## 1. 背景与术语

- **ACP**：基于 JSON-RPC 的标准，用于 **Client（如编辑器）↔ Agent（编码助手进程）** 通信；与 IM 无关。
- **对 cc-connect 的价值**：在 `agent/` 侧实现 **ACP Client**（连接子进程或 socket 上的 ACP Agent），将 ACP 消息映射为现有的 `core.Agent` / `core.AgentSession` / `core.Event`，从而使 **飞书 / Telegram 等平台** 与「任意兼容 ACP 的 Agent 后端」对接。
- **不在本文范围（可选二期）**：让 cc-connect **作为 ACP Agent 对外暴露**，供 Zed 等编辑器直连；需完整实现协议 Agent 侧，工作量更大。

## 2. 架构约束（与仓库规则一致）

- `core/` **不** import `agent/*`；新逻辑全部放在 `agent/acp/`（或 `agent/acpclient/`）。
- 通过 `core.RegisterAgent("acp", factory)` 在 `init()` 注册；`cmd/cc-connect/plugin_agent_acp.go` + `Makefile` + `config.example.toml` 与现有 agent 插件一致。
- 权限、会话、卡片等多为 Engine 已有能力；适配层专注 **协议 ↔ Event**。

## 3. 目标与非目标

### 3.1 一期目标（MVP）

- 配置驱动启动子进程：`command` + `args` + `work_dir` + `env`（与现有 agent options 风格一致）。
- Transport：**stdio JSON-RPC**（ACP 文档中最常见）；后续再评估 HTTP/WebSocket。
- 映射能力（按优先级）：
  1. 会话生命周期：与 `StartSession` / `Close` / `CurrentSessionID` 对齐。
  2. **Prompt turn**：用户文本（及后续可选图片/文件）→ ACP 对应方法；响应流 → `Event`（`EventResult`、`EventThinking`、增量文本等，与现有 Engine 消费方式一致）。
  3. **工具调用与用户批准**：映射到 `EventPermission` + `RespondPermission`（若 ACP 方法名与字段与 core 不完全一致，在适配层做字段转换）。
- 单项目、单用户会话语义与现有一致：`session_key` 仍由 Platform 提供，ACP 侧使用独立 `sessionID` 字符串与 cc-connect 会话绑定策略需在实现阶段定稿（建议：cc-connect `sessionID` 传入 adapter，ACP session id 由子进程返回或持久化路径配置）。

### 3.2 明确延后（二期+）

- ACP **File System / Terminal** 全量映射（若与 IM 展示模型差距大，可先降级为文本摘要或仅日志）。
- **Slash commands / Agent plan** 与 IM 命令体系的统一（可先忽略或透传为纯文本）。
- cc-connect **作为 ACP Server** 供编辑器连接。

## 4. 组件划分

| 组件 | 职责 |
|------|------|
| `agent/acp/agent.go` | 实现 `core.Agent`：`Name`、`StartSession`、`ListSessions`、`Stop` |
| `agent/acp/session.go` | 实现 `core.AgentSession`：`Send`、`Events`、`RespondPermission`、`Close` 等 |
| `agent/acp/rpc.go`（或 `transport_stdio.go`） | stdio 上的 JSON-RPC 读写、request id、并发与取消 |
| `agent/acp/mapping.go` | ACP 通知/结果 → `core.Event`；`PermissionResult` ↔ ACP 工具批准结构 |
| 测试 | 子进程 mock：固定 JSON-RPC 回放 fixture，避免 CI 依赖真实 Cursor/Codex 二进制 |

## 5. 配置草案（`config.example.toml`）

```toml
# [[projects]]
# [projects.agent]
# type = "acp"
# [projects.agent.options]
# command = "path/to/agent"   # 或 npx / uvx 等
# args = []                     # 可选
# # cwd 默认 work_dir；env 可扩展
# # acp_transport = "stdio"    # 默认；预留 "http" 等
```

具体字段名以实现时与 `config` 解析为准，需 **向后兼容**：未安装插件时 `no_acp` build tag 行为与现有 agent 一致。

## 6. 风险与依赖

- **协议版本**：需锁定所实现的 ACP schema 版本；上游变更时通过集成测试与 changelog 跟进。
- **Agent 差异**：列表中各产品对 ACP 子集支持不同；MVP 文档中写明「已验证」矩阵（至少 1～2 个开源/可脚本化 Agent）。
- **router / 代理场景**：若子进程同时向 stdout 打非 JSON 日志，会破坏流式解析；与 claudecode `router_url` 下禁用 `--verbose` 同类问题需在 ACP 层统一约束（仅 JSON-RPC 行写入协议通道）。

## 7. 实施顺序建议

1. 阅读官方 **Protocol / Session / Prompt / Content / Tool** 章节与 **Schema**，列出与 `core.Event` 的字段对照表。
2. 实现 stdio transport + 最小会话握手（无 UI）。
3. 打通一轮 prompt → 文本结果 → `EventResult`。
4. 接入权限与工具事件；补 `engine` 层无需改动的验证测试。
5. 文档：`docs/` 简短用户说明 + `config.example.toml` 示例。
6. （可选）在 CI 中使用 mock server 跑 `go test ./agent/acp/...`。

## 8. 参考链接

- [ACP Introduction](https://agentclientprotocol.com/get-started/introduction)
- [ACP Agents 列表](https://agentclientprotocol.com/get-started/agents)
- [Protocol Overview](https://agentclientprotocol.com/protocol/overview)（以官网当前版本为准）

---

*Status: design draft — 实现跟踪可在本文件追加「Implementation log」小节或单独 tasks JSON。*
