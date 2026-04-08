# 引用标准化与平台展示联调计划

本文档用于在本地开发完成后，对 `cc-connect` 的“本地引用标准化 + 平台展示适配”功能做一次高覆盖、低重复的联调验证。

## 1. 目标

本轮联调主要验证两部分：

1. 第一阶段：对 `Codex` / `Claude Code` 输出中的本地引用做标准化识别
2. 第二阶段：在 `Feishu` / `Weixin` 发送前，将本地引用渲染成统一、易读的展示形式

重点不是“让本地路径真的可点击打开文件”，而是：

- 去掉原始 `[path](path)` 样式的噪音
- 保留引用语义（文件、目录、行号、列号、范围）
- 保证普通消息、卡片消息、进度消息使用一致的展示逻辑
- 在默认关闭的情况下不影响现有行为

## 2. 推荐配置

联调时默认使用如下推荐配置：

```toml
[projects.references]
normalize_agents = ["all"]
render_platforms = ["all"]
display_path = "relative"
marker_style = "emoji"
enclosure_style = "code"
```

说明：

- `display_path = "relative"`：优先显示相对当前 workspace 的路径，更适合 IM 阅读
- `marker_style = "emoji"`：使用 `📄` / `📁` 区分文件与目录
- `enclosure_style = "code"`：路径主体使用 inline code，和正文区分更清楚

同时保留一组“全关闭”的对照配置，用于 PR before/after 图：

```toml
# 不写 [projects.references]，或保持其默认空值
```

## 3. 测试策略

整体分两层：

### 3.1 平台展示测试

目的：验证第二阶段渲染效果。

方法：通过 `cc-connect` 的真实发送路径，将人工构造的固定测试语料发送到目标平台。

这一步主要覆盖：

- 普通消息
- Card 消息
- streaming preview
- progress compact
- progress card

优势：

- 不依赖 agent 自由发挥
- 可重复
- 能快速发现不同消息类型下的展示不一致问题

### 3.2 真实 agent 输出测试

目的：验证第一阶段标准化识别。

方法：分别让 `Codex` 和 `Claude Code` 输出多种本地引用格式，并观察最终渲染结果。

这一步主要覆盖：

- `Codex` 风格 Markdown 本地文件引用
- `Claude Code` 风格绝对路径、反引号路径、`path:line-range`
- 真实文件分析任务下的自然输出

优势：

- 直接验证 normalize 逻辑是否能吃到真实 agent 输出
- 能发现 synthetic corpus 未覆盖到的自然表达差异

## 4. 最小高覆盖测试矩阵

### 4.1 平台展示层

#### Feishu

1. 普通消息
2. Card 消息
3. progress compact
4. progress card

#### Weixin

1. 普通消息
2. 进度消息路径（以平台支持的 update/fallback 形式为准）

### 4.2 真实 agent 层

1. `Codex` synthetic prompt
2. `Claude Code` synthetic prompt
3. `Codex` 真实文件分析任务
4. `Claude Code` 真实文件分析任务

## 5. 测试语料

### 5.1 Synthetic corpus

建议用一条固定语料覆盖以下格式：

- 绝对文件路径  
  `/root/code/demo/src/app.ts`
- 绝对路径 + 行号  
  `/root/code/demo/src/app.ts:42`
- 绝对路径 + 行列  
  `/root/code/demo/src/app.ts:42:7`
- 绝对路径 + 行范围  
  `/root/code/demo/src/app.ts:5-10`
- 绝对目录路径  
  `/root/code/demo/src/components/`
- 相对文件路径  
  `src/app.ts`
- 相对路径 + 行号  
  `src/app.ts:42`
- Markdown 本地文件链接  
  `[app.ts](/root/code/demo/src/app.ts)`
- Markdown 本地文件链接 + `#L42`  
  `[app.ts](/root/code/demo/src/app.ts#L42)`
- Markdown 相对路径链接  
  `[app.ts](src/app.ts)`
- Claude 常见反引号绝对路径  
  `` `/root/.claude/settings.json:5-10` ``
- 网页链接  
  `[OpenAI](https://openai.com/)`
- fenced code block 中的路径
- inline code 中的路径

### 5.2 真实任务

建议至少准备两个真实任务：

1. 让 agent 读取某个文件，并引用其中多个位置解释其逻辑
2. 让 agent 比较两个文件，并分别引用多个片段位置

## 6. 重点观察项

### 6.1 展示正确性

- 本地 Markdown 文件链接是否被改写为统一展示
- `relative` 路径是否相对当前 workspace 正确计算
- 行号 / 列号 / 范围是否保留
- 网页链接是否保持原样
- fenced code block 是否不被错误改写
- inline code 中的路径是否仍能正确转换

### 6.2 平台一致性

- Feishu 普通消息与 Feishu card 消息是否风格一致
- Feishu progress card 与最终回复是否风格一致
- Weixin 普通消息与进度消息是否风格一致

### 6.3 开关正确性

- 不开启 `[projects.references]` 时，输出是否保持原样
- 开启推荐配置后，输出是否统一变为目标样式

## 7. PR 截图计划

建议在 PR 中至少放两张图：

1. `Feishu + Codex + 功能关闭`
2. `Feishu + Codex + 推荐配置开启`

推荐作为 before / after 对比。

若再补第三张，优先选择：

3. `Feishu progress card` 效果图

这样可以同时证明：

- 原始可读性问题
- 推荐配置后的改善
- 进度视图也已经接入同一套处理逻辑

## 8. 建议的联调顺序

1. 替换现有 Feishu 部署到新二进制
2. 先用人工构造语料测试 Feishu：
   - 普通消息
   - card 消息
   - progress card
3. 再切到 Weixin，做相同的展示测试
4. 再分别用 `Codex` / `Claude Code` 做 synthetic prompt
5. 最后跑少量真实文件分析任务
6. 记录结果并补进 PR 描述 / issue 评论

## 9. 当前实现范围

当前代码仅显式支持：

- normalize agents:
  - `codex`
  - `claudecode`
- render platforms:
  - `feishu`
  - `weixin`

`all` 仅展开到以上当前已支持目标。

## 10. 通过标准

本轮联调通过的最低标准：

- 推荐配置下，Feishu 与 Weixin 的本地引用展示明显优于原始输出
- 普通消息与进度消息都能正确应用渲染逻辑
- `Codex` / `Claude Code` 的主要本地引用格式都能被识别
- 功能关闭时行为与当前主线一致
