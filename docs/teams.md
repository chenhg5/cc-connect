# Microsoft Teams

cc-connect connects to Microsoft Teams as a Bot Framework bot. Unlike the
outbound-connection platforms (Slack socket mode, Feishu websocket), Teams
delivers messages by **POSTing to a public HTTPS webhook**, so this platform
requires a publicly reachable URL.

## How it works

- Azure Bot Service relays Teams messages to the connector's webhook
  (`/api/messages` by default) as Bot Framework activities.
- The connector validates each request's JWT, then forwards the message to the
  cc-connect engine.
- Replies are sent back through the Bot Connector REST API and stream as an
  **Adaptive Card**: a "working" card is posted immediately, then edited in place
  as the answer grows. Every card — the working card included — carries the native
  "AI generated" label, so it is present from the first render. The card
  renders uniformly in channels, group chats, and 1:1 (unlike the native
  `streamType` protocol, which is one-on-one only).

Engagement model: in a **channel or group chat**, an **@mention** of the bot
engages that reply thread; subsequent messages **in that thread** are then
followed without re-mentioning. A message that @mentions other people but not the
bot is treated as human-to-human side chatter and ignored. Engagement is
persisted under the cc-connect data dir (`<data_dir>/teams/<project>-engaged.json`),
so it survives a restart. In a **1:1 (personal) chat** the bot responds to every
message — Teams does not allow @mentioning a bot there.

## Prerequisites

- An **Azure Bot resource** (Azure Portal → "Azure Bot"), created as a
  **single-tenant** app. Note its **Microsoft App ID**, its **tenant (directory)
  ID**, and create a **client secret**. (Multi-tenant bots are not supported —
  Azure deprecated their creation after 2025-07-31.)
- The bot's **Microsoft Teams** channel enabled.
- A public HTTPS endpoint that routes to the connector's webhook port.

## Setup

1. **Create the Azure Bot** (single-tenant) and record the App ID, tenant ID, and client secret.
2. **Set the messaging endpoint** (Azure Bot → Configuration →
   "Messaging endpoint") to:

   ```
   https://<your-host><webhook_path>
   ```

   where `<webhook_path>` matches your config (default `/api/messages`).
3. **Enable the Teams channel** (Azure Bot → Channels → Microsoft Teams).
4. **Configure cc-connect** — add a Teams platform to your project:

   ```toml
   [[projects.platforms]]
   type = "teams"

   [projects.platforms.options]
   app_id = "00000000-0000-0000-0000-000000000000"  # Azure Bot App (client) ID
   app_password = "YOUR_CLIENT_SECRET"              # Azure Bot client secret
   tenant_id = "<your-tenant-id>"                   # required — AAD tenant that owns the bot (single-tenant)
   webhook_port = "3978"                            # local bind port
   webhook_path = "/api/messages"                   # must match the messaging endpoint path
   allow_from = "*"                                 # "*"/empty = all users in the tenant, or AAD object IDs
   session_scope = "thread"                         # "thread" (default) | "channel" | "user"
   card_update_interval_ms = 1500                   # streaming card edit throttle (ms)
   ```

   > **Access control:** the connector is **single-tenant** (`tenant_id` required), so only
   > users in that one organization can reach the bot. `allow_from = "*"` (the default) then
   > permits everyone in the tenant; set an AAD-object-ID allowlist to restrict to specific
   > people when you want a tighter boundary.

5. **Expose the webhook.** Put a reverse proxy / ingress with TLS in front of
   `webhook_port`, or use a tunnel (e.g. for local testing) so Azure Bot Service
   can reach `https://<your-host><webhook_path>`.
6. **Install the bot in Teams** (sideload an app manifest pointing at your bot,
   or add it from your org's catalog) and @mention it to start.

## Options

| Option | Required | Default | Description |
|--------|----------|---------|-------------|
| `app_id` | yes | — | Azure Bot App (client) ID; the inbound JWT audience |
| `app_password` | yes | — | Azure Bot client secret (outbound token) |
| `tenant_id` | **yes** | — | AAD tenant that owns the bot. Single-tenant only — Azure deprecated multi-tenant bot creation after 2025-07-31 |
| `webhook_port` | no | `3978` | Local port the webhook binds to |
| `webhook_path` | no | `/api/messages` | Path of the messaging endpoint |
| `allow_from` | no | `""` | Comma-separated AAD object IDs allowed to use the bot; `*` or empty = all users in the tenant |
| `session_scope` | no | `thread` | `thread` (one session per reply thread), `channel` (one shared session across the channel), `user` (one session per user within a thread) |
| `card_update_interval_ms` | no | `1500` | Streaming-card edit throttle in ms; Teams rate-limits edits to ~1/s |
| `card_loading_text` | no | `""` | Label on the placeholder card shown while the agent works (e.g. `💭 Thinking…`); empty renders a label-less card |
| `service_url_allowlist` | no | `""` | Comma-separated hosts the bot may send replies to. Empty = any JWT-validated host (default). Set it to pin the bot to your cloud's Bot Connector host(s) as defense-in-depth. See "serviceURL allowlist" below |
| `max_attachment_bytes` | no | `20971520` (20 MiB) | Cap per inbound 1:1 file/image download. A larger attachment is skipped with a notice rather than buffered. See "Receiving files and images" below |

## serviceURL allowlist

The bot sends its replies (carrying its Bot Connector bearer token) to the
`serviceUrl` from each inbound activity. That URL is already bound to a
JWT-validated request, so by default any authenticated `serviceUrl` is trusted —
the same model as the Bot Framework / M365 Agents SDK.

For defense-in-depth (or a compliance lockdown), set `service_url_allowlist` to
the Bot Connector host(s) your tenant's cloud uses; the bot then drops any
activity whose `serviceUrl` host is not listed. Matching is on **host** (so
regional paths like `/amer/`, `/emea/` are fine). Current Microsoft hosts for
reference (verify against Azure docs — this list can change):

| Cloud | Host |
|-------|------|
| Public | `smba.trafficmanager.net` |
| GCC | `smba.infra.gcc.teams.microsoft.com` |
| GCC High | `smba.infra.gov.teams.microsoft.us` |
| DoD | `smba.infra.dod.teams.microsoft.us` |

Leave it empty unless you have a reason to pin — a too-narrow list silently drops
legitimate traffic.

## Receiving files and images

In a **1:1 (personal) chat**, a file or image the user attaches to the bot is
downloaded by the connector and handed to the agent (a file becomes a saved file
the agent can read; an image is passed as an image attachment). Text and an
attachment sent together arrive on the same turn.

**Manifest prerequisite:** for the bot to receive files in 1:1, the Teams app
manifest must declare:

```json
"bots": [
  { "botId": "<your-app-id>", "supportsFiles": true, "scopes": ["personal"] }
]
```

Without `supportsFiles: true`, Teams does not deliver file attachments to the bot
— no connector setting substitutes for it.

**Size limit:** each download is capped by `max_attachment_bytes` (default
20 MiB). An oversized attachment, or one whose download fails, is skipped and the
user gets a brief notice; the turn still proceeds with any text and other
attachments.

## Sending images

When the agent produces an image (or a user runs `cc-connect send --image`), the
bot sends it back **inline** as a base64 data-URI attachment, threaded to the
originating message. It renders in 1:1 chats, group chats, and channels. If the
Bot Connector rejects the image as too large (HTTP 413), the bot degrades to a
brief text notice rather than failing the turn — so Teams' own size limit
governs, not a fixed cap (a generous safety guard only rejects pathologically
large images up front). Sending **files** (non-image) is not supported — Teams
requires a separate file consent / SharePoint flow.

**Not supported:**
- **Channel / group attachments.** Files posted in a channel or group chat live
  in SharePoint and require Microsoft Graph + tenant admin consent; attachments
  outside a 1:1 chat are ignored, not partially handled.
- **Outbound files** (the bot *sending* non-image files) are not implemented.
  Outbound **images** are supported (see "Sending images").

## Connection type

Webhook (Bot Framework) — **a public HTTPS URL is required**. This is inherent
to the Bot Framework: Teams messages always route through Azure Bot Service,
which POSTs to your endpoint.

## Limitations (MVP)

- Replies stream as an Adaptive Card only. Plain text streaming, a `reply_format`
  toggle, and the native `streamType` 1:1 animation are deferred to follow-ups.
- Permission prompts and AskUserQuestion prompts render as their **own
  interactive Adaptive Card** (Allow / Deny / Allow-all; one button per option) —
  a distinct message, not folded into the streaming answer card, so a prompt is
  never missed. Replying with text (`allow`, `deny`, a number) still works.
  multiSelect questions also render as buttons and resolve on the **first tap**
  (no multi-pick) — reply with comma-separated numbers as text to pick several.
- Inbound files and images are supported in **1:1 chats only** (see "Receiving
  files and images"); channel/group attachments and inbound audio are not.
- Proactive messages (cron/timer/heartbeat → Teams) require the bot to have seen
  the conversation before: the per-conversation `serviceURL` is captured from
  inbound activities and persisted, then reconstructed at send time. A
  conversation the bot has never received a message in cannot be addressed yet.
  Under `session_scope = channel`, a proactive send targets the most recently
  active thread in the channel.
