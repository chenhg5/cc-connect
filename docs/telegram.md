# Telegram Setup Guide

This guide walks you through connecting **cc-connect** to Telegram, so you can chat with your local Claude Code via a Telegram bot.

## Prerequisites

- A Telegram account
- A machine that can run cc-connect (no public IP needed)
- Claude Code installed and configured

> 💡 **Advantage**: Uses Long Polling mode — no public IP, no domain, no reverse proxy needed.

---

## Step 1: Create a Telegram Bot

### 1.1 Open BotFather

Search for **@BotFather** in Telegram (the official bot manager) and start a chat.

> ⚠️ Make sure it's the verified official BotFather — don't use third-party imitations.

### 1.2 Create a New Bot

Send the command `/newbot`. BotFather will ask you to provide a name and username.

### 1.3 Set the Bot Name

Enter a **display name** for your bot (e.g. `cc-connect`).

### 1.4 Set the Bot Username

Enter a **username** (must end with `bot`, e.g. `cc_connect_bot`).

> 💡 **Naming rules:**
> - Must end with `bot` (case-insensitive)
> - Only letters, numbers, and underscores
> - Must be globally unique

### 1.5 Get the Bot Token

After creation, BotFather will reply with something like:

```
Done! Congratulations on your new bot...
Use this token to access the HTTP API:
1234567890:ABCdefGHIjklMNOpqrsTUVwxyz-123456

Keep your token secure...
```

> ⚠️ Save this token immediately — it's only shown once! If lost, use `/mybots` → select bot → `API Token` → `Revoke current token` to regenerate.

---

## Step 2: Configure cc-connect

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
type = "telegram"

[projects.platforms.options]
token = "1234567890:ABCdefGHIjklMNOpqrsTUVwxyz-123456"
```

---

## Step 3: Get Chat ID (Optional)

If you want to restrict the bot to specific users/groups, you'll need the Chat ID.

### 3.1 Get Your Personal Chat ID

1. Send any message to your bot
2. Visit the following URL (replace `{{TOKEN}}` with your token):

```
https://api.telegram.org/bot{{TOKEN}}/getUpdates
```

3. Find the `chat.id` field in the returned JSON

### 3.2 Get a Group Chat ID

1. Add the bot to a group
2. Send a message mentioning @your_bot in the group
3. Check the `getUpdates` URL — group Chat IDs are usually negative numbers

> Note: Chat ID whitelisting is planned for a future release.

---

## Step 4: Set Bot Commands (Optional)

### 4.1 Set Command Menu

In BotFather, send:

```
/setcommands
```

Select your bot, then enter the command list:

```
help - Show available commands
new - Start a new session
list - List sessions
```

### 4.2 Set Bot Description

```
/setdescription
```

Enter a description — users will see this when they first open the bot.

---

## Step 5: Start cc-connect

### 5.1 Launch

```bash
cc-connect
# Or specify a config file
cc-connect -config /path/to/config.toml
```

### 5.2 Verify Connection

You should see logs like:

```
level=INFO msg="telegram: connected" bot=cc_connect_bot
level=INFO msg="platform started" project=my-project platform=telegram
level=INFO msg="cc-connect is running" projects=1
```

---

## Step 6: Start Chatting

### 6.1 Direct Message

1. Search for your bot's username in Telegram
2. Click "Start" to begin
3. Send a message

### 6.2 Group Chat

1. Create or open a group
2. Go to group settings → Add members
3. Search and add your bot
4. Send messages in the group

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
│                      Telegram Cloud                          │
│                                                              │
│   User Message ──→ Telegram Bot API ◄── Long Polling         │
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

## Long Polling vs Webhook

| Feature | Long Polling | Webhook |
|---------|-------------|---------|
| Public IP | ❌ Not needed | ✅ Required |
| Domain | ❌ Not needed | ✅ Required |
| HTTPS cert | ❌ Not needed | ✅ Required |
| Complexity | Simple | More complex |
| Latency | Low (long poll) | Low |
| Best for | Local dev, private network | Production |

---

## FAQ

### Q: Bot doesn't respond to messages?

Check the following:
1. Is cc-connect running?
2. Is the bot token correct?
3. Have you sent a message after starting cc-connect? (The bot only receives messages after startup)

### Q: How to regenerate the token?

1. Send `/mybots` to BotFather
2. Select your bot
3. Click `API Token` → `Revoke current token`

### Q: Bot doesn't respond in groups?

Make sure Group Privacy mode is disabled. In BotFather: `/mybots` → select bot → `Bot Settings` → `Group Privacy` → `Turn off`.

---

## References

- [Telegram Bot API Documentation](https://core.telegram.org/bots/api)
- [BotFather Guide](https://core.telegram.org/bots#botfather)
- [Telegram Bot Tutorial](https://core.telegram.org/bots/tutorial)

---

## See Also

- [Feishu Setup](./feishu.md)
- [DingTalk Setup](./dingtalk.md)
- [Slack Setup](./slack.md)
- [Discord Setup](./discord.md)
- [Back to README](../README.md)
