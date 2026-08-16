# cc-connect-next

这是 [CC Connect](https://github.com/chenhg5/cc-connect) 的独立后继项目，第一阶段重点是彻底完善飞书原生 Card 2.0 的回答体验。

[English](README.md) · [完整安装文档](INSTALL.md) · [飞书配置](docs/feishu.md) · [回答卡片契约](docs/feishu-card-contract.md) · [迁移兼容矩阵](docs/migration-compatibility.md) · [上游飞书能力对齐](docs/upstream-feishu-parity-2026-08-15.md) · [上游 beta.3 审计](docs/upstream-v1.5.0-beta.3-audit.md)

> 当前版本：`0.1.0`（首个正式稳定版）。它不是 MCP、代理、伴生插件或消息快照方案，也不要求官方 CC Connect 做任何修改；它拥有自己的仓库、命令、数据目录、daemon 和 npm 包。

## 飞书里会看到什么

一次 Agent 回合始终使用同一张、引用原始提问的原生卡片：

1. 收到消息后立即回复一张非空的 `⏳ 正在思考…` 卡片，不再长时间白屏等待。
2. 只展示匿名进度：`推理 N 次 · 工具 N 次`。
3. 工具执行阶段切换为 `⏳ 正在调用工具…`。
4. 答案开始生成时，同一张卡片立即切换为 `✍️ 正在回答`，此前进度随即消失。
5. 有 `card_id` 时通过 CardKit 高频更新 `main_text`；即使 Agent 一次性返回整段答案，也会先保留可感知的“正在回答”与打字机阶段再完成，不可用时安全退化为整卡更新。
6. 完成后原卡变为 `✅ 已完成`；异常时变为 `⚠️ 未完成`。

隐私不是“默认折叠但还能展开”：引擎只记录匿名事件类型，飞书渲染器还会再次丢弃推理文本、工具名称、参数、结果、模型、token、上下文、工作目录和 footer。卡片 JSON 中不存在 `collapsible_panel`。

项目也已经补入官方 CC Connect 在原始分叉点之后合并的飞书能力：话题级工作区绑定、首次 @ 时补入已有话题根上下文、带隐私门禁的引用文件按需下载、Relay 可见消息留在原话题，以及机器人间 `mention_map`。这些能力围绕 Next 已有的 Card 2.0 与迁移安全边界重新适配，并不是整段合并上游。真正解析成功的原生 @ 是“一回合一张卡片”的有意例外：飞书不会从卡片触发机器人 @ 事件，所以 Next 会发送一条可追踪、保留引用关系的文本答案，再删除过程卡；不含原生 @ 的回答仍保持 CardKit 卡片。

## 安装

### npm

```bash
npm install -g cc-connect-next
cc-connect-next --version
```

有预发布版本时，`@beta` 仍指向最新的那一个：

```bash
npm install -g cc-connect-next@beta
```

npm 包与 GitHub Release 使用相同版本。安装脚本先下载同一 Release 的 `checksums.txt`，精确匹配当前平台归档并验证 SHA-256，校验通过后才解压和原子替换二进制。

### 从当前源码构建

```bash
git clone https://github.com/timmyagentic/cc-connect-next.git
cd cc-connect-next
make build
./cc-connect-next --version
```

首次执行 `cc-connect-next` 会在 `~/.cc-connect-next/config.toml` 创建权限收紧的模板。该模板由 `cc-connect-next feishu setup` 套用的同一份推荐飞书配置渲染而来，需要你填的每个值都标记为 `REPLACE`。只要还有 `REPLACE` 没替换，启动就会拒绝执行，并指名是哪个键、下一步该做什么——而不是先自称运行正常、再连不上。

`cc-connect-next doctor` 会检查配置、Agent 命令行及其登录态、已配置的平台、本地依赖与网络，全程不建立任何平台连接，因此实例没跑起来时同样可用。

## 从官方 CC Connect 迁移

迁移是显式操作，不会停止、卸载或修改官方 CC Connect：

```bash
cc-connect-next migrate --dry-run
cc-connect-next migrate
cc-connect-next --config ~/.cc-connect-next/config.toml
```

这一条命令会在写入前覆盖三类来源：只读取官方的 `config.toml`、清点配置实际生效的 `data_dir`（包括自定义路径），以及从项目配置、多工作区根目录、项目状态和 workspace bindings 中发现的每个项目内 `.cc-connect`。当配置文件与实际数据目录分离时，它不会清点配置文件旁边的任何兄弟文件或目录。因此配置、会话、项目覆盖、cron/timer/heartbeat、绑定、本地 provider 配置以及项目内暂存的图片和附件都会迁移，但项目仓库、`.env`、备份目录或整个 service home 不会被误带走。Codex、Claude 等 Agent 自己的外部会话库保持原位，原有 session ID 继续有效。

迁移会先为每个源文件计算 SHA-256，在同级 staging 目录构建并校验完整结果；正式启用前还会重新生成一遍完整源清单，任何新增、删除、内容变化、项目发现变化或访问权限变化都会让本次迁移安全失败，不会启用一个不完整的目标。已有目标也会在 staging 前建立包含内容、类型、权限和属主的快照，复制后重新校验，在每个目标正式切换前再次校验，并在原子 rename 后通过备份路径做最后一次比对；如果另一个 cc-connect-next 进程在迁移期间新建或改写目标状态，即使是已打开的写入句柄恰好在 rename 边界落盘，尤其是 `--force` 合并期间，命令也会恢复并原样保留较新的目标，而不会启用陈旧的 staging 副本。清单稳定后才以原子 rename 逐个启用目标；如果后续目标启用失败，每个已经启用的目标都会先完整移动到唯一的 `.failed-migration-*/preserved` 恢复目录，再恢复迁移前备份。回滚绝不会删除可能包含切换后新写入的数据，错误信息会列出全部恢复路径。所有目标都会先解析真实路径并按文件系统 identity 比较；只要与任一官方源目录重叠就会拒绝执行，符号链接父目录和大小写不敏感卷上的仅大小写别名也不例外。实际生效的全局 `data_dir` 与项目内 `.cc-connect-next` 都会保留源目录/文件权限与属主，兼容 `run_as_user` 穿透读取；若自定义目标的中间父目录不存在，迁移新建的父目录会继承对应源根目录的穿透权限与属主，已经存在的父目录绝不改动。重写后的 `config.toml` 仍是单独生成的 `0600` 文件。日志、socket、锁、重启通知和 daemon 元数据不会复制，源符号链接会跳过。目标非空时默认拒绝；显式使用 `--force` 表示有意合并。只要目标原先存在，即使它是空目录，也会完整保留为带时间戳的 `*.pre-migration-*` 备份，并同时记录在报告和 manifest 中。最终生成 `migration-manifest.json`，逐项记录源、目标、大小和 SHA-256。只有明确不需要项目内图片和附件时才使用 `--skip-project-data`。

官方实例仍可保持安装和运行；如果它恰好在迁移窗口持续写入持久数据，命令会要求你在较安静的时刻重跑，而不是静默漏掉新文件。

自定义路径：

```bash
cc-connect-next migrate \
  --source /path/to/official-data \
  --target /path/to/next-data \
  --source-version v1.5.0-beta.3 \
  --dry-run
```

当前兼容矩阵覆盖官方 v1.4.1 与 v1.5.0-beta.1 至 beta.3 的已知持久化布局。默认 `--source-version auto` 不执行 daemon 元数据中记录的二进制，而是直接校验实际 TOML schema、正常启动所需的语义、当前构建已注册的 Agent/平台和持久化目录；已知版本也可以显式记录。缺失 Agent/平台、无效展示模式、无法保留行为的新字段或未安装插件都会在写入前失败，详见[迁移兼容矩阵](docs/migration-compatibility.md)。

相对形式的 `data_dir`、`work_dir` 和 `base_dir` 会优先按官方 daemon 记录的工作目录解析。即使 `--source` 指向单独的配置目录，迁移仍会从官方 `$HOME/.cc-connect/daemon.json` 读取这份元数据，不会信任任意配置目录旁边的同名文件。元数据格式损坏，或其中记录的工作目录已经不存在、无法访问时，预检会直接失败，不会改用另一个目录解析相对路径。如果没有配置 `data_dir`，迁移也会严格沿用 `$HOME/.cc-connect` 默认值，即使 `--source` 指向 `/etc/cc-connect` 等自定义配置根也不会漏掉真实状态；自定义配置根中只复制 `config.toml`。若 daemon 元数据已过期，或官方版本一直由手工命令启动，请显式传入 `--runtime-work-dir /原运行目录的绝对路径`；该参数优先级最高。

出于安全考虑，如果实际生效的 `data_dir` 包含官方配置根目录（例如 `data_dir = "~"`），迁移会直接拒绝执行。即使自定义 `data_dir` 与配置根完全分离，也只会按当前支持的官方版本明确拥有的持久路径清点：会话、项目状态与模型缓存、cron/timer、绑定、heartbeat/目录历史、MiniMax 本地配置、微信状态、Agent prompt 和 Matrix 加密状态。只要出现意外的普通文件或目录，预检就会直接失败；因此配置放在 `/etc`、数据却误指向整个 service home 时，也不会把 SSH 密钥、浏览器资料或其他无关数据递归带走。请先把官方安装指向专用数据目录并确认状态，再重新迁移；命令不会静默生成不完整目标。

配置路径中的环境变量沿用官方 CC Connect 的 `${NAME}` 占位符语法，但迁移进程必须实际拥有每个被引用的变量；变量未设置时会安全拒绝，而不会用空字符串误选其他目录。配置了但尚未创建的 `data_dir` 会按空目录处理，已有配置文件仍能正常迁移。如果可选的项目内数据无法读取，或项目状态 / binding 元数据已损坏，全局迁移仍会继续，且这些元数据文件仍会原样复制；每个跳过的发现来源都会输出并写入 `migration-manifest.json`。授权或修复元数据后应重新迁移，确认项目内数据完整。

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
hide_agent_footer = true

[[projects]]
name = "my-project"

[projects.agent]
type = "codex"

[projects.agent.options]
work_dir = "/absolute/path/to/project"

[projects.references]
normalize_agents = ["codex", "claudecode"]
render_platforms = ["feishu"]
display_path = "smart"
marker_style = "emoji"
enclosure_style = "code"

[[projects.platforms]]
type = "feishu"

[projects.platforms.options]
app_id = "${FEISHU_APP_ID}"
app_secret = "${FEISHU_APP_SECRET}"
reply_to_trigger = true
done_emoji = "Done"
# resolve_mentions = true
# mention_map = { Reviewer-Bot = "ou_reviewer_bot_open_id" }
```

需要恢复继承自上游的旧消息展示时，可以显式设置 `card_mode = "legacy"`。

完整的状态转换、隐私边界、降级规则、多语言覆盖和可执行验收命令见[飞书回答卡片契约](docs/feishu-card-contract.md)。

## 与官方版本并存

| 隔离边界 | 官方 CC Connect | cc-connect-next |
|---|---|---|
| 命令 | `cc-connect` | `cc-connect-next` |
| 数据 | `~/.cc-connect` | `~/.cc-connect-next` |
| macOS 服务 | `com.cc-connect.service` | `com.cc-connect-next.service` |
| Linux 服务 | `cc-connect.service` | `cc-connect-next.service` |
| API socket | `~/.cc-connect/run/api.sock` | `~/.cc-connect-next/run/api.sock` |

两者可以同时安装，但不要让它们同时使用同一个飞书应用凭证建立 WebSocket：两个消费者可能争抢或重复处理消息。并行验收请使用单独的飞书测试应用；正式切换时再停止官方 daemon。安装和迁移本身不会影响官方实例。

正式切换服务时，迁移后的配置路径与官方原运行目录必须分别传入。若此前用独立飞书测试应用启动过 Next，应先停掉测试实例。停止官方 CC Connect 后、启动生产 Next 前必须再做一次最终迁移，把前一次测试迁移之后新增的会话、绑定、定时器和项目状态同步过来。此时目标已经存在，所以显式使用 `--force`；命令会先把整个旧目标保留为带时间戳的备份，再从已经静止的官方源刷新：

```bash
cc-connect daemon stop
cc-connect-next migrate --dry-run --force
cc-connect-next migrate --force
cc-connect-next daemon install \
  --config ~/.cc-connect-next/config.toml \
  --work-dir /原运行目录的绝对路径
```

最终同步必须重复此前用过的所有自定义 `--source`、`--target`、`--runtime-work-dir` 参数，并在启动前检查新的 manifest 和备份路径。本次刷新以官方配置为准；应从测试目标备份中有选择地重新应用确实需要的 Next 专属设置，绝不能恢复过期的测试应用凭证。最终迁移失败时不要启动 Next，应先重启官方 daemon，再处理命令报告的源目录、权限或并发问题。迁移命令会输出检测到的 `Official runtime work_dir`，请原样使用。`daemon status` 会同时显示两条路径，安装后的 launchd、systemd 或 Windows 计划任务都会显式传入迁移配置。

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
~/.cc-connect-next，再执行真实的一键迁移；检查 migration-manifest.json，并报告所有
带时间戳的迁移前备份。验证版本、配置文件权限、独立 daemon 名称
和独立 API socket。正式切换时，先停止官方 CC Connect，再依次执行
`cc-connect-next migrate --dry-run --force` 与 `cc-connect-next migrate --force`，
检查新的 manifest 和备份，然后同时传入
`--config ~/.cc-connect-next/config.toml`，并把 `--work-dir` 设为迁移输出的
`Official runtime work_dir` 原值。最终迁移失败时重启官方 daemon，不要启动 Next。
不要让两个运行时同时连接同一个飞书应用。
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
