# Slack Setup Guide

This guide walks you through connecting **cc-connect** to Slack, so you can chat with your local Claude Code via a Slack bot.

## Prerequisites

- A Slack workspace account (with permission to create apps)
- A machine that can run cc-connect (no public IP needed)
- Claude Code installed and configured

> 💡 **Advantage**: Uses Socket Mode (WebSocket) — no public IP, no domain, no reverse proxy needed.

---

## Step 1: Create a Slack App

### 1.1 Open the Slack API Console

Go to [Slack API](https://api.slack.com/apps) and sign in with your Slack account.

### 1.2 Create a New App

1. Click "Create New App"
2. Select "From scratch"
3. Fill in the app details:

| Field | Suggested Value |
|-------|----------------|
| App Name | `cc-connect` |
| Development Slack Workspace | Select your workspace |

4. Click "Create App"

---

## Step 2: Configure Bot User

### 2.1 Go to App Home

In the left sidebar, click "App Home".

### 2.2 Set Bot Info

1. Click "Edit" to configure the bot display name
2. Fill in:

| Field | Suggested Value |
|-------|----------------|
| Display Name (Bot Name) | `cc-connect` |
| Default Username | `cc_connect` |

### 2.3 Always Show Bot Online

Toggle on "Always Show My Bot as Online".

---

## Step 3: Configure Permissions (OAuth Scopes)

### 3.1 Go to OAuth & Permissions

In the left sidebar, click "OAuth & Permissions".

### 3.2 Add Bot Token Scopes

Under "Scopes" → "Bot Token Scopes", add:

| Scope | Purpose |
|-------|---------|
| `app_mentions:read` | Read @mention messages |
| `chat:write` | Send messages |
| `im:history` | Read DM history |
| `im:read` | Read DM list |
| `im:write` | Send DMs |
| `channels:history` | Read channel messages (optional) |
| `groups:history` | Read private channel messages (optional) |
| `users:read` | Get user info |

---

## Step 4: Enable Socket Mode

### 4.1 Go to Socket Mode Settings

In the left sidebar, click "Socket Mode".

### 4.2 Enable Socket Mode

1. Toggle on "Enable Socket Mode"
2. Click "Generate Token and Enter Socket Mode"

### 4.3 Generate App-Level Token

1. Enter a token name (e.g. `cc-connect-socket-token`)
2. Add the following scope:
   - `connections:write` — establish WebSocket connections
3. Click "Generate"

### 4.4 Save the Token

The system will generate an App-Level Token (format: `xapp-xxxxxxx...`). Save it immediately.

> ⚠️ The token is only shown once — copy it now!

---

## Step 5: Configure Event Subscriptions

### 5.1 Go to Event Subscriptions

In the left sidebar, click "Event Subscriptions".

### 5.2 Enable Events

1. Toggle on "Enable Events"
2. Since we're using Socket Mode, no Request URL is needed

### 5.3 Subscribe to Bot Events

Under "Subscribe to bot events", add:

| Event | Purpose |
|-------|---------|
| `app_mention` | Triggered when the bot is @mentioned |
| `message.im` | Triggered when a DM is received |

### 5.4 Save Changes

Click "Save Changes".

---

## Step 6: Install App to Workspace

### 6.1 Install the App

In the left sidebar, click "Install App" → "Install to Workspace".

### 6.2 Authorize

Review the permissions and click "Allow".

### 6.3 Get the Bot Token

After installation, you'll see:

```
Bot User OAuth Token: xoxb-xxxxxxx...
```

> ⚠️ Save this token — you'll need it for configuration.

---

## Step 7: Configure cc-connect

Add both tokens to your `config.toml`:

```toml
[[projects]]
name = "my-project"

[projects.agent]
type = "claudecode"

[projects.agent.options]
work_dir = "/path/to/your/project"
mode = "default"

[[projects.platforms]]
type = "slack"

[projects.platforms.options]
bot_token = "xoxb-xxxxxxx..."
app_token = "xapp-xxxxxxx..."
```

### Token Reference

| Token | Prefix | Purpose |
|-------|--------|---------|
| Bot Token | `xoxb-` | Bot API authentication |
| App Token | `xapp-` | Socket Mode connection |

---

## Step 8: Start cc-connect

### 8.1 Launch

```bash
cc-connect
# Or specify a config file
cc-connect -config /path/to/config.toml
```

### 8.2 Verify Connection

You should see logs like:

```
level=INFO msg="slack: connected"
level=INFO msg="platform started" project=my-project platform=slack
level=INFO msg="cc-connect is running" projects=1
```

---

## Step 9: Start Chatting

### 9.1 Direct Message

1. Search for your bot name in Slack
2. Open a DM conversation
3. Send a message

### 9.2 Channel Usage

1. Add the bot to a channel (`/invite @cc_connect`)
2. @mention the bot: `@cc_connect help me analyze the code`
3. The bot will respond

---

## Usage Example

```
User: @cc_connect Help me analyze the current project structure

cc-connect: 🤔 Thinking...
cc-connect: 🔧 Tool: Bash(ls -la)
cc-connect: Here's the project structure...
```

---

## Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                       Slack Cloud                            │
│                                                              │
│   User Message ──→ Slack API ──→ Socket Mode Gateway         │
│                                       │                      │
└───────────────────────────────────────┼──────────────────────┘
                                        │
                                        │ WebSocket (no public IP needed)
                                        ▼
┌─────────────────────────────────────────────────────────────┐
│                    Your Local Machine                         │
│                                                              │
│   cc-connect ◄──► Claude Code CLI ◄──► Your Project Code    │
│                                                              │
└─────────────────────────────────────────────────────────────┘
```

---

## Socket Mode vs Webhook

| Feature | Socket Mode | Webhook |
|---------|-------------|---------|
| Public IP | ❌ Not needed | ✅ Required |
| Domain | ❌ Not needed | ✅ Required |
| HTTPS cert | ❌ Not needed | ✅ Required |
| Reverse proxy | ❌ Not needed | ✅ Required |
| Connection | WebSocket | HTTP callback |
| Complexity | Simple | More complex |
| Best for | Local dev, private network | Production |

---

## FAQ

### Q: Socket Mode connection fails?

Check the following:
1. Is the App Token correct? (starts with `xapp-`)
2. Does the App Token have `connections:write` scope?
3. Is Socket Mode enabled in the app settings?

### Q: Bot doesn't respond to messages?

Check the following:
1. Is the Bot Token correct? (starts with `xoxb-`)
2. Are event subscriptions configured correctly?
3. Are the required scopes added?

### Q: Changes to permissions don't take effect?

**⚠️ Important**: After modifying scopes or events, you must reinstall the app!

1. Go to "Install App"
2. Click "Reinstall to Workspace"

### Q: Bot doesn't respond in DMs?

Make sure you've subscribed to the `message.im` event.

### Q: Bot doesn't respond in channels?

Make sure:
1. You've subscribed to the `app_mention` event
2. The bot has been added to the channel
3. You @mentioned the bot in your message

---

## References

- [Slack API Documentation](https://api.slack.com/)
- [Slack App Building Guide](https://api.slack.com/start/building)
- [Socket Mode Documentation](https://api.slack.com/apis/connections/socket)
- [Bot Token Scopes](https://api.slack.com/scopes)
- [Event Types](https://api.slack.com/events)

---

## See Also

- [Feishu Setup](./feishu.md)
- [DingTalk Setup](./dingtalk.md)
- [Telegram Setup](./telegram.md)
- [Discord Setup](./discord.md)
- [Back to README](../README.md)
