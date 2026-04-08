# CC-Connect Issue / Feature 开发与 PR 提交流程

本文档总结了在 `cc-connect` 中从 issue 选择、问题分析、实现、测试到 PR 提交的完整流程，并补充了统一的 PR 文档归档要求，避免在长周期开发和多轮联调中丢失上下文。

适用场景：

- 修复现有 GitHub issue
- 开发新的 feature
- 评估一个想法是否值得做
- 将本地修复整理成可提交的 fork PR

## 1. 总原则

### 1.1 永远先看最新 upstream 状态

不要直接基于自己 fork 的 `main` 开发。fork 的 `main` 很容易落后于上游，导致：

- GitHub compare 页面出现大量无关差异
- PR 显示 `Can’t automatically merge`
- 本地实现和上游已经合并的修复发生重叠
- 后续 cherry-pick 时出现不必要冲突

正确做法：

1. 把上游仓库作为 `upstream` remote
2. 每次开始新任务前先 `fetch upstream`
3. 永远基于最新 `upstream/main` 建新分支

推荐命令：

```bash
git clone https://github.com/xukp20/cc-connect.git /tmp/cc-connect-work
cd /tmp/cc-connect-work
git remote add upstream https://github.com/chenhg5/cc-connect.git
git fetch upstream
git checkout -b fix/your-topic upstream/main
```

### 1.2 优先在 `/tmp` 工作

如果机器上的 cephfs 较慢，开发、测试、切分支、跑 `go test` 都应尽量在 `/tmp` 下完成，然后再 push 到 fork。这样可以显著减少：

- `go test` / `go build` 的 I/O 开销
- `git status` / `git diff` 的延迟
- worktree / cherry-pick / merge 过程中的卡顿

### 1.3 一个分支只做一件事

推荐一条 branch 对应一件事：

- `fix/workspace-model-routing`
- `fix/feishu-image-upload`
- `feat/reference-normalization-rendering`

这样更容易：

- review
- 回滚
- 拆分 cherry-pick
- 判断 CI 失败来源

## 2. 新增要求：每个 PR 都建立独立文档目录

从现在开始，所有准备提交的 PR 都应在：

`docs/prs/`

下建立一个独立目录，目录名建议使用：

`YYYY-MM-DD-topic`

例如：

- `docs/prs/2026-04-07-reference-normalization-and-platform-rendering/`

该目录用于承载这一个 PR 的完整上下文材料。

### 2.1 目录内要求的文档

每个 PR 目录至少应包含以下文档：

1. `01-issue.md`
   - 可选，但 feature 强烈建议提供
   - 如果是已有 issue，可整理 issue 背景、关键评论、当前范围
   - 如果是新 feature，可放拟提交 issue 的草稿或 issue 摘要

2. `02-design.zh-CN.md`
   - 开发前讨论后的设计文档
   - 记录问题背景、目标、范围边界、设计取舍、配置设计、代码实现计划

3. `03-development-log.zh-CN.md`
   - 开发中的持续记录
   - 记录实现步骤、关键改动、重要发现、已修问题、暂不处理的问题

4. `04-test-and-validation.zh-CN.md`
   - 测试和联调记录
   - 记录自动化测试、平台联调、真实 prompt 测试、失败现象与对应修正

5. `05-pr-draft.md`
   - 最终 PR 文案草稿
   - 记录准备提交到 GitHub 的标题、摘要、测试结果、截图说明、风险说明

建议额外提供：

- `README.md`
  - 作为该 PR 目录的索引页
  - 说明当前状态、文档用途、后续动作

### 2.2 文档维护要求

这些文档不是开发结束后一次性补写，而是要跟着开发过程同步更新：

- 在分析阶段写 issue / design
- 在实现阶段更新 development log
- 每轮联调后更新 test 文档
- 准备提交时整理 PR draft

目标是：

- 防止上下文丢失
- 防止多轮讨论后无法回忆为什么这么设计
- 让后续 review、回归和继续开发都有清晰材料

## 3. Issue / Feature 选择流程

### 3.1 如果目标是修 issue

先收集四类信息，再决定要不要做：

- issue 本身
- 相关 issue
- 已有 PR
- 当前 `upstream/main`

结论一般分三种：

1. 已被主线完全解决：不需要再做
2. 只被部分覆盖：补剩余缺口
3. 仍然存在：按当前主线实现重新设计修复

### 3.2 如果目标是做 feature

必须先确认：

- 当前主线是否已经有同类功能
- open PR 里是否已经有人做了完整实现
- 有没有更大的设计方向会吞掉这个 feature
- 这个 feature 是否应做成 core 能力，而不是单个平台特化

### 3.3 建议的分析输出

在真正改代码前，至少要形成：

- 目标 issue / feature
- 当前主线是否已解决
- 相关 issue / PR
- 这次准备解决的范围
- 明确不解决的范围
- 预计会改哪些模块

这部分应写入 `02-design.zh-CN.md`。

## 4. 开发前准备

### 4.1 标准分支准备流程

```bash
git clone https://github.com/xukp20/cc-connect.git /tmp/cc-connect-work
cd /tmp/cc-connect-work
git remote add upstream https://github.com/chenhg5/cc-connect.git
git fetch origin
git fetch upstream
git checkout -b fix/your-topic upstream/main
```

### 4.2 开发前先读这些内容

- 目标 issue 和评论区
- 所有相关 open PR
- 所有相关 recently merged PR
- 当前 `upstream/main` 中对应模块的实现
- `.github/workflows/ci.yml`
- 仓库内 `AGENTS.md`

## 5. 实现最佳实践

### 5.1 只在当前主线差异上动手

修复时应基于“当前主线还缺什么”来写，而不是基于 issue 提出时的旧代码机械重放。

### 5.2 测试必须跟着修复一起提交

要求：

- 新 feature 必须有单元测试
- bug fix 必须有回归测试
- 只修代码不补测试，后续很容易被上游重构再次打破

### 5.3 文档必须跟着开发同步更新

从本流程更新开始，文档不再是可选附属物，而是交付物的一部分。

最低要求：

- 代码设计变化，要同步到 `02-design.zh-CN.md`
- 实现中发现新问题，要同步到 `03-development-log.zh-CN.md`
- 每次关键测试和联调，要同步到 `04-test-and-validation.zh-CN.md`
- 准备发 PR 前，整理 `05-pr-draft.md`

### 5.4 不要把无关问题顺手混进来

更好的做法：

- 在设计文档里明确“这次不处理什么”
- 如果发现新问题，记录为 follow-up
- 如果上游已有独立 PR，就不要并进当前修复

### 5.5 平台真实效果验证要停下来请求用户协助

如果修复涉及真实平台效果，例如：

- 飞书卡片展示
- 飞书图片 / 文件发送
- Weixin 消息呈现
- Telegram thread / topic 行为

本地单元测试通常不够，必须做真实链路验证。

这时应：

- 明确请求用户协助
- 说明需要用户做什么
- 说明你会发送什么
- 说明需要用户反馈什么

## 6. 本地 CI / 验证流程

提交前至少应跑：

### 6.1 Build

```bash
go build ./...
```

### 6.2 Tests

```bash
go test ./...
```

### 6.3 建议额外检查

```bash
go test -race ./...
go test ./... -coverprofile=coverage.out -covermode=atomic
staticcheck ./...
```

是否全跑，按当前仓库 CI 和改动范围决定。

## 7. 提交前检查清单

提交前至少确认：

1. 代码构建通过
2. 自动化测试通过
3. 当前 PR 目录已建立
4. `01-issue.md` 到 `05-pr-draft.md` 已补齐当前阶段应有内容
5. 平台联调记录已写入测试文档
6. PR 文案、截图、风险点已整理
7. 没有把临时脚本、临时截图、临时调试文件混进提交

## 8. 建议的 PR 目录模板

建议结构：

```text
docs/prs/2026-04-07-your-topic/
├── README.md
├── 01-issue.md
├── 02-design.zh-CN.md
├── 03-development-log.zh-CN.md
├── 04-test-and-validation.zh-CN.md
└── 05-pr-draft.md
```

推荐做法：

- `README.md` 给出总览和当前状态
- 其他文档按阶段职责维护
- 如果已有旧文档，可在 PR 目录中复制整理后的版本，避免文档散落在 `docs/` 根目录各处
