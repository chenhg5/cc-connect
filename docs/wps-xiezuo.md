# WPS Xiezuo Platform Setup Guide

This guide explains how to connect **cc-connect** to WPS Xiezuo (WPS 365 collaboration) so users can talk to an AI coding agent from WPS chats.

## Prerequisites

- A WPS Open Platform application with app chat events enabled
- `app_id` and `app_secret` for the application
- A machine running cc-connect; no public IP is required
- An agent such as Claude Code, Codex, or Gemini CLI configured in cc-connect

## Connection Model

The platform uses WPS event WebSocket delivery and WPS REST APIs:

- Incoming events: WebSocket at `wss://openapi.wps.cn/v7/event/ws`
- Authentication: KSO-1 HMAC-SHA256 headers
- Event payloads: encrypted with AES-256-CBC and verified with HMAC-SHA256 signatures
- Outgoing replies: REST API with a cached `client_credentials` access token

cc-connect sends ACK frames on the WebSocket writer loop, so no public callback URL is needed.

## Configure WPS

In the WPS Open Platform console:

1. Create or select an application.
2. Enable app chat/message capabilities for the application.
3. Enable event WebSocket delivery.
4. Subscribe to message events:
   - `kso.app_chat.message`
   - `kso.app_chat.message.recall` if recall notifications are needed
5. Grant the permissions needed to send app chat messages and reactions.
6. Grant `kso.chat_message.readwrite` so cc-connect can download images and local files attached to chat messages.
7. Grant `kso.file.read` (or `kso.file.readwrite`) so cc-connect can resolve shared cloud-document links and extract their content.
8. Copy the application `app_id` and `app_secret`.

The exact console names may vary by WPS tenant and app type. If the connection fails with authorization errors, verify that the app is published/enabled for the target organization and has the required app chat permissions.

## Configure cc-connect

Add `wps-xiezuo` to a project in `config.toml`:

```toml
[[projects]]
name = "my-project"

[projects.agent]
type = "claudecode"

[projects.agent.options]
work_dir = "/path/to/your/project"

[[projects.platforms]]
type = "wps-xiezuo"

[projects.platforms.options]
app_id = "your-wps-xiezuo-app-id"
app_secret = "your-wps-xiezuo-app-secret"
allow_from = "*"        # optional; set to WPS user IDs in production
clean_reply = false     # optional; strip thinking/tool progress lines
```

### Options

| Option | Required | Default | Description |
|--------|----------|---------|-------------|
| `app_id` | yes | - | WPS Open Platform application ID |
| `app_secret` | yes | - | WPS Open Platform application secret |
| `allow_from` | no | all users | Comma-separated WPS user IDs allowed to use the bot; set this in production |
| `clean_reply` | no | `false` | Removes common thinking/tool progress lines from replies before sending |
| `base_url` | no | `https://openapi.wps.cn` | Override WPS REST API base URL for private or test environments |

## Start and Verify

Start cc-connect:

```bash
cc-connect -config /path/to/config.toml
```

Expected logs include:

```text
level=INFO msg="wps-xiezuo: connecting" endpoint=wss://openapi.wps.cn/v7/event/ws
level=INFO msg="wps-xiezuo: connected"
level=INFO msg="platform started" project=my-project platform=wps-xiezuo
```

Send a message to the WPS app chat. cc-connect should receive the encrypted event, ACK it, forward the text to the configured agent, and send the reply back through the WPS message API.

### Incoming files and images

Images and local files sent in WPS chats are downloaded through the WPS message-resource API and forwarded to the configured agent as attachments. Images embedded in rich-text messages are handled the same way. A single attachment is limited to 50 MiB to avoid unbounded memory use.

WPS cloud-document messages are different from local attachments. With `kso.file.read` (or `kso.file.readwrite`) application permission, cc-connect resolves the `link_id` and attempts to extract Markdown正文 using the Drive content API. The document title, link, and extracted content are forwarded to the agent under an explicit marker stating that the content was read with application authorization; the agent should use that body directly instead of trying to open the login-protected web link. If the application cannot access a document, cc-connect falls back to forwarding only the title and `link_url`.

## Security Notes

- Always set `allow_from` for production deployments.
- Keep `app_secret` out of source control. Environment variable substitution is supported in `config.toml`, for example `app_secret = "${WPS_XIEZUO_APP_SECRET}"`.
- Debug logs avoid printing decrypted WPS message payloads by default.

## Troubleshooting

**Connection fails immediately**

- Confirm `app_id` and `app_secret` are correct.
- Confirm the app has WebSocket event delivery enabled.
- Confirm the app is available to the organization where you are testing.

**Messages arrive but replies fail**

- Confirm the app has message send permissions.
- Check whether the tenant requires the `/oauth2/token` or `/openapi/oauth2/token` token endpoint; cc-connect tries both.
- Verify the target chat allows app messages.

**Images or local files are ignored**

- Confirm the app has `kso.chat_message.readwrite` permission.
- Confirm the permission is enabled for the organization where the file was sent.
- Check for `wps-xiezuo: image download failed` or `wps-xiezuo: file download failed` in the logs.

**Bot responds to unexpected users**

- Set `allow_from` to a comma-separated list of WPS user IDs.
- Users can send `/whoami` to discover the ID used by cc-connect.
