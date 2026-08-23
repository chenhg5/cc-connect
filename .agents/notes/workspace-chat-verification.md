# Codex 原生工作区聊天验证

## 适用场景

适用于同时影响 `core/workspace_chat*`、`core/native_conversation.go`、`agent/codex` App Server、`storage/workspacechat`、Web 工作区聊天或企业微信接线的变更。

## 已验证路径

- 先以 `core/native_conversation.go` 的通用类型和能力接口建立影响图，再核对 `core/workspace_chat_management.go` 的唯一 REST/WS 消费者、`agent/codex/native_conversation.go` 的 App Server RPC、`storage/workspacechat/sqlite.go` 的当前 schema，以及 `web/src/api/workspaceChat.ts` 和 `core/workspace_chat_wecom.go`。这个顺序能先发现契约漂移，避免直接进入全量构建。
- 工作区协议静态清理使用精确符号检索：旧历史 RPC、旧 WebSocket 请求/事件、旧 thread 创建路由和旧接口名应在生产文档与当前消费者中同时消失；平台会话域中的同名通用命令不能据此误删。
- 本次任务修改依赖前后均运行严格构建基础审计，结果均为 `0 error / 0 warning / 0 exception`。依赖或锁文件再次变化后仍须重跑，不能复用本结果。
- Web 当前存在 `test`（Vitest）与 `build`（TypeScript + Vite）脚本。Go 验证应先命中受影响包，再运行 Core CUJ、全仓测试和 race；所有构建、测试、lint 与长驻服务按项目规则通过设备资源守卫执行，Go 使用 `-p=2 -parallel=2`。

## 应避免路径

- 不使用已删除的整线程读取 RPC、固定条数读取或旧事件扁平化来替代 `thread/turns/list`、`thread/items/list` 和 `NativeEventEnvelope`；这会重新引入第二套历史或事件协议。
- 不用空 `thread/start` 实现“新建”。首个 Turn 前没有可恢复 rollout，新建必须先落 SQLite 草稿。
- 不为 `workspace_chat.db` 增加迁移 runner、旧表读取或损坏数据库 fallback。只有通过完整性检查的版本不匹配数据库才允许精确重建。
- 不先反复运行全仓门禁来定位接口错误。先完成静态契约清理和聚焦测试，修复真实原因后再运行一次完整门禁。

## 证据与耗时

- 2026-08-24 在基线提交 `bd0000ba` 的工作树上完成静态影响图和旧协议检索。生产代码中没有 `NativeThreadProvider`、`agent_event`、旧 WS 请求、历史双协议、migration runner、双读双写或工作区聊天兼容解析；`thread/read` 只以 `includeTurns=false` 校验 metadata 与 cwd。
- Web 通过 `pnpm test`（7 个文件、47 个测试）和 `pnpm build`。生产构建仅报告既有的 500 kB chunk 提示。
- Go 使用仓库声明的 `GOTOOLCHAIN=go1.25.1`。`go build -p=2 ./...`、`go vet -p=2 ./...`、`go mod tidy -diff`、`go mod verify`、`CI=1 go test -p=2 -parallel=2 ./...` 均通过。
- Core 的 Engine teardown、A5/H2、Relay deadline、Hook 和 Cron 回归均重复运行 20 次通过；`go test ./core -run TestCUJ` 通过。终审补充共享事件泵、审批 worker 登记和 `thread/resume` 跨 cwd 终态后，Engine teardown、新审批停止测试、A5/H2 与相关 Native lifecycle 又连续运行 20 次通过，完整 Core CUJ 再次通过。
- 最终格式化后运行 `CI=1 go test -p=2 -parallel=2 ./...` 全仓通过。受影响包 race 通过：`CI=1 GOTOOLCHAIN=go1.25.1 go test -race -p=2 -parallel=2 ./core ./agent/codex ./storage/workspacechat ./config ./platform/wecom`；其中 Core、Codex、SQLite 分别约 66 秒、47 秒、38 秒。Codex 11 MiB JSON 行、草稿物化以及 Cron API 的 race 回归也单独通过。
- `golangci-lint v2.11.4` 由 Go 1.25.1 构建。未显式设置 `GOTOOLCHAIN` 时，本机默认 Go 1.26 的导出数据会触发 `file requires newer Go version go1.26` panic；显式运行 `GOTOOLCHAIN=go1.25.1 golangci-lint run -j 2 --new-from-rev=HEAD ./...` 后结果为 `0 issues`。这属于门禁输入工具链不一致，不能把未固定版本的结果作为验证。
- 最终严格仓库基础审计、`git diff --check` 和测试产物检查均通过；`core/test_ws_*.json` 没有残留。
- 个人 fork PR `shusfun/cc-connect#2` 首次 head `7b3a518b400d151d054c29a53478305284e95f78` 的 CI run `32658717982` 于 `2026-08-23T18:44:46Z`（北京时间 `2026-08-24 02:44:46`）结束。`lint` 通过，`unit-test` 唯一失败为 `TestOpenRebuildsHealthyVersionMismatchAndReplacesSidecars` 的 `os.SameFile` 断言；后续 smoke、regression、performance 因依赖关系跳过。失败日志同时显示当前 schema、旧表清理和损坏保护测试均已执行。
- Linux 可能在删除旧数据库后立即创建新文件时复用刚释放的 inode，使 `os.SameFile(oldInfo, newInfo)` 把真实的 unlink + replace 误判为原文件仍存在。文件替换测试可在重建前为旧文件创建临时 hard link，令旧 inode 在断言完成前保持占用；该方法不改变生产实现，也不降低“旧路径必须被替换”的断言。加入 hard link 后，Go 1.25.1 下目标测试连续 50 次通过，`./storage/workspacechat` 常规测试与 race 均通过。

## 可复用的生命周期经验

- Engine、Hook 与 Cron 创建的异步任务都必须在停止锁下登记，并由唯一生命周期所有者等待；只取消 context 而不等待任务会在测试临时目录销毁后继续写文件。
- session 的 unsolicited pump 必须先关闭并等待，再关闭 Agent session；停止开始后不得重新登记 pump，否则会在 session 关闭后继续消费事件或写 Session。后台审批响应也属于 Engine worker，不能以裸 goroutine 绕过 stopping 锁和 WaitGroup。
- 旧 Engine 与工作区 Native 会话应共用同一个事件泵 runner 来仲裁 channel 关闭、context 取消、已取出事件的 handoff 和退出清理；审批等不可重放事件只在 runner 的 handoff 回调中收口，避免两个消费者各自复制有差异的 select 循环。
- Codex `thread/read` 与 `thread/resume` 的 thread/cwd 归属失败都必须包装 `ErrNativeThreadNotFound`。Core 只依赖该通用终态回收 provisional actor；普通字符串错误会让跨目录 actor 永久重试订阅。
- 测试中的空主 SessionManager 路径必须连同派生工作区存储一起保持禁用。否则相对路径会在包目录生成 `test_ws_*.json`，产物本身也是后台生命周期未收口的诊断信号。

## 当前限制与候选优化

- 无真实企业微信凭据时只能验证适配、命令与伪边界，线上 WebSocket 连接和真实附件投递仍是交付风险。
- settings、collaboration mode、分页历史和 realtime 属于 App Server 能力探测范围；上游缺失时应验证明确不可用错误，不能改走旧 RPC。
- 当前任务没有获得浏览器自动化授权，不运行浏览器或 Playwright；Web 以单测、类型检查和构建为验证入口。
- 不设置 `CI=1` 的全仓测试会执行两个真实 Cursor CLI 集成：`agent/acp/TestCursorCLI_ACPHandshake` 因本机未登录返回 `Device not configured`，`agent/cursor/TestFetchModelsFromAgentCLI` 返回空模型。两项源码都明确在 CI 跳过，因此 GitHub unit 同口径使用 `CI=1`；本机真实 Cursor 登录链路仍未验证。
- macOS launchd 文件模式测试已改为显式创建 plist 并设为 `0644`，在资源守卫的 `umask 077` 下连续 20 次通过。外置卷 `rename invalid argument` 在 Engine、Hook、Cron 和 CUJ 生命周期收口后未再复现；若再次出现，仍须追踪真实写入者，不能归类为环境波动。

## 失效条件

以下任一项变化后必须重新核验本 Note：`NativeConversationBackend` 或事件 envelope 改名；管理路由或 WebSocket 请求集变化；SQLite schema 版本策略变化；App Server RPC 版本变化；Web 测试脚本、Makefile 门禁或设备资源守卫入口变化。

## 最后验证

2026-08-24，基线 `bd0000ba` 加 `feat/codex-native-conversations-v2` 当前工作树；本地适用门禁已完成。首次 PR CI 的 Linux inode 复用假失败已在测试层修正并完成聚焦复验；修复后的 PR head 和 squash 后 `main` 的 GitHub CI 仍须按各自 SHA 独立核验。
