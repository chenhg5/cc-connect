# Grok Bot

[English](./grokbot.md)

Grok Bot 是 cc-connect 的一级 agent（`type = "grokbot"`）。即时通讯平台可以把对话交给一个 Grok Bot routine，包括双向图片。个人微信走 [weixin/ilink](./weixin.md)，飞书及其他平台同样适用。

它不是 Cursor IDE 的 Agent CLI。那个适配器仍然是 `type = "cursor"`，走 `agent --print --output-format stream-json`。Grok Bot 是由 Cursor HTTPS 自动化 webhook 唤醒的桌面或云端助手。HTTP 200 只表示 run **已经开始**，不表示回复已经写完。

## 一轮对话

1. 用户在微信、飞书等平台发出文字、图片或文件。
2. cc-connect 用和其他 agent 相同的方法把附件写到 `work_dir`（`SaveFilesToDisk`、`AppendImageRefs`、`AppendFileRefs`）。
3. 适配器把一份 JSON POST 到你的 webhook。同一聊天的 `session_id` 在多轮之间保持不变。
4. Grok Bot 处理完后，用下面任一方式交回回复：
   - 向本机 `reply.http` 发 `POST`，或
   - 把同一份 JSON 写到 `reply.file`。
5. cc-connect 把文字发回聊天。回复里的图片和文件走平台自己的图片/文件发送接口（微信个人号和飞书都实现了）。

```text
微信 / 飞书 / ...  →  cc-connect  →  POST webhook
                                 ←  POST /outbox/<id>  或  outbox/<id>.json
                  ←  文字 + 图片
```

## 1. 创建 Grok Bot webhook

在 Cursor 里创建一个以 HTTPS webhook 为触发器的自动化，复制 URL，形如：

```text
https://api2.cursor.sh/automations/webhook/<routine-id>
```

再创建自动化 API key（`crsr_...`）。把它放进 **cc-connect 进程的环境变量**，不要写进 git，也不要写进 `config.toml`：

```bash
export GROK_BOT_WEBHOOK_KEY='crsr_...'
```

systemd / launchd 不会继承你的 shell。请把变量写进服务环境。`cc-connect doctor` 只会告诉你这个变量有没有设置，不会打印它的值。

Routine 需要读取下面的 webhook JSON，并把回复写到 `reply.http` 或 `reply.file`。`{"success":true,"runUuid":"..."}` 只表示 run 已启动。在回复到达或 `timeout` 到期之前，聊天一侧会一直处于“正在输入”。

## 2. 配置 cc-connect

```toml
[[projects]]
name = "grok-bot"

[projects.agent]
type = "grokbot"

[projects.agent.options]
webhook_url = "https://api2.cursor.sh/automations/webhook/<routine-id>"
work_dir = "/path/to/project"
timeout = "6m"

# 下面是默认值，不需要改就可以删掉。
# webhook_key_env = "GROK_BOT_WEBHOOK_KEY"
# callback = true
# callback_addr = "127.0.0.1:9876"
# callback_auth = true
# outbox = true
# embed_images = false

[[projects.platforms]]
type = "weixin"

# [projects.platforms.options]
# token 由 `cc-connect weixin setup` 写入，不要贴到聊天里。
```

同一段 agent 配置可以换成飞书（`type = "feishu"`）或其他平台。平台本身的接入不变：[weixin.md](./weixin.md)、[feishu.md](./feishu.md)。

必须先设好 `GROK_BOT_WEBHOOK_KEY` 再启动。缺少 URL 或密钥为空时，进程会在收到任何消息之前直接退出。

### 选项

| 选项 | 默认 | 含义 |
| --- | --- | --- |
| `webhook_url` | 必填 | 自动化 webhook。必须是 `https`，避免 API key 和回复 token 明文传输。只有回环地址（`127.0.0.1`、`localhost`、`::1`）可以用 `http`，这是本机 mock。其他地址要设 `webhook_insecure_ok = true`，此时密钥和 token 会以明文发送。 |
| `webhook_key_env` | `GROK_BOT_WEBHOOK_KEY` | 存放 bearer key 的环境变量名。TOML 里的 `webhook_key` 会被拒绝。 |
| `work_dir` | `.` | 入站图片和文件的保存目录。聊天里的 `/dir` 会作用于下一轮。 |
| `timeout` | `6m` | 等待回复的时间。Go duration（`6m`、`180s`）。也可以改用 `timeout_mins`。 |
| `callback` | `true` | 监听 `POST /outbox/<id>`。 |
| `callback_addr` | `127.0.0.1:9876` | 监听地址。回环地址（`127.0.0.1`、`::1`）会原样写进 `reply.http`。通配地址（`0.0.0.0`、`::`）必须同时设置 `callback_advertise_host`，填 Grok Bot 真正能连上的主机或 IP。不会再把通配绑定悄悄改写成 `127.0.0.1`。 |
| `callback_auth` | `true` | 要求 `Authorization: Bearer <reply.token>`（或 `X-Grokbot-Token`）。token 每次进程启动随机生成，只出现在 webhook 正文里。设为 `false` 时，除非 `callback_addr` 是回环地址，或显式 `callback_insecure_ok = true`，否则拒绝启动。 |
| `callback_insecure_ok` | `false` | 允许在非回环地址上关闭 `callback_auth` 的显式开关。 |
| `callback_advertise_host` | 空 | 写进 `reply.http` 的主机或 IP。`callback_addr` 为 `0.0.0.0` 或 `::` 时必填。例如套接字监听通配地址，这里填 `192.168.1.10`。 |
| `reply_path_allow` | 空 | 回复还可以读取的额外目录。默认只允许 `work_dir`、`<work_dir>/.cc-connect/attachments` 和 outbox。 |
| `outbox` | `true` | 同时接受一个 JSON 文件。 |
| `outbox_dir` | `<work_dir>/.cc-connect/grokbot/outbox` | `reply.file` 所在目录，权限 `0700`。 |
| `embed_images` | `false` | 入站图片同时带上 `data_base64`。Routine 在云端、读不到本机 `path` 时打开。 |
| `max_embed_bytes` | `2097152` | 超过该大小的图片只发路径，不嵌 base64。 |

`callback` 和 `outbox` 至少开一个。同一台机器上的两个项目不能同时占用 `127.0.0.1:9876`，第二个要改 `callback_addr`。

`attachment_send = "off"` 时文字回复仍然发送，回复中的图片和文件会被丢掉，这和 `cc-connect send --image` 的开关一致。

## 3. Webhook 正文

```json
{
  "source": "cc-connect",
  "id": "本次请求 id",
  "session_id": "这条聊天多轮保持不变",
  "message_id": "平台消息 id",
  "text": "用户文字；如果带了图片或文件，末尾会附上本机绝对路径",
  "images": [
    {"path": "/abs/in.png", "name": "in.png", "mime": "image/png", "data_base64": "仅 embed_images 时出现"}
  ],
  "files": [
    {"path": "/abs/notes.txt", "name": "notes.txt", "mime": "text/plain"}
  ],
  "reply": {
    "http": "http://127.0.0.1:9876/outbox/<id>",
    "file": "/abs/outbox/<id>.json",
    "token": "本进程的回复 token",
    "hint": "如何回写"
  }
}
```

请求头：`Authorization: Bearer <GROK_BOT_WEBHOOK_KEY>`。如果环境变量本身已经以 `Bearer ` 开头，则原样发送，不会再套一层。

希望多轮连续对话时，让 routine 记住 `session_id`。聊天里执行 `/new` 会换一个新 id。

### 回复 JSON

POST 到 `reply.http`，或原子写入 `reply.file`（先写临时文件再 `rename`）：

```json
{
  "text": "回复正文",
  "images": ["/abs/chart.png", {"name": "shot.png", "mime": "image/png", "data_base64": "..."}],
  "files": ["/abs/report.pdf"]
}
```

`content` 等同于 `text`。`{"error":"model overloaded"}` 会结束这一轮，并把错误发到聊天里。每张图片或文件要么是 `data_base64`，要么是本机路径。解析符号链接之后，路径必须落在 `work_dir`、`<work_dir>/.cc-connect/attachments`、outbox 目录或 `reply_path_allow` 之内。主目录、SSH 密钥、配置文件等其他路径会让这一轮失败，内容不会被读进聊天。不会去拉取远程 URL。单个附件上限 50 MiB。扩展名无法识别时用内容嗅探（`http.DetectContentType`）判断类型，而不是当成 `image/png`。

`reply.file` 应先写临时文件再 rename。文件必须是普通文件，且不超过 32 MiB（与 HTTP 回调正文上限相同）。符号链接、设备文件和 FIFO 会被拒绝，不会以可能阻塞的方式打开。大约一秒后仍然不是合法 JSON 的文件会让这一轮直接失败，而不是一直等到总超时。轮次已经结束后才到达的回调返回 HTTP 409，而不是 “unknown id”。这种情况下正文不合法仍然是 HTTP 400。

默认 `callback_auth = true`，回传时要带上 token：

```bash
curl -sS -X POST "$REPLY_HTTP" \
  -H "Authorization: Bearer $REPLY_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"text":"pong"}'
```

写文件不需要 token。只有那些本来就往本机 POST、而且不带头的旧 routine，才需要设 `callback_auth = false`。

Webhook 本身的 HTTP 结果：

| 状态 | 含义 |
| --- | --- |
| 200 且 `success` 不是 `false` | Run 已启动。`runUuid` 会记入日志。cc-connect 继续等回复。 |
| 401 | 密钥错误或缺失。正文不会展示，避免把密钥回显出来。 |
| 400 | 请求被拒绝。Routine 被禁用时就是 400。响应文字会展示，但密钥会被打码。 |
| 其他 | 带上状态码展示。这一轮不会一直挂着。 |

## 4. 图片

**入站（聊天 → Grok Bot）。** 平台把图片字节交给 cc-connect。文件落在 `work_dir/.cc-connect/attachments/<message-id>/`，绝对路径会同时追加到 `text` 和 `images`。跑在同一台机器上的桌面 routine 可以直接打开 `path`。云端 routine 打不开本机路径，请设 `embed_images = true`，webhook 里会多带 `data_base64`（单张不超过 `max_embed_bytes`）。

**出站（Grok Bot → 聊天）。** 在 `images` / `files` 里给路径或 base64。cc-connect 读入后调用平台的发送接口，微信和飞书收到的是图片本身，而不是贴在文字里的路径。平台如果不能发图片或文件，文字仍然会到；如果回复里只有附件，聊天里会看到无法送达的文件名，而不是一条空白消息。

## 5. 不连 Grok Bot 的本地试跑

把 `webhook_url` 指到一个本机 mock：它打印正文，并把回复写进出站文件。下面没有真实密钥。

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

从微信或飞书发一条消息。Mock 会打印这一轮，聊天里会收到 `mock reply`。要试出站图片，让 mock 写成：

```json
{"text": "see", "images": ["/absolute/path/chart.png"]}
```

`go test ./agent/grokbot/` 覆盖了 webhook、两种回执通道、入站和出站图片、401/400、超时，以及日志和错误里不出现密钥。测试不会访问 Cursor。

## 错误与限制

- `GROK_BOT_WEBHOOK_KEY` 为空、缺少 `webhook_url`，或两个回执通道都关了：启动时以 `grokbot: ...` 退出。
- 密钥错误：聊天里显示 HTTP 401，并写出环境变量名。密钥不会出现在这条消息或日志里。
- `timeout`（默认 6 分钟）内没有回复：聊天里显示超时。会话还在，下一条消息可以继续。
- `/stop` 或进程退出会取消等待。
- Grok Bot 不会向 cc-connect 要工具权限。如果权限卡片被送到这里，`RespondPermission` 会返回错误。
- 没有本地 `grokbot` 可执行文件。`cc-connect doctor` 会跳过 PATH 检查，并报告 webhook、密钥变量名、回调地址和 outbox 目录。
- 回调默认只绑在回环地址。如果改成非回环地址，会打一条警告。不要把 9876 端口暴露到公网。
- API key 和回复 token 如果出现在错误字符串里会被打码。自己包装这个适配器时，也不要记录 webhook 正文。

## 构建标签

默认会编译进来。不需要时：

```bash
make build EXCLUDE=grokbot
# 或
go build -tags no_grokbot ./cmd/cc-connect
```
