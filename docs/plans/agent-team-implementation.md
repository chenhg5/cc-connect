# CC-Connect Team 分期实施计划（v3）

更新：2026-10-03。分支：`feat/agent-team-foundation`。飞书方案：[实施计划](https://bytedance.larkoffice.com/docx/L67kdCJp5oUt0AxZ7VocQq4JnIt)。操作指南：[Team 使用说明](../agent-team.md)。

## 已确认的目标

完整复用 cc-connect 的执行后端、飞书支持、流式回复、卡片、原生命令、会话、工作区、relay、定时任务及配置能力。Team 是可选扩展。Bot 绑定一个执行平台，Team 成员配置 Bot 的角色和身份说明；角色不绑定 Codex、Claude Code 或 Trae。

每创建一个业务项目就新建一个群，群中始终包含管理者。一个 Bot 可参与多个项目群，群 ID 自动确定项目和目录，用户不做 `/project use` 或工作区切换。私聊管理者用于理解需求、创建 Bot 和创建项目。

v3 替代 v2 的精简构建、新 Bot 配置体系和显式项目选择路线。v2 读取与迁移代码保留兼容，不继续扩充该路径；新功能落在普通 `[[projects]]` 与原生 Engine 上。

## 对象和边界

| 对象 | 来源与作用 |
|---|---|
| Bot 运行实例 | 普通 `[[projects]]`，名称为稳定引用，保留 agent/platform/模型/权限等全部原配置 |
| Team | 可选 `[[teams]]`，指定 manager，成员只引用 Bot 名称并配置 role/prompt |
| Project（业务项目） | `team_projects.json`，项目 ID、群 ID、参与 Bot、管理者、目录、目标、创建状态、重试 UUID |
| Workspace / Bot 项目上下文 | 复用 workspace agent pool 和 SessionManager，以 Bot + 项目 ID + 目录区分；会话内按用户/话题区分 |
| 协作交接 | 复用原生 relay，以项目成员表提供绑定，自动执行前检查管理者在群 |

Team 管人员与身份，Project 管目标、群与参与者，Workspace 管代码目录。`/help` 在同一原生卡片中提供团队管理和项目管理分页；`/team` 与 `/project` 为独立入口。旧 `/team project`、`/team projects` 仍兼容，现有 TOML 的 `projects` 名称保留。

每个 Team 有一个指定管理者；首版一个 Bot 只能属于一个 Team，但可以属于该 Team 的多个项目群。项目 ID 独立于目录，相同目录不同项目不共用会话。文件系统、CLI 账号和原生全局配置不自动隔离。

## 分期与验收标准

| 阶段 | 实现内容 | 验收标准 |
|---|---|---|
| P1 原生能力与 Team 配置 | 完整构建；普通 projects + teams；角色 Prompt；兼容迁移并保留旧管理者会话 | 未配置 Team 的原有配置与功能通过回归；/help、/model 等可用；角色和后端独立 |
| P2 项目建群与自动路由 | 管理者创建项目；建新群并加入用户和成员；保存创建进度；项目群继承目录 | 自动包含管理者；重复同参数请求继续原群；权限或部分加入失败不报就绪；不同群无需切换 |
| P3 上下文与协作复用 | 每 Bot 每项目独立 agent/session/模型覆盖；话题路由与卡片；会话历史归属过滤；复用 relay；管理者检查 | 同目录跨项目跨 Bot 不串会话；重启恢复；缺失管理者时自动派工/定时任务失败 |
| P4 配置体验与交付 | 私聊扫码新建 Bot、配置角色/后端、自动认领和启动；已有 Bot 入 Team；文档、服务升级与验收步骤 | 扫码成功后无需手填凭据或再次认领；配置失败可恢复；现有管理者历史保留 |

P1–P4 的代码已经落地，自动化与安装验证记录见下文。真实租户建群、多 Bot 对话和后端账号联调作为用户现场验收，不能用模拟测试替代。

## 接口与关键失败路径

- Team 入口：`/team members`（成员列表与身份表单）、`/team bot create`。Project 入口：`/project list`、`/project create`、`/project settings`。项目群中的 Workspace 入口归入项目设置，普通会话沿用原生命令。
- 管理者自然语言建群：通过注入的本地 `project --config ... --manager ... create ...` 命令完成，只有工具成功才能报告已创建。
- 配置层：`config/teams.go` 校验管理者与引用，角色只追加 Prompt；`config/team_members.go` 保存成员变更。角色变更在重启后生效。
- 核心：`core/team_project.go` 定义平台可选接口、项目登记及路由，不导入具体适配器。Engine 继续处理原生命令。
- 飞书：`platform/feishu/team_group.go` 创建群、添加应用成员、验证管理者。接入使用每个应用自己的凭据，不跨应用复制用户身份。
- 创建恢复：先落盘计划，调用幂等建群接口，保存 chat_id，再加入成员并验证；任一步失败保留记录，相同请求重试。
- 迁移：`team adopt` 输出新配置，保留旧会话目录，拒绝覆盖已有文件；已有业务项目需要显式群映射。

## 当前范围与限制

原有命令保持可用；Team 上下文只过滤其他项目的原生会话，原生权限控制和适配器不支持的能力仍照常报错。项目群内 `/workspace` 与 `/dir` 合并到 `/project settings`，项目目录是唯一有效配置；未关联项目的会话、provider、`/lang` 等继续沿用原生作用域。

群内协作复用 cc-connect relay，不依赖飞书 Bot 互相发送普通文本唤醒。原生转交超时、状态与输出沿用已有能力；本版未建立任务依赖图、统一看板、无限重试或自主全流程发布。没有后台监测管理者退出；在建群结束和自动任务开始时校验，不会自动重新邀请。

上下文与模型隔离已经实现，但共享目录仍可能产生文件冲突。并行写入使用人工分工或独立 worktree。多目录目前仅 Claude Code 适配器支持；项目成员中有不支持后端时创建前明确失败。

## 验证记录

新增自动测试覆盖：普通 Team 配置和后端/角色分离、应用注册后身份绑定、模拟飞书建群请求与部分加入失败、失败恢复不重复建群、manager 强制加入、同目录不同项目/不同 Bot 独立模型和会话、卡片作用域、话题继承目录、外部会话过滤、缺失 manager 的 relay/定时任务拒绝、v2 迁移保留会话路径及凭据占位符。CUJ 通过 ReceiveMessage 驱动完整原生命令和跨群行为。

2026-10-03 最终验证：

- `go test ./...`：45 个有测试的包通过。
- `make test-team`：Team 完整构建下的核心、配置、三类主要后端、飞书及入口测试通过。
- `go test ./core/ -run TestCUJ`：通过。
- `go test -race ./core ./config ./cmd/cc-connect ./platform/feishu -run 'Test(Team|ConfigureTeam|CUJ_Team)'`：通过。
- `go vet ./...`、`make build-team`、`git diff --check`：通过。
- 已将本机 cc-connect-team.service 升级为新产物并迁移配置；启动日志确认旧管理者会话载入、Engine 启动、飞书 WebSocket 连接成功，服务 active，重启计数为 0。旧会话 JSON 文件哈希保持不变。
- 原始二进制、配置和会话已备份到 `/home/bytedance/.cc-connect-team/backups/team-v3-tdn1ot0v`。

同日增补：将 Team / Project 纳入同一原生帮助卡片，拆分 `/team` 和 `/project` 入口，保留旧命令别名。新增单卡片、独立分页、原生命令菜单、旧别名权限、项目创建重试及多步用户流程回归；全量 45 个包、CUJ、针对改动的 race、vet 和完整构建通过。服务已更新且飞书连接正常，本轮备份为 `/home/bytedance/.cc-connect-team/backups/project-help-28_08pug`。

真实飞书群创建、租户权限、多 Bot 执行和 Codex / Claude Code / Trae 联合协作尚待现场验收。未为测试额外创建项目群。

## 统一设置增补（2026-10-03）

项目群仅展示 Project 设置，Workspace 为内部代码目录。已补充项目设置卡片、名称/目标/目录编辑、跨 Bot 目录联动、旧目录冲突显式选择、任务繁忙互斥、新上下文版本及历史保留。Bot 设置展示模型来源、当前项目作用范围和恢复默认模型；账号/后端/身份标记为共享作用域。帮助菜单按项目会话隐藏重复目录入口，普通会话保持兼容。项目成员页查看当前成员，调整群成员不在此增补范围内。

验收：进入项目群 `/project settings` → `/workspace` 展示同一卡片 → 修改目录后其他 Bot/话题使用新目录、其他项目不变 → 存在任务时修改失败 → 旧路由冲突选择一次 → 模型切换/来源显示/恢复默认 → 重启恢复目录与模型。增加了对应回归和 ReceiveMessage 多步 CUJ 测试。

本次验证及安装完成：`go test ./...` 的 45 个有测试的包、全部 CUJ、`go test -race ./core ./cmd/cc-connect -run 'Test(ProjectSettings|Team|CUJ_Team)'`、`go vet ./...` 和完整 `make build-team` 均通过。已安装新二进制，服务 active/running、重启计数 0，日志确认管理者 Engine 和飞书 WebSocket 就绪；安装产物哈希与构建产物一致，已有 JSON 状态文件哈希保持不变。二进制、配置与数据备份：`/home/bytedance/.cc-connect-team/backups/unified-settings-q0prkeez`。飞书方案已同步并读回验证到 revision 34。未为验证新建真实项目群或修改现有项目目录，群内展示与操作等待用户验收。

## 创建表单（2026-10-04）

`/team bot create` 与 `/project create` 无参数时打开填写卡片，完整参数命令继续使用原有流程。Bot 表单提供标识、角色、后端和选填身份说明；项目表单提供标识、代码目录、多选成员和选填目标，管理者自动包含。表单作为结构化输入直接交给原有创建处理器，不拼接为命令文本，不改变会话、目录或创建生命周期。

新增可选 FormCardSender 能力和飞书 schema 2.0 表单渲染、提交分发。平台保留 30 分钟有效的随机引用，绑定原始用户与聊天，核验并消费后走正常 Engine 权限检查。失败重发已填表单，原有同名 Bot/项目重试规则继续生效；未知引用、过期、转发跨聊天、其他用户、重复点击和非法字段拒绝。旧卡片与不支持表单的平台保留原有行为。Help 打开注册表单时仅复用服务端观察到的聊天类型，不信任卡片携带的私聊声明。

验收：管理者私聊 `/team bot create` → 选择角色后端、填写标识 → 提交扫码 → 自动配置启动；`/project create` → 填写标识目录、选成员 → 创建独立群；失败后同参数重试不重复建群。自动化覆盖字段校验、渲染、回调作用域、并发双击、过期/重启、私聊保护、默认成员与必含管理者、实际创建处理器重试，以及 ReceiveMessage 权限和创建用户旅程。

验证完成：`go test ./...` 45 个测试包、core 与 cmd 的全部 CUJ、表单相关 race、`go vet ./...` 和 `make build-team` 通过。已更新服务，管理者 Engine、飞书平台和 WebSocket 就绪，重启计数为 0；构建与安装产物哈希一致，现有 JSON 状态未改写。备份：`/home/bytedance/.cc-connect-team/backups/creation-forms-oeknvcdg`。本次未创建真实 Bot 或项目群，实际客户端填写、扫码和建群等待用户验收。

## Members 身份表单（2026-10-04）

成员身份编辑收敛到 Members：管理者的成员列表提供“配置身份”按钮，打开预填角色和身份说明的表单；提交后复用原有配置保存逻辑，重启生效，执行后端不变。管理者角色固定，普通成员可选角色。非管理者仅有只读成员列表，提交仍检查 Engine 管理员权限和当前 Team 归属。旧 member set 的命令处理、Help 入口及使用说明已移除。Bot 创建表单将“Bot 标识”明确为内部名称，并解释它与飞书 App ID、展示名称的区别。

验证及安装完成：45 个测试包、core/cmd 的全部 CUJ、成员及创建表单相关 race、go vet 和完整 build-team 均通过。服务已更新并连接飞书，管理者 Engine 就绪、重启计数为 0；配置及现有 2 个 JSON 状态文件未改写。备份：`/home/bytedance/.cc-connect-team/backups/member-forms-1ddqq2vy`。自动化验证了列表按钮进入、身份预填、保存后重新打开、清空身份说明、后端不变、管理者保护、权限及旧命令失效；实际客户端操作等待用户验收。

## Bot 名称与统一配置卡片（2026-10-04）

创建及 Members 编辑复用同一表单构造器，字段均为飞书名称、角色、执行后端、身份说明，按钮分别为创建、修改。创建表单随机生成内部标识并绑定服务端命令，重试继续使用原标识；名称支持中文和空格，不参与路径或项目引用。Members 与项目选人读取每个 Bot 自己的飞书应用名称。身份保存后保留已核验的名称供离线展示，原有标识、项目引用和会话路径不变。后端切换沿用已有配置逻辑，并修复保持 Trae 后端时不应重置其模型/参数的问题。

实际核验的外部限制：本机 lark-cli 1.0.89 和本地 CLI 源码中的 `config init --name` 仅表示 profile 名；registration begin 只发送 archetype/auth_method/request_user_info。官方 GET bot/v3/info 可读发布名称，application/v6 applications PATCH 文档仅包含分类/回调等字段，没有名称字段。未伪造名称参数或写入私有接口。当前表单明确提示在扫码页填写名称；完成后核验实际名称并提示差异。已有 Bot 改名提示开发者后台操作，表单改名请求不会伪装为成功，也不会连带保存其他字段。自动带入名称和通过卡片同步飞书改名仍未实现，等待可用接口或用户补充命令。

验证及安装：全量 45 个测试包、全部 core/cmd CUJ、相关四包 race、go vet 和完整 build-team 通过；额外只读核验当前应用的飞书名称成功。服务已更新并连接，管理者 Engine 就绪，重启计数 0；安装产物与构建一致，配置和已有 JSON 状态未改写。备份：`/home/bytedance/.cc-connect-team/backups/bot-profile-esz8p7r2`。统一表单、自动标识、名称读取、角色及后端修改可验收；名称自动预填及改名同步仍为上述明确未完成项。

## Bot 名称只读调整（2026-10-04）

按用户最新确认，替代上一节的名称交互：创建表单取消名称输入，内部标识自动生成、隐藏且固定；Members 编辑共用角色、执行后端、身份说明三项字段，额外只读展示从飞书读取的名称。创建与修改按钮保持区分。创建进度与结果不展示自动标识，扫码完成后读取实际名称。名称预填、名称修改不再属于需求范围。已有注册记录仍可通过原表单引用重试，普通命令和会话逻辑保持兼容。

验证及安装完成：全量 45 个测试包、core/cmd 全部 CUJ、相关四包 race、go vet 与 build-team 通过。覆盖无名称创建、名称查询失败时仍隐藏自动标识、只读名称展示、伪造 name/id/bot 字段不可修改名称或关联、旧注册记录继续重试。服务已更新，管理者 Engine、飞书平台及 WebSocket 就绪，重启计数 0；安装哈希与构建一致，配置与原有 2 个 JSON 状态文件保持不变。备份：`/home/bytedance/.cc-connect-team/backups/bot-readonly-zrda2n2s`。重新打开 Help/创建/Members 获取新卡片即可验收；未为测试创建真实 Bot。

## Help 单卡导航与样式统一（2026-10-04）

Help、Members、项目设置标记为可复用面板，导航产生的卡片和文本命令回复更新点击的原消息；表单提交结果也更新原卡片。直接输入命令以及其他普通消息按钮保留原有回复行为。按消息及点击记录更新目标，异步导航不依赖会话内最后点击的消息，已经离开的页面不再覆盖当前页面。原消息无法更新时保留发送替代回复的退路。

Team、Members、项目设置及创建/修改表单统一采用原生蓝色标题、列表布局和返回入口。表单返回位于表单之外，不触发必填校验，并使离开的表单引用失效；失败提示与保留填写内容的重试表单合并显示。Bot 名称只读和自动内部标识规则保持不变。

回归覆盖真实 Engine 到飞书回调和 HTTP 边界的 Help → Members → 表单 → 返回 → 提交 → 原生命令流程、同会话多卡片、慢导航、旧响应、失败退路、保留重试输入及普通命令兼容；core 增加三步以上的 Help/项目设置用户旅程。

验证及安装完成：全量 45 个测试包、core/cmd 全部 CUJ、相关三包 race、go vet、go build ./... 与 build-team 通过。服务 active/running，管理者 Engine、飞书平台和 WebSocket 就绪，重启计数 0；构建和安装哈希一致，配置及原有 2 个 JSON 状态文件保持不变。备份：`/home/bytedance/.cc-connect-team/backups/help-panel-4c042suz`。重新发送 `/help` 获取新面板；真实飞书客户端的卡片切换和表单填写待用户验收，本轮未创建真实 Bot 或项目群。
