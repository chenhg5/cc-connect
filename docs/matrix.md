# Matrix Setup Guide

This guide walks you through connecting **cc-connect** to [Matrix](https://matrix.org/), the open standard for decentralized communication. Once set up, you can chat with your local AI agent from any Matrix client (Element, FluffyChat, Nheko, etc.).

## Prerequisites

- A Matrix account on any homeserver (public like `matrix.org`, or self-hosted)
- A machine that can run cc-connect (no public IP needed)
- Claude Code (or another supported agent) installed and configured

> **Advantage**: Uses `/sync` long polling — no public IP, no domain, no reverse proxy needed. Works behind NAT and firewalls.

---

## Step 1: Create a Matrix Account

If you don't already have a Matrix account:

1. Visit [https://app.element.io](https://app.element.io) (or your self-hosted Element instance)
2. Click **Create Account**
3. Choose a homeserver (the default `matrix.org` works for most users)
4. Complete registration

You can also use any existing Matrix account — a dedicated bot account is recommended but not required.

---

## Step 2: Get Your Access Token

You need an access token so cc-connect can authenticate as your Matrix user.

### Via Element (Web/Desktop)

1. Log in to **Element** ([app.element.io](https://app.element.io))
2. Open **Settings** (click your avatar → **Settings**)
3. Go to **Help & About** → scroll to **Advanced**
4. Click **Access Token** → copy the token

> **Warning**: Treat your access token like a password. Anyone with it can send messages as you. If it leaks, you can invalidate it by logging out of all sessions in Element.

### Via curl (alternative)

```bash
curl -XPOST "https://matrix.org/_matrix/client/v3/login" \
  -d '{"type":"m.login.password","user":"your-username","password":"your-password"}'
```

The response contains `"access_token": "syt_..."`.

---

## Step 3: Find Your User ID (Optional)

Your user ID looks like `@username:matrix.org`. cc-connect can auto-detect it from the access token, but you can also specify it explicitly in config.

In Element: click your avatar — your user ID is shown at the top.

---

## Step 4: Configure cc-connect

Add the Matrix platform to your `config.toml`:

```toml
[[projects]]
name = "my-project"

[projects.agent]
type = "claudecode"

[projects.agent.options]
work_dir = "/path/to/your/project"
mode = "default"

[[projects.platforms]]
type = "matrix"

[projects.platforms.options]
homeserver = "https://matrix.org"
access_token = "syt_xxx_xxx"

# ── Optional settings ────────────────────────────────────────
# user_id = "@bot:matrix.org"        # auto-detected if omitted
# allow_from = "*"                   # "*" = all users, or "id1,id2"
# auto_join = true                   # auto-accept room invites (default: true)
# share_session_in_channel = false   # true = all users share one session per room
# group_reply_all = false            # true = respond to all messages in group rooms
# proxy = ""                         # HTTP/SOCKS5 proxy, e.g. "http://proxy:8080"
```

> **Common mistake:** `homeserver` must include the scheme (`https://`) and must be the same server your account is registered on.

---

## Step 5: Start cc-connect

```bash
cc-connect
# Or specify a config file
cc-connect -config /path/to/config.toml
```

You should see logs like:

```
level=INFO msg="matrix: connected" user=@bot:matrix.org
level=INFO msg="platform started" project=my-project platform=matrix
level=INFO msg="cc-connect is running" projects=1
```

---

## Step 6: Start Chatting

### 6.1 Direct Message

1. Open your Matrix client (Element, FluffyChat, etc.)
2. Start a new DM with the bot's user ID (e.g. `@bot:matrix.org`)
3. Send a message — cc-connect will respond

### 6. Group Chat

1. Create or open a room
2. Invite the bot's user ID to the room
3. The bot will auto-join if `auto_join = true` (default)
4. Send messages in the room

> **Note**: In group rooms, the bot responds when mentioned (e.g. `@bot:matrix.org`) or when `group_reply_all = true` is set.

---

## Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                    Matrix Homeserver                         │
│                                                              │
│   User Message ──→ /sync endpoint ◄── Long Polling          │
│                          ▲                                   │
└──────────────────────────┼───────────────────────────────────┘
                           │
                           │ HTTPS (no public IP needed)
                           ▼
┌─────────────────────────────────────────────────────────────┐
│                    Your Local Machine                         │
│                                                              │
│   cc-connect ◄──► Claude Code CLI ◄──► Your Project Code    │
│                                                              │
└─────────────────────────────────────────────────────────────┘
```

---

## Configuration Reference

| Option | Required | Default | Description |
|--------|----------|---------|-------------|
| `homeserver` | Yes | — | Matrix homeserver URL (e.g. `https://matrix.org`) |
| `access_token` | Yes | — | Access token for authentication |
| `user_id` | No | auto-detected | Matrix user ID (e.g. `@bot:matrix.org`) |
| `allow_from` | No | `"*"` | Comma-separated user IDs allowed to interact, or `"*"` for all |
| `auto_join` | No | `true` | Automatically accept room invitations |
| `share_session_in_channel` | No | `false` | Share a single agent session among all users in a room |
| `group_reply_all` | No | `false` | Respond to all messages in group rooms (not just mentions) |
| `proxy` | No | `""` | HTTP or SOCKS5 proxy URL |

---

## FAQ

### Q: Bot doesn't respond to messages?

1. Is cc-connect running and showing `matrix: connected` in logs?
2. Is the access token valid? Try regenerating it.
3. In group rooms, is the bot mentioned or is `group_reply_all = true` set?

### Q: How to restrict who can use the bot?

Set `allow_from` to a comma-separated list of Matrix user IDs:

```toml
allow_from = "@alice:matrix.org,@bob:matrix.org"
```

### Q: Bot doesn't join rooms?

Make sure `auto_join = true` (this is the default). If the bot was already invited before cc-connect started, re-invite it.

### Q: How to use a self-hosted Matrix server?

Set `homeserver` to your server's URL (e.g. `https://synapse.example.com`). Make sure the URL is reachable from the machine running cc-connect.

### Q: How to use a proxy?

```toml
proxy = "http://proxy-host:8080"
# or SOCKS5:
proxy = "socks5://proxy-host:1080"
```

---

## References

- [Matrix Protocol Specification](https://spec.matrix.org/)
- [Element Web Client](https://app.element.io)
- [Matrix.org](https://matrix.org/)

---

## See Also

- [Telegram Setup](./telegram.md)
- [Discord Setup](./discord.md)
- [Slack Setup](./slack.md)
- [Back to README](../README.md)
