# Contributing to cc-connect-next

[中文](#为-cc-connect-next-做贡献) · [English](#contributing-to-cc-connect-next)

cc-connect-next is an independent successor derived from [CC Connect](https://github.com/chenhg5/cc-connect). Please report successor-specific bugs and proposals in this repository. For behavior inherited unchanged from upstream, include the upstream issue or pull request when one exists.

## Before opening an issue

- Search [issues](https://github.com/timmyagentic/cc-connect-next/issues) and [pull requests](https://github.com/timmyagentic/cc-connect-next/pulls).
- Reproduce on the latest available cc-connect-next build.
- Include `cc-connect-next --version`, OS and architecture, installation method, agent type, platform, minimal reproduction steps, and expected versus actual behavior.
- Redact app secrets, tokens, cookies, local usernames, private paths, and message contents that are not needed for the report.
- For Feishu card bugs, say whether `card_mode = "rich"` and `reply_to_trigger = true` are enabled. A redacted screenshot is useful.

## Pull requests

Keep changes narrow and preserve the independent runtime boundaries: the `cc-connect-next` command, `~/.cc-connect-next` data directory, daemon/service names, API socket, release assets, and npm package must not collide with official CC Connect.

CI is intentionally triggered only by pushing a version tag matching `v*`. Pull requests and ordinary branch pushes do not start the CI workflow, so run the local checks below before opening a PR. The release workflow runs its own complete validation for the same tag.

Before submitting, run at least:

```bash
make web
go test ./... -count=1
make build-noweb
```

Changes to native Feishu rich cards should also run:

```bash
go test ./platform/feishu -count=1
go test ./core -run TestProcessInteractiveEvents_RichCard -count=1
```

Document behavior and migration changes. Do not include real credentials or generated runtime state. Attribution for inherited work must remain intact under [LICENSE](LICENSE) and [NOTICE](NOTICE).

---

# 为 cc-connect-next 做贡献

cc-connect-next 是从 [CC Connect](https://github.com/chenhg5/cc-connect) 派生的独立后继项目。新项目特有的问题和建议请提交到本仓库；如果问题来自未修改的上游能力，请附上已有的上游 Issue 或 PR。

## 提交 Issue 前

- 先搜索本仓库的 [Issues](https://github.com/timmyagentic/cc-connect-next/issues) 和 [Pull Requests](https://github.com/timmyagentic/cc-connect-next/pulls)。
- 在最新可用的 cc-connect-next 构建上复现。
- 提供 `cc-connect-next --version`、系统与架构、安装方式、Agent、平台、最小复现步骤，以及预期和实际结果。
- 隐去应用密钥、Token、Cookie、本地用户名、私有路径和无关聊天内容。
- 飞书卡片问题请注明是否启用了 `card_mode = "rich"` 与 `reply_to_trigger = true`，并尽量附上打码截图。

## Pull Request

改动应保持聚焦，并保护独立运行边界：`cc-connect-next` 命令、`~/.cc-connect-next` 数据目录、daemon/service、API socket、Release 产物和 npm 包均不能与官方 CC Connect 冲突。

CI 现在只在推送匹配 `v*` 的版本 tag 时触发；Pull Request 和普通分支 push 不会启动 CI。因此提交 PR 前请先在本地执行下面的检查；同一个 tag 的 Release workflow 还会执行完整发布验证。

提交前至少执行：

```bash
make web
go test ./... -count=1
make build-noweb
```

修改飞书原生 Rich Card 时还应执行：

```bash
go test ./platform/feishu -count=1
go test ./core -run TestProcessInteractiveEvents_RichCard -count=1
```

行为与迁移变化需要同步更新文档。不要提交真实凭证或运行态文件，并按 [LICENSE](LICENSE) 与 [NOTICE](NOTICE) 保留上游归属。
