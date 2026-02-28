# Discord Setup Guide

This guide walks you through connecting **cc-connect** to Discord, so you can chat with your local Claude Code via a Discord bot.

## Prerequisites

- A Discord account
- A machine that can run cc-connect (no public IP needed)
- Claude Code installed and configured

> 💡 **Advantage**: Uses Gateway (WebSocket) — no public IP, no domain, no reverse proxy needed.

---

## Step 1: Create a Discord Application

### 1.1 Open the Developer Portal

Go to [Discord Developer Portal](https://discord.com/developers/applications) and sign in.

### 1.2 Create a New Application

1. Click "New Application" in the top right
2. Enter an application name (e.g. `cc-connect`)
3. Agree to the Terms of Service
4. Click "Create"

---

## Step 2: Create a Bot User

### 2.1 Go to Bot Settings

In the left sidebar, click "Bot".

### 2.2 Add a Bot

1. Click "Add Bot"
2. Confirm the action

### 2.3 Configure Bot Info

| Field | Suggested Value |
|-------|----------------|
| Username | `cc-connect` |
| Avatar | Upload an icon you like |

---

## Step 3: Get the Bot Token

### 3.1 Generate Token

On the Bot page:

1. Click "Reset Token"
2. You may need to enter a 2FA code
3. Click "Copy" to copy the token

> ⚠️ The token is only shown once — save it immediately! Format: `MTk4NjIyNDgzNDcOTY3NDUxMg.G8vKqh.xxx...`

### 3.2 Lost Your Token?

Click "Reset Token" at any time to regenerate. The old token will be invalidated immediately.

---

## Step 4: Configure Privileged Intents (Important!)

### 4.1 What Are Intents?

Intents control which events your bot can receive from Discord's Gateway.

### 4.2 Enable Required Intents

On the Bot page, under "Privileged Gateway Intents", enable:

| Intent | Purpose | Required? |
|--------|---------|-----------|
| **Message Content Intent** | Read message content | ✅ **Required** |
| Presence Intent | Read user status | Optional |
| Server Members Intent | Read server members | Optional |

> ⚠️ **You must enable Message Content Intent**, or the bot won't be able to read messages!

### 4.3 Save Changes

Click "Save Changes".

---

## Step 5: Configure cc-connect

Add the token to your `config.toml`:

```toml
[[projects]]
name = "my-project"

[projects.agent]
type = "claudecode"

[projects.agent.options]
work_dir = "/path/to/your/project"
mode = "default"

[[projects.platforms]]
type = "discord"

[projects.platforms.options]
token = "MTk4NjIyNDgzNDcOTY3NDUxMg.G8vKqh.xxx..."
```

> cc-connect automatically configures the required Intents (MESSAGE_CONTENT, GUILD_MESSAGES, DIRECT_MESSAGES).

---

## Step 6: Generate an Invite Link

### 6.1 Go to OAuth2 Settings

In the left sidebar, click "OAuth2" → "URL Generator".

### 6.2 Select Scopes

Under "Scopes", check:
- ✅ `bot`

### 6.3 Select Permissions

Under "Bot Permissions", check:

| Permission | Purpose |
|------------|---------|
| Read Messages/View Channels | Read messages |
| Send Messages | Send messages |
| Send Messages in Threads | Send messages in threads |
| Read Message History | Read message history |

### 6.4 Copy the Link

1. The invite link will be generated at the bottom of the page
2. Click "Copy"

---

## Step 7: Invite the Bot to Your Server

### 7.1 Open the Invite Link

Open the copied URL in your browser and sign in to Discord.

### 7.2 Select a Server

Choose the server you want to add the bot to from the dropdown.

### 7.3 Authorize

Review the permissions and click "Authorize". Complete the CAPTCHA if prompted.

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
level=INFO msg="discord: connected" bot=cc-connect#0000
level=INFO msg="platform started" project=my-project platform=discord
level=INFO msg="cc-connect is running" projects=1
```

---

## Step 9: Start Chatting

### 9.1 Channel Usage

Send a message in any channel where the bot has permissions.

### 9.2 Direct Message

1. Click the bot's avatar
2. Send a DM

---

## Usage Example

```
User: Help me analyze the current project structure

cc-connect: 🤔 Thinking...
cc-connect: 🔧 Tool: Bash(ls -la)
cc-connect: Here's the project structure...
```

---

## Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                      Discord Cloud                           │
│                                                              │
│   User Message ──→ Discord Gateway ◄── WebSocket             │
│                         │                                    │
└─────────────────────────┼────────────────────────────────────┘
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

## Discord Gateway Features

| Feature | Details |
|---------|---------|
| **Connection** | WebSocket |
| **Public IP** | ❌ Not needed |
| **Heartbeat** | Automatic keepalive |
| **Reconnection** | Automatic on disconnect |
| **Intents** | Must declare required event types |
| **Message limit** | 2000 characters per message (auto-split by cc-connect) |
| **Markdown** | Full native support |

---

## FAQ

### Q: Bot can't read message content?

**Most common issue**: Message Content Intent is not enabled!

Fix:
1. Go to Discord Developer Portal
2. Select your app → Bot
3. Enable "Message Content Intent"
4. Save changes
5. Restart cc-connect

### Q: Bot connects then immediately disconnects?

Check:
1. Is the bot token correct?
2. Are intents configured properly?
3. Are you hitting Discord rate limits? (from frequent reconnects)

### Q: Bot doesn't appear in the server?

1. Make sure you used the invite link to add the bot
2. Check if the bot was kicked from the server

### Q: How to regenerate the token?

1. Go to Discord Developer Portal
2. Select your app → Bot
3. Click "Reset Token"
4. Update your config.toml

### Q: Bot has insufficient permissions?

1. Generate a new invite link with the correct permissions
2. Re-invite the bot to the server

---

## References

- [Discord Developer Portal](https://discord.com/developers/applications)
- [Discord API Documentation](https://discord.com/developers/docs/intro)
- [Bot Getting Started Guide](https://discord.com/developers/docs/getting-started)
- [Gateway Intents](https://discord.com/developers/docs/topics/gateway#privileged-intents)
- [OAuth2 Scopes](https://discord.com/developers/docs/topics/oauth2#shared-resources-oauth2-scopes)

---

## See Also

- [Feishu Setup](./feishu.md)
- [DingTalk Setup](./dingtalk.md)
- [Telegram Setup](./telegram.md)
- [Slack Setup](./slack.md)
- [Back to README](../README.md)
