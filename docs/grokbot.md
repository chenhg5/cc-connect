# Grok Bot

[中文](./grokbot.zh-CN.md)

Grok Bot is a first-class cc-connect agent (`type = "grokbot"`). A messaging platform — personal WeChat via the [weixin/ilink](./weixin.md) platform, Feishu/Lark, Telegram, and the others — can chat with a Grok Bot routine, including images in both directions.

This is not the Cursor IDE agent CLI. That adapter remains `type = "cursor"` and speaks `agent --print --output-format stream-json`. Grok Bot is the desktop or cloud assistant that Cursor wakes with an HTTPS automation webhook. The run is asynchronous: HTTP 200 means a run **started**, not that the reply is ready.

## How a turn works

1. Someone sends a message (text, images, files) on the messaging platform.
2. cc-connect saves images and files under `work_dir` with the same helpers the other agents use (`SaveFilesToDisk`, `AppendImageRefs`, `AppendFileRefs`).
3. The adapter POSTs one JSON document to your webhook and keeps the cc-connect session id stable across turns.
4. Grok Bot works, then writes the reply back by either:
   - `POST` to the localhost callback `reply.http`, or
   - writing the same JSON to `reply.file`.
5. cc-connect sends the text to the chat. Images and files on the reply are delivered with the platform's image and file senders (Weixin and Feishu both implement this).

```text
WeChat / Feishu / ...  →  cc-connect  →  POST webhook
                                      ←  POST /outbox/<id>   or   outbox/<id>.json
                       ←  text + images
```

## 1. Create the Grok Bot webhook

In Cursor, create an automation whose trigger is an HTTPS webhook. Copy the URL. It looks like:

```text
https://api2.cursor.sh/automations/webhook/<routine-id>
```

Create an automation API key (`crsr_...`). Put it in the environment of the cc-connect process, not in git and not in `config.toml`:

```bash
export GROK_BOT_WEBHOOK_KEY='crsr_...'
```

systemd and launchd do not inherit your shell. Set the variable in the service environment. `cc-connect doctor` reports whether the variable is set. It never prints the value.

Teach the routine to read the webhook JSON below and to answer on `reply.http` or `reply.file`. A 200 response of `{"success":true,"runUuid":"..."}` only means the run started. The chat stays on "typing" until the reply arrives or `timeout` elapses.

## 2. Configure cc-connect

```toml
[[projects]]
name = "grok-bot"

[projects.agent]
type = "grokbot"

[projects.agent.options]
webhook_url = "https://api2.cursor.sh/automations/webhook/<routine-id>"
work_dir = "/path/to/project"
timeout = "6m"

# Defaults shown for clarity. Omit any line you do not need to change.
# webhook_key_env = "GROK_BOT_WEBHOOK_KEY"
# callback = true
# callback_addr = "127.0.0.1:9876"
# callback_auth = true
# outbox = true
# embed_images = false

[[projects.platforms]]
type = "weixin"

# [projects.platforms.options]
# token is written by `cc-connect weixin setup`. Do not paste it into chat.
```

The same agent block works with Feishu (`type = "feishu"`) or any other platform. Platform setup is unchanged: [weixin.md](./weixin.md), [feishu.md](./feishu.md).

Start cc-connect only after `GROK_BOT_WEBHOOK_KEY` is set. A missing URL or an empty key fails at startup, before any message is sent.

### Options

| Option | Default | Meaning |
| --- | --- | --- |
| `webhook_url` | required | Automation webhook URL. Must be `https` so the API key and reply token are not sent in cleartext. `http` is accepted only for a loopback host (`127.0.0.1`, `localhost`, `::1`), which is the local mock. Anywhere else requires `webhook_insecure_ok = true`, and that sends both secrets in cleartext. |
| `webhook_key_env` | `GROK_BOT_WEBHOOK_KEY` | Environment variable that holds the bearer key. `webhook_key` in the TOML file is rejected. |
| `work_dir` | `.` | Where inbound images and files are saved. `/dir` updates it for the next turn. |
| `timeout` | `6m` | How long to wait for the reply. A Go duration (`6m`, `180s`). `timeout_mins` is accepted instead. |
| `callback` | `true` | Listen for `POST /outbox/<id>`. |
| `callback_addr` | `127.0.0.1:9876` | Bind address. Loopback (`127.0.0.1`, `::1`) is advertised as that same address in `reply.http`. A wildcard (`0.0.0.0`, `::`) is refused unless `callback_advertise_host` names the host or IP the routine can actually dial. cc-connect does not rewrite a wildcard bind to `127.0.0.1`. |
| `callback_auth` | `true` | Require `Authorization: Bearer <reply.token>` (or `X-Grokbot-Token`). The token is random per process and is sent only inside the webhook body. `false` is refused unless `callback_addr` is loopback, or `callback_insecure_ok = true`. |
| `callback_insecure_ok` | `false` | Explicit opt-in for `callback_auth = false` on a non-loopback bind. |
| `callback_advertise_host` | empty | Host or IP put in `reply.http`. Required when `callback_addr` is `0.0.0.0` or `::`. Example: `192.168.1.10`, while the socket still listens on the wildcard. |
| `reply_path_allow` | empty | Extra directories a reply may read, besides `work_dir`, `<work_dir>/.cc-connect/attachments`, and the outbox. |
| `outbox` | `true` | Also accept a JSON file. |
| `outbox_dir` | `<work_dir>/.cc-connect/grokbot/outbox` | Directory for `reply.file`. Created mode `0700`. |
| `embed_images` | `false` | Also include `data_base64` on inbound images. Turn this on when the routine runs in the cloud and cannot read `path`. |
| `max_embed_bytes` | `2097152` | Skip base64 above this size and still send the path. |

At least one of `callback` and `outbox` must stay enabled. Two projects on one machine cannot share `127.0.0.1:9876`; give the second a different `callback_addr`.

`attachment_send = "off"` still allows the text reply and drops reply images and files, same as `cc-connect send --image`.

## 3. Webhook body

```json
{
  "source": "cc-connect",
  "id": "request id",
  "session_id": "stable across turns of this chat",
  "message_id": "platform message id",
  "text": "user text, plus local image/file paths when any were attached",
  "images": [
    {"path": "/abs/in.png", "name": "in.png", "mime": "image/png", "data_base64": "only if embed_images"}
  ],
  "files": [
    {"path": "/abs/notes.txt", "name": "notes.txt", "mime": "text/plain"}
  ],
  "reply": {
    "http": "http://127.0.0.1:9876/outbox/<id>",
    "file": "/abs/outbox/<id>.json",
    "token": "per-process reply token",
    "hint": "how to send the reply"
  }
}
```

Header: `Authorization: Bearer <GROK_BOT_WEBHOOK_KEY>`. If the variable already starts with `Bearer `, it is sent as-is.

Keep `session_id` in the routine's memory if you want a continuous conversation. `/new` in chat starts a new id.

### Reply JSON

POST it to `reply.http` or write it atomically to `reply.file` (`write temp` + `rename`):

```json
{
  "text": "reply text",
  "images": ["/abs/chart.png", {"name": "shot.png", "mime": "image/png", "data_base64": "..."}],
  "files": ["/abs/report.pdf"]
}
```

`content` is accepted as an alias of `text`. `{"error":"model overloaded"}` ends the turn and shows the error in chat. Each image or file is either `data_base64` or a local path. After symlink resolution the path must sit inside `work_dir`, `<work_dir>/.cc-connect/attachments`, the outbox directory, or `reply_path_allow`. Anything else (home directory, SSH keys, config files) fails the turn and is not read into the chat. Remote URLs are not fetched. The per-attachment cap is 50 MiB. A path whose extension is unknown is typed with `http.DetectContentType`, not assumed to be `image/png`.

Write `reply.file` by renaming a finished temp file into place. The file must be a regular file of at most 32 MiB (the same cap as the HTTP callback body). A symlink, device, or FIFO is rejected and is not opened in a way that can block. A file that is still not valid JSON after about a second fails the turn with a clear error instead of waiting out the full timeout. A callback POST that arrives after the turn has finished gets HTTP 409, not "unknown id". An invalid body is HTTP 400 even in that case.

With `callback_auth = true` (the default), send the token back:

```bash
curl -sS -X POST "$REPLY_HTTP" \
  -H "Authorization: Bearer $REPLY_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"text":"pong"}'
```

A file reply does not use the token. Set `callback_auth = false` only for a prototype routine that POSTs to localhost with no header.

HTTP results from the webhook itself:

| Status | Meaning |
| --- | --- |
| 200 and `success` is not `false` | Run started. `runUuid` is logged. cc-connect keeps waiting. |
| 401 | Bad or missing key. The body is not shown, in case it echoes the key. |
| 400 | Rejected. A disabled routine returns 400. The response text is shown with the key redacted. |
| other | Shown with the status code. The turn does not hang. |

## 4. Images

**Inbound (chat → Grok Bot).** The platform hands cc-connect image bytes. They are written under `work_dir/.cc-connect/attachments/<message-id>/` and the absolute path is both appended to `text` and listed in `images`. A desktop routine on the same machine can open `path`. A cloud routine cannot; set `embed_images = true` so the webhook also carries `data_base64` (up to `max_embed_bytes` per image).

**Outbound (Grok Bot → chat).** Put paths or base64 in `images` / `files`. cc-connect reads them and calls the platform sender, so WeChat and Feishu receive real images rather than a path printed in the text. A platform that cannot send images or files still gets the text, and a reply that is only attachments is not dropped: the chat shows which names could not be delivered.

## 5. Try it without Grok Bot

Point `webhook_url` at a local mock that prints the body and writes the outbox file. Nothing here is a real key.

```bash
export GROK_BOT_WEBHOOK_KEY='local-test-key'
python3 - <<'PY'
from http.server import BaseHTTPRequestHandler, HTTPServer
import json
class H(BaseHTTPRequestHandler):
    def do_POST(self):
        n = int(self.headers.get("Content-Length", 0))
        body = json.loads(self.rfile.read(n) or b"{}")
        print(json.dumps({k: body.get(k) for k in ("id", "session_id", "text", "reply")}, ensure_ascii=False, indent=2))
        path = (body.get("reply") or {}).get("file")
        if path:
            with open(path, "w", encoding="utf-8") as f:
                json.dump({"text": "mock reply"}, f)
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.end_headers()
        self.wfile.write(b'{"success":true,"runUuid":"local"}')
    def log_message(self, fmt, *args):
        return
HTTPServer(("127.0.0.1", 8765), H).serve_forever()
PY
```

```toml
webhook_url = "http://127.0.0.1:8765/"
```

Send a message from WeChat or Feishu. The mock prints the turn and the chat receives `mock reply`. To exercise images, put a real PNG path in the JSON the mock writes:

```json
{"text": "see", "images": ["/absolute/path/chart.png"]}
```

`go test ./agent/grokbot/` covers the webhook, both reply channels, inbound and outbound images, 401/400, timeout, and redaction without calling Cursor.

## Errors and limits

- Empty `GROK_BOT_WEBHOOK_KEY`, a missing `webhook_url`, or both reply channels disabled: cc-connect exits at startup with `grokbot: ...`.
- Wrong key: the chat shows HTTP 401 and names the environment variable. The key is not in the message or the log.
- No reply before `timeout` (default 6 minutes): the chat shows a timeout. The session stays open for the next message.
- `/stop` or shutdown cancels the wait.
- Grok Bot does not ask cc-connect for tool permissions. `RespondPermission` returns an error if a permission card is somehow routed here.
- There is no local `grokbot` binary. `cc-connect doctor` skips the PATH check and reports the webhook, the key variable, the callback address, and the outbox directory.
- The callback binds to loopback unless you set another address. A non-loopback bind logs a warning. Do not publish port 9876.
- The reply token and the API key are redacted if they appear in an error string. Do not log the webhook body yourself if you wrap this adapter.

## Build tag

Grok Bot is compiled in by default. To leave it out:

```bash
make build EXCLUDE=grokbot
# or
go build -tags no_grokbot ./cmd/cc-connect
```
