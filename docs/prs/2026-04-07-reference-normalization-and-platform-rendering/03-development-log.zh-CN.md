# 开发记录

## 已完成的主要实现

1. 新增 project 级 references 配置
2. 新增 `core/reference_render.go`
3. 接入 engine / streaming / progress 发送链路
4. 新增配置校验与单元测试
5. 增加 Feishu / Weixin 首批平台范围
6. 新增 `/show` 命令，用于基于引用查看文件 / 目录 / 代码片段

## 关键修复记录

### 1. tool result 被误处理

现象：

- bash / ls 等 tool result 输出被渲染逻辑污染

处理：

- 仅保留对 agent-originated text 的处理
- tool result 保持 raw

### 2. 系统消息被误处理

现象：

- `/workspace bind .`
  -> `✅ 工作区绑定成功: 📄 .`

处理：

- 从通用 `send/reply/card` 入口移除渲染
- 收口到 agent 事件输出路径

### 3. 相对路径被绝对路径 matcher 误拆

现象：

- 相对路径中间的 `/src/...` 被误识别为新绝对路径

处理：

- 增加更严格的边界检查

### 4. 网页链接被本地路径污染

现象：

- `[OpenAI](https://openai.com/)`
  被错误替换成本地引用

处理：

- 先保护 web markdown link
- placeholder 全局唯一

### 5. 中文顿号分隔多个路径时只识别第一个

现象：

- `Claude Code + Feishu` 场景下
- 一句话里用 `、` 列出的多个路径没有被分别识别

处理：

- 排除中文分隔符进入候选正文
- 边界检查改为按 rune 解码而非按字节取前一个字符

## 当前实现边界

- 已支持：`codex` / `claudecode`
- 已支持：`feishu` / `weixin`
- 其他 agent / 平台暂未扩展

## 新增：引用感知查看命令 `/show`

本轮在完成引用标准化与平台渲染之后，继续落地了第一版引用感知查看命令：

- `/show <引用>`

已完成的实现方向：

- 作为 `Engine` 内建命令实现
- 加入 `privilegedCommands`
- 将解析公共部分抽到：
  - `core/reference_parse.go`
- 新增：
  - `core/reference_show.go`

当前支持：

- 文件，无位置 -> 文件前 80 行
- `path:line` / `path:line:col` -> 上下文片段
- `path:start-end` -> 精确 range
- 目录 -> 一级目录列表

当前不支持：

- 直接解析前端展示层包装后的 `📄 ...` / `[FILE] ...` 样式

当前实现复用：

- 本地引用解析
- `workspaceDir` 相对路径解析
- 文件 / 目录判断

并且保证：

- `/show` 输出不会再次经过 references transform
- 代码块中的原始内容保持 raw
