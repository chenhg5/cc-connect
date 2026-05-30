# Reasonix Agent Guide

<p align="center">
  <img src="https://img.shields.io/badge/Agent-Reasonix-0f766e?style=for-the-badge" alt="Reasonix agent"/>
  <img src="https://img.shields.io/badge/Transport-ACP%20stdio-2563eb?style=for-the-badge" alt="ACP stdio"/>
  <img src="https://img.shields.io/badge/Model-DeepSeek-b91c1c?style=for-the-badge" alt="DeepSeek"/>
  <img src="https://img.shields.io/badge/Public%20IP-Not%20required-16a34a?style=for-the-badge" alt="No public IP required"/>
</p>

Reasonix is a DeepSeek-native coding agent. cc-connect runs it as a first-class
agent through the Agent Client Protocol (ACP) over stdio:

```text
Telegram / Feishu / Weixin / ... -> cc-connect -> reasonix acp -> DeepSeek
```

Because the integration is at the agent layer, every cc-connect platform can use
the same Reasonix project once the platform credentials are configured.

## Status

| Area | Status |
|------|--------|
| Agent type | `reasonix` |
| Transport | ACP over stdio (`reasonix acp`) |
| Session resume | Supported when Reasonix advertises ACP session loading |
| Permission prompts | Routed through cc-connect chat permission flow |
| Provider switching | Not wired through cc-connect providers yet; configure Reasonix directly |
| Public IP | Not required by the agent; depends only on the chosen platform |

## Verified Channels

These channels were exercised with a local cc-connect build using the Reasonix
ACP adapter:

| Platform | Connection | Result |
|----------|------------|--------|
| Telegram | Long polling | Message received, Reasonix session spawned, reply sent |
| Weixin personal | ilink / OpenClaw-style long polling | QR login, token reuse, message received, reply sent |
| Feishu / Lark | WebSocket long connection | Bot identified, `im.message.receive_v1`, session resumed, reply sent |

The platform setup remains unchanged. Use the existing guides:
[Telegram](telegram.md), [Weixin personal](weixin.md), and
[Feishu / Lark](feishu.md).

## Install Reasonix

```bash
npm install -g reasonix
reasonix --version
reasonix setup
reasonix doctor
```

`reasonix setup` configures the local Reasonix account/API key and defaults.
cc-connect does not need to know the DeepSeek key if Reasonix can read its own
local config.

## Minimal Config

```toml
[[projects]]
name = "reasonix-telegram"

[projects.agent]
type = "reasonix"

[projects.agent.options]
work_dir = "/absolute/path/to/your/project"

[[projects.platforms]]
type = "telegram"

[projects.platforms.options]
token = "${TELEGRAM_BOT_TOKEN}"
allow_from = "123456789"
```

The dedicated `reasonix` agent applies these defaults:

| Option | Default |
|--------|---------|
| `command` | `reasonix` |
| `args` | `["acp"]` |
| `display_name` | `Reasonix` |

## Advanced Config

Use an absolute command path when a service manager such as launchd or systemd
does not inherit your interactive shell `PATH`.

```toml
[[projects]]
name = "reasonix-feishu"

[projects.agent]
type = "reasonix"

[projects.agent.options]
work_dir = "/absolute/path/to/your/project"
command = "/opt/homebrew/bin/reasonix"
args = ["acp", "--yolo", "--preset", "flash"]
mode = "yolo"

[[projects.platforms]]
type = "feishu"

[projects.platforms.options]
app_id = "${FEISHU_APP_ID}"
app_secret = "${FEISHU_APP_SECRET}"
enable_feishu_card = false
allow_from = "ou_xxx"
allow_chat = "oc_xxx"
```

Reasonix supports additional ACP flags such as `--model`, `--budget`,
`--transcript`, `--mcp`, and `--dir`. Keep `acp` as the first argument.

## Secrets

Prefer one of these approaches:

1. Let `reasonix setup` store the DeepSeek credential in Reasonix's own config.
2. Inject `DEEPSEEK_API_KEY` through your service environment.
3. Use cc-connect env substitution for local-only config files:

```toml
[projects.agent.options.env]
DEEPSEEK_API_KEY = "${DEEPSEEK_API_KEY}"
```

Do not commit API keys, bot tokens, Feishu secrets, Weixin tokens, or generated
session stores.

## Runtime Tips

- Send `/whoami` to a platform bot first, then restrict `allow_from`.
- Use `/mode` to inspect modes advertised by the ACP session.
- Use `/new` to start a fresh Reasonix session and `/list` / `/switch` to manage
  saved sessions.
- If the bot works in your terminal but fails under launchd/systemd, set
  `command` to the absolute `reasonix` path and make sure credential files are
  readable by the service user.

## Troubleshooting

| Symptom | Check |
|---------|-------|
| `acp: command "reasonix" not found in PATH` | Use `which reasonix`, then set `command = "/absolute/path/to/reasonix"` |
| Session starts but Reasonix cannot call DeepSeek | Run `reasonix doctor` as the same OS user that runs cc-connect |
| Feishu says `app do not have bot` | Enable the Bot capability in Feishu Open Platform, publish the app, then restart cc-connect |
| Feishu receives no messages | Use WebSocket long connection and subscribe to `im.message.receive_v1` |
| Weixin QR scan succeeds but messages do not arrive | Confirm `allow_from`, restart cc-connect, then send a fresh message to seed the context token |
| Permission request appears in chat | Reply `allow`, `deny`, or `allow all` according to the requested tool |
