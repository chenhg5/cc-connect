# Contributing to cc-connect

[中文](#为-cc-connect-做贡献) | [English](#contributing-to-cc-connect)

Thank you for using cc-connect and for every issue, pull request, and piece of feedback that helps improve it. This guide turns the contributor welcome note from [#295](https://github.com/chenhg5/cc-connect/issues/295) into a permanent repo document.

## Before You Open An Issue Or PR

1. Search first.
Check [Issues](https://github.com/chenhg5/cc-connect/issues) and [Pull requests](https://github.com/chenhg5/cc-connect/pulls) for duplicates or related discussion before starting new work.

2. Try the latest beta.
Many bugs are fixed in beta or pre-release builds before they reach stable. Please retry on the latest beta first when possible.

## Writing A Helpful Issue

Please include as much of the following as possible:

- Version: `cc-connect --version`, npm tag, or release asset
- Environment: OS, installation method, agent type, and platform
- Reproduction steps: the smaller the repro, the better
- Expected behavior vs. actual behavior
- Logs or errors, with secrets redacted
- Optional analysis or a proposed fix

We usually acknowledge new issues within about 1 to 2 business days. More complex bugs may take longer to investigate.

## Pull Requests

- Follow the repo guidance in [`CLAUDE.md`](./CLAUDE.md) and [`AGENTS.md`](./AGENTS.md).
- Run local validation before submitting. At minimum:

```bash
go test ./...
```

- Call out breaking changes explicitly in the PR description.
- Update docs or examples when behavior or configuration changes.
- If you are fixing an issue, link it in the PR body with `Closes #<number>` when appropriate.

## Release Cadence

- Beta / pre-release: roughly every 2 to 3 days
- Stable: roughly every 2 weeks

Always treat the [GitHub Releases](https://github.com/chenhg5/cc-connect/releases) page as the source of truth.

## Community

- Discord: <https://discord.gg/kHpwgaM4kq>
- Telegram: <https://t.me/+odGNDhCjbjdmMmZl>
- X: <https://twitter.com/chg80333>
- WeChat: `@mongorz` (mention cc-connect when adding)

Commercial support, custom work, or enterprise inquiries can go through the same channels.

---

# 为 cc-connect 做贡献

感谢你使用 cc-connect，也感谢你通过 issue、PR 和反馈帮助项目持续改进。这份文档把 [#295](https://github.com/chenhg5/cc-connect/issues/295) 里的欢迎与参与指南正式沉淀到仓库中。

## 提交 Issue 或 PR 之前

1. 先搜索。
先查看 [Issues](https://github.com/chenhg5/cc-connect/issues) 和 [Pull requests](https://github.com/chenhg5/cc-connect/pulls)，避免重复劳动，也方便在已有讨论里继续跟进。

2. 先试最新 beta。
很多问题会先在 beta / 预发布版本中修复。如果条件允许，建议先在最新 beta 上复现一次。

## 如何提交高质量 Issue

建议尽量包含以下信息：

- 版本号：`cc-connect --version`、npm 标签或 release 资源名
- 环境：操作系统、安装方式、Agent 类型、平台类型
- 复现步骤：越小越好
- 预期行为和实际行为
- 日志或报错，注意打码敏感信息
- 可选的原因分析或修复思路

我们通常会在 1 到 2 个工作日内进行首次响应，复杂问题可能需要更长的排查时间。

## Pull Request

- 请遵循仓库中的 [`CLAUDE.md`](./CLAUDE.md) 和 [`AGENTS.md`](./AGENTS.md)。
- 提交前请先做本地验证，至少执行：

```bash
go test ./...
```

- 如果包含 breaking change，请在 PR 描述中明确说明。
- 如果改动影响行为或配置，请同步更新文档或示例。
- 如果是在修复 issue，适合时请在 PR 描述中使用 `Closes #<编号>` 关联。

## 发版节奏

- Beta / 预发布：大约每 2 到 3 天一次
- 稳定版：大约每 2 周一次

请以 [GitHub Releases](https://github.com/chenhg5/cc-connect/releases) 页面为准。

## 社区

- Discord: <https://discord.gg/kHpwgaM4kq>
- Telegram: <https://t.me/+odGNDhCjbjdmMmZl>
- X: <https://twitter.com/chg80333>
- 微信: `@mongorz`（添加时请备注 cc-connect）

如果是商业合作、定制需求或企业支持，也可以通过以上渠道联系。