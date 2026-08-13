# cc-connect-next

这是 [CC Connect](https://github.com/chenhg5/cc-connect) 的独立后继项目，第一阶段重点是彻底完善飞书原生 Card 2.0 的回答体验。

[English](README.md) · [完整安装文档](INSTALL.md) · [飞书配置](docs/feishu.md)

> 当前版本：`0.1.0-beta.1`。它不是 MCP、代理、伴生插件或消息快照方案，也不要求官方 CC Connect 做任何修改；它拥有自己的仓库、命令、数据目录、daemon 和 npm 包。

## 飞书里会看到什么

一次 Agent 回合始终使用同一张、引用原始提问的原生卡片：

1. 收到消息后立即回复一张非空的 `⏳ 正在思考…` 卡片，不再长时间白屏等待。
2. 只展示匿名进度：`推理 N 次 · 工具 N 次`。
3. 工具执行阶段切换为 `⏳ 正在调用工具…`。
4. 答案开始生成时，同一张卡片立即切换为 `✍️ 正在回答`，此前进度随即消失。
5. 有 `card_id` 时通过 CardKit 高频更新 `main_text`，实现更自然的打字机效果；不可用时安全退化为整卡更新。
6. 完成后原卡变为 `✅ Done`；异常时变为 `⚠️ 未完成`。

隐私不是“默认折叠但还能展开”：引擎只记录匿名事件类型，飞书渲染器还会再次丢弃推理文本、工具名称、参数、结果、模型、token、上下文、工作目录和 footer。卡片 JSON 中不存在 `collapsible_panel`。

## 安装

### npm Beta

首个 Beta 发布后：

```bash
npm install -g cc-connect-next@beta
cc-connect-next --version
```

npm 包与 GitHub Release 使用相同版本，安装脚本下载对应平台的原生二进制。

### 从当前源码构建

```bash
git clone https://github.com/timmyagentic/cc-connect-next.git
cd cc-connect-next
make build
./cc-connect-next --version
```

首次执行 `cc-connect-next` 会在 `~/.cc-connect-next/config.toml` 创建权限收紧的模板，然后填入飞书应用凭证即可。

## 从官方 CC Connect 迁移

迁移是显式操作，不会停止、卸载或修改官方 CC Connect：

```bash
cc-connect-next migrate --dry-run
cc-connect-next migrate
cc-connect-next --config ~/.cc-connect-next/config.toml
```

迁移内容包括配置、sessions、projects、cron/timer、本地 provider 配置和绑定状态；`data_dir` 会改写为 `~/.cc-connect-next`。日志、socket、锁、重启通知和 daemon 元数据不会复制。目标目录已有内容时默认拒绝，只有明确传入 `--force` 才会合并覆盖同名文件。

自定义路径：

```bash
cc-connect-next migrate \
  --source /path/to/official-data \
  --target /path/to/next-data \
  --dry-run
```

## 推荐飞书配置

新配置模板默认包含：

```toml
[display]
mode = "compact"
card_mode = "rich"
thinking_messages = false
tool_messages = false
show_context_indicator = false
reply_footer = false

[[projects]]
name = "my-project"

[projects.agent]
type = "codex"

[projects.agent.options]
work_dir = "/absolute/path/to/project"

[[projects.platforms]]
type = "feishu"

[projects.platforms.options]
app_id = "${FEISHU_APP_ID}"
app_secret = "${FEISHU_APP_SECRET}"
reply_to_trigger = true
done_emoji = "Done"
```

需要恢复继承自上游的旧消息展示时，可以显式设置 `card_mode = "legacy"`。

## 与官方版本并存

| 隔离边界 | 官方 CC Connect | cc-connect-next |
|---|---|---|
| 命令 | `cc-connect` | `cc-connect-next` |
| 数据 | `~/.cc-connect` | `~/.cc-connect-next` |
| macOS 服务 | `com.cc-connect.service` | `com.cc-connect-next.service` |
| Linux 服务 | `cc-connect.service` | `cc-connect-next.service` |
| API socket | `~/.cc-connect/run/api.sock` | `~/.cc-connect-next/run/api.sock` |

两者可以同时安装，但不要让它们同时使用同一个飞书应用凭证建立 WebSocket：两个消费者可能争抢或重复处理消息。并行验收请使用单独的飞书测试应用；正式切换时再停止官方 daemon。安装和迁移本身不会影响官方实例。

回滚只需要：

```bash
cc-connect-next daemon stop
cc-connect daemon start
```

官方数据始终保留。

## 交给 Agent 一键执行

```text
请从 https://github.com/timmyagentic/cc-connect-next 安装 cc-connect-next。
先确认操作系统、CPU 架构以及 cc-connect 是否正在运行。
不要停止、卸载、覆盖或修改官方 CC Connect。
Beta 已发布时使用 npm 包，否则从当前源码构建。先执行
`cc-connect-next migrate --dry-run`，确认目标目录确实是
~/.cc-connect-next，再执行真实迁移。验证版本、配置文件权限、独立 daemon 名称
和独立 API socket。不要让两个运行时同时连接同一个飞书应用。
```

## 开发与验证

```bash
make web
go test ./...
make build-noweb
```

飞书卡片与迁移的聚焦测试：

```bash
go test ./platform/feishu -run TestBuildRichCard -count=1
go test ./core -run TestProcessInteractiveEvents_RichCard -count=1
go test -tags no_web ./cmd/cc-connect -run TestMigrateLegacyData -count=1
```

## 来源与许可证

cc-connect-next 以 CC Connect v1.4.1 为初始基线，并保留完整 Git 历史。归属说明见 [NOTICE](NOTICE)，MIT 条款见 [LICENSE](LICENSE)。
