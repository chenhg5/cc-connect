# 引用标准化与平台展示实现记录

本文档记录本轮 `cc-connect` “本地文件/目录/代码位置引用”功能开发的实现范围、解决的问题、关键设计取舍、测试流程与当前联调结果，便于后续继续开发、写 PR 描述、补 issue 跟进和做回归测试。

## 1. 功能目标

本轮实现的目标不是保留原始 Markdown 本地链接源码，而是：

- 识别 `Codex` / `Claude Code` 常见的本地引用格式
- 将其标准化为统一的内部语义
- 在发送到 IM 平台前，渲染成更易读、更稳定的展示形式
- 让同一平台上的普通消息、进度消息、卡片消息尽量遵循同一套展示规则

当前推荐展示样式是：

- `display_path = "relative"`
- `marker_style = "emoji"`
- `enclosure_style = "code"`

即：

- 文件：`📄 \`demo-repo/src/app.ts:42\``
- 目录：`📁 \`demo-repo/src/components/\``

## 2. 本轮新增的配置

当前新增 project 级配置：

```toml
[projects.references]
normalize_agents = ["all"]
render_platforms = ["all"]
display_path = "relative"
marker_style = "emoji"
enclosure_style = "code"
```

配置语义：

- `normalize_agents`
  - 控制第一阶段“引用标准化”对哪些 agent 输出生效
  - 当前支持：`codex`、`claudecode`、`all`
- `render_platforms`
  - 控制第二阶段“平台展示适配”对哪些平台生效
  - 当前支持：`feishu`、`weixin`、`all`
- `display_path`
  - 控制路径主体展示层级
  - 当前支持：`absolute`、`relative`、`basename`、`dirname_basename`、`smart`
- `marker_style`
  - 控制文件/目录前缀标记
  - 当前支持：`none`、`ascii`、`emoji`
- `enclosure_style`
  - 控制路径主体包裹方式
  - 当前支持：`none`、`bracket`、`angle`、`fullwidth`、`code`

当前 feature 默认仍然是增量式的：

- 不配置 `[projects.references]` 时，现有行为保持不变
- 只有命中 `normalize_agents` 和 `render_platforms` 时，才会启用这套逻辑

## 3. 当前实现范围

### 3.1 已支持的 agent

- `Codex`
- `Claude Code`

### 3.2 已支持的平台

- `Feishu`
- `Weixin`

### 3.3 已接入的消息类型

当前引用渲染已接入：

- agent thinking 文本
- agent final response 文本
- stream preview 文本
- progress compact / progress card 中的 agent 文本

### 3.4 明确不处理的消息类型

当前明确不处理：

- 系统命令回复
  - `/workspace`
  - `/status`
  - `/switch`
  - `/help`
  - `/provider`
  - `/dir`
  - 其他 `cc-connect` 自己生成的管理类文本
- tool result 原始输出
- 平台错误消息
- 系统错误消息

这是一个明确的作用域收口：

- 只处理 agent-originated text
- 不污染 `cc-connect` 自己的系统消息和工具原始输出

## 4. 当前支持识别的引用格式

### 4.1 本地路径

- 绝对文件路径
  - `/root/code/demo-repo/src/app.ts`
- 相对文件路径
  - `demo-repo/src/app.ts`
- 绝对目录路径
  - `/root/code/demo-repo/src/components/`
- 相对目录路径
  - `demo-repo/src/components/`

### 4.2 位置型引用

- `path:line`
- `path:line:col`
- `path:start-end`
- `path#L42`
- `path#L42C7`

### 4.3 Markdown 本地链接

- `[app.ts](/root/code/demo-repo/src/app.ts)`
- `[app.ts](/root/code/demo-repo/src/app.ts#L42)`
- `[app.ts](demo-repo/src/app.ts)`

### 4.4 Claude 常见风格

- `` `/root/code/.claude/settings.json:5-10` ``

### 4.5 网页链接

- `[OpenAI](https://openai.com/)`

网页链接会保留原样，不进入本地文件引用渲染逻辑。

## 5. 当前实现中的关键判断逻辑

### 5.1 文件 / 目录 / unknown 判断

当前引用类型判断采用分层策略：

1. 优先基于真实文件系统判断
   - 对可解析到绝对路径的候选，先 `os.Stat`
   - `stat` 成功且是目录 -> `dir`
   - `stat` 成功且是文件 -> `file`

2. `stat` 失败时再走启发式
   - 有 `:line` / `:line:col` / `:start-end` / `#L42` -> `file`
   - 末尾有 `/` -> `dir`
   - 有扩展名 -> `file`
   - 其他 -> `unknown`

### 5.2 unknown 的展示

对于无法可靠判断的路径：

- 不强行判成文件或目录
- 不加 `📄` / `📁`
- 只渲染路径主体

这是为了避免在不存在的 fake path 场景里误导用户。

### 5.3 `relative` 的基准

`display_path = "relative"` 的基准不是 `base_dir`，而是当前 channel 最终绑定出来的 `workspaceDir`。

例如当前联调时：

- `base_dir = /root/code`
- `/workspace bind .`

最终 `workspaceDir` 就是：

- `/root/code`

所以：

- `/root/code/demo-repo/src/app.ts:42`
  -> `demo-repo/src/app.ts:42`
- `/root/code`
  -> `./`

## 6. 本轮解决的主要问题

### 6.1 原始本地 Markdown 链接在 IM 中可读性差

之前像：

```md
[user_profile_service.ts](/root/code/demo-repo/src/services/user_profile_service.ts#L42)
```

在不同平台 / 不同消息类型下会出现：

- 原样显示源码
- 只剩 label
- 被当网页链接
- 视觉上像链接但点击无意义

当前改成统一渲染后，会变成类似：

- `📄 \`demo-repo/src/services/user_profile_service.ts#L42\``

### 6.2 系统消息被误处理

早期实现把引用渲染挂在通用 `send/reply/card` 入口，导致：

- `/workspace bind .`
  -> `✅ 工作区绑定成功: 📄 .`

这是不符合预期的。

当前已经修正为：

- 通用 `send/reply/card` 保持 raw
- 只在 agent 事件输出路径里做渲染

### 6.3 tool result 被误处理

早期实现曾把 tool result 也做了 transform，导致：

- bash 原始输出被替换

当前已经修正为：

- tool use 保持原样
- tool result 保持原样
- 只处理 agent 自己的 thinking / response 文本

### 6.4 相对路径误被绝对路径 matcher 拆分

早期实现里，相对路径中间的 `/src/...` 片段会被绝对路径 matcher 误命中，出现：

- `lean-steward📄 /src/...`

当前已经修复边界检查，不再从相对路径中间错误起跳。

### 6.5 网页链接被本地路径污染

曾出现：

- `[OpenAI](https://openai.com/)`
  被错误替换成
- `OpenAI (📄 /root/code/demo-repo/...)`

原因是 placeholder 编号冲突和分段保护不充分。

当前已修复：

- web markdown link 先整体保护
- placeholder 全局唯一

### 6.6 中文顿号分隔的多个路径被串成一个候选

在 `Claude Code + Feishu` 联调中，出现过：

- 第一句里的第一个路径正确
- 后面多个以 `、` 分隔的路径仍保留绝对路径

根因有两层：

- 候选正则没有正确排除中文标点
- 边界检查按字节取“前一个字符”，在 UTF-8 中文符号前失效

当前已修复：

- 中文分隔符不再被当成路径正文
- 边界检查按 rune 解码

## 7. 本轮联调用到的真实测试环境

为了验证目录 / 文件判断逻辑，本轮在本地创建了真实测试目录：

- `/root/code/demo-repo/README`
- `/root/code/demo-repo/config`
- `/root/code/demo-repo/src/components/profile`
- `/root/code/demo-repo/src/components/profile.ts`
- `/root/code/demo-repo/docs/spec.v1`

这些对象故意覆盖边界情况：

- 无扩展名文件
- 无扩展名目录
- 带扩展名目录

联调目的不是依赖这些对象做仓库测试，而是验证人工联调时真实平台上的显示效果。

后续为了更清楚地展示 `absolute` 与 `relative` 的差异，本轮又新增了一组“外层更深、仓内更短”的 demo repo：

- `/root/code/platform-programs/customer-success/incident-simulations/demo-repo`

其内部结构更收敛为：

- `ui/recovery_contact_form.tsx`
- `svc/recovery_session_reconciler.go`
- `svc/recovery_session_reconciler_test.go`
- `docs/spec.v1/`
- `scripts/recovery`

这样当 workspace 直接绑定到 repo root 时：

- baseline 中的绝对路径会非常长
- 推荐配置下的 `relative` 效果会明显缩短为 `ui/...`、`svc/...`、`docs/...`

## 8. 自动化测试设计与环境依赖处理

### 8.1 测试原则

自动化测试分两类：

1. 纯字符串测试
   - 只验证 transform 行为
   - 不依赖真实文件系统是否存在这些路径

2. 自建临时文件系统测试
   - 用 `t.TempDir()` 创建临时 workspace
   - 在测试内部创建真实文件和目录
   - 验证 `os.Stat` 优先级、目录识别、workspace root 等行为

### 8.2 已修复的环境依赖问题

本轮开发中，曾有一条测试硬编码依赖：

- `/root/code/demo-repo/...`

这会导致换环境后测试不稳。

当前已修复为：

- 使用 `t.TempDir()`
- 在测试内部创建：
  - `demo-repo/README`
  - `demo-repo/src/components/profile`
  - `demo-repo/src/components/profile.ts`
  - `demo-repo/docs/spec.v1`

因此当前新增测试不会依赖开发机上的 `/root/code` 目录结构。

### 8.3 当前关键自动化覆盖点

自动化测试已覆盖：

- feature 默认关闭时不改输出
- `all` scope 展开
- 网页链接保留
- inline code + web link 共存
- `smart` display 在 basename 冲突时回退
- `relative` path 基于 workspace
- 功能关闭时，agent 最终回复保持原文
- 相对路径不被绝对路径 matcher 误切分
- 中文顿号分隔多个路径
- 真实存在但不带 `/` 的目录识别
- workspace root 显示为 `./`
- unknown path 不加 marker
- 通用 `reply/card` 不做 transform
- stream preview 做 transform
- progress payload 中仅 agent 文本做 transform
- tool result 不做 transform

## 9. 当前联调结果

### 9.1 Feishu + Codex

已通过：

- 基础引用展示
- 真实目录 / 文件边界情况
- progress / card 视图

验证结果包括：

- `README`、`config` 显示为文件
- `profile`、`profile.ts`、`spec.v1` 显示为目录
- 本地 markdown 链接统一成相对路径展示
- `/root/code` 显示为 `📁 \`./\``
- tool result 保持原样

补充：

- 已额外用更深外层路径的 demo repo 做了一组更贴近日常工程消息的 before / after 对比
- 在 workspace 绑定到：
  - `/root/code/platform-programs/customer-success/incident-simulations/demo-repo`
  之后，推荐配置下可稳定缩短为：
  - `📄 ui/recovery_contact_form.tsx`
  - `📄 svc/recovery_session_reconciler.go:12:2`
  - `📄 svc/recovery_session_reconciler_test.go:8-17`
  - `📁 docs/spec.v1/`
  - `📄 scripts/recovery`
- 这一组消息同时覆盖了：
  - 长路径 label=长路径 target 的本地 markdown 链接
  - `path:line`
  - `path:line:col`
  - `path:start-end`
  - 目录路径
  - 网页链接

另补 `/show` 真实联调，已成功：

- `/show ui/recovery_contact_form.tsx`
- `/show ui/recovery_contact_form.tsx:11`
- `/show svc/recovery_session_reconciler.go:12:2`
- `/show svc/recovery_session_reconciler_test.go:8-17`
- `/show svc/`

这些用例证明：

- 同一批在 IM 中展示过的相对路径，可以继续直接作为本地查看入口使用
- `/show` 第一版已经覆盖：
  - 文件头部
  - 单点上下文
  - `line:col` 上下文
  - range 展示
  - 目录列表

实际观感结论：

- raw baseline 噪音明显，包括长绝对路径和重复的本地 md link
- 推荐配置下，消息长度和可扫描性明显改善
- 这组素材适合作为 PR 的主 before / after 对比图

### 9.2 Feishu + Claude Code

已通过：

- 基础引用展示
- progress / card 视图
- 中文顿号分隔多个路径的场景

验证结果包括：

- Claude 常见的反引号绝对路径可转成相对展示
- 同一句中多个 `、` 分隔的路径可分别识别和渲染
- thinking / final summary 风格一致

### 9.3 Weixin

已完成基础联调记录。

#### Weixin + Codex

已验证：

- 基础短任务引用展示

结果：

- 通过

验证点包括：

- 绝对路径正确转成相对 `/root/code`
- `README`、`config` 显示为文件
- `profile`、`profile.ts`、`spec.v1` 显示为目录
- 本地 markdown 链接统一成相同展示
- 网页链接保留原样

补充发现：

- Weixin 存在长任务结束后 `sendMessage` 失败的问题，已单独整理为新的调查目录：
  - [docs/prs/2026-04-07-weixin-sendmessage-timeout-investigation/README.md](./prs/2026-04-07-weixin-sendmessage-timeout-investigation/README.md)
- 因此 Weixin 本轮以“短任务、高覆盖”联调为主，避免将该平台既有问题与本 feature 混淆。

#### Weixin + Claude Code

已验证：

- 基础短任务引用展示
- 短 progress / 多步骤说明场景

结果：

- 通过

验证点包括：

- 绝对路径正确转成相对 `/root/code`
- 文件 / 目录图标与路径展示正确
- 本地 markdown 链接统一成相同展示
- 过程文本与最终总结风格一致

### 9.4 展示 preset 对比矩阵

为了避免频繁改配置和重启服务，本轮额外增加了一个本地调试脚本：

- [`/.devtools/reference_render_send_matrix.go`](/tmp/cc-connect-ref-render/.devtools/reference_render_send_matrix.go)

它会：

- 读取一段固定的模拟 agent 输出
- 本地调用 `TransformLocalReferences(...)`
- 用不同 preset 分别渲染
- 再通过当前活跃 session 直接发到 Feishu / Weixin

本轮实际对比了 6 组 preset：

- `absolute-none-none`
- `relative-emoji-code`
- `basename-ascii-bracket`
- `dirname-basename-none-angle`
- `smart-emoji-fullwidth`
- `dirname-basename-ascii-code`

这 6 组里，前 5 组构成一套“最小值覆盖集”：

- `display_path`
  - `absolute`
  - `relative`
  - `basename`
  - `dirname_basename`
  - `smart`
- `marker_style`
  - `none`
  - `ascii`
  - `emoji`
- `enclosure_style`
  - `none`
  - `bracket`
  - `angle`
  - `fullwidth`
  - `code`

额外保留：

- `dirname-basename-ascii-code`

作为“非 emoji 候选样式”参考。

#### Weixin 上的观感结论

Weixin 上这 6 组都能正常显示。

观察结果：

- `absolute-none-none`
  - 适合作 baseline，但可读性最差
- `relative-emoji-code`
  - 信息量和可读性最平衡
  - 是当前最推荐的默认样式
- `basename-ascii-bracket`
  - 太短，重名时歧义风险高
- `dirname-basename-none-angle`
  - 较干净，但没有文件/目录类型提示
- `smart-emoji-fullwidth`
  - 更短，但更容易丢失上下文
- `dirname-basename-ascii-code`
  - 适合不喜欢 emoji 的场景，可作为备选

#### Feishu 上的观感结论

Feishu 上同样完成了这 6 组对比。

观察结果：

- `absolute-none-none`
  - 仍然只是 baseline
- `relative-emoji-code`
  - 观感最好
  - 但 Feishu 对 `code` 包裹的视觉保留不明显，实际更接近 `relative-emoji-none`
- `basename-ascii-bracket`
  - 视觉较重，不适合作默认
- `dirname-basename-none-angle`
  - 很干净，但缺少文件/目录类型提示
- `smart-emoji-fullwidth`
  - 路径压缩更激进，信息损失偏大
- `dirname-basename-ascii-code`
  - 是不错的“无 emoji”候选，但不如 `relative-emoji-code` 直观

补充：

- `[OpenAI](https://openai.com/)` 在这轮 matrix 消息里仍然是网页链接
- 只是复制文本时通常只会看到 `OpenAI` label，不能据此判断“链接丢失”

后续仍建议按照 [reference-rendering-manual-test-plan.zh-CN.md](./reference-rendering-manual-test-plan.zh-CN.md) 继续补齐更细粒度记录，例如：

- Weixin 的更多真实工作流 prompt
- 更长但仍控制在回复窗口内的多步骤任务

## 10. 当前已确认的设计结论

### 10.1 应只处理 agent 输出

本功能的正确作用域是：

- agent thinking
- agent response
- progress preview / progress card 中的 agent 文本

而不是：

- `cc-connect` 自己的系统消息
- tool result

### 10.2 平台内要统一，而不是只改某一种消息类型

如果只处理最终文本消息，会导致：

- 最终回复一种样式
- 卡片另一种样式
- progress 再一种样式

所以当前实现明确覆盖：

- final response
- streaming preview
- progress compact / progress card

### 10.3 `stat` 优先于启发式

对于目录 / 文件判断，最可靠的是先用真实文件系统判断，再做启发式 fallback。

这也是为什么：

- 不带扩展名目录
- 带扩展名目录
- workspace root

都能在真实存在的情况下被正确识别。

### 10.4 对 fake path 要保守

如果路径不存在、无扩展名、又没有明确位置语义：

- 不要强行加 `📄`
- 不要强行加 `📁`
- 用 `unknown` 更稳

## 11. 当前仍未纳入的问题域

以下问题当前不属于本轮 feature 范围：

- 飞书中点击本地路径后的跳转行为
  - 这属于 IM 平台能力限制
- `Codex` / `Claude Code` 自身是否总是保留网页 markdown link
  - agent 可能主动改写文本
- Feishu 的某些现有上下文回显 / 回复串展示细节
  - 这不是本地引用渲染逻辑本身的问题

## 12. 后续建议

### 12.1 继续补 Weixin 联调记录

建议补齐：

- Weixin + Codex
- Weixin + Claude Code
- 普通消息 + 进度消息

### 12.2 PR 中建议体现的内容

建议在 PR 描述中突出：

- 问题背景
- 两阶段设计
- 只处理 agent 输出，不处理系统消息 / tool result
- `relative + emoji + code` 的推荐展示
- preset matrix 对比结论
- Feishu 前后对比图
- `/show` 的 5 个代表性示例：
  - `ui/recovery_contact_form.tsx`
  - `ui/recovery_contact_form.tsx:11`
  - `svc/recovery_session_reconciler.go:12:2`
  - `svc/recovery_session_reconciler_test.go:8-17`
  - `svc/`
- 自动化测试已去除本地环境依赖

### 12.3 若继续扩展平台

后续扩展到其他平台时，建议继续保持当前分层：

- 第一层：引用标准化
- 第二层：平台展示适配

不要重新回到“每个平台各自用正则临时处理”的模式。
