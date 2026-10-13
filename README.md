# cc-connect

[简体中文](README.zh-CN.md) · [Team guide](docs/agent-team.md) · [Configuration](config.example.toml) · [Upstream](https://github.com/chenhg5/cc-connect)

cc-connect connects local coding agents to messaging apps. Send a request from Feishu/Lark, Telegram, Discord or another supported channel, and the agent runs against your local workspace. Replies, files, questions and supported permission requests return to the conversation.

This branch adds **Agent Team**: reusable bots with distinct roles, a Feishu group for each business project, persistent member conversations, and explicit task handoffs and acceptance. Existing single-bot configurations and commands remain available.

## Choose a workflow

| Workflow | Use it for | Starting point |
| --- | --- | --- |
| Single bot | One coding agent connected to one or more channels | Ordinary `[[projects]]` configuration |
| Agent Team | A manager coordinating project members in Feishu groups | `team init`, then `/team` and `/project` |

A **Team** is a reusable roster. A business **Project** has a goal, code directories, participating bots and its own group. A **Workspace** is the directory the agent uses. For compatibility, TOML `[[projects]]` entries still define bot runtimes; `[[teams]]` refers to those runtimes by name.

Roles and backends are independent. A manager, planner, developer, tester or reviewer can use Codex, Claude Code or Trae through ACP.

## Build this branch

Install and authenticate your agent CLI before starting cc-connect. Source builds require Go 1.25 or newer. Building the Web UI also requires Node.js and npm.

```sh
# From the repository root:
make build-team
# Output: dist/cc-connect-team

# Optional: build the Web UI, then rebuild to embed its assets.
make web
make build-team
```

`build-team` includes the registered agents, platforms and Web backend. The `agent_team` build tag is additive. An ordinary build remains available with `make build`; selective compilation is described in [AGENTS.md](AGENTS.md#selective-compilation).

Use a source-built binary for this branch's changes. Upstream release binaries and npm packages are separate distribution artifacts.

## Start an Agent Team

### Create the manager

Run onboarding from your local terminal with an existing workspace:

```sh
./dist/cc-connect-team team init --agent codex --work-dir "$HOME/Workspace"
```

Supported onboarding backends are `codex`, `claudecode` and `trae`. Follow the QR registration flow to create the manager bot, bind its owner, save configuration and start the service. Feishu app publication, visibility and tenant permissions still apply.

The default configuration is `~/.cc-connect-team/config.toml`. Use `--no-start` to save without launching, or `--config` to choose a new configuration path. For later starts:

```sh
./dist/cc-connect-team --config "$HOME/.cc-connect-team/config.toml"
```

Keep the service running on the machine hosting your code and agent credentials. Bot registration and user OAuth flows belong in a local terminal or private chat.

### Add members and create a project

In a **private chat with the manager**, send `/help` or `/team bot create`. Select a role and backend and complete registration. Bot names come from Feishu; internal IDs remain stable when display names change.

Send `/project create` to fill in a project name, directory, goal and members. Creation returns a new group containing the manager, creator and selected bots. Text commands are also supported:

```text
/team bot create developer-a developer codex
/team bot create reviewer-a reviewer claudecode
/project create shop /absolute/path/to/shop developer-a,reviewer-a Implement and verify the order API
```

Enter the project group and describe the task. With `auto_coordinate = true`, the manager handles the creator's messages when nobody is mentioned. **Other people must directly mention the intended bot**; ordinary conversation, plain-text `@name` and `@all` do not trigger it. Direct mentions remain subject to existing access rules.

### Follow work and decisions

| Command | Purpose |
| --- | --- |
| `/project` or `/project settings` | View the goal, code directories and bot settings |
| `/project board` | Create or refresh the project overview |
| `/project tasks` | List tasks and their states |
| `/project task T001` | Inspect a task and its deliverable |
| `/project task T001 logs` | Download recorded tool logs |
| `/project pause` / `/project resume` | Stop project execution / allow new work again |
| `/project finish` | Confirm closure while retaining history and artifacts |

The manager dispatches work, checks the deliverable and explicitly accepts it. A completed model turn means a result was submitted; it does not prove acceptance or passing tests. Dependent tasks require accepted predecessors. Planned work stays pending until the manager dispatches it.

Routine operations such as committing completed changes or creating a worktree default to one qualified executor and one acceptance. Complete member answers are reused; the manager adds corrections or new decisions when needed. Backend permissions and repository checks continue to apply.

## Conversations, cards and permissions

- **Persistent context:** each bot continues one model conversation within a project across tasks and visible threads. Projects stay separate even when they share a directory. `/new` starts a new member conversation; changing project directories or the goal creates a new context revision after running work has stopped.
- **Automatic naming:** conversations use `Project name · Bot display name`. Supported Codex App Server and Trae sessions synchronize their native title on creation or resume. Naming failures are logged without blocking execution. Older inactive sessions are retained.
- **Readable execution:** public progress and tools appear in the process card, with current activity at the bottom. Conclusions render Markdown separately when configured. Contentless turns leave no empty process card; ordinary replies do not automatically mention the human requester. Command summaries have no detail jump.
- **Native decisions:** questions and supported tool approvals return to the same agent session. Expired or mismatched callbacks cannot answer a newer request. Permission behavior comes from the selected backend and mode.

Team onboarding defaults to interactive permissions: Codex uses `backend = "app_server"` and `mode = "suggest"`; Claude Code uses `mode = "default"`; Trae starts ACP with explicit default permissions. Codex `exec` lacks the approval response channel used here. Model, mode and reasoning choices depend on backend capabilities.

Context separation does not isolate files or accounts. Coordinate writes to shared repositories or use separate worktrees. Codex `full-auto` retains its sandbox; writable runtime roots and network access require explicit configuration. Resuming a project does not replay interrupted tasks automatically.

For cards, multiple directories, labels, migration and coordination commands, see the [Team guide](docs/agent-team.md) and [Team configuration example](config.agent-team.example.toml).

## Connect a single bot

Team setup is optional. A minimal Feishu/Codex configuration:

```toml
language = "en"

[[projects]]
name = "my-code"
admin_from = "${FEISHU_OWNER_OPEN_ID}"

[projects.agent]
type = "codex"
[projects.agent.options]
work_dir = "/absolute/path/to/repository"
backend = "app_server"
mode = "suggest"

[[projects.platforms]]
type = "feishu"
[projects.platforms.options]
app_id = "${FEISHU_APP_ID}"
app_secret = "${FEISHU_APP_SECRET}"
allow_from = "${FEISHU_OWNER_OPEN_ID}"
```

Provide the environment variables, replace the workspace path and save as `config.toml`, then run:

```sh
./dist/cc-connect-team --config ./config.toml
```

Follow the [Feishu setup guide](docs/feishu.md) for permissions and events. `/help` lists commands; `/whoami` reports your platform user ID. `/new`, `/list`, `/switch`, `/history`, `/model`, `/mode`, `/reasoning`, `/memory`, `/cron` and `/workspace` retain their existing backend capabilities and access checks.

Ordinary configurations default to session rotation after 30 minutes of inactivity. Set `reset_on_idle_mins = 0` under `[[projects]]` to disable it. Team project conversations continue across ordinary idle periods.

## Other channels and backends

The plugin registry also supports Gemini CLI, Cursor, OpenCode, Copilot, Kimi, Pi and ACP-compatible agents. See [config.example.toml](config.example.toml) for options.

| Channel | Setup guide |
| --- | --- |
| Feishu / Lark | [feishu.md](docs/feishu.md) |
| Telegram | [telegram.md](docs/telegram.md) |
| Discord | [discord.md](docs/discord.md) |
| Slack | [slack.md](docs/slack.md) |
| DingTalk | [dingtalk.md](docs/dingtalk.md) |
| WeChat Work / Weixin | [wecom.md](docs/wecom.md), [weixin.md](docs/weixin.md) |
| QQ / QQ Bot | [qq.md](docs/qq.md) |
| Matrix | [matrix.md](docs/matrix.md) |
| WPS Xiezuo | [wps-xiezuo.md](docs/wps-xiezuo.md) |

Channel capabilities differ. Feishu-specific Team registration, project groups and native cards should not be assumed to exist on every adapter.

## Development and documentation

```sh
go build ./...
go test ./...
make test-team
go test ./core -run TestCUJ
go vet ./...
```

Keep core behavior independent of specific agents and platforms, using optional capability interfaces. Changes to user journeys need meaningful regression coverage. See [AGENTS.md](AGENTS.md) and [CONTRIBUTING.md](CONTRIBUTING.md).

- [Usage and commands](docs/usage.md)
- [Agent Team guide](docs/agent-team.md)
- [Annotated runtime configuration](config.example.toml)
- [Team configuration](config.agent-team.example.toml)
- [Design history and local prototypes](docs/prototypes/team-interaction/README.md)

This repository builds on [chenhg5/cc-connect](https://github.com/chenhg5/cc-connect). Prototypes are design references; runtime behavior and tests determine which interactions are implemented.
