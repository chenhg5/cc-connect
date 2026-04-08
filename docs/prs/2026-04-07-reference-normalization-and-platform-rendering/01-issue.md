# Issue 记录

## GitHub Issue

- 已提交：`#484`
- 主题：本地文件/目录/代码位置引用的标准化与平台展示适配

## 背景

当前 `cc-connect` 在接入 `Codex` / `Claude Code` 时，agent 输出中会频繁出现：

- 绝对路径
- 相对路径
- `path:line`
- `path:line:col`
- `path:start-end`
- Markdown 本地文件链接

这些内容在 Feishu / Weixin 等 IM 平台中存在以下问题：

- raw markdown 噪音重
- `[path](path)` 重复信息影响可读性
- 某些消息类型只保留 label
- 某些消息类型点击后只是当网页路径打开
- 同一平台上的普通消息、卡片消息、进度消息展示不一致

## 本次范围

- 新增平台无关的引用标准化能力
- 新增平台相关的展示适配能力
- 首批支持：
  - agent：`codex`、`claudecode`
  - platform：`feishu`、`weixin`

## 明确不在本次范围

- 真正打开本地文件的点击跳转能力
- 其他 agent / IM 平台的全面适配
- 把系统消息、tool result 一并纳入渲染
