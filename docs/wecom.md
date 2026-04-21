# 企业微信 (WeChat Work) 接入指南

本文档介绍如何将 **cc-connect** 接入企业微信，让你可以通过企业微信（甚至个人微信）远程调用 Claude Code。

> 💡 **特色功能**：配置完成后，**个人微信用户也可以直接对话** —— 只需在企业微信管理后台关联微信插件即可。

企业微信支持两种接入模式：

| 模式 | 优势 | 要求 |
|------|------|------|
| **WebSocket 长连接**（推荐） | 无需公网 URL、无需消息加解密、无需 IP 白名单 | 创建「智能机器人」 |
| **Webhook 回调** | 支持图片/语音消息、Markdown 格式 | 公网 URL + 可信 IP |

---

## 模式一：WebSocket 长连接（推荐）

企业微信「智能机器人」支持 WebSocket 长连接模式，cc-connect 主动连接企业微信服务器，无需公网 URL、无需消息加解密、无需 IP 白名单，配置最简单。

### 前置要求

- 企业微信管理员权限
- 一台可运行 cc-connect 的服务器（**无需公网 IP**）
- Claude Code 已安装并配置完成

### 第一步：创建智能机器人

1. 登录 [企业微信管理后台](https://work.weixin.qq.com/wework_admin/frame)
2. 进入 **应用管理** → **智能机器人** → **创建智能机器人**
3. 填写机器人信息（名称、头像等）
4. 创建完成后，记录以下凭证：

```
BotID:  xxxxxxxxxxxxxxxx
Secret: xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
```

> ⚠️ Secret 只会显示一次，请立即保存！

### 第二步：配置 cc-connect

将凭证配置到 `config.toml` 中：

```toml
[[projects]]
name = "my-project"

[projects.agent]
type = "claudecode"

[projects.agent.options]
work_dir = "/path/to/your/project"
mode = "default"

[[projects.platforms]]
type = "wecom"

[projects.platforms.options]
mode = "websocket"
bot_id = "your-bot-id"
bot_secret = "your-bot-secret"
allow_from = "*"
```

#### 配置项说明

| 配置项 | 必填 | 说明 |
|--------|------|------|
| `mode` | ✅ | 必须为 `"websocket"` |
| `bot_id` | ✅ | 智能机器人 BotID |
| `bot_secret` | ✅ | 智能机器人 Secret |
| `allow_from` | ❌ | 允许的用户 ID（默认 `"*"` 允许所有） |

### 第三步：启动并验证

```bash
cc-connect
```

你应该看到类似日志：

```
level=INFO msg="wecom-ws: connecting" endpoint=wss://openws.work.weixin.qq.com
level=INFO msg="wecom-ws: subscribed successfully" bot_id=your-bot-id
```

在企业微信中找到你的机器人，发送一条消息测试即可。

### 技术细节

- **连接地址**：`wss://openws.work.weixin.qq.com`
- **认证方式**：连接后发送 `aibot_subscribe`（bot_id + secret）
- **心跳**：每 30 秒发送 `ping`
- **自动重连**：连接断开后指数退避重连（1s → 2s → 4s → ... → 30s max）
- **限制**：同一机器人仅支持 1 个长连接；30 条/分钟、1000 条/小时

---

## 模式二：Webhook 回调

> 💡 如果你不需要图片/语音消息或 Markdown 格式，推荐使用上方的 WebSocket 长连接模式，配置更简单。

### 前置要求

- 企业微信管理员权限
- 一台可运行 cc-connect 的服务器
- **公网可访问的 URL**（用于接收企业微信回调）
- Claude Code 已安装并配置完成

---

## 第一步：创建企业微信自建应用

### 1.1 进入管理后台

登录 [企业微信管理后台](https://work.weixin.qq.com/wework_admin/frame)。

### 1.2 创建应用

1. 进入 **应用管理** → **自建** → **创建应用**
2. 填写应用信息：

| 字段 | 填写建议 |
|------|---------|
| 应用名称 | `cc-connect` |
| 应用Logo | 上传一个喜欢的图标 |
| 可见范围 | 选择需要使用的部门/成员 |

### 1.3 记录凭证

创建完成后，记录以下信息：

```
AgentId:  1000002
Secret:   xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
```

> ⚠️ Secret 只会显示一次，请立即保存！

---

## 第二步：获取企业 ID

1. 在管理后台首页，点击 **我的企业**
2. 在页面底部找到 **企业ID (CorpId)**
3. 复制保存

```
CorpId: wwxxxxxxxxxxxxxx
```

---

## 第三步：配置接收消息

### 3.1 进入消息配置

进入你创建的应用 → **接收消息** → **设置API接收**

### 3.2 填写配置

| 字段 | 填写内容 |
|------|---------|
| **URL** | `https://你的公网域名/wecom/callback`（见第四步） |
| **Token** | 自定义一个随机字符串 |
| **EncodingAESKey** | 点击「随机获取」生成（43 个字符） |

> ⚠️ **暂时不要点保存！** 需要先启动 cc-connect 再回来保存（因为保存时企业微信会立即验证回调 URL）。

### 3.3 记录配置

把 Token 和 EncodingAESKey 记下来，后面配置 config.toml 要用。

---

## 第四步：配置公网访问

企业微信需要能够访问你的回调 URL。推荐方案：

### 方案 A：cloudflared tunnel（推荐，免费）

```bash
# 安装
# macOS: brew install cloudflared
# Linux: 参考 https://developers.cloudflare.com/cloudflare-one/connections/connect-apps/install-and-setup/

# 快速启动（会生成一个临时公网 URL）
cloudflared tunnel --url http://localhost:8081
```

启动后会输出类似 `https://xxx-xxx.trycloudflare.com`，将其作为回调 URL 的域名。

### 方案 B：ngrok（开发测试用）

```bash
ngrok http 8081
```

### 方案 C：有公网 IP 的服务器 + Nginx

```nginx
server {
    listen 443 ssl;
    server_name your-domain.com;

    ssl_certificate /path/to/cert.pem;
    ssl_certificate_key /path/to/key.pem;

    location /wecom/callback {
        proxy_pass http://127.0.0.1:8081;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
    }
}
```

---

## 第五步：配置企业可信 IP

企业微信要求调用 API 的服务器 IP 在白名单中。

### 5.1 查询服务器出口 IP

```bash
curl -s https://ifconfig.me
```

> 如果你的出口 IP 是动态的（如家用宽带），可以使用 VPS 正向代理方案，见后文「动态 IP 场景」。

### 5.2 添加到白名单

1. 进入 **应用管理** → 选择你的应用
2. 滚动到底部，找到 **企业可信IP**
3. 点击 **配置**，添加你的出口 IP

---

## 第六步：配置 cc-connect

将凭证配置到 `config.toml` 中：

```toml
[[projects]]
name = "my-project"

[projects.agent]
type = "claudecode"

[projects.agent.options]
work_dir = "/path/to/your/project"
mode = "default"

[[projects.platforms]]
type = "wecom"

[projects.platforms.options]
corp_id = "wwxxxxxxxxxxxxxx"
corp_secret = "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"
agent_id = "1000002"
callback_token = "你在第三步设置的Token"
callback_aes_key = "你在第三步获取的EncodingAESKey"
port = "8081"
callback_path = "/wecom/callback"
api_base_url = "https://qyapi.weixin.qq.com"
enable_markdown = false
```

### 配置项说明

| 配置项 | 必填 | 说明 |
|--------|------|------|
| `corp_id` | ✅ | 企业 ID |
| `corp_secret` | ✅ | 应用 Secret |
| `agent_id` | ✅ | 应用 AgentId |
| `callback_token` | ✅ | 回调 Token |
| `callback_aes_key` | ✅ | 回调 EncodingAESKey（43字符） |
| `port` | ❌ | Webhook 监听端口（默认 `8081`） |
| `callback_path` | ❌ | Webhook 路径（默认 `/wecom/callback`） |
| `api_base_url` | ❌ | 企业微信 API 基础地址（默认 `https://qyapi.weixin.qq.com`） |
| `enable_markdown` | ❌ | 是否发送 Markdown 消息（默认 `false`） |
| `proxy` | ❌ | HTTP 正向代理地址（动态 IP 场景使用） |

### 关于 enable_markdown

- `false`（默认）：发送纯文本消息，**企业微信应用和个人微信都能正常显示**
- `true`：发送 Markdown 格式消息，**仅企业微信应用内可渲染**，个人微信会显示「暂不支持的消息类型」

> 💡 如果你的用户主要通过个人微信使用，建议保持 `false`。

---

## 第七步：启动并验证

### 7.1 启动 cc-connect

```bash
cc-connect
# 或指定配置文件
cc-connect -config /path/to/config.toml
```

你应该看到类似日志：

```
level=INFO msg="platform started" project=my-project platform=wecom
level=INFO msg="cc-connect is running" projects=1
level=INFO msg="wecom: webhook server listening" port=8081 path=/wecom/callback
```

### 7.2 确保公网隧道在运行

```bash
# 确认 cloudflared / ngrok 正在运行并转发到 8081 端口
cloudflared tunnel --url http://localhost:8081
```

### 7.3 回到企业微信保存回调配置

1. 回到企业微信管理后台 → 你的应用 → 接收消息
2. 确认 URL 填写正确（cloudflared 生成的公网 URL + `/wecom/callback`）
3. 点击 **保存**
4. 如果验证通过，配置完成！

---

## 第八步：关联个人微信（可选）

如果希望**个人微信**也能直接与 AI 对话：

1. 登录企业微信管理后台
2. 进入 **我的企业** → **微信插件**
3. 用个人微信扫描页面上的二维码
4. 关联后，个人微信中会出现企业微信的应用入口

> 💡 关联后，个人微信用户可以直接发送消息给应用，无需安装企业微信。

---

## 动态 IP 场景

如果你的服务器没有固定公网 IP（如家用宽带），企业微信可信 IP 白名单无法使用动态 IP。解决方案：

### 使用 VPS 正向代理

1. 在一台有固定公网 IP 的 VPS 上安装 tinyproxy：

```bash
# Ubuntu/Debian
apt install tinyproxy

# 编辑配置：允许你的机器访问
vim /etc/tinyproxy/tinyproxy.conf
# 添加: Allow your-home-ip

systemctl restart tinyproxy
```

2. 在 cc-connect 配置中添加 proxy：

```toml
[projects.platforms.options]
# ... 其他配置 ...
proxy = "http://vps-ip:8888"
```

3. 将 VPS 的公网 IP 添加到企业可信 IP 白名单

这样 cc-connect 调用企业微信 API 时会通过 VPS 代理，出口 IP 固定为 VPS 的 IP。

---

## 架构图

```
┌─────────────────────────────────────────────────────────────┐
│                 企业微信 / 个人微信                            │
│                       服务器                                  │
│                        │                                     │
│                  加密 XML 回调                                │
└────────────────────────┼─────────────────────────────────────┘
                         │
                         │ HTTPS (需要公网 URL)
                         ▼
┌─────────────────────────────────────────────────────────────┐
│                    你的服务器                                  │
│                                                              │
│   cloudflared ──→ cc-connect ──→ Claude Code CLI             │
│   / ngrok            │                                       │
│                      │ (可选) proxy                          │
│                      ▼                                       │
│                企业微信 API ──→ VPS 正向代理                   │
│                                                              │
└─────────────────────────────────────────────────────────────┘
```

---

## 常见问题

### Q: 回调验证失败？

1. 确认 cc-connect 已启动且 webhook server 在监听
2. 确认公网隧道（cloudflared/ngrok）正在运行
3. 检查 URL 是否能公网访问：`curl https://你的域名/wecom/callback`
4. 检查 Token 和 EncodingAESKey 是否与管理后台一致

### Q: 消息发不出去？

1. 检查日志是否有 `get access_token failed` 错误
2. 确认出口 IP 在企业可信 IP 白名单中
3. 如果使用代理，确认代理服务正常运行

### Q: 报错 `60020` (not allow to access from your ip)？

日志中会提示实际的出口 IP，将该 IP 添加到企业可信 IP 白名单。

### Q: 个人微信显示「暂不支持的消息类型」？

将 `enable_markdown` 设为 `false`（默认值），改为发送纯文本消息。

### Q: 动态 IP 导致发送失败？

参考上文「动态 IP 场景」，使用 VPS 正向代理。

---

## 参考链接

- [企业微信管理后台](https://work.weixin.qq.com/wework_admin/frame)
- [企业微信开发文档](https://developer.work.weixin.qq.com/document/)
- [消息加解密说明](https://developer.work.weixin.qq.com/document/path/90307)
- [Cloudflare Tunnel 文档](https://developers.cloudflare.com/cloudflare-one/connections/connect-apps/)

---

## 下一步

- [接入飞书](./feishu.md)
- [接入钉钉](./dingtalk.md)
- [接入微博](./weibo.md)
- [接入 Telegram](./telegram.md)
- [接入 Slack](./slack.md)
- [接入 Discord](./discord.md)
- [返回首页](../README.md)
