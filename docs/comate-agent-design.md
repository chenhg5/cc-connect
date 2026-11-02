# Comate Agent 设计文档（agent/comate）

接入百度文心快码终端 CLI（`comatecli`，npm 包 `@comate/comatecli`），使 cc-connect 用户可以通过消息平台与 Comate 对话。已实测 `comatecli` v2.0.0 的无头协议，本文档描述实现方案。

## 1. comatecli 协议（实测结论）

### 1.1 无头调用

```bash
# 首轮
comatecli run -q "<query>" --display event-stream [--cwd <dir>] [--model <m>] [--mode Agent]
# 多轮
comatecli run -c <conversationId> -q "<query>" --display event-stream ...
# 权限恢复（新进程）
comatecli run -c <conversationId> --approve --display event-stream
comatecli run -c <conversationId> --reject  --display event-stream
```

- **stdout**：逐行 JSON 事件；**stderr**：Langfuse 等噪音，只捕获用于错误信息，绝不解析。
- 会话 ID 由 CLI 分配，从任意事件的 `conversationInfo.id` 获取；无 `--session-id` 输入参数。
- 登录凭证本地存储（`comatecli login`），子进程自动继承。

### 1.2 事件类型

| type | 内容 | 映射 |
|---|---|---|
| `conversation-status` | `conversationInfo{id, status: Running/Completed}` | 完成且无挂起审批 → `EventResult{Done:true}` |
| `partial-message-data` | `messages[].elements[].children[]`，`type:"TEXT"`，**content 为累积全量** | 差量转换后发 `EventText` |
| `pending-approvals` | `pendingApprovals[]{toolId, conversationId, toolName, command, params}` | `EventPermissionRequest` |
| `conversation-messages` | 全量会话快照 | 忽略（仅用于取会话 ID） |

### 1.3 权限挂起协议（实测验证）

`--disable-auto-approve` 启动时，模型调用 `run_command` 等工具：

1. 输出 `pending-approvals` 事件（含 `toolId`、`command`、`params`；同一轮会出现多次，前几次为空数组，随后出现 `requires_action`（含 `resumeToken`）两次）
2. 进程退出（`conversation-status → Completed`，exit 0），**命令未执行**
3. 恢复：`comatecli run -c <convID> --approve`（或 `--reject`）→ 新进程继续会话、执行命令、流式输出结果直至 Completed；恢复进程重放的 `pending-approvals` 均为空数组，不会重复触发审批

注意：**`pending-approvals`（非空）先于 `Completed` 到达**，且引擎在用户点击批准后立刻调用 `RespondPermission`，两者存在竞态窗口（详见 2.4）。工作区内文件写入类工具不触发审批（CLI 内部放行），仅 `run_command` 等外部命令需要。

## 2. 与 core 的映射

### 2.1 进程模型（codex exec 模式）

一轮一进程，会话对象跨轮存活（`agent/codex/session.go` 结构）：

- `Send()` → `beginTurn()` 互斥 → `spawnTurn(args)` → `readLoop` 解析事件
- `Close()` → cancel → wg.Wait(8s) → 进程组强杀 → closeOnce 关闭 channel
- `proc_unix.go`：`Setpgid` + `syscall.Kill(-pid, SIGKILL)`

### 2.2 文本流式（引擎语义已验证）

引擎对 `EventText` 是**纯追加**（`core/engine.go:5995` `textParts = append`），无去重替换。comatecli 的 TEXT content 是累积快照，直接透传会导致 "ok" + "ok world" 拼接乱码。因此必须做**累积→差量**转换（qoder 先例 `agent/qoder/session.go:359-398`）：

```
prev=""      full="ok"        → chunk="ok"
prev="ok"    full="ok world"  → chunk=" world"（前缀裁剪）
prev="ok world" full="新快照"  → chunk="\n\n" + full（非前缀，快照重置）
```

`EventResult.Content` 留空，引擎用 `strings.Join(textParts, "")` 重组全文（engine.go:6229-6231）。

### 2.3 会话 ID 生命周期

- 首个事件的 `conversationInfo.id` → `sessionID.Store(id)` → 立即发 `EventText{SessionID: id, Content: ""}`（codex thread.started 模式），引擎随即持久化映射（engine.go:6063-6075）
- 后续 `Send` 读 `CurrentSessionID()` → args 带 `-c <id>`
- `core.ContinueSession`（`"__continue__"`）哨兵视为新会话（comatecli `--continue` 不处理挂起交互，不安全；codex 同款处理）

### 2.4 权限闭环（引擎语义已验证）

引擎在 `EventPermissionRequest` 时**阻塞等待** `<-pending.Resolved`（engine.go:6162），期间事件在 channel 缓冲——因此"EventResult 先于审批到达"在结构上不可能。挂起期间不发任何终结事件即可让引擎自然等待。

```
Send/RespondPermission ──► spawnTurn ──► readLoop
    ├─ pending-approvals（非空）──► suspended=true（turn 级标志）+ turnCompleted 提前置位
    │                                + pending[toolId] 登记 + EventPermissionRequest{RequestID: toolId}
    ├─ Completed 且 suspended/有未决审批 ──► 挂起，不发终结事件（引擎阻塞在审批等待）
    ├─ Completed 且无未决审批 ──► EventResult{Done:true}
    └─ 进程退出且无 Completed（且非 Close 触发）──► EventError（附 stderr）

RespondPermission(requestID, result):
    校验 alive → pending map 弹出 requestID（缺失报错）
    → beginTurn（turnCompleted 已置位 → 等待挂起进程退出后再接管）
    → spawnTurn(run -c <convID> --approve|--reject --display event-stream)
    → 事件进同一 channel → 新进程 Completed → EventResult{Done:true}
    （恢复进程重放的 pending-approvals 为空数组，不会重复挂起）
```

两处实测踩坑（e2e 发现并修复）：

1. **挂起判定必须用 turn 级 `suspended` 标志，不能只看 `hasPending()`**：`RespondPermission` 在 `Completed` 被解析前就已弹出 `pending[toolId]`，若只查 map，挂起轮会误发 `EventResult`，引擎误认为轮次结束。
2. **`turnCompleted` 须在 `pending-approvals` 时即置位**：否则引擎响应审批时 `beginTurn` 仍视为"轮次进行中"直接拒绝（真实 CLI 两个事件间隔可达数秒）。

`Behavior == "allow"` → `--approve`，其余 → `--reject`。`UpdatedInput` v1 忽略（comatecli `--approve` 不接受修改后的输入）。

## 3. 文件清单

| 文件 | 内容 |
|---|---|
| `agent/comate/comate.go` | Agent 结构、`New()` 工厂、注册、选项解析、`AgentDoctorInfo`/`WorkDirSwitcher`/`SessionEnvInjector` |
| `agent/comate/session.go` | 会话、spawn/readLoop、事件解析与映射、差量文本、权限挂起/恢复、Close |
| `agent/comate/proc_unix.go` / `proc_windows.go` | 进程组强杀（照搬 codex 改包名） |
| `agent/comate/session_test.go` | 单元测试（fake binary on PATH 模式） |
| `cmd/cc-connect/plugin_agent_comate.go` | `//go:build !no_comate` 空导入 |
| `Makefile` | `ALL_AGENTS` 加 `comate`（第 38 行生效的那处） |
| `config.example.toml` | 配置示例 |

## 4. 配置选项

```toml
[projects.agent]
type = "comate"

[projects.agent.options]
work_dir = "/path/to/project"      # 默认 "."
cmd = "comatecli"                  # 默认 "comatecli"，可带额外参数
# model = "模型显示名或ID"          # -> --model
# mode = "Agent"                   # Agent|Ask|Plan|Cloud|Bot -> --mode
# auto_approve = false             # 默认 false -> 每轮传 --disable-auto-approve，
                                   #   工具调用在聊天中出审批卡片；true 则用 CLI 内置自动审批
# disable_hooks = false            # true -> --disable-hooks
# enable_interactive_tools = false # true -> --enable-interactive-tools
# [projects.agent.options.env]     # 子进程额外环境变量
```

v1 实现的可选接口：`AgentDoctorInfo`（doctor 检查）、`WorkDirSwitcher`（/dir 切换）、`SessionEnvInjector`。不做：`ProviderSwitcher`（SaaS 登录无 provider 概念）、`ModelSwitcher`（`comatecli model list` 输出为本地化人读文本，解析脆弱；静态 `model` 配置覆盖选型）、`HistoryProvider`。

## 5. args 构造

```
查询轮: run --display event-stream [-c <convID>] [--cwd <dir>] [--model <m>] [--mode <mode>]
        [--disable-auto-approve] [--disable-hooks] [--enable-interactive-tools] -q <query>
恢复轮: run --display event-stream -c <convID> --approve|--reject [--cwd <dir>]
```

- 查询经 argv `-q` 传递（comatecli run 无 stdin 模式），平台消息长度远低于 argv 上限
- `cliExtraArgs`（cmd 中附加参数）前置于 `run`（codex 模式）
- `cmd.Dir = workDir` + 显式 `--cwd` 保持 CLI 工作区追踪一致
- 环境变量：`core.MergeEnv(os.Environ(), extraEnv)`

## 6. 测试计划

复用 codex 的 fake-binary-on-PATH 模式（shell 脚本假 CLI：记录 `$@` 到 args 文件、输出罐头 JSONL；Windows 用 PowerShell shim）：

1. `TestBuildRunArgs_FreshTurn` / `_ResumeUsesConversationFlag` / `_AutoApproveOmitsDisableFlag`
2. `TestBuildResumeArgs`（approve/reject，无 -q）
3. `TestSend_FreshTurn_AnnouncesConversationIDAndStreams`（ID 广播 + 差量文本 + EventResult）
4. `TestSend_MultiTurn_UsesConversationID`
5. `TestHandleEvent_AccumulatedSnapshotsBecomeDeltas`（纯函数）
6. `TestHandleEvent_PendingApprovalsEmitsPermissionRequest`（纯函数）
7. `TestSuspendedTurn_EmitsNoEventResult`（挂起不出终结事件，alive 保持）
8. `TestRespondPermission_ApproveSpawnsNewProcess` / `_DenySpawnsReject` / `_UnknownRequestID`
9. `TestSend_RejectsConcurrentTurn`
10. `TestContinueSessionTreatedAsFresh`
11. `TestClose_ForceKillsProcessGroup`
12. `TestNew_OptionsParsing`
13. 非 JSON 行忽略、stderr 噪音不产生事件

## 7. 验证清单

```bash
go build ./...
go vet ./agent/comate/
go test ./agent/comate/ -v && go test -race ./agent/comate/
go test ./...
make build AGENTS=comate && make build
go build -tags no_comate ./cmd/cc-connect
```

真实 e2e（已登录 comatecli）：审批卡片→approve 执行→多轮上下文→reject→重启恢复会话 ID→auto_approve 直通。

## 8. 已知限制 / 风险

- `maxTurnTime`（引擎级，默认关闭）若启用会杀掉长时间挂起的审批
- 长提示词经 argv 传递，受平台消息长度限制约束（comatecli 无 stdin 模式）
- `ask_user_question` 类挂起 v1 只出通用审批卡片（approve/reject），`--answer qid=oid` 映射留待 e2e 验证其 params 结构后扩展
- 图片忽略并告警（qoder 先例）；`--attach-image` 是现成扩展点
