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
- 远程 Runtime 的 `connection_generation` 不能只在 control 内存递增；control 重启后必须从 `control.db` 的最后 checkpoint 继续，否则新连接会复用旧代际。每个连接还需要独立 context 和任务等待，断线后旧 RPC 响应与旧原生订阅不得进入新连接。
- Runtime 原生订阅的取消函数和事件泵必须由同一个登记项拥有。旧订阅退出时只删除自身登记，不能按 workspace/thread key 无条件删除后来建立的新订阅；连接释放需要先取消再等待事件泵退出。
- 设备撤销不能只写数据库标记。Broker 必须在同一操作中摘除并关闭活动 WebSocket、释放附件并记录审计事件，否则已认证连接会在“离线”显示后继续工作。
- Runtime 版本激活在本机写入 `pending-activation.json` 后才切换 `current`，并且只有候选 control 的 `runtime/update/confirm` 能清除看门狗；未确认、启动失败或超时都恢复上一槽。control 的不可取消部署阶段从停止 server 的提交点开始，提交前必须最后检查取消。
- bootstrap 与 Runtime installer 应用真实脚本夹具验证幂等、权限和部分状态。已有 Runtime 身份必须在任何下载或槽切换前校验 server URL；不同控制面必须在产生本地修改前失败。

## 远程控制面与 Runtime 的 v0.1.0 验证

- 2026-08-24 在 `e492ec49` 基线和 `codex/control-runtime-v0.1.0` 工作树上完成控制面重构验证。Web `pnpm test` 为 9 个文件、51 个测试通过，`pnpm build` 通过；只保留既有 500 kB chunk 提示。
- Go 使用仓库声明的 `GOTOOLCHAIN=go1.25.1` 和最多 2 workers。控制面、Runtime、远程后端、Release、Core/Codex/SQLite/配置/企业微信聚焦测试通过；`go build -p=2 ./...`、`go vet -p=2 ./...`、`go mod tidy -diff`、`go mod verify` 和 `CI=1 go test -p=2 -parallel=2 ./...` 均通过。
- 受影响包 race 通过：`./core ./agent/codex ./storage/workspacechat ./config ./platform/wecom ./controlstore ./controlplane ./runtimeclient ./runtimeidentity ./runtimeprotocol ./remotenative ./releaseinstall`。`golangci-lint v2.11.4` 使用 Go 1.25.1 运行，结果为 `0 issues`。
- macOS 已 `Wait` 的旧进程组可能对负 PGID 返回 `EPERM`；只有直接子进程的 `os.Process` 同时确认 `os.ErrProcessDone` 时才能将其视为幂等终态。对应 Claude Code 回归连续 20 次通过。
- OpenCode 后台模型刷新先原子写磁盘、再提交内存状态。测试必须等待已有的 `refreshWg`，不能把“磁盘已变化”当作 goroutine 已完成；对应回归连续 20 次通过。
- 严格仓库基础审计再次为 `0 error / 0 warning / 0 exception`，`bash -n deploy/bootstrap.sh deploy/install-runtime.sh` 和 `git diff --check` 通过。生产代码中没有旧 management token、`template_project`、旧 daemon/Web 命令、旧工作区事件或服务器本地 Codex 后端；`thread/read(includeTurns=false)` 仍只用于 metadata/cwd 校验。
- Release 输入必须同时满足“本地存在”和“Git 已跟踪”。首个 `v0.1.0` tag run 因 `scripts/` 整目录忽略，导致 manifest 生成器只存在于本机、全新 checkout 缺失；Release workflow 现在在安装工具链和交叉编译前显式检查 manifest 生成器、bootstrap 与 OpenResty 模板。

## Docker 正式部署通道

- 2026-08-24 在基线 `c9701214` 的工作树新增正式 Docker 通道。容器中只有 control，server 仍由 control 通过私有 Unix Socket 监管；宿主 systemd 只管理 `cc-connect-deploy-host`。control 不挂 Docker Socket，只通过只读挂载的 `/run/cc-connect-deploy/host.sock` 请求固定目标操作。
- deploy-host 只接受单一协议指纹和 `latest/status/prepare/activate/commit/cancel/confirm`，并用 `SO_PEERCRED` 限制 UID。Docker/cosign 命令固定仓库 `ghcr.io/shusfun/cc-connect`、GitHub OIDC identity、Compose project 与 service；API 不接受仓库、路径或命令。Web 在宿主在线时支持更新/回滚，离线时返回结构化 `container_host_unavailable`，不切换旧路径。
- control 负责活动操作检查、Runtime 暂存/确认和 `control.db` 备份；deploy-host 负责 control 容器替换、确认超时和失败回滚。候选确认同时比对运行中 control 版本、activation 目标和 host 状态。数据库、环境或 Compose 恢复失败时保持容器停止；成功回滚后恢复数据库所有权为 UID/GID 10001。
- Compose 使用 `/var/lib/cc-connect-docker/control` 与 `/var/lib/cc-connect-docker/app` 固定 bind 持久目录。签名 `bootstrap-container.sh` 从 deploy-host 制品安装二进制与 Compose，只创建一个 systemd service。Release workflow 从后续 tag 起构建两个 Linux 架构的 deploy-host、八个 manifest 目标和签名 GHCR 镜像；既有 `v0.1.0` Release 不包含这些制品。

## 当前限制与候选优化

- 无真实企业微信凭据时只能验证适配、命令与伪边界，线上 WebSocket 连接和真实附件投递仍是交付风险。
- settings、collaboration mode、分页历史和 realtime 属于 App Server 能力探测范围；上游缺失时应验证明确不可用错误，不能改走旧 RPC。
- 当前任务没有获得浏览器自动化授权，不运行浏览器或 Playwright；Web 以单测、类型检查和构建为验证入口。
- 不设置 `CI=1` 的全仓测试会执行两个真实 Cursor CLI 集成：`agent/acp/TestCursorCLI_ACPHandshake` 因本机未登录返回 `Device not configured`，`agent/cursor/TestFetchModelsFromAgentCLI` 返回空模型。两项源码都明确在 CI 跳过，因此 GitHub unit 同口径使用 `CI=1`；本机真实 Cursor 登录链路仍未验证。
- macOS launchd 文件模式测试已改为显式创建 plist 并设为 `0644`，在资源守卫的 `umask 077` 下连续 20 次通过。外置卷 `rename invalid argument` 在 Engine、Hook、Cron 和 CUJ 生命周期收口后未再复现；若再次出现，仍须追踪真实写入者，不能归类为环境波动。

## 失效条件

以下任一项变化后必须重新核验本 Note：`NativeConversationBackend` 或事件 envelope 改名；管理路由或 WebSocket 请求集变化；SQLite schema 版本策略变化；App Server RPC 版本变化；Web 测试脚本、Makefile 门禁或设备资源守卫入口变化；Docker 基础镜像 digest、Compose 持久卷、`deployment` capability 或 Release 镜像签名流程变化。

## 最后验证

2026-08-24，基线 `c9701214` 加当前 Docker 部署工作树；本地适用门禁已完成：Web 9 个文件 53 个测试和生产构建通过；Go 1.25.1 聚焦测试、`go build -p=2 ./...`、`go vet -p=2 ./...`、`CI=1 go test -p=2 -parallel=2 ./...`、受影响部署包 race、`go mod tidy -diff`、`go mod verify` 均通过。`golangci-lint v2.11.4` 通过 `go run` 固定版本执行为 `0 issues`，`actionlint v1.7.8` 通过；所有部署脚本 `bash -n`、Compose 配置解析、严格仓库基础审计和 `git diff --check` 通过。

按 macOS 设备规则未启动 Docker，也未运行浏览器自动化、真实服务器或真实企业微信连接；Docker 镜像冷/暖构建、容器健康检查、systemd/deploy-host 实机权限、GHCR 推送与 cosign 在线验证留给包含本变更的下一次 tag Release。已发布的 `v0.1.0` 没有镜像、deploy-host 或容器 bootstrap，本次没有创建 tag 或 Release。

`v0.1.1` tag 的 Signed Release run `32754800828` 在多架构镜像冷构建中失败：Dockerfile 已把唯一根 `pnpm-lock.yaml` 复制到 `/src`，却调用 `pnpm --dir web install --frozen-lockfile`，BuildKit 中 pnpm 因 `/src/web/pnpm-lock.yaml` 不存在而返回 `ERR_PNPM_NO_LOCKFILE`。仅切换工作目录或添加 `--lockfile-dir` 仍会分别触发缺少锁文件或 importer 不匹配；根锁已有 `web` importer，但 Dockerfile 没有复制仓库现有的 `pnpm-workspace.yaml`。修复复制唯一根 workspace 契约，在 `/src` 执行一次 `pnpm install --frozen-lockfile`，随后进入 `/src/web` 构建；不得复制第二份锁或关闭 frozen。隔离临时目录按 Docker 顺序执行根 frozen install、复制 Web 源码和生产构建已通过，331 个包全部来自锁定依赖图。`deploy/scripts_test.go` 固化这条约束，禁止恢复 `pnpm --dir web install`。`v0.1.1` tag 不移动，修复通过新提交和下一补丁版本发布。
