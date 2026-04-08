# 引用标准化与平台渲染设计

本文档整理了关于 `cc-connect` 中“本地文件/目录/代码位置引用”处理的调研结果、设计目标、测试结论与建议实现方案。

讨论的直接背景是：

- Codex、Claude Code 在输出文件引用时风格不同
- 飞书等 IM 平台无法真正像本地 TUI/IDE 一样理解本地文件链接
- 当前 `cc-connect` 对链接的处理主要是平台内临时适配，缺少统一的“引用标准化”抽象

目标不是保留所有原始 Markdown 链接源码，而是：

- 识别不同 CLI 风格的引用
- 在平台无关层做统一标准化
- 在平台相关层做一致、易读、可配置的展示适配
- 让文本消息、卡片消息、进度卡消息在同一平台上尽量遵循同一套引用展示逻辑

---

## 1. 基本出发点

### 1.1 问题本质

本地引用和网页链接不是一回事。

- 网页链接：通常可直接点击访问
- 本地文件引用：在聊天平台里无法真正打开本地文件内容，最多只是被当作某种链接文本或网页路径处理

因此，对本地引用继续保留类似：

```md
[path](/abs/path)
```

这种源码形式，很多时候只会：

- 重复显示路径
- 降低可读性
- 在不同平台出现不一致的降级行为

所以更合理的方向是：

1. 先识别并标准化引用语义
2. 再针对平台渲染成更适合聊天阅读的展示样式

### 1.2 设计目标

本设计追求以下目标：

- 平台无关层负责识别和统一表示“引用是什么”
- 平台适配层负责决定“引用怎么显示”
- 两层可以独立开启/关闭
- 平台适配不应只对某一种消息类型生效，而应覆盖：
  - 普通文本消息
  - 结构化卡片
  - 进度卡/预览卡
- 对本地引用的展示目标是“易读、好看、稳定”，而不是伪造一个看似可点击但实际无用的链接

---

## 2. 现有 `cc-connect` 实现现状

### 2.1 当前没有“平台无关的引用标准化层”

目前仓库中没有看到一层专门针对 Codex / Claude Code 输出做统一解析的实现。

现有行为基本是：

- agent 输出什么，`cc-connect` 大多原样转发
- 某些平台在发送前再做自己的内容转换

这意味着：

- 目前没有统一识别：
  - 本地绝对路径
  - 相对路径
  - `path:line`
  - `path:line:col`
  - `path:line-range`
  - Markdown 本地文件链接
- 也没有把这些都收敛成统一的内部引用对象

### 2.2 现有类似逻辑基本都属于“平台适配层”

当前最接近“平台适配”的实现包括：

- 飞书：
  - `buildReplyContent(...)`
  - `sanitizeMarkdownURLs(...)`
  - 卡片 markdown / post md / post rich-text 的不同分支处理
  - 进度卡中的重复清洗逻辑
- Telegram：
  - `core.MarkdownToSimpleHTML(...)`
  - 用于把 Markdown 转成 Telegram HTML

这些逻辑的共同特点是：

- 平台相关
- 以最终 transport 格式为中心
- 不关心“引用语义抽象”

### 2.3 当前发送链路需要统一覆盖的消息类型

如果未来做引用处理，不能只改普通 `content string`，因为 `cc-connect` 的消息发送路径不止一条。

至少要覆盖这三类：

1. 普通文本消息
   - `Engine.send(...)`
   - `Engine.reply(...)`

2. 结构化卡片
   - `replyWithCard(...)`
   - `sendWithCard(...)`
   - `CardSender`

3. 进度卡 / 预览卡
   - `ProgressCardPayload`
   - 各平台的 progress card 渲染逻辑

如果只处理第一类，会导致：

- 普通回答一种展示
- 卡片另一种展示
- 进度卡再另一种展示

这与“同一平台内一致展示”的目标相违背。

### 2.4 上游最新 `main` 结论

已在 `/tmp/cc-connect-link-design` 拉取上游最新 `main`（调研时对应提交 `92d4d81`）。

结论：

- 上游最新版本也没有成型的“统一引用标准化层”
- 现状仍然是平台各自处理
- 因而本设计不是为了适配某个刚合入的新框架，而是一个新增抽象

---

## 3. Codex 与 Claude Code 的实际引用风格调研

### 3.1 Claude Code 的真实输出风格

对指定 session：

```text
claude --resume 266f8cdc-f3f7-48fd-9a00-7c4319c76606
```

进行了历史记录检查。

其中用户要求：

```text
请帮我引用一下settings.json 这个文件的5-10行。
```

Claude 最终输出为：

```text
可以，`/root/.claude/settings.json:5-10` 内容如下：
```

也就是说，这次真实会话里 Claude 使用的是：

- 反引号包裹的绝对路径
- 后接行范围 `:5-10`

而不是 Markdown 本地文件链接。

### 3.2 Claude Code 独立样例输出

单独让 Claude 输出多种示例后，得到的典型形式包括：

- `/root/code/demo/src/app.ts`
- `/root/code/demo/src/`
- `` `/root/code/demo/src/app.ts` ``
- `/root/code/demo/src/app.ts:42`
- `/root/code/demo/src/app.ts:42:7`
- `/root/code/demo/src/app.ts:42-48`
- `[app.ts](src/app.ts)`
- `[app.ts](src/app.ts#L42)`
- `[app.ts](src/app.ts):42`
- `src/app.ts`
- `src/app.ts:42`
- `[Example](https://example.com)`

可见 Claude 常见风格是混合的：

- 纯路径
- `path:line[:col]`
- `path:line-range`
- 相对 Markdown 链接
- 网页 Markdown 链接

### 3.3 Claude 源码中的相关提示与 UI 机制

在 `tools/cloud-code` 中调研到：

- 系统提示要求：
  - bash 场景尽量使用绝对路径
  - final response 中 relevant file paths 应该优先 absolute，不要 relative
- TUI/UI 内部存在 `FilePathLink` 组件
  - 将绝对路径转为 `file://...`
  - 再封装为 OSC 8 终端超链接

这说明 Claude 内部更偏向：

- 自然语言里给绝对路径
- 终端渲染层再把路径变成终端超链接

并没有看到像 Codex 那样强约束的“统一 Markdown 文件引用格式”。

### 3.4 Codex 的实际输出风格

独立让 Codex 输出多种示例后，得到的典型形式包括：

- `/root/code/demo/src/app.ts`
- `/root/code/demo/src/`
- `` `/root/code/demo/src/app.ts` ``
- `/root/code/demo/src/app.ts:42`
- `/root/code/demo/src/app.ts:42:7`
- `/root/code/demo/src/app.ts:42-45`
- `[app.ts](/root/code/demo/src/app.ts)`
- `[app.ts](/root/code/demo/src/app.ts#L42)`
- `[app.ts](/root/code/demo/src/app.ts):42`
- `src/app.ts`
- `src/app.ts:42`
- `[OpenAI](https://openai.com)`

这与当前 Codex 环境中的文件引用规范相匹配：

- 更常见地使用 Markdown 文件引用
- 并支持：
  - 绝对路径
  - `#L42`
  - `:42`
  - `:42:7`
  - 范围形式

### 3.5 调研结论

如果未来只支持一种引用识别规则，会不够。

更现实的解析优先级应覆盖：

1. 绝对路径
2. `path:line`
3. `path:line:col`
4. `path:line-range`
5. Markdown 本地文件链接
6. Markdown 本地文件链接 + `#L42`
7. Markdown 本地文件链接后接 `:42`
8. 相对路径 / 相对 Markdown 链接
9. 网页链接（Markdown 或裸 URL）
10. `file://...`

---

## 4. 引用标准化之后的下一阶段能力

完成“识别引用并把它渲染得更易读”之后，最自然的下一步是：

- 允许用户直接基于一个引用查看文件、目录或代码片段

也就是说，从：

- “看懂 agent 发来的引用”

进一步走到：

- “直接消费这个引用”

### 4.1 为什么需要这一步

当前用户如果想基于一个引用继续查看内容，通常只能手写：

- `/shell ls -la ...`
- `/shell sed -n '1,80p' ...`
- `/shell nl -ba file | sed -n '40,70p'`

这虽然能做到，但有明显问题：

- 命令负担高
- 需要用户自己区分文件 / 目录 / line / range
- 与已经做好的引用标准化能力脱节

因此更合理的产品化方向是新增一个“引用感知查看命令”。

### 4.2 命令命名建议

当前更推荐：

- `/show <引用>`

而不是：

- `/open <引用>`

原因：

- `/show` 更接近“查看”
- 不像 `/open` 那样容易让人误解成“打开/进入”
- 与现有 `/dir`、`/memory`、`/shell` 风格更一致

### 4.3 输入范围

第一版建议只支持“纯引用文本”：

- 绝对路径
- 相对路径
- `path:line`
- `path:line:col`
- `path:start-end`
- `path#L42`
- Markdown 本地文件链接
- 目录路径

不要求第一版支持：

- `📄 ui/recovery_contact_form.tsx`
- `[FILE] demo-repo/README`
- `【components/profile/】`

也就是不做“展示层反解析”。

### 4.4 默认行为

`/show <引用>` 自动分流：

1. 文件，无位置
   - 显示文件前 `80` 行

2. `path:line` / `path#L42`
   - 显示该点附近上下文
   - 默认前后各 `8` 行

3. `path:line:col`
   - 与单点上下文一致
   - 仅在标题保留列号

4. `path:start-end`
   - 显示精确 range

5. 目录
   - 显示一级目录列表
   - 默认最多 `50` 项

### 4.5 展示方式

建议保持简单：

- 文件 / snippet / range
  - 一行标题
  - 下面代码块展示

- 目录
  - 一行标题
  - 下面普通文本列表

这样更符合 Feishu / Weixin 当前能力，不需要复杂交互。

### 4.6 与本设计的关系

这项能力不是独立的新系统，而是建立在当前已完成抽象之上的直接延伸。

它将复用：

- 本地引用解析
- `workspaceDir` 相对路径解析
- 文件 / 目录 / unknown 判断
- `line` / `line:col` / `range` / `#Lxx` 识别

因此，它适合作为：

- “引用标准化与平台渲染”之后的第二部分功能

---

## 4. 飞书平台的实际测试

### 4.1 测试方法

向实际飞书会话发送了一组测试消息，覆盖：

- `text`
- `post + md`
- `post rich-text`
- `interactive card markdown`

同一批样例包含：

- 裸网页 URL
- 网页 Markdown 链接
- 绝对本地路径
- 本地路径加行号
- 本地 Markdown 绝对链接
- 本地 Markdown 相对链接
- `file://` 链接
- `#L42`
- `:42`

### 4.2 API 层结果

测试结果如下：

- `text`：成功
- `post + md`：成功
  - 网页 markdown link 成功
  - 绝对本地 markdown link 成功
  - 相对本地 markdown link 成功
  - `file://` markdown link 成功
- `post rich-text`
  - `a.href=https://...` 成功
  - `a.href=/root/...` 失败，错误码 `230001 invalid href`
- `interactive card markdown`：成功

### 4.3 客户端展示结果

根据飞书客户端截图观察，实际效果如下。

#### `text`

- 裸 `https://...` 会自动识别成网页链接
- Markdown 网页链接也能显示为链接文本
- 绝对路径、`path:line` 只是普通文本
- 本地 Markdown 文件链接不会被好好渲染为“文件引用”

#### `post + md`

- 网页链接展示正常
- 对本地 Markdown 链接，往往只保留 label
  - 如只显示 `app.ts`
  - URL / path 本体不显示
- 这意味着如果 label 不带信息，会丢失引用细节

#### `post rich-text`

- HTTP(S) `a.href` 正常
- 本地路径 `href` 会被 API 直接拒绝
- 所以这条路径不适合承载本地文件引用

#### `interactive card markdown`

- 网页链接显示正常
- 本地绝对路径链接、相对路径链接、`file://` 链接都会显示为蓝色 label
- 纯文本路径和 `path:line` 也能稳定显示
- `[app.ts](/root/...):42` 这类形式可以保留 `app.ts:42` 的视觉效果

但点击后实际行为是：

- 飞书会把它当成网页路径去打开
- 并不能打开本地文件内容

### 4.4 飞书测试结论

飞书中的“本地引用”应视为：

- 一种展示样式
- 不是可用的本地跳转能力

因此：

- 不应该为了保留“可点击”而保留一堆原始 Markdown 链接源码
- 更应该追求：
  - 易读
  - 清晰
  - 信息不丢失
  - 在文本消息与卡片消息里风格一致

---

## 5. 设计原则

### 5.1 两阶段模型

设计采用两个阶段：

#### 第一阶段：引用标准化

职责：

- 平台无关
- 面向 agent 输出风格
- 识别并统一表示引用语义

输入：

- Codex / Claude Code / 其他 agent 的原始文本

输出：

- 统一的引用对象列表
- 以及一份被替换/标注后的中间内容

这一层不应该决定 Feishu、Telegram、Weixin 怎么渲染。

#### 第二阶段：平台渲染适配

职责：

- 平台相关
- 面向展示效果
- 根据第一阶段的标准化结果决定最终怎么显示

输出：

- 适合该平台的最终文本 / markdown / card 内容

### 5.2 两阶段都应可独立开关

需要支持：

- 只开第一阶段，不做平台适配
- 第一阶段 + 第二阶段都开
- 两个都关，维持原始行为

### 5.3 位置语义不应再单独配置

讨论后决定：

- 不增加额外的 `show_location` 配置项
- 引用中的位置粒度应保留 agent 原始语义

例如：

- `app.ts:42` 保持单行
- `app.ts:42:7` 保持行列
- `app.ts:5-10` 保持范围
- 只引用文件名就只显示文件

也就是说，标准化时应记录：

- 是单行、行列、范围，还是无位置
- 展示阶段只改变“路径怎么显示”和“是否加标记”

---

## 6. 建议的数据模型

建议在 `core/` 中引入统一引用对象，例如：

```go
type RefKind string

const (
    RefKindWeb  RefKind = "web"
    RefKindFile RefKind = "file"
    RefKindDir  RefKind = "dir"
)

type RefLocationKind string

const (
    RefLocationNone      RefLocationKind = "none"
    RefLocationLine      RefLocationKind = "line"
    RefLocationLineCol   RefLocationKind = "line_col"
    RefLocationLineRange RefLocationKind = "line_range"
)

type Reference struct {
    Kind         RefKind
    LocationKind RefLocationKind

    Raw          string
    Label        string

    Target       string
    PathAbs      string
    PathRel      string
    WorkspaceDir string
    IsRelative   bool

    LineStart    int
    LineEnd      int
    Column       int
}
```

说明：

- `Raw`：原始命中的文本
- `Label`：如果原始是 markdown link，则记录其 label
- `Target`：原始链接 target
- `PathAbs / PathRel`：标准化后的路径信息
- `LocationKind`：保留原始位置语义

---

## 7. 收敛后的配置项

讨论后，最终建议的核心展示配置项保留为三个。

### 7.1 `display_path`

决定路径主体如何展示。

候选值：

- `absolute`
  - `/root/code/demo/src/app.ts:42`
- `relative`
  - `src/app.ts:42`
  - 相对路径应优先基于当前 session/workspace 的 `workdir`
  - 如果无法安全相对化，应回退为 absolute
- `basename`
  - `app.ts:42`
- `dirname_basename`
  - `src/app.ts:42`
  - 比 `basename` 更稳，重名更少
- `smart`
  - 优先短显示
  - 出现歧义再升级显示更完整的路径

### 7.2 `marker_style`

决定是否加前缀标记。

候选值：

- `none`
  - `src/app.ts:42`
- `ascii`
  - `[FILE] src/app.ts:42`
  - `[DIR] src/components/`
- `emoji`
  - `📄 src/app.ts:42`
  - `📁 src/components/`

### 7.3 `enclosure_style`

决定是否给路径主体做包裹强调。

候选值：

- `none`
  - `📄 src/app.ts:42`
- `bracket`
  - `📄 [src/app.ts:42]`
- `angle`
  - `📄 <src/app.ts:42>`
- `fullwidth`
  - `📄【src/app.ts:42】`
- `code`
  - `📄 \`src/app.ts:42\``

讨论结论：

- `enclosure_style` 只包裹路径主体
- 不建议把 marker 一起包进去

例如：

- 推荐：
  - `📄 \`src/app.ts:42\``
- 不建议默认：
  - `[📄 src/app.ts:42]`

---

## 8. 推荐默认展示方案

推荐默认值：

```toml
display_path = "dirname_basename"
marker_style = "emoji"
enclosure_style = "code"
```

效果：

- `📄 \`src/app.ts:42\``
- `📄 \`settings.json:5-10\``
- `📁 \`src/components/\``

为什么推荐这组：

- 比原始 Markdown 链接干净
- 比纯路径更容易和正文区分
- 跨平台稳定
- 在飞书里即使没有真正的本地文件跳转，也能清晰表达“这是一个文件引用”

如果不想用 emoji，可选保守默认：

```toml
display_path = "dirname_basename"
marker_style = "ascii"
enclosure_style = "none"
```

效果：

- `[FILE] src/app.ts:42`
- `[DIR] src/components/`

---

## 9. 实现建议

### 9.1 第一阶段：新增平台无关标准化层

建议新增：

- `core/reference_normalizer.go`
- `core/reference_types.go`

职责：

- 扫描文本中的各种引用风格
- 解析出统一 `Reference` 对象
- 生成中间表示

识别优先级建议：

1. Markdown 网页链接
2. Markdown 本地文件链接
3. `path:line:col`
4. `path:line-range`
5. `path:line`
6. 绝对路径
7. 相对路径
8. `file://...`

### 9.2 第二阶段：新增平台适配渲染层

建议新增：

- `core/reference_render.go`
- `core/reference_render_config.go`

职责：

- 根据 `Reference` 和配置生成平台无关的“展示文本”
- 再由平台决定是否包装成 markdown / HTML / card 元素

注意：

- 第二阶段不应重新解析原始字符串
- 应该只消费第一阶段的结构化引用对象

### 9.3 需要接入的现有路径

至少需要覆盖：

1. 普通文本消息
   - `Engine.send(...)`
   - `Engine.reply(...)`

2. 卡片消息
   - `replyWithCard(...)`
   - `sendWithCard(...)`
   - 对 `CardMarkdown.Content`
   - 以及 plain-text fallback 的 `Card.RenderText()`

3. 进度卡
   - `ProgressCardPayload`
   - 平台对 progress payload 的渲染

### 9.4 平台适配层的实际建议

#### Feishu

- 本地引用不应再作为真正本地跳转链接保留
- 应优先追求统一视觉文本展示
- 同一平台内：
  - 普通消息
  - post
  - card markdown
  应尽量使用同一种引用文本渲染结果

不建议：

- 只在某一种消息类型里特殊处理
- 只在 `post-md` 生效而 `card markdown` 不生效

#### Telegram

- 当前走 `MarkdownToSimpleHTML(...)`
- 后续可让标准化结果在进入 HTML 转换前先重写为更适合 Telegram 的文本

#### Weixin / WeCom / 其他平台

- 如果平台本身没有强链接能力
- 直接使用统一的文本展示反而更稳

---

## 10. 为什么不直接保留原始 Markdown 链接

这是本设计最重要的产品判断之一。

原因：

1. 平台并不能真正打开本地文件
2. 同一链接在不同消息类型中的降级行为不同
3. `path + label` 往往重复信息，降低可读性
4. 实际价值不在“伪链接”，而在“用户一眼知道这是哪个文件/目录/位置”

因此，合理目标应当是：

- 不保留冗余的源码形式
- 保留引用语义
- 用更适合聊天阅读的样式展示

---

## 11. 最终结论

当前 `cc-connect` 的链接处理能力，主要是零散的“平台内适配”，还没有：

- 平台无关的引用标准化层
- 覆盖文本/卡片/进度卡的一致引用渲染框架

结合 Codex、Claude Code 与飞书的实际测试，推荐采用：

### 阶段一：引用标准化

- 平台无关
- 针对 agent 输出风格
- 把不同风格本地引用统一为结构化引用对象

### 阶段二：平台展示适配

- 平台相关
- 针对最终展示效果
- 用统一配置把本地引用渲染为清晰、好读的文本样式

### 最终收敛的配置项

- `display_path`
- `marker_style`
- `enclosure_style`

位置粒度不配置，而是继承 agent 原始引用语义。

推荐默认展示方案：

```text
📄 `src/app.ts:42`
📄 `settings.json:5-10`
📁 `src/components/`
```

这比保留 `[path](path)` 一类源码形式更适合 IM 展示，也更符合飞书等平台的真实能力边界。

---

## 12. 配置命名与作用域设计

这一部分补充讨论“这些配置该叫什么、放在哪一层、哪些应该共用、哪些应该平台独有”。

### 12.1 现有 `cc-connect` 配置风格

当前仓库里的配置大致分成三层：

1. 全局强类型 section
   - 例如：
     - `[display]`
     - `[stream_preview]`
     - `[rate_limit]`
     - `[outgoing_rate_limit]`
   - 这类配置的特点是：
     - 面向整个进程
     - 与具体某个 project 无关
     - 通常有明确的结构体类型

2. project 级强类型字段 / section
   - 例如：
     - `[[projects]]`
     - `[projects.heartbeat]`
     - `[projects.auto_compress]`
     - `[projects.users]`
   - 这类配置的特点是：
     - 作用于“一个 agent + 一个 work_dir + 多个平台”的组合
     - 属于项目共用行为
     - 不应该绑到某个具体平台

3. agent / platform 私有 `options`
   - 例如：
     - `[projects.agent.options]`
     - `[projects.platforms.options]`
   - 这类配置的特点是：
     - 只对某个 agent backend 或某个平台生效
     - 当前仓库里大部分是 flat key 风格
     - 如：
       - agent：`model`、`mode`、`provider`
       - feishu：`enable_feishu_card`、`progress_style`、`thread_isolation`
       - telegram/feishu/discord：`share_session_in_channel`

因此，这次“引用标准化 + 平台展示适配”的配置边界，最好也沿用这套结构，而不是新造一套完全不同的配置风格。

### 12.2 第一层为什么不应该放进 `agent.options`

讨论中的第一层是：

- 处理 Codex / Claude Code 输出中的本地引用格式
- 对不同风格做标准化
- 结果会被所有下游平台复用

这层虽然“面向 agent 输出”，但它本质上不是 agent runtime 配置。

它不改变：

- `codex` / `claudecode` 如何运行
- 用什么模型
- 权限模式是什么
- 是否 `yolo`

它改变的是：

- agent 输出结果在 `cc-connect` 内部如何被理解
- 后续发往多个平台前，如何形成统一的引用语义

所以它更像：

- project 的消息后处理能力

而不是：

- 某个 agent backend 的私有选项

因此，不建议把第一层配置放进：

```toml
[projects.agent.options]
```

更合适的是 project 级单独 section。

### 12.3 为什么不建议一开始做成全局 `[references]`

从语义上讲，这个能力也可以想象成全局默认行为。

但第一版更合理的范围仍然是 project 级，因为：

1. `display_path = "relative"` 依赖当前 project / workspace 的 `work_dir`
2. 不同项目可能希望不同展示风格
   - 有的项目想保守一些：`basename`
   - 有的项目想信息更全：`relative`
3. 一个 `cc-connect` 进程可能同时服务多个项目，没必要强迫它们共享完全同一套展示风格

所以第一版更建议直接落在：

```toml
[projects.references]
```

如果未来确实需要全局默认值，再考虑：

```toml
[references]
```

作为全局 fallback，而 `projects.references` 做覆盖。

但这不是第一版必须引入的复杂度。

### 12.4 推荐的主配置范围：`[projects.references]`

推荐新增一个 project 级强类型 section：

```toml
[projects.references]
normalize_agents = []
render_platforms = []
display_path = "dirname_basename"
marker_style = "emoji"
enclosure_style = "code"
```

建议语义如下：

- `normalize_agents`
  - 第一阶段“引用标准化”对哪些 agent 生效
  - `[]` 表示默认关闭
  - `["codex", "claudecode"]` 表示只对这两个 agent 开启
  - `["all"]` 表示对当前实现已支持的全部 agent 开启
- `render_platforms`
  - 第二阶段“引用展示重写”对哪些平台生效
  - `[]` 表示默认关闭
  - `["feishu", "weixin"]` 表示只在这两个平台发送前改写展示
  - `["all"]` 表示对当前实现已支持的全部平台开启
- `display_path`
  - 路径主体如何显示
- `marker_style`
  - 是否加文件/目录标记
- `enclosure_style`
  - 是否给路径主体加包裹强调

之所以把这五个放在同一个 section，是因为它们共同描述的是：

- 某个 project 的“引用处理管线”

而不是平台 transport 或 agent runtime。

### 12.4.1 `normalize` 与 `render` 的依赖关系

虽然内部实现仍然是“两阶段”：

1. normalize
2. render

但对外配置不再暴露 `normalize = true` / `render = true` 这种布尔开关。

更准确的外部语义是：

- 哪些 agent 允许进入 normalize
- 哪些平台允许应用 render

当前设计下，`render` 不是一个脱离 `normalize` 单独运行的能力。

也就是说：

- 只有当当前 project 的 agent 命中 `normalize_agents`
- 且当前发送目标平台命中 `render_platforms`
- 且文本中成功识别出了结构化引用

第二阶段的 render 才会真正发生。

换句话说：

- `render_platforms` 是“允许在哪些平台上应用引用重写”
- 但它依赖于前面已经完成了 normalize

因此当前实现语义上应理解为：

- `normalize_agents` 决定“哪些输入来源参与这套功能”
- `render_platforms` 决定“这些已标准化引用在哪些平台上被重写展示”

这也意味着，如果：

- `normalize_agents = []`

那么即使：

- `render_platforms = ["feishu", "weixin"]`

也不会产生任何实际效果。

这是有意为之，因为第二阶段不应重新猜测原始字符串，而应只消费第一阶段得到的结构化引用对象。

### 12.5 Feishu / Weixin / Telegram 的共用配置如何处理

如果同一个 project 同时挂：

- Feishu
- Weixin
- Telegram

那么最自然的共享方式就是：

- 它们默认共同继承 `[projects.references]`

也就是说：

- “飞书和微信的共同配置”
- 不需要额外再造一个 `feishu_weixin_shared_*` 前缀

project 级默认值本身就是共享层。

这也符合本次设计的核心思想：

- 第一层与平台无关
- 第二层先有一个 project 共用默认渲染策略
- 只有当某个平台确实需要特殊化时，才单独 override

### 12.6 平台专有覆盖应放在哪里

当某个平台必须偏离 project 默认值时，才需要 platform override。

推荐作用域：

```toml
[projects.platforms.options.references]
render_platforms = ["all"]
display_path = "basename"
marker_style = "none"
enclosure_style = "none"
```

这样做的优点是：

- 范围清晰：只作用于这个 platform
- 命名整齐：与 `[projects.references]` 同名镜像
- 不会把一堆 `reference_*` 平铺到 `options` 根上

这里虽然当前仓库里还没有大量使用：

```toml
[projects.platforms.options.xxx]
```

这种 nested option table，但对这类“成组出现的新功能配置”来说，这种写法更优雅，也更便于后续扩展。

#### 如果团队坚持保持 `options` 扁平风格

则次优方案是：

```toml
[projects.platforms.options]
reference_render_platforms = ["all"]
reference_display_path = "basename"
reference_marker_style = "none"
reference_enclosure_style = "none"
```

这也能工作，但缺点是：

- key 比较长
- 同类配置分组感更差
- 后续再增加与引用相关的平台私有开关时，`options` 容易继续膨胀

因此，本文更推荐：

- project 级：强类型 `[projects.references]`
- platform 级：`[projects.platforms.options.references]`

### 12.7 为什么平台 override 不应该先行

这次功能的关键不是“给飞书单独修一个链接逻辑”，而是：

1. 先把 Codex / Claude Code 的引用风格标准化
2. 再在平台发送前统一渲染

如果反过来先在 Feishu / Weixin 里各自加开关，会导致：

- 平台之间重复解析
- 文本消息和卡片消息继续各走一套
- 新平台接入时还要再复制一遍逻辑

所以平台 override 应该只是：

- 覆盖默认显示风格

而不是：

- 承担主要语义解析职责

### 12.8 关于 Codex / Claude Code 的 source 配置

理论上可以做一个显式配置，例如：

```toml
[projects.references]
source_hint = "auto"   # auto | codex | claudecode
```

但第一版不建议暴露这个配置。

原因：

1. `project.agent.type` 已经存在
   - `codex`
   - `claudecode`
2. `cc-connect` 每个 project 只绑定一个 agent
3. 引用标准化层完全可以根据当前 `agent.type` 自动决定：
   - 优先启用哪套识别规则
   - 以及是否追加通用 fallback 规则

因此，第一版更建议：

- 不增加新的 source 配置项
- 由实现内部根据 `projects.agent.type` 自动选择解析优先级

只有当未来真的出现：

- 一个 project 中混用多种 agent 输出
- 或需要手动强制某种解析器

再考虑引入 `source_hint`

### 12.9 推荐的第一版配置方案

第一版建议只引入：

```toml
[projects.references]
normalize_agents = []
render_platforms = []
display_path = "dirname_basename"
marker_style = "emoji"
enclosure_style = "code"
```

以及可选的 platform override：

```toml
[projects.platforms.options.references]
display_path = "basename"
marker_style = "none"
enclosure_style = "none"
```

这套方案的优点是：

- 配置范围符合现有 `cc-connect` 结构
- 第一层处理不被错误塞进 `agent.options`
- 飞书/微信/Telegram 默认可共用同一套 project 级策略
- 有平台差异时也有清晰的局部 override 位置
- 未来若继续扩展，不会污染已有的 `display` / `stream_preview` / `platform options` 命名体系

### 12.10 最终命名建议

收敛后的命名建议如下。

#### Project 级

```toml
[projects.references]
normalize_agents = ["codex", "claudecode"]
render_platforms = ["feishu", "weixin"]
display_path = "dirname_basename"
marker_style = "emoji"
enclosure_style = "code"
```

#### Platform 级 override

推荐：

```toml
[projects.platforms.options.references]
render_platforms = ["all"]
display_path = "basename"
marker_style = "none"
enclosure_style = "none"
```

不推荐但可接受的 flat 备选：

```toml
[projects.platforms.options]
reference_render_platforms = ["all"]
reference_display_path = "basename"
reference_marker_style = "none"
reference_enclosure_style = "none"
```

最终设计原则是：

- “语义标准化”归 project
- “展示默认值”也先归 project
- “平台差异”只在必要时进入 platform options
- 不把消息后处理配置塞进 agent runtime options
