# Usage Guide

Complete guide to using cc-connect features.

## Table of Contents

- [Session Management](#session-management)
- [Permission Modes](#permission-modes)
- [API Provider Management](#api-provider-management)
- [Model Selection](#model-selection)
- [Work Directory Switching (`/dir`, `/cd`)](#work-directory-switching-dir-cd)
- [Feishu Setup CLI](#feishu-setup-cli)
- [Weixin (personal) Setup CLI](#weixin-personal-setup-cli)
- [Claude Code Router Integration](#claude-code-router-integration)
- [Voice Messages (STT)](#voice-messages-speech-to-text)
- [Voice Reply (TTS)](#voice-reply-text-to-speech)
- [Image and File Send-Back](#image-and-file-send-back)
- [Scheduled Tasks (Cron)](#scheduled-tasks-cron)
- [Multi-Bot Relay](#multi-bot-relay)
- [Daemon Mode](#daemon-mode)
- [Multi-Workspace Mode](#multi-workspace-mode)
- [Web Admin Dashboard (Beta)](#web-admin-dashboard-beta)
- [Bridge — External Adapter Access (Beta)](#bridge--external-adapter-access-beta)
- [Configuration Reference](#configuration-reference)

---

## Session Management

Each user gets an independent session with full conversation context. Manage sessions via slash commands:

| Command | Description |
|---------|-------------|
| `/new [name]` | Start a new session |
| `/list` | List all agent sessions for this project |
| `/switch <id>` | Switch to a different session |
| `/current` | Show current session info |
| `/history [n]` | Show last n messages (default 10) |
| `/usage` | Show account/model quota usage (if supported) |
| `/provider [...]` | Manage API providers |
| `/model [switch <alias>]` | List available models or switch by alias |
| `/dir [path]` | Show or switch the agent work directory |
| `/allow <tool>` | Pre-allow a tool (next session) |
| `/reasoning [level]` | View or switch reasoning effort (Codex) |
| `/mode [name]` | View or switch permission mode |
| `/stop` | Stop current execution |
| `/help` | Show available commands |

During a session, the agent may request tool permissions. Reply **allow** / **deny** / **allow all**.

You can also configure automatic session rotation after inactivity:

```toml
[[projects]]
name = "demo"
reset_on_idle_mins = 60
```

When enabled, the next normal message after a long idle period starts in a fresh session automatically, without deleting the old session from `/list`.

### Model switch preserves history

`/model` preserves the current session — the agent resumes the conversation with the new model (no extra token cost). Model switching affects the shared agent instance — if multiple platforms use the same project, the model change applies to all of them.

---

## Permission Modes

All agents support permission modes switchable at runtime via `/mode`.

### Claude Code Modes

| Mode | Config Value | Behavior |
|------|-------------|----------|
| Default | `default` | Every tool call requires approval |
| Accept Edits | `acceptEdits` / `edit` | File edits auto-approved |
| Auto | `auto` | Claude decides when to ask for permission |
| Plan Mode | `plan` | Claude only plans, no execution |
| YOLO | `bypassPermissions` / `yolo` | All tools auto-approved |

### Codex Modes

| Mode | Config Value | Behavior |
|------|-------------|----------|
| Suggest | `suggest` | Only trusted commands run without approval |
| Auto Edit | `auto-edit` | Model decides when to ask |
| Full Auto | `full-auto` | Auto-approve with sandbox |
| YOLO | `yolo` | Bypass all approvals and sandbox |

### Cursor Agent Modes

| Mode | Config Value | Behavior |
|------|-------------|----------|
| Default | `default` | Trust workspace, ask before tools |
| Force (YOLO) | `force` / `yolo` | Auto-approve all |
| Plan | `plan` | Read-only analysis |
| Ask | `ask` | Q&A style, read-only |

### Gemini CLI Modes

| Mode | Config Value | Behavior |
|------|-------------|----------|
| Default | `default` | Prompt for approval |
| Auto Edit | `auto_edit` / `edit` | Auto-approve edits |
| YOLO | `yolo` | Auto-approve all |
| Plan | `plan` | Read-only plan mode |

### Qoder CLI / OpenCode / iFlow CLI

| Mode | Config Value | Behavior |
|------|-------------|----------|
| Default | `default` | Standard permissions |
| YOLO | `yolo` | Skip all checks |

### Configuration

```toml
[projects.agent.options]
mode = "default"
# allowed_tools = ["Read", "Grep", "Glob"]
```

Switch at runtime:
```
/mode          # show current and available modes
/mode yolo     # switch to YOLO mode
/mode default  # switch back
```

---

## API Provider Management

Switch between API providers at runtime without restart.

### Configure Providers

```toml
[projects.agent.options]
work_dir = "/path/to/project"
provider = "anthropic"   # active provider

[[projects.agent.providers]]
name = "anthropic"
api_key = "sk-ant-xxx"

[[projects.agent.providers]]
name = "relay"
api_key = "sk-xxx"
base_url = "https://api.relay-service.com"
model = "claude-sonnet-4-20250514"

[[projects.agent.providers.models]]
model = "claude-sonnet-4-20250514"
alias = "sonnet"

[[projects.agent.providers.models]]
model = "claude-opus-4-20250514"
alias = "opus"

[[projects.agent.providers.models]]
model = "claude-haiku-3-5-20241022"
alias = "haiku"

# MiniMax — OpenAI-compatible, 1M context
[[projects.agent.providers]]
name = "minimax"
api_key = "your-minimax-api-key"
base_url = "https://api.minimax.io/v1"
model = "MiniMax-M2.7"

# For Bedrock, Vertex, etc.
[[projects.agent.providers]]
name = "bedrock"
env = { CLAUDE_CODE_USE_BEDROCK = "1", AWS_PROFILE = "bedrock" }
```

### CLI Commands

```bash
cc-connect provider add --project my-backend --name relay --api-key sk-xxx --base-url https://api.relay.com
cc-connect provider list --project my-backend
cc-connect provider remove --project my-backend --name relay
cc-connect provider import --project my-backend  # from cc-switch
```

### Chat Commands

```
/provider                   Show current provider
/provider list              List all providers
/provider add <name> <key> [url] [model]
/provider remove <name>
/provider switch <name>
/provider <name>            Shortcut for switch
```

### Env Var Mapping

| Agent | api_key → | base_url → |
|-------|-----------|------------|
| Claude Code | `ANTHROPIC_API_KEY` | `ANTHROPIC_BASE_URL` |
| Codex | `OPENAI_API_KEY` | `OPENAI_BASE_URL` |
| Gemini CLI | `GEMINI_API_KEY` | use `env` map |
| OpenCode | `ANTHROPIC_API_KEY` | use `env` map |
| iFlow CLI | `IFLOW_API_KEY` | `IFLOW_BASE_URL` |

---

## Model Selection

Pre-configure a list of selectable models per provider using `[[providers.models]]`. Each entry has a `model` identifier and an optional `alias` (short name shown in `/model`).

### Configure Models

```toml
[[projects.agent.providers]]
name = "openai"
api_key = "sk-xxx"

[[projects.agent.providers.models]]
model = "gpt-5.3-codex"
alias = "codex"

[[projects.agent.providers.models]]
model = "gpt-5.4"
alias = "gpt"

[[projects.agent.providers.models]]
model = "gpt-5.3-codex-spark"
alias = "spark"
```

### Chat Commands

```
/model              List available models (format: alias - model)
/model switch <alias>      Switch to the model matching the alias
/model switch <name>       Switch to the model by its full name
/model <alias>             Legacy syntax, still supported
```

When `models` is configured, `/model` shows exactly that list without making an API round-trip. When omitted, models are fetched from the provider API or fall back to a built-in list.

---

## Work Directory Switching (`/dir`, `/cd`)

Switch where the next agent session starts, directly from chat.

### Chat Commands

```
/dir                    Show current work directory and recent history
/dir <path>             Switch to a path (relative or absolute)
/dir <number>           Switch to a directory from history
/dir -                  Switch back to previous directory
/dir help               Show command usage
/cd <path>              Backward-compatible alias of /dir <path>
```

### Behavior Notes

- Directory changes apply to the next session in the current project.
- Relative paths are resolved from the current agent work directory.
- Directory history is project-scoped and can be switched by index.
- `/cd` is kept for compatibility, but `/dir` is the primary command.

Examples:

```text
/dir ../another-repo
/dir 2
/dir -
```

---

## Running agents as a different Unix user (`run_as_user`)

> **Platform support**: Linux and macOS. Not supported on Windows.
> **Agent support**: Claude Code today. Other agents fall back to the
> supervisor user; see the tracking issue for migration status.

### What this is

By default, every agent session cc-connect spawns runs as the same Unix
user that runs `cc-connect` itself. If an agent misbehaves — reads a
secret, overwrites a sibling repo, trashes `~/.ssh/` — it has the
supervisor user's full file-system reach.

`run_as_user` sets a per-project target Unix user. When it is set,
cc-connect spawns that project's agent command via

```
sudo -n -iu <target-user> -- claude ...
```

The target user is a real, unprivileged Unix account that you create.
The agent runs under that account's uid/gid, with **its own** home
directory, shell profile, PATH, and tool credentials. File-system
isolation is enforced by the kernel, not by hooks or allowlists.

### Security guarantee and non-guarantee

**This provides OS-user isolation from any file or process the target
user cannot reach.** An agent can no longer read or clobber the
supervisor's `~/.ssh/`, another project user's `~/.pgpass`, or a repo
whose UNIX permissions don't grant access to the target user.

**This does not automatically isolate projects from each other** if they
share the same `run_as_user`. If you want per-project isolation, create
a separate Unix user per project.

**This is not a sandbox in the sense of Linux namespaces, seccomp, or
container isolation.** It is strictly file-system scoping by uid.

### Setup

#### 1. Create the target user and install their tooling

The target user needs its own copy of everything the agent touches,
because `sudo -i` loads the *target* user's login environment — not the
supervisor's.

```bash
sudo useradd -m -s /bin/bash partseeker-coder
sudo -iu partseeker-coder

# Install the agent CLI under the target user's PATH
#   (for Claude Code, follow the normal install instructions)

# Set up the target user's ~/.claude/
mkdir -p ~/.claude
# Copy or re-create:
#   ~/.claude/settings.json     (MCP servers, hooks, model settings)
#   ~/.claude.json              (Claude Code auth)
#   ~/.claude/plugins/          (claude-mem and any other plugin state)

exit
```

#### 2. Grant the supervisor passwordless sudo to the target

Add a scoped sudoers rule. Do **not** use `NOPASSWD: ALL` for the
supervisor — that grants the supervisor root, which is irrelevant here
and dangerous.

```
# /etc/sudoers.d/cc-connect (install with `sudo visudo -f ...`)
partseeker-orchestrator ALL=(partseeker-coder) NOPASSWD: ALL
```

Adjust the usernames for your setup. The rule says: *"the supervisor
user may run any command as this specific target user, without a
password."*

#### 3. Verify the target user cannot sudo

The whole point of stepping down into a target user is that the target
cannot immediately escalate back. Verify:

```bash
sudo -n -iu partseeker-coder -- sudo -n true
# must FAIL with "a password is required" or similar
```

If that command succeeds, cc-connect will refuse to start. Remove any
`NOPASSWD` sudo grants for the target user first.

#### 4. Make the project's `work_dir` accessible to the target user

The target user needs read AND write on the project's `work_dir`. If
the directory is owned by the supervisor, either `chown` it to the
target, add group ownership the target is in, or apply a POSIX ACL:

```bash
sudo setfacl -R -m u:partseeker-coder:rwX /home/leigh/workspace/sandboxed-repo
sudo setfacl -R -dm u:partseeker-coder:rwX /home/leigh/workspace/sandboxed-repo
```

cc-connect refuses to start if the target user cannot read+write the
`work_dir` root, and warns (non-fatal) for descendant paths that look
inaccessible.

#### 5. Audit the setup before starting cc-connect

```bash
cc-connect doctor user-isolation
```

This runs the full preflight (the three go/no-go gates from
[#496](https://github.com/chenhg5/cc-connect/issues/496)) and an
**isolation probe**: it spawns a fixed shell script as the target user
and reports what the target can read, what it's denied, and any
cross-user leaks. Output goes to stdout plus a JSON report in
`~/.cc-connect/audits/<timestamp>-<project>.json`.

Exit code 0 = clean. Exit code 1 = at least one fatal problem.

You can inspect the probe script itself with:

```bash
cc-connect doctor user-isolation --print-script
```

### Configuration

```toml
[[projects]]
name = "claude-sandboxed"
run_as_user = "partseeker-coder"

# Optional: extend the default env var allowlist that crosses the sudo
# boundary. The defaults (PATH, LANG, LC_*, TERM) are always included.
# Only list vars the target user cannot reasonably set in their own
# shell profile. Secrets belong in the target user's ~/.claude/settings.json
# env block, NOT here.
run_as_env = ["PGSSLROOTCERT", "PGSSLMODE"]

[projects.agent]
type = "claudecode"

[projects.agent.options]
mode = "default"
model = "sonnet"
work_dir = "/home/leigh/workspace/sandboxed-repo"
```

### Environment propagation: what moves into the target user's home

This is the 2am-debugging section. When you switch a project to
`run_as_user`, the supervisor's environment is **not** forwarded across
the sudo boundary — that's the whole point. Everything the agent needs
has to live in the target user's home.

Migration checklist:

- [ ] **Agent config** — `~/.claude/settings.json` (MCP servers, hooks,
      model settings), `~/.claude.json` (auth). Copy from the supervisor
      or re-create from scratch.
- [ ] **Plugin state** — `~/.claude/plugins/` — claude-mem, any other
      Claude Code plugins.
- [ ] **MCP server binaries** — must be on the target user's `PATH`, not
      just the supervisor's. Either install under the target user or
      reference full paths in `settings.json`.
- [ ] **Postgres TLS** — `PGSSLROOTCERT`, `PGSSLCERT`, `PGSSLKEY` belong
      in the target user's `~/.claude/settings.json` `env` block. Their
      referenced cert files must be readable by the target user.
- [ ] **Claude OAuth credentials** — if you authenticate via `claude.ai`
      (OAuth), the token lives in `~/.claude/.credentials.json`. OAuth
      access tokens expire after a few hours and are refreshed
      automatically by whichever Claude CLI session is running. The
      target user's token will **not** be refreshed unless the target
      user has an active session — which it often doesn't between
      cc-connect spawns. The recommended fix is to symlink the target
      user's credentials to the supervisor's file so both share one
      token that stays fresh:

      ```bash
      # Grant target user read access via ACL (keeps 600 for everyone else)
      setfacl -m u:<target-user>:rx ~/.claude/
      setfacl -m u:<target-user>:r  ~/.claude/.credentials.json

      # Replace the target user's credentials with a symlink
      sudo -iu <target-user> bash -c \
        'rm -f ~/.claude/.credentials.json && \
         ln -s /home/<supervisor>/.claude/.credentials.json \
               ~/.claude/.credentials.json'
      ```

      **If you use an API key** (`ANTHROPIC_API_KEY`) instead of OAuth,
      this is not an issue — set the key in the target user's
      `~/.claude/settings.json` `env` block and it won't expire.
- [ ] **Credential files** — `~/.pgpass`, `~/.gitconfig`, `~/.netrc`,
      `~/.aws/`, `~/.config/gh/`, `~/.kube/` — whichever the agent
      actually uses. Each needs its own copy or a group-readable shared
      copy.
- [ ] **SSH keys** — `~/.ssh/id_ed25519` etc., if the agent runs `git
      push` over SSH. Same story: copy or group-share.
- [ ] **Key material under** `~/keys/` — custom directories the
      supervisor uses need an equivalent under the target user's home
      or a group-readable shared copy.
- [ ] **Language toolchains** — if the agent uses `asdf`, `mise`, `nvm`,
      `rustup`, etc., those live in `~`. The target user needs either
      its own install or a system-wide install that both users can run.
- [ ] **Shell profile** — `~/.profile` / `~/.bashrc` on the target user
      needs to set `PATH` and any tool init the agent depends on. Test
      with `sudo -iu partseeker-coder` before wiring cc-connect.

After migration, run `cc-connect doctor user-isolation` again. The
`target home` section reports which expected paths are present and
which are missing — missing isn't necessarily wrong, but it's your
checklist.

### Opting out

Remove `run_as_user` from the project entry, or set it to `""`. Legacy
behavior (spawn as supervisor) returns on the next restart.

### Failure modes and error messages

- **"passwordless sudo to user X is not configured"** — step 2 of setup
  is missing or the sudoers rule is scoped to the wrong supervisor. Fix
  the rule, run `visudo -c` to validate syntax, then restart cc-connect.
- **"target user X can run passwordless sudo"** — step 3 failed. The
  error includes the output of `sudo -l` from the target context; find
  the offending rule and remove it.
- **"target user X cannot read AND write work_dir Y"** — step 4 failed.
  `chown` the directory or add an ACL as shown above.
- **"CROSS_LEAKED"** or **"SUPERVISOR_LEAKED"** in the audit — the
  target user can read another user's secrets. Tighten the offending
  file's permissions (usually `chmod 600 file; chown user:user file`)
  and re-audit.
- **"descendant scan timed out"** — non-fatal. The `work_dir` is large
  enough that the permission walk exceeded its timeout. Run
  `cc-connect doctor user-isolation` manually if you want the full
  walk, or narrow the project's `work_dir`.

---

## Feishu Setup CLI

Use CLI to create or bind Feishu/Lark bot credentials and write them back to `config.toml`.

```bash
# Recommended: unified entry
cc-connect feishu setup --project my-project
cc-connect feishu setup --project my-project --app cli_xxx:sec_xxx

# Force modes (usually unnecessary)
cc-connect feishu new --project my-project
cc-connect feishu bind --project my-project --app cli_xxx:sec_xxx
```

Differences:
- `setup`: unified entry. No credentials => behaves like `new`; with `--app` => behaves like `bind`.
- `new`: force QR onboarding flow; rejects `--app`.
- `bind`: force credential binding flow; requires credentials.

Behavior:
- `setup` uses QR onboarding by default, or bind mode when `--app` is provided.
- If `--project` does not exist, it is created automatically.
- If project exists but has no `feishu/lark` platform, one is added automatically.
- The command writes credentials (`app_id`, `app_secret`); in QR onboarding flow, Feishu usually pre-configures permissions and event subscriptions.
- Still verify app publish status and availability scope in Feishu Open Platform.
- Runtime platform config also supports an optional `domain` override for Feishu/Lark API endpoints; this does not change setup/onboarding URLs.

---

## Weixin (personal) Setup CLI

Weixin personal chat uses the **ilink bot HTTP API** (long polling + `sendMessage`, same family as OpenClaw `openclaw-weixin`). Use the CLI to scan a QR code or bind an existing Bearer token and write `config.toml`.

**Full walkthrough (Chinese): [docs/weixin.md](./weixin.md).**

```bash
cc-connect weixin setup --project my-project
cc-connect weixin bind --project my-project --token '<token>'
cc-connect weixin new --project my-project
```

Notes:
- `setup` without `--token` runs QR login; with `--token` behaves like bind.
- Auto-creates the project and/or a `weixin` platform block when missing.
- After login, send a message from WeChat once so `context_token` is cached.
- See `cc-connect weixin help` for flags (`--api-url`, `--cdn-url`, `--route-tag`, etc.).

---

## Claude Code Router Integration

[Claude Code Router](https://github.com/musistudio/claude-code-router) routes requests to different model providers.

### Setup

1. Install: `npm install -g @musistudio/claude-code-router`

2. Configure `~/.claude-code-router/config.json`:
```json
{
  "APIKEY": "your-secret-key",
  "Providers": [
    {
      "name": "deepseek",
      "api_base_url": "https://api.deepseek.com/chat/completions",
      "api_key": "sk-xxx",
      "models": ["deepseek-chat", "deepseek-reasoner"],
      "transformer": { "use": ["deepseek"] }
    }
  ],
  "Router": {
    "default": "deepseek,deepseek-chat",
    "think": "deepseek,deepseek-reasoner"
  }
}
```

3. Start: `ccr start`

4. Configure cc-connect:
```toml
[projects.agent.options]
router_url = "http://127.0.0.1:3456"
router_api_key = "your-secret-key"  # optional
```

---

## Voice Messages (Speech-to-Text)

Send voice messages — cc-connect transcribes them automatically.

**Supported:** Feishu, WeChat Work, Telegram, LINE, Discord, Slack

**Requirements:** OpenAI/Groq API key, `ffmpeg`

### Configure

```toml
[speech]
enabled = true
provider = "openai"    # or "groq"
language = ""          # "zh", "en", or auto-detect

[speech.openai]
api_key = "sk-xxx"
# base_url = ""
# model = "whisper-1"

# [speech.groq]
# api_key = "gsk_xxx"
# model = "whisper-large-v3-turbo"
```

### Install ffmpeg

```bash
# Ubuntu/Debian
sudo apt install ffmpeg

# macOS
brew install ffmpeg
```

---

## Voice Reply (Text-to-Speech)

Synthesize AI replies into voice messages.

**Supported:** Feishu (Lark)

### Configure

```toml
[tts]
enabled = true
provider = "qwen"        # or "openai"
voice = "Cherry"
tts_mode = "voice_only"  # "voice_only" | "always"
max_text_len = 0         # 0 = no limit

[tts.qwen]
api_key = "sk-xxx"
# model = "qwen3-tts-flash"
```

### TTS Modes

| Mode | Behavior |
|------|----------|
| `voice_only` | Reply with voice only when user sends voice |
| `always` | Always send voice reply |

Switch: `/tts always` or `/tts voice_only`

---

## Image and File Send-Back

When an agent generates a local image, PDF, report, bundle, or other file and needs to deliver it directly to the current chat, use attachment mode in `cc-connect send`.

**Currently supported platforms:**
- Feishu
- Telegram

### When to run setup first

If the current agent does not natively inject the system prompt, run this once in chat after upgrading:

```text
/bind setup
```

or:

```text
/cron setup
```

These two commands write the same cc-connect instructions. Either one is enough. After that, the agent knows:
- normal text replies should be returned normally
- generated attachments should be sent back with `cc-connect send --image/--file`

If you have run setup before, run it again after upgrading so the instructions are refreshed to the latest version.

### Config switch

Add this to `config.toml` if you want to disable agent-driven attachment send-back:

```toml
attachment_send = "off"
```

The default is `on`. This switch is independent from the agent's `/mode` and only affects `cc-connect send --image/--file`.

### CLI examples

```bash
cc-connect send --image /absolute/path/to/chart.png
cc-connect send --file /absolute/path/to/report.pdf
cc-connect send --file /absolute/path/to/report.pdf --image /absolute/path/to/chart.png
```

Notes:
- `--image` is for image attachments.
- `--file` is for any file attachment.
- `--message` is optional and sends a text note before the attachments.
- `--image` and `--file` can both be repeated.
- Absolute paths are recommended so the command does not depend on the agent's current working directory.
- With `attachment_send = "off"`, image/file send-back is blocked but ordinary text replies still work.

### Typical use cases

1. The agent generates a screenshot or chart and should send it directly to the user.
2. The agent generates a PDF, Markdown export, log bundle, or patch file that should be delivered as an attachment.
3. The agent wants to send a short status message together with one or more generated files.

### Important notes

- This command is for attachment delivery, not ordinary text replies.
- The files must exist on the local machine where the agent runs.
- There must be an active session; otherwise the command fails because cc-connect has no chat context to deliver to.
- Platform-specific file size and file type limits still apply.

---

## Scheduled Tasks (Cron)

Create scheduled tasks that run automatically.

### Chat Commands

```
/cron                                          List all jobs
/cron add <min> <hour> <day> <mon> <wk> <prompt>   Create job
/cron del <id>                                 Delete job
/cron enable <id>                              Enable job
/cron disable <id>                             Disable job
```

Example:
```
/cron add 0 6 * * * Summarize GitHub trending repos
```

### CLI Commands

```bash
cc-connect cron add --cron "0 6 * * *" --prompt "Summarize GitHub trending" --desc "Daily Trending"
cc-connect cron list
cc-connect cron del <job-id>
```

Optional: `--session-mode new-per-run` starts a fresh agent session on each run (default is `reuse`, same as before). `--timeout-mins N` sets how long the scheduler waits per run (`0` = no limit; omit = 30 minutes).

### Natural Language (Claude Code)

> "Every day at 6am, summarize GitHub trending"

Claude Code auto-creates the cron job. For other agents that rely on memory files, run `/cron setup` or `/bind setup` once first; both write the same instructions.

---

## Multi-Bot Relay

Cross-platform bot communication in group chats.

### Group Chat Binding

```
/bind              Show bindings
/bind claudecode   Add claudecode project
/bind gemini       Add gemini project
/bind -claudecode  Remove claudecode
```

### Bot-to-Bot Communication

```bash
cc-connect relay send --to gemini "What do you think about this architecture?"
```

---

## Daemon Mode

Run as background service.

```bash
cc-connect daemon install --config ~/.cc-connect/config.toml
cc-connect daemon start
cc-connect daemon stop
cc-connect daemon restart
cc-connect daemon status
cc-connect daemon logs [-f]
cc-connect daemon uninstall
```

---

## Multi-Workspace Mode

One bot serving multiple workspaces per channel.

### Configure

```toml
[[projects]]
name = "my-project"
mode = "multi-workspace"
base_dir = "~/workspaces"

[projects.agent]
type = "claudecode"
```

### Commands

```
/workspace                    Show current binding
/workspace bind <name>        Bind local folder
/workspace init <git-url>     Clone and bind repo
/workspace unbind             Remove binding
/workspace list               List all bindings
```

### How It Works

- Channel name `#project-a` → auto-binds to `base_dir/project-a/`
- Each channel has isolated sessions and agent state

---

## Web Admin Dashboard (Beta)

> **Status: Beta.** This feature is available since v1.2.2-beta.5. The UI and API may change in future releases.

A full-featured management UI embedded in the binary — project CRUD, session management, cron job editor, global settings, chat interface, and i18n support.

### Quick Setup (Chat Command)

The easiest way to enable web admin:

```
/web setup
```

This automatically enables both the **Management API** and the **Bridge** in `config.toml`, generates tokens, and prints the access URL. You may need to run `/restart` for changes to take effect.

After setup, open the URL shown (default `http://localhost:9820`) and log in with the token.

### Check Status

```
/web           # or /web status — show current web admin URL and status
```

### Manual Configuration

Add the following to `config.toml`:

```toml
[management]
enabled = true
port = 9820                     # Management UI & API listen port
token = "your-secret-token"     # Login token; /web setup generates one automatically
cors_origins = ["*"]            # Allowed CORS origins; empty = no CORS headers
```

Then restart cc-connect.

### Build Options

Web assets are compiled into the binary by default. To exclude them (saves ~1MB):

```bash
make build-noweb
# or
go build -tags 'no_web' ./cmd/cc-connect
```

When built with `no_web`, the `/web` command will report that web admin is not available.

### Management API

The Management API is served on the same port as the UI. Base URL: `http://<host>:<port>/api/v1`

All API requests require the `Authorization: Bearer <token>` header.

Key endpoints:

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/api/v1/status` | System status (version, uptime, platforms) |
| `POST` | `/api/v1/restart` | Restart cc-connect |
| `POST` | `/api/v1/reload` | Reload configuration |
| `GET` | `/api/v1/projects` | List projects |
| `GET` | `/api/v1/sessions?project=<name>` | List sessions for a project |
| `GET` | `/api/v1/cron` | List cron jobs |
| `GET` | `/api/v1/settings` | Get global settings |
| `PATCH` | `/api/v1/settings` | Update global settings |

Full API reference: [management-api.md](./management-api.md)

---

## Bridge — External Adapter Access (Beta)

> **Status: Beta.** This feature is available since v1.2.2-beta.5. The protocol may change in future releases.

The Bridge exposes a WebSocket + REST server so external adapters (custom UIs, bots, scripts) can interact with cc-connect sessions — send messages, receive events, manage sessions.

### Enable via Chat

The `/web setup` command enables Bridge automatically alongside the Management API.

### Manual Configuration

Add the following to `config.toml`:

```toml
[bridge]
enabled = true
port = 9810                     # Bridge listen port (separate from management)
token = "your-bridge-secret"    # Auth token for WebSocket and REST
path = "/bridge/ws"             # WebSocket endpoint path
cors_origins = ["*"]            # Allowed CORS origins; empty = no CORS
```

Then restart cc-connect.

### Authentication

All Bridge connections require a token. Supported methods:

- Query parameter: `?token=<bridge-token>`
- Header: `Authorization: Bearer <bridge-token>`
- Header: `X-Bridge-Token: <bridge-token>`

### WebSocket

Connect to:

```
ws://<host>:<bridge-port>/bridge/ws?token=<bridge-token>
```

The WebSocket supports bidirectional messaging — send user messages to the agent and receive agent events (text, tool calls, permission requests, etc.) in real time.

### REST API

Served on the same port as the WebSocket.

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/bridge/sessions?session_key=...&project=...` | List sessions |
| `POST` | `/bridge/sessions` | Create a new session |
| `GET` | `/bridge/sessions/{id}?session_key=...&project=...` | Get session detail + history |
| `DELETE` | `/bridge/sessions/{id}?session_key=...&project=...` | Delete a session |
| `POST` | `/bridge/sessions/switch` | Switch active session |

Full protocol reference: [bridge-protocol.md](./bridge-protocol.md)

### Port Summary

| Service | Default Port | Config Block |
|---------|-------------|--------------|
| Management (Web UI + API) | 9820 | `[management]` |
| Bridge (WebSocket + REST) | 9810 | `[bridge]` |

---

## Configuration Reference

See [config.example.toml](../config.example.toml) for full examples.

### Project Structure

```toml
[[projects]]
name = "my-project"

[projects.agent]
type = "claudecode"  # or codex, cursor, gemini, qoder, opencode, iflow

[projects.agent.options]
work_dir = "/path/to/project"
mode = "default"
provider = "anthropic"

[[projects.platforms]]
type = "feishu"  # or dingtalk, telegram, slack, discord, wecom, weixin, line, qq, qqbot

[projects.platforms.options]
# platform-specific options
```
