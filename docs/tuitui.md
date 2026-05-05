# TuiTui Platform Setup Guide / 推推平台接入指南

This guide connects cc-connect to TuiTui through the TuiTui robot WebSocket callback API.

## Prerequisites / 前置条件

- A TuiTui robot app with `app_id` and `app_secret`
- A local cc-connect project configured with an agent

TuiTui uses a WebSocket callback connection, so cc-connect does not need a public inbound HTTP endpoint.

## Configuration / 配置

Add a TuiTui platform block to your project:

```toml
[[projects.platforms]]
type = "tuitui"

[projects.platforms.options]
app_id = "${TUITUI_APP_ID}"
app_secret = "${TUITUI_APP_SECRET}"

# Direct-message users. Empty or "*" allows all direct-message users.
# Explicit users here may also @ the bot from any group/channel.
# "*" does not open all groups; use group_policy/group_allow_from for that.
allow_from = "*"

# Group IDs, team IDs, or channel IDs allowed for group/channel messages.
# Keep this explicit for group/channel use.
group_allow_from = "7652669648832580"

# Group chats require @bot by default. Channel posts are gated by allowlists.
require_mention = true

# "allowlist" (default), "open", or "disabled"
group_policy = "allowlist"

# Optional endpoint overrides.
api_base = "https://im.live.360.cn:8282"
ws_base = "wss://im.live.360.cn:8282"
```

## Supported Features / 支持能力

- Direct messages, group chats, and teams/channel posts
- Text replies and slash commands
- Image/file send-back through `cc-connect send --image` and `cc-connect send --file`
- Inbound image/file/voice download for agent processing
- Chat history reads and history attachment downloads through `cc-connect tuitui`
- Cron/proactive sends through session-key reply context reconstruction

## Access Policy / 权限策略

- Direct messages use `allow_from`; empty or `*` allows all direct-message users.
- Group chats require `@bot` by default when `require_mention = true`.
- A concrete user listed in `allow_from` can mention the bot from any group/channel.
- `allow_from = "*"` does not bypass group/channel allowlists.
- `group_allow_from` allows whole groups, teams, or channels.
- `group_policy = "disabled"` disables group/channel handling; `group_policy = "open"` allows mentioned group messages without a group allowlist.
- Channel posts remain gated by explicit `allow_from` users or `group_allow_from` even when `group_policy = "open"`.

## Session Keys / 会话键

TuiTui session keys use these formats:

```text
tuitui:<user>
tuitui:<group_id>:<user>
tuitui:teams_<team_id>_<channel_id>_<thread_id>
```

When `share_session_in_channel = true`, group chats use a shared session key:

```text
tuitui:<group_id>
```

## Reading History / 读取历史

`cc-connect` includes TuiTui history helpers for agents and operators. Credentials are loaded from the configured TuiTui platform by default, or from `TUITUI_APP_ID` / `TUITUI_APP_SECRET`.

Read recent messages:

```bash
cc-connect tuitui messages \
  --project my-project \
  --chat 7652669648832580 \
  --chat-type group \
  --relative-time last_7_days \
  --limit 100
```

Search recent history:

```bash
cc-connect tuitui search \
  --project my-project \
  --chat 7652669648832580 \
  --chat-type group \
  --relative-time last_7_days \
  --q 周报
```

Download a file or image URL found in history:

```bash
cc-connect tuitui download \
  --url "https://example.com/report.xlsx" \
  --out ./tmp/tuitui
```

Supported chat types:

- `direct`: `--chat <user_account>` uses `/robot/message/single/sync`
- `group`: `--chat <group_id>` uses `/robot/message/group/sync`
- `channel`: `--chat teams_<team_id>_<channel_id>_<thread_id>` or `--chat <channel_id>` uses `/robot/teams/post/topic/list`
