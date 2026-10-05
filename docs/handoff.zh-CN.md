# 技术交接

先读 [索引](README.md)、[更新记录](../CHANGELOG.md) 和 [开发规则](../AGENTS.md)。Go module 名称为 `github.com/chenhg5/cc-connect`。

## 架构与入口

`cmd/cc-connect` 负责配置、CLI、daemon 和 MCP；`core` 负责消息、命令、会话、卡片和翻译；`agent/codex` 管理独立 Codex app-server；`platform/feishu` 接收消息及卡片事件并发送/更新卡片。core 不导入具体 agent/platform，通过能力接口调用。

| 内容 | 主要入口 |
|---|---|
| MCP stdio、initialize、EOF | `cmd/cc-connect/codex_plugin.go` |
| 无控制台 Windows 启动器 | `tools/windows-plugin-launcher/main_windows.go` |
| Codex 桌面生命周期、后台复用 | `daemon/windows.go` |
| 后台进程组、无窗口标志、定向清理 | `agent/codex/proc_windows.go`、`appserver_session.go` |
| 原始输入语言、消息/会话快照 | `core/message_locale.go`、`message.go`、`engine.go` |
| 翻译与命令规范化 | `core/i18n.go` |
| 状态、Token、真实容量、额度格式 | `core/status_footer_locale.go`、`engine.go` |
| Codex Token 与额度映射 | `agent/codex/context_usage.go`、`appserver_session.go` |
| 飞书按钮语言、单卡、折叠与限长 | `platform/feishu/card.go`、`feishu.go` |

## 启动生命周期

Windows MCP 定义启动 `cc-connect-plugin.exe`；它查找同目录 `cc-connect.exe`，追加固定 `codex-plugin` 子命令，转交 stdio 和退出码。启动器使用 GUI 子系统，子进程设置无窗口属性。启动器缺失或与主程序分目录会导致插件连接失败。

Windows 任务计划也通过同目录 GUI 启动器的 `--supervisor <脚本路径>` 模式创建无窗口 PowerShell。守护脚本使用 .NET `ProcessStartInfo` 的 `UseShellExecute=false`、`CreateNoWindow=true` 创建后台；不要恢复 `Start-Process -WindowStyle Hidden`，默认 Windows Terminal 会创建可见或最小化窗口。Windows daemon 安装会先检查配套启动器存在，再修改任务。

UTF-8 包装程序遇到 detached 工具进程没有控制台代码页时，以 `CREATE_NO_WINDOW` 重新执行自身，获得无窗口控制台后设置代码页再启动真实 PowerShell。不要用 `AllocConsole` 后隐藏窗口，或直接删除编码修复。回归测试检查原生 `GetConsoleWindow()` 为 0、中文输出及退出码。

MCP initialize 请求已有 daemon manager 启动后台，重复连接复用同一个后台。每个 MCP 客户端断开 stdio 后，只退出自己的桥接进程；单次聊天关闭不能停止其他聊天仍在使用的后台。Windows supervisor 在 Codex 桌面开启期间维持连接，桌面关闭后停止所管理的后台及子进程树。原有守护重启、日志查看和多聊天复用保持不变。

Codex app-server 启动前调用 `prepareCmdForKill`，Windows 同时设置新进程组、`HideWindow` 和 `CREATE_NO_WINDOW`；Unix 设置独立进程组。清理仅针对保存的进程对象/进程树。不要隐藏用户共享终端或按进程名称全局终止。

## 语言流程

平台的实际回调入口是 `Engine.handleMessage`，`ReceiveMessage` 只是公开包装。语言必须在实际入口确定，否则测试会通过而真实消息不生效。

自动模式按原始文本解析，斜杠命令仅取原始命令名；先确定语言，再替换别名、转换本地化命令、合并 ExtraContent。本地化命令表优先确定语言；同名简体/繁体入口默认简体。自然语言沿用现有字符检测，混合文本无法精确推断用户偏好时可手动固定。

`Message.Language` 随排队消息传递，执行状态保存本轮语言；命令和卡片渲染使用独立 I18n 快照，不能修改共享语言对象来临时切换。队列开始下一轮时换成对应消息快照。`I18n.SetLang` 才持久化，自动检测不调用保存回调。配置 `auto` 重启后仍保持自动模式。

`Card.Language` 由飞书渲染写入按钮 value；回调用 `LocalizedCardAction` 将语言交给 core，规范化命令不触发英文检测。旧卡片缺少字段时用该会话最近语言回退。增加卡片适配器时应接入相同元数据规则，保留原动作值以兼容删除选择和权限处理。

富卡片平台可实现可选 `LocalizedRichCardSupporter`，旧 `RichCardSupporter` 继续有效。执行中工具区展开，完成、失败、停止后收起。停止保留已有工具结果与统计；失败/关闭通道不会伪造未收到的指标。

## 指标口径

模型/推理使用会话运行时值，再回退 agent。Codex `thread/tokenUsage/updated` 的 **Last** 是最新请求快照，不是整轮累计或五小时用量。Input 包含 CachedInput，不重复相加；输出 0 可以是有效值。ContextWindow 使用运行时真实容量，使用量显示绝对 Token 与百分比，不写死产品宣传容量。

额度优先读取当前 app-server 的 `account/rateLimits/read` 和 `account/rateLimits/updated`，按接口窗口长度 300/10080 分钟识别 5 小时/7 天。百分比是已用；不计算价格，不从 API Token 数估算 ChatGPT 配额。

app-server 缓存原始 UsageReport 30 秒；agent 回退缓存同样保存原始数据，渲染时才翻译。错误和过期数据不延长有效期。绝对 resetsAt 转为主机本地时间并标 UTC 偏移；重置已过的窗口隐藏，直到新数据到达。接口：[官方文档](https://learn.chatgpt.com/docs/app-server#6-rate-limits-chatgpt)。

## 构建与验证

需要 go.mod 指定版本及以上的 Go。全平台构建保持 CLI 模式；只有 Windows 启动器使用 `-H windowsgui`。

```powershell
go build ./...
go build -o cc-connect.exe ./cmd/cc-connect
go build -ldflags '-s -w -H windowsgui' -o cc-connect-plugin.exe ./tools/windows-plugin-launcher
go test ./core -run 'TestCUJ|TestRichFooter|TestQuota_|TestMessageLocale|TestRichCard_Stop' -count=1
go test ./agent/codex -run 'TestBackgroundCodex|TestAppServer|TestMapAppServer' -count=1
go test ./tools/windows-plugin-launcher ./tools/windows-utf8-shell -count=1
go test ./platform/feishu -count=1
go test ./...
```

Windows 完整测试可能遇到已有 Unix 假 CLI、HOME/path 和计时用例失败，需与旧提交比较；Linux CI 跑完整 race/coverage、CUJ、冒烟、回归与性能检查。启动器测试检查 PE 子系统、真实 stdio/EOF 和退出码。新增修复必须增加能体现旧行为问题的回归测试。

安装前备份用户配置、程序、MCP 源定义及已安装缓存。等待机器人任务空闲，再替换后台和重载本插件；保留 Codex 桌面。UTF-8 包装程序的 daemon PATH 前缀不能在重装过程中丢失。回退需同时恢复二进制和对应定义。源仓库不保存个人配置或登录材料。

## 发布与限制

新改动先记 [CHANGELOG](../CHANGELOG.md) 的 Unreleased。Windows 插件及 daemon 需要配套 GUI 启动器；发布与安装渠道应同时分发主程序和启动器。可选 UTF-8 包装程序需与安装脚本一起分发。测试阶段不新建标签。

桌面和飞书仍有独立 writer，无法同时控制同一桌面执行会话；本轮没有开放桌面 stdio 或修改桌面应用。公开包装与平台实际回调、最终卡片与流式卡片、旧卡片按钮与新卡片按钮都应单独测试，避免只覆盖一条路径。
