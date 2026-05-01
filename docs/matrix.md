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

### Via curl (Recommended)

Use curl to create a dedicated device with its own device ID. This ensures E2EE (end-to-end encryption) works correctly:

```bash
curl -XPOST "https://matrix.org/_matrix/client/v3/login" \
  -H "Content-Type: application/json" \
  -d '{"type":"m.login.password","user":"your-username","password":"your-password","device_id":"CC-CONNECT"}'
```

The response contains `"access_token": "syt_..."`. Copy it for the config.

> **Recommended**: Set `device_id` to `CC-CONNECT` or another recognizable name. A dedicated device ensures encryption keys are distributed correctly.

### Via Element (Web/Desktop)

1. Log in to **Element** ([app.element.io](https://app.element.io))
2. Open **Settings** (click your avatar → **Settings**)
3. Go to **Help & About** → scroll to **Advanced**
4. Click **Access Token** → copy the token

> **Note**: Tokens from Element reuse Element's device ID, which may cause E2EE issues. Creating a dedicated device via curl is recommended.

> **Warning**: Treat your access token like a password. Anyone with it can send messages as you. If it leaks, you can invalidate it by logging out of all sessions in Element.

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
# auto_verify = true                 # auto-accept SAS key verification (default: true)
# cross_signing_password = ""        # bot account password for cross-signing setup (one-time)
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
level=INFO msg="matrix: E2EE enabled" device_id=CC-CONNECT
level=INFO msg="matrix: connected" user=@bot:matrix.org
level=INFO msg="platform started" project=my-project platform=matrix
level=INFO msg="cc-connect is running" projects=1
```

If you see `E2EE not available`, encryption initialization failed. Encrypted rooms won't work. See the FAQ below.

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
| `auto_verify` | No | `true` | Auto-accept SAS key verification requests |
| `cross_signing_password` | No | `""` | Bot account password for cross-signing key setup (one-time, needed on first run or when keys are reset) |
| `share_session_in_channel` | No | `false` | Share a single agent session among all users in a room |
| `group_reply_all` | No | `false` | Respond to all messages in group rooms (not just mentions) |
| `proxy` | No | `""` | HTTP or SOCKS5 proxy URL |

---

## FAQ

### Q: Bot doesn't respond to messages?

1. Is cc-connect running and showing `matrix: connected` in logs?
2. Is the access token valid? Try regenerating it.
3. In group rooms, is the bot mentioned or is `group_reply_all = true` set?
4. If logs show `E2EE not available` or `decrypt failed`, see E2EE questions below.

### Q: How to restrict who can use the bot?

Set `allow_from` to a comma-separated list of Matrix user IDs:

```toml
allow_from = "@alice:matrix.org,@bob:matrix.org"
```

### Q: Bot doesn't join rooms?

Make sure `auto_join = true` (this is the default). If the bot was already invited before cc-connect started, re-invite it.

### Q: E2EE (End-to-End Encryption)

cc-connect automatically supports encrypted rooms (E2EE) with no extra configuration. If you see `matrix: E2EE enabled` at startup, encryption is working.

#### Logs show "E2EE not available"?

Possible causes and fixes:

1. **`device ID not available from whoami`** — The server didn't return a device ID. Create a dedicated device via curl with `device_id`.
2. **`not marked as shared, but there are keys on the server`** — Old crypto data conflicts with the current device. cc-connect tries to auto-recover. If it persists, delete old crypto databases: `rm ~/.cc-connect/matrix-crypto-*.db*`
3. **`mismatching device ID in client and crypto store`** — The token's device ID doesn't match the crypto database. Delete the database: `rm ~/.cc-connect/matrix-crypto-*.db*`

#### Logs show "decrypt failed: no session found"?

The sender's client didn't send the encryption key to the bot's device. This usually happens when:

1. **Reusing Element's access token** — Element's device ID conflicts with the bot's encryption keys. Create a dedicated device via curl (see Step 2).
2. **Just changed the access token** — The sender's client may not have discovered the bot's new device yet. Wait 1-2 minutes and send a new message.
3. **Corrupted crypto database** — Delete and restart: `rm ~/.cc-connect/matrix-crypto-*.db*`

#### How to get a dedicated access token (recommended)?

Use the Matrix API to create a dedicated device, avoiding conflicts with Element or other apps:

```bash
# Replace homeserver URL, username, and password
curl -XPOST "https://your-homeserver.com/_matrix/client/v3/login" \
  -H "Content-Type: application/json" \
  -d '{
    "type": "m.login.password",
    "user": "your-bot-username",
    "password": "your-password",
    "device_id": "CC-CONNECT"
  }'
```

The `access_token` in the response can be used in config. The `device_id` will be `CC-CONNECT`, easy to identify and manage.

#### Red exclamation mark on bot messages ("encrypted by a device not verified by its owner")?

This means the bot's device hasn't been cross-signed. cc-connect automatically sets up cross-signing on first run, but some Matrix servers require password authentication (UIA) to publish the cross-signing keys.

If logs show `no supported UIA flow for cross-signing`, add the bot account's password to config:

```toml
cross_signing_password = "your-bot-password"
```

This is a one-time operation — once cross-signing keys are published and saved, the password is no longer needed.

#### How to verify the bot's device?

cc-connect auto-accepts SAS key verification requests (when `auto_verify = true`, which is the default). To verify from Element:

1. Open a DM with the bot
2. Click the bot's avatar → **Verify** (or go to **Settings** → **Security** → find the bot's device)
3. The bot will automatically accept and confirm the verification
4. Element will show the device as verified

After verification, encrypted messages from the bot will no longer show warnings.

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
