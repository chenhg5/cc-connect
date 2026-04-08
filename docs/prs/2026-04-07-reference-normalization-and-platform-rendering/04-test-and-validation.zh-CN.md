# 测试与联调记录

完整实现与测试记录见：

- [reference-rendering-implementation-and-test-record.zh-CN.md](../../reference-rendering-implementation-and-test-record.zh-CN.md)
- [reference-rendering-manual-test-plan.zh-CN.md](../../reference-rendering-manual-test-plan.zh-CN.md)

这里保留本次 PR 最直接相关的测试摘要。

## 自动化测试

已通过：

```bash
go test ./core -run 'TestTransformLocalReferences_' -count=1
go test ./core -count=1
go test ./... -count=1
```

补充：

- 已新增一条更直接的回归测试，验证“功能关闭时，agent 最终回复保持原文不变”：
  - `TestProcessInteractiveEvents_FinalReplyRemainsRawWhenReferencesDisabled`
- 已新增 `/show` 相关测试，覆盖：
  - 模式分流（文件 / 行号 / range / 目录 / markdown 本地链接）
  - 目录带行号错误
  - 文件前览 / 代码上下文 / 目录列表输出
  - multi-workspace 下相对路径解析
  - 非 admin 权限拦截
  - `/show` 输出保持 raw，不再次经过 references transform

新增回归点：

- `/show` 已补真实 agent 路径之外的命令层回归，保证：
  - 功能关闭时，agent 最终回复保持原文
  - `/show` 命令自身输出不被 references 渲染二次污染

### 环境依赖检查

本轮新增测试中，曾有一条测试硬编码依赖 `/root/code/demo-repo/...`。

当前已修复为：

- 使用 `t.TempDir()`
- 在测试内部自建：
  - 文件 `demo-repo/README`
  - 目录 `demo-repo/src/components/profile`
  - 目录 `demo-repo/src/components/profile.ts`
  - 目录 `demo-repo/docs/spec.v1`

因此当前新增测试不依赖开发机上的真实仓库目录。

## 真实联调

### Feishu + Codex

已验证：

- 基础引用展示
- 真实存在的文件/目录/扩展名目录边界情况
- progress / card 视图
- `/show` 文件 / 行号 / 行列 / range / 目录查看

结果：

- 通过

补充：

- 额外完成了一组更贴近日常工程消息的 before / after 对比
- 新的测试 workspace 使用更深的 repo 外层路径：
  - `/root/code/platform-programs/customer-success/incident-simulations/demo-repo`
- 然后将 workspace 直接绑定到该 repo root，使：
  - baseline 保持长绝对路径
  - `relative` 推荐配置稳定缩短为 `ui/...`、`svc/...`、`docs/...`

实际结论：

- raw baseline 保留了长绝对路径和本地 md link 噪音
- 推荐配置下：
  - `ui/recovery_contact_form.tsx`
  - `svc/recovery_session_reconciler.go`
  - `svc/recovery_session_reconciler_test.go`
  - `docs/spec.v1/`
  - `scripts/recovery`
  都能以更短、更稳定的形式展示
- `path:line`
- `path:line:col`
- `path:start-end`
- 本地 markdown 文件引用
- 网页链接
  都在同一条真实排查结论消息中得到验证

另补 `/show` 真实 Feishu 用例，均已成功：

- `/show ui/recovery_contact_form.tsx`
- `/show ui/recovery_contact_form.tsx:11`
- `/show svc/recovery_session_reconciler.go:12:2`
- `/show svc/recovery_session_reconciler_test.go:8-17`
- `/show svc/`

这些用例分别覆盖：

- 文件头部展示
- 单点上下文
- `line:col` 上下文
- range 片段
- 目录列表

建议在 PR 中将上述 5 条作为 `/show` 能力的展示示例。

### Feishu + Claude Code

已验证：

- 基础引用展示
- progress / card 视图
- 中文顿号分隔多个路径场景

结果：

- 通过

### Weixin

#### Weixin + Codex

已验证：

- 基础短任务引用展示

结果：

- 通过

验证要点：

- 绝对路径转相对 `/root/code`
- 文件 / 目录识别正确
- 本地 markdown 链接统一展示
- 网页链接保留原样

补充说明：

- Weixin 存在独立的长任务回复窗口 / `sendMessage` 失败问题
- 为避免与本 feature 混淆，本轮 Weixin 联调主要采用短任务 prompt
- 相关调查已单独记录在：
  - [docs/prs/2026-04-07-weixin-sendmessage-timeout-investigation/README.md](../2026-04-07-weixin-sendmessage-timeout-investigation/README.md)

#### Weixin + Claude Code

已验证：

- 基础短任务引用展示
- 短 progress / 多步骤说明场景

结果：

- 通过

验证要点：

- 绝对路径转相对 `/root/code`
- 文件 / 目录识别正确
- 本地 markdown 链接统一展示
- 过程文本与最终总结风格一致

## preset 对比矩阵

为减少频繁改配置和重启服务，本轮额外使用本地调试脚本直接把同一段模拟 agent 输出按不同 preset 渲染后发送到真实会话：

- [`/.devtools/reference_render_send_matrix.go`](/tmp/cc-connect-ref-render/.devtools/reference_render_send_matrix.go)

本轮对比的 preset：

- `absolute-none-none`
- `relative-emoji-code`
- `basename-ascii-bracket`
- `dirname-basename-none-angle`
- `smart-emoji-fullwidth`
- `dirname-basename-ascii-code`

### Weixin 结果

结果：

- 6 组 preset 都成功显示
- `relative-emoji-code` 观感最佳，信息量和可读性最平衡
- `dirname-basename-ascii-code` 是可接受的非 emoji 备选
- `smart-emoji-fullwidth` 虽然更短，但压缩过度，不适合作默认

### Feishu 结果

结果：

- 6 组 preset 都成功显示
- `relative-emoji-code` 仍然是最推荐默认值
- Feishu 对 `code` 包裹的视觉保留较弱，实际更接近 `relative-emoji-none`
- `dirname-basename-none-angle` 较干净，但缺少文件/目录类型提示
- `dirname-basename-ascii-code` 可作为无 emoji 备选

补充：

- 网页链接 `[OpenAI](https://openai.com/)` 在 matrix 消息中仍为可点击网页链接
- 复制文本时通常只显示 label `OpenAI`，不应将此误判为链接丢失

## 当前剩余工作

- 视情况整理 PR 截图
- 视需要补更长但仍不触发 Weixin 回复窗口问题的真实工作流测试

## PR 展示计划

建议在 PR 中展示两组内容：

1. 引用展示 before / after
- 使用深层 workspace：
  - `/root/code/platform-programs/customer-success/incident-simulations/demo-repo`
- 展示：
  - raw baseline
  - 推荐配置 `relative-emoji-code`

2. `/show` 能力示例
- `/show ui/recovery_contact_form.tsx`
- `/show ui/recovery_contact_form.tsx:11`
- `/show svc/recovery_session_reconciler.go:12:2`
- `/show svc/recovery_session_reconciler_test.go:8-17`
- `/show svc/`

这两组组合后，能够同时说明：

- 本地引用在 IM 中如何被缩短和统一展示
- 用户如何基于同一批引用继续查看文件、目录和代码片段
