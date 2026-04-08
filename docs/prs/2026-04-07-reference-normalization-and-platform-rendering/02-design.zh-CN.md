# 设计文档

本 PR 的完整设计讨论见：

- [reference-normalization-and-platform-rendering-design.zh-CN.md](../../reference-normalization-and-platform-rendering-design.zh-CN.md)

这里保留本次实现最终采用的设计摘要。

## 两阶段设计

### 第一阶段：引用标准化

目标：

- 识别 agent 输出中的本地引用语义
- 不直接依赖具体平台展示

当前覆盖：

- 绝对路径
- 相对路径
- 文件 / 目录
- `path:line`
- `path:line:col`
- `path:start-end`
- `#L42` / `#L42C7`
- Markdown 本地文件链接
- Claude 常见反引号绝对路径
- 网页链接保留

### 第二阶段：平台展示适配

目标：

- 基于第一阶段结果，在发送到具体平台前做统一展示

当前首批平台：

- `feishu`
- `weixin`

## 第二部分能力：引用感知查看命令

在“引用标准化 + 平台展示适配”完成之后，下一步最自然的能力是：

- 用户可以直接基于一个文件 / 目录 / 代码位置引用查看内容
- 不必手写 `/shell sed`、`/shell nl -ba`、`/shell ls`

这部分已经作为本 PR 的第二阶段能力完成第一版实现。

### 命令名称

当前建议从：

- `/show <引用>`

开始。

选择 `/show` 的原因：

- 语义比 `/open` 更轻，更接近“查看”
- 与现有 `/dir`、`/memory`、`/shell` 风格更一致
- 当前仓库中没有现成 `/show` 命令，不会与已有命令冲突

### 输入范围

第一版只支持“纯引用文本”，不处理展示层包装后的样式。

支持示例：

- `/show /abs/path/file.ts`
- `/show rel/path/file.ts`
- `/show /abs/path/file.ts:42`
- `/show /abs/path/file.ts:42:7`
- `/show /abs/path/file.ts:5-10`
- `/show /abs/path/file.ts#L42`
- `/show [file.ts](/abs/path/file.ts#L42)`
- `/show /abs/path/dir/`

不要求支持：

- `📄 ui/recovery_contact_form.tsx`
- `[FILE] demo-repo/README`
- `【components/profile/】`

也就是说，第一版假设用户复制的是“纯路径 / 纯引用”。

### 默认行为

`/show <引用>` 根据解析结果自动分流：

1. 文件，无位置
   - 显示文件前 `80` 行

2. `path:line` / `path#L42`
   - 显示该点附近上下文
   - 默认上下各 `8` 行

3. `path:line:col`
   - 与单点上下文相同
   - 仅在标题中保留列号

4. `path:start-end`
   - 显示精确 range

5. 目录
   - 显示一级目录内容
   - 默认最多 `50` 项

### 展示形式

第一版建议保持简单稳定：

- 文件 / snippet / range
  - 标题一行
  - 下方代码块展示内容

- 目录
  - 标题一行
  - 下方普通文本列表

示例：

```text
📄 ui/recovery_contact_form.tsx
```

```tsx
const form = ...
...
```

```text
📄 svc/recovery_session_reconciler.go:12:2
```

```go
if !ShouldAcceptRecoveryContact(...) {
    return ErrRecoverySessionExpired
}
```

```text
📁 docs/spec.v1/
- overview.md
- incidents/
- metrics.yaml
```

### 边界与限制

建议的默认限制：

- 文件头部：最多 `80` 行
- 单点上下文：前后各 `8` 行
- range：最多 `120` 行
- 目录列表：最多 `50` 项

超限时返回截断提示，而不是无界展开。

错误处理：

- 路径不存在：`❌ 引用路径不存在`
- 解析失败：`❌ 无法解析引用`
- 目录带行号：`❌ 目录引用不能带行号`
- 读取失败：返回具体错误

### 与当前引用能力的关系

`/show` 不是独立的新解析器，而应复用当前已经建立的基础能力：

- 本地引用解析
- `workspaceDir` 相对路径解析
- 文件 / 目录 / unknown 判断
- `line` / `line:col` / `range` / `#Lxx` 识别

因此它应被视为：

- “引用标准化与平台渲染”之后的下一阶段能力
- 基于已实现抽象的直接产品化使用场景

## 代码实现规划与实际落地

本节记录 `/show` 最终采用的代码结构，便于后续扩展时沿用当前仓库已有抽象，而不是重新发明一套命令路径。

### 1. 命令接入位置

`/show` 最适合直接作为 `Engine` 内建命令接入。

实际修改位置：

- `core/engine.go`
  - `builtinCommands`
  - `handleCommand(...)`
  - 新增 `cmdShow(...)`

理由：

- 它本质上是本地文件系统查看命令
- 与 `/dir`、`/memory`、`/shell` 同层
- 不需要 agent 参与
- 不属于平台能力

### 2. 权限建议

`/show` 已作为 privileged command 接入。

原因：

- 能直接读取本地文件内容
- 安全级别与 `/shell`、`/dir` 接近
- 默认不应对所有用户开放

推荐加入：

- `privilegedCommands["show"] = true`

### 3. 解析层重构建议

当前本地引用解析相关逻辑主要在：

- `core/reference_render.go`

包括：

- `localReference`
- `referenceKind`
- `referenceLocationFormat`
- `parseLocalReference(...)`
- `inferReferenceKind(...)`

这些逻辑已经不再只是“渲染”私有实现，而是未来 `/show` 也要直接复用的基础能力。

本轮已将解析公共部分抽出到：

- `core/reference_parse.go`

新的职责划分：

- `reference_parse.go`
  - 负责解析和分类
- `reference_render.go`
  - 负责把引用变成展示文本
- `reference_show.go`
  - 负责把引用变成查看请求并读取内容

### 4. `/show` 的中间层

不建议把所有逻辑直接塞进 `cmdShow(...)`。

本轮已新增：

- `core/reference_show.go`

用于承接：

- 解析结果 → 查看模式
- 文件/目录读取
- 文本输出拼装

建议的数据结构：

```go
type referenceViewMode string

const (
    referenceViewFileHead referenceViewMode = "file_head"
    referenceViewContext  referenceViewMode = "context"
    referenceViewRange    referenceViewMode = "range"
    referenceViewDir      referenceViewMode = "dir"
)

type referenceViewRequest struct {
    Ref        *localReference
    Mode       referenceViewMode
    Window     int
    MaxLines   int
    MaxEntries int
}
```

并提供类似：

```go
func buildReferenceViewRequest(rawRef, workspaceDir string) (*referenceViewRequest, error)
```

### 5. workspace 解析策略

`/show` 的相对路径解析基准，不应该直接取 `base_dir`，也不应只看 workspace binding。

更合理的优先级是：

1. 当前命令上下文中的 agent work dir
2. 当前 channel 已绑定的 workspace
3. agent 默认 work dir
4. 进程当前目录（最后 fallback）

这与：

- `/dir`
- `/shell`

的使用语义保持一致。

为避免逻辑重复，建议在 `engine.go` 中新增一个小 helper，例如：

```go
func (e *Engine) commandWorkDir(agent Agent, msg *Message) string
```

后续 `/shell` 和 `/show` 都可复用这套 workdir 解析。

### 6. 读取与展示策略

建议第一版输出保持简单稳定：

- 文件 / snippet / range
  - 一行标题
  - 下方 fenced code block

- 目录
  - 一行标题
  - 下方普通文本列表

不建议第一版优先做 card 交互，因为：

- 文件/snippet 更适合普通消息里的代码块
- 目录列表也不需要复杂交互
- card 只会增加实现复杂度

### 7. 文件与目录读取实现

建议不要通过内部调用 `/shell` 或拼接 `sed` / `ls` 命令实现。

推荐直接使用 Go 文件系统 API：

- `bufio.Scanner` 按行读取文件
- `os.ReadDir` 列目录

建议拆出如下函数：

```go
func readFileHead(path string, maxLines int) ([]string, bool, error)
func readFileRange(path string, start, end, maxLines int) ([]string, bool, error)
func readFileContext(path string, line, before, after, maxLines int) ([]string, bool, error)
func readDirEntries(path string, maxEntries int) ([]string, bool, error)
```

其中：

- `bool` 表示是否发生截断

### 8. 默认限制

建议第一版采用以下固定阈值：

- 文件前览：`80` 行
- 单点上下文：前后各 `8` 行
- range 最大：`120` 行
- 目录列表最大：`50` 项

超限时以提示方式说明截断，而不是无界展开。

### 9. 语言高亮

建议加一个轻量版 code fence 语言推断：

- `.go` -> `go`
- `.ts` -> `ts`
- `.tsx` -> `tsx`
- `.js` -> `js`
- `.py` -> `python`
- `.md` -> `markdown`

其他扩展名则退化为普通 fenced code block。

### 10. 测试建议

推荐补四类测试：

1. 解析/模式分流
   - 文件
   - `:line`
   - `:line:col`
   - `:start-end`
   - 目录
   - 目录带行号错误

2. 文件/目录 IO
   - 使用 `t.TempDir()` 构造临时目录树
   - 避免依赖真实机器目录结构

3. 命令层
   - `/show` 空参数
   - `/show` 文件
   - `/show` 行号
   - `/show` range
   - `/show` 目录
   - multi-workspace 下相对路径解析
   - 非 admin 权限拦截

4. 回归
   - `/show` 输出不应再次经过 references transform
   - 含路径的代码块内容应保持原样

## 配置设计

```toml
[projects.references]
normalize_agents = ["all"]
render_platforms = ["all"]
display_path = "relative"
marker_style = "emoji"
enclosure_style = "code"
```

采用的关键设计：

- 用 `normalize_agents` / `render_platforms` 控制作用范围
- 只有同时命中 agent 与 platform 才启用整条链路
- feature 默认不启用，保持增量式引入

## 作用域设计

只处理：

- agent thinking
- agent final response
- stream preview
- progress compact / card 中的 agent 文本

不处理：

- 系统命令回复
- tool result
- 平台 / 系统错误消息

## 路径展示设计

本次最终采用：

- `display_path`
- `marker_style`
- `enclosure_style`

没有引入额外的 `show_location` 配置。

位置粒度沿用 agent 原始输出语义：

- `app.ts`
- `app.ts:42`
- `app.ts:42:7`
- `app.ts:5-10`

## 文件 / 目录判断策略

优先级：

1. `os.Stat`
2. 位置语义
3. 末尾 `/`
4. 扩展名
5. `unknown`

`unknown` 不加 `📄` / `📁`。

## 当前状态说明

本 PR 当前已完成的是：

- 引用标准化
- 平台展示适配
- `/show` 引用感知查看命令（第一版）
