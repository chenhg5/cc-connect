# Google Chat Setup Guide

This guide walks you through connecting **cc-connect** to Google Chat, so you can chat with your local Claude Code from a Google Chat space or DM.

cc-connect uses a registered **Google Chat app** whose **Cloud Pub/Sub connection** publishes events to a topic. cc-connect pulls that topic locally and replies through the Chat REST API as the app's service account. This means:

- **No public IP / domain / reverse proxy** — events arrive over a Pub/Sub pull.
- **No subscription expiry or per-restart resource leak** — the Pub/Sub subscription is fixed (unlike the Workspace Events API, whose subscriptions expire and are recreated each run).

## Prerequisites

- A **Google Workspace** account. The Google Chat API is only available to Workspace users; consumer `@gmail.com` accounts cannot configure a Chat app. (This applies to every Chat-app/REST integration, not just cc-connect.)
- A Google Cloud project (billing enabled).
- `gws` (google-workspace-cli) installed and logged in (`gws auth login`). cc-connect uses it to pull events.
- `gcloud` CLI installed and authenticated (for the one-time GCP setup below).
- Claude Code installed and configured.

> ℹ️ **One Chat app per project.** Google Cloud allows exactly one Chat app configuration per project. To run multiple Chat apps, use separate projects.

---

## Architecture

```
┌──────────────────────────────────────────────────────────────┐
│                        Google Chat                            │
│                                                               │
│  Your message ─→ Chat app (Cloud Pub/Sub connection)          │
│                       │                                       │
│                       ▼                                       │
│            Cloud Pub/Sub topic ──→ subscription               │
└───────────────────────────────────────┼──────────────────────┘
                                         │ pull (gws, no public IP)
                                         ▼
┌──────────────────────────────────────────────────────────────┐
│                     Your Local Machine                        │
│                                                               │
│  cc-connect ◄──► Claude Code CLI ◄──► Your Project Code       │
│       │                                                       │
│       └─ reply: Chat REST API as service account (chat.bot)   │
└──────────────────────────────────────────────────────────────┘
```

- **Receive**: `gws events +subscribe --subscription <sub>` pulls the topic and emits one JSON event per line. gws authenticates with its own OAuth login.
- **Send**: cc-connect posts to the Chat REST API authenticated as the app's **service account** (`chat.bot` scope), so replies appear as the bot. (gws has no service-account auth, so sending is done natively by cc-connect.)

---

## Step 1: Enable APIs

In your Google Cloud project, enable:

- **Google Chat API** (`chat.googleapis.com`)
- **Cloud Pub/Sub API** (`pubsub.googleapis.com`)

```bash
gcloud services enable chat.googleapis.com pubsub.googleapis.com --project YOUR_PROJECT_ID
```

---

## Step 2: Create the Pub/Sub topic and subscription

Create a topic, allow Google Chat to publish to it, and create a pull subscription that cc-connect will read.

```bash
PROJECT_ID=YOUR_PROJECT_ID

# Topic the Chat app publishes events to
gcloud pubsub topics create cc-connect-chat --project "$PROJECT_ID"

# Allow Google Chat's system service account to publish
gcloud pubsub topics add-iam-policy-binding cc-connect-chat --project "$PROJECT_ID" \
  --member='serviceAccount:chat-api-push@system.gserviceaccount.com' \
  --role='roles/pubsub.publisher'

# Pull subscription cc-connect reads
gcloud pubsub subscriptions create cc-connect-chat-sub --topic cc-connect-chat --project "$PROJECT_ID"
```

The subscription resource name is `projects/YOUR_PROJECT_ID/subscriptions/cc-connect-chat-sub` — you'll put this in `config.toml`.

---

## Step 3: Create a service account for replies

The bot replies via the Chat REST API authenticated as a service account.

```bash
gcloud iam service-accounts create cc-connect-bot --project "$PROJECT_ID" \
  --display-name "cc-connect Chat bot"

# Download a JSON key (this is a secret — store it safely, e.g. ~/.config/cc-connect/)
mkdir -p ~/.config/cc-connect
gcloud iam service-accounts keys create ~/.config/cc-connect/cc-connect-bot-key.json \
  --iam-account="cc-connect-bot@${PROJECT_ID}.iam.gserviceaccount.com" \
  --project "$PROJECT_ID"
chmod 600 ~/.config/cc-connect/cc-connect-bot-key.json
```

> ⚠️ The key file grants the bot's identity — keep it private and never commit it.

No extra IAM role on the project is required: a service account in the project, calling the Chat API with the `chat.bot` scope, acts as the configured Chat app.

---

## Step 4: Configure the Chat app

Go to **[Chat API → Configuration](https://console.cloud.google.com/apis/api/chat.googleapis.com/hangouts-chat)** (select your project) and set:

| Section | Setting |
|---------|---------|
| **App status** | **Live — available to users** (the app must be LIVE or it cannot send replies) |
| Build as a Workspace add-on | **Unchecked** (Cloud Pub/Sub connection requires the classic app model; do **not** convert to add-on — it's one-way) |
| **Application info** | App name (e.g. `Claude`), Avatar URL (HTTPS square image), Description |
| Interactive features | Enable |
| **Functionality** | ☑ Receive 1:1 messages (for DM use) and/or ☑ Join spaces and group conversations (for @mention in spaces) |
| **Connection settings** | **Cloud Pub/Sub** → topic `projects/YOUR_PROJECT_ID/topics/cc-connect-chat` |
| **Visibility** | Make available to specific people → enter **your own email only** (keeps the app private to you; up to 5 people or a group) |

Click **Save**.

---

## Step 5: Configure cc-connect

Add a `googlechat` platform to your `config.toml`:

```toml
[[projects]]
name = "my-project"

[projects.agent]
type = "claudecode"

[projects.agent.options]
work_dir = "/path/to/your/project"   # must be a real directory
mode = "default"

[[projects.platforms]]
type = "googlechat"

[projects.platforms.options]
# Pub/Sub subscription the Chat app publishes to (required)
subscription = "projects/YOUR_PROJECT_ID/subscriptions/cc-connect-chat-sub"
# Service-account key used to reply as the bot (chat.bot). Without it the bot
# can receive but not send.
credentials_file = "/Users/you/.config/cc-connect/cc-connect-bot-key.json"
# Allowed sender IDs (e.g. "users/1234567890"); "*" = everyone (default).
allow_from = "*"
# "space" (default) | "thread" | "user"
session_scope = "space"
# Optional: only handle messages starting with this prefix (e.g. "claude:").
# Leave empty to handle the whole message.
# trigger = ""
# gws_path = "gws"   # path to the gws binary if not on PATH
```

### Options reference

| Option | Required | Purpose |
|--------|----------|---------|
| `subscription` | ✅ | Pub/Sub subscription the Chat app publishes to |
| `credentials_file` | recommended | Service-account JSON key for bot replies (`chat.bot`). Omit → receive-only |
| `allow_from` | — | Comma-separated allowed sender IDs; `*` = all |
| `session_scope` | — | `space` (default) / `thread` / `user` |
| `trigger` | — | If set, only messages starting with the prefix are handled (prefix stripped) |
| `gws_path` | — | Path to the `gws` binary (default `gws`) |

---

## Step 6: Start cc-connect

```bash
cc-connect
# or: cc-connect --config /path/to/config.toml
```

You should see:

```
level=INFO msg="googlechat: started" subscription=projects/.../cc-connect-chat-sub scope=space
```

---

## Step 7: Start chatting

- **DM**: in Google Chat, search for your app name, open a DM, and send a message. Every message in the DM reaches the bot.
- **Space**: add the app to a space and **@mention** it (in spaces, a Chat app only receives messages that @mention it).

The bot replies in-thread as the app.

> 💡 Find your own sender ID for `allow_from` by running with `[log] level = "debug"` and sending a message — the log shows `sender=users/<id>`. Then restrict `allow_from` to that ID.

---

## Key facts

- **The Chat app must be Live.** If App status is not *Live*, the app neither receives events nor sends replies. Set it to *Live — available to users* in the Chat API Configuration tab.
- **gws OAuth (receive) vs service account (send) are separate.** Receiving uses gws's own login; replying uses the service-account key in `credentials_file`.
- **Fixed subscription = no expiry, no leak.** Unlike the Workspace Events API, the Chat app's Pub/Sub connection uses one stable topic/subscription, so there is no subscription to renew and nothing is recreated on restart.
- **One Chat app per Google Cloud project.**

---

## FAQ

### Q: I sent a message but the bot doesn't respond at all.

1. Is the Chat app **App status = Live**? (required for both receiving and sending)
2. Is `cc-connect` running, and is there a `gws events +subscribe` child process? (receive path)
3. Is `gws` logged in? (`gws auth status`)
4. Is `work_dir` a real directory? The agent can't start otherwise.
5. Give the agent a few seconds on the first message (cold start).

### Q: It receives but never replies.

1. Is `credentials_file` set and readable, and is it a service-account key in the same project?
2. Check logs for `googlechat: send: status ...`.

### Q: "Google Chat app is inactive" error when sending.

Set App status to **Live — available to users** in the Chat API Configuration tab.

### Q: "Google Chat API is only available to Google Workspace users."

You're signed in with a consumer account. Use a Google Workspace account.

### Q: Connection settings has no "Cloud Pub/Sub" option.

The "Build this Chat app as a Google Workspace add-on" checkbox is on. Cloud Pub/Sub is available in the classic model — clear that checkbox (or keep the add-on model, which also supports Pub/Sub but is configured differently).

---

## References

- [Configure the Google Chat API](https://developers.google.com/workspace/chat/configure-chat-api)
- [Build a Google Chat app behind a firewall with Pub/Sub](https://developers.google.com/workspace/chat/quickstart/pub-sub)
- [Authenticate as a Chat app (service account)](https://developers.google.com/workspace/chat/authenticate-authorize)

---

## See Also

- [Slack Setup](./slack.md)
- [Feishu Setup](./feishu.md)
- [Telegram Setup](./telegram.md)
- [Back to README](../README.md)
