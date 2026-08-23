# 统一工作区聊天验证路径

适用于同时修改 `core/workspace_chat*`、`agent/codex` App Server、SQLite、Web 和企业微信接线的任务。

## 推荐顺序

1. 先运行 Web 构建及受影响包聚焦测试，避免把接口或类型错误带入全量门禁。
2. App Server 先用伪服务测试分页、读取、创建、复用、事件/审批隔离、断线重连和 `turn/interrupt`。
3. Core 聚焦测试覆盖 REST、WebSocket、企业微信 CUJ、thread FIFO、审批、取消和关闭排队。
4. 运行全部 CUJ、`go vet ./...`、增量 `golangci-lint --new-from-rev origin/main` 和 `actionlint`。
5. race 优先覆盖 `core` 工作区测试、`agent/codex` 工作区测试和 `storage/workspacechat`，发现竞争后先做确定性回归测试再重跑。
6. 最后运行 `go test ./...` 和严格构建基础审计；全量门禁只运行一次，除非修复确实影响全仓。

所有构建、测试、lint 和长驻服务都必须通过设备资源守卫运行，Go 使用 `-p=2 -parallel=2`。

## 已核验的环境型失败

- `actions/setup-go` 的 `go-version-file: go.mod` 使用 `go` 指令，不会按 `toolchain` 指令安装版本；两者 patch 不同时会污染构建缓存。不要把 `go` 与 `toolchain` 改成相同 patch，因为 `go mod tidy` 会删除冗余的 `toolchain`；CI 应显式安装根 `toolchain` 声明的版本。
- `agent/acp` 的 `TestCursorCLI_ACPHandshake` 需要本机 Cursor Agent 登录。
- `agent/cursor` 的 `TestFetchModelsFromAgentCLI` 依赖本机 Cursor CLI 模型枚举。
- macOS 上 `daemon` 的 launchd 状态和文件模式测试受当前用户域及外置卷权限语义影响。
- Core 全包偶发 `TestHandleRelay_*` 的 50ms 时序失败和 `TestCUJ_A3/A5` TempDir 清理竞态；必须单独重跑相关测试判断，不能据此修改工作区聊天代码。
- Core 全包可能在包目录留下空的 `test_ws_*.json` Session 快照。确认内容为空且无进程占用后删除；工作区聊天自身使用无持久化的 `SessionManager`，SQLite 才是选择和 Turn 投递状态的所有者。

这些失败只能作为环境或既有测试风险记录；若同一测试出现新的错误文本、race 报告或命中本次文件，必须按真实回归处理。
