# 微信个人号（Weixin / ilink）接入指南

本文档说明如何通过 **cc-connect** 接入**微信个人号**侧的对话能力。底层使用腾讯 **ilink 机器人 HTTP 网关**（与 OpenClaw 插件 `openclaw-weixin` 同类接口：`getUpdates` 长轮询 + `sendMessage` 下发）。

> **说明**：这是「个人微信 + ilink」通道，与 **[企业微信 WeChat Work](wecom.md)**（`type = "wecom"`）不是同一套协议，请勿混淆。

---

## 前置要求

- 可运行 cc-connect 的环境（无需公网 IP；ilink 由云端提供）
- 已安装并可正常使用的 Agent（如 Claude Code、Codex 等）
- 使用 **微信（手机端）** 扫码完成 ilink 登录（或由运营商提供 Bearer Token）

---

## 推荐流程：一条命令扫码

装好 `cc-connect` 后，在项目目录执行（将 `my-project` 换成你的 `config.toml` 里的项目名，或留空在仅有一个项目时自动选择）：

```bash
cc-connect weixin setup --project my-project
```

终端会打印：

1. **二维码**（终端 ASCII）以及 **可复制的 URL**（手机微信打开或扫码均可，取决于网关返回的链接形式）  
2. 按提示在手机上 **确认登录**  
3. 成功后，命令会把 **`token`（Bearer）**、**`base_url`**（若网关返回）、**`account_id`（ilink_bot_id）** 等写回 `config.toml`  
4. 若当前 `allow_from` 为空且你使用了 `--set-allow-from-empty`（默认开启），会尝试填入扫码关联的 **微信用户 ID**，便于限制谁可以使用机器人

### 命令对照

| 命令 | 作用 | 何时使用 |
|------|------|----------|
| `weixin setup` | 无 `--token` → 走扫码；有 `--token` → 等同绑定 | **默认首选** |
| `weixin new` | 强制扫码，不接受 `--token` | 明确只要重新扫码 |
| `weixin bind` | 强制只写 token，必须 `--token` | 已有 Token（例如从 OpenClaw 导出） |

已有 Token 时：

```bash
cc-connect weixin bind --project my-project --token '<你的_Bearer_Token>'
# 或
cc-connect weixin setup --project my-project --token '<你的_Bearer_Token>'
```

若校验失败，可检查 `--api-url` 是否与运营商一致（默认 `https://ilinkai.weixin.qq.com`），或使用 `--skip-verify` 仅写入配置（不推荐生产环境）。

### 常用参数

| 参数 | 说明 |
|------|------|
| `--config` | 指定 `config.toml` 路径 |
| `--project` | 目标项目名；不存在会自动创建并挂上 `weixin` 平台 |
| `--platform-index` | 同一项目多个 `weixin` 平台时，按 1 基索引选择 |
| `--api-url` | ilink 网关根地址（无尾部路径） |
| `--cdn-url` | 可选，同时写入 `cdn_base_url` |
| `--timeout` | 等待扫码秒数，默认 `480` |
| `--qr-image` | 将二维码 URL 导出为 PNG 路径 |
| `--route-tag` | 若运营商要求，设置 `SKRouteTag` 请求头 |
| `--bot-type` | `get_bot_qrcode` 的 `bot_type`，默认 `3` |
| `--debug` | 打印 HTTP 调试信息 |

---

## 配置说明（config.toml）

典型片段如下（具体键名以 `config.example.toml` 为准）：

```toml
[[projects.platforms]]
type = "weixin"

[projects.platforms.options]
token = "ilink_bot_bearer_token"       # 必填；扫码或 bind 写入
# base_url = "https://ilinkai.weixin.qq.com"   # 可选，默认同左
# cdn_base_url = "https://novac2c.cdn.weixin.qq.com/c2c"  # 可选，CDN 根路径
# allow_from = "user@im.wechat"        # 建议限制使用者；逗号分隔或 "*"
# account_id = "default"               # 多账号时区分状态目录，见下
# route_tag = ""                       # 与 CLI --route-tag 一致
# long_poll_timeout_ms = 35000
# proxy = ""                           # 可选 HTTP 代理
```

### `allow_from`

- 空或 `"*"` 表示不限制发送者（**不安全**，仅建议本机调试）。  
- 生产环境请填允许的 **ilink 用户 ID**（形如 `xxx@im.wechat`），多个用英文逗号分隔。  
- 扫码成功后，若开启默认的「空则回填」，会把扫码用户写入 `allow_from`（仍建议你核对后再上线）。

### `account_id` 与状态目录

多微信账号或多机器人时，可用不同 `account_id` 隔离本地状态。状态文件默认在：

`<data_dir>/weixin/<project>/<account_id>/`

其中含 `get_updates` 游标、`context_token` 缓存等，**勿手动泄露**。

### `context_token`（首次对话）

网关下发消息时可能带 `context_token`；cc-connect 会缓存并在回复时使用。  
**首次连接**：请先启动 cc-connect，再用允许的微信账号 **给机器人发一条消息**，完成关联后再使用 `/new` 等指令。

---

## 能力与限制（摘要）

- **文字、引用、语音转写文本**：与网关一致。  
- **图片 / 文件 / 视频 / 语音文件**：支持从微信 CDN 下载并按 AES-128-ECB 解密后交给 Agent（需正确配置 `cdn_base_url` 等）。  
- **出站图片与文件**：平台实现了 `ImageSender` / `FileSender`，可通过 `cc-connect send --image` / `--file` 等能力下发（需引擎侧已支持附件发送）。  
- **语音 SILK**：无转写文字时可走 STT（需配置语音转写且通常依赖 ffmpeg）。

---

## 精简编译（可选）

若不需要本通道，构建时可排除：

```bash
go build -tags no_weixin ./cmd/cc-connect
```

详见仓库 `Makefile` / `AGENTS.md` 中的构建标签说明。

---

## 故障排查

| 现象 | 建议 |
|------|------|
| 扫码无反应 / 超时 | 检查网络、 `--api-url`、`--timeout`；重试 `weixin setup` |
| 写入配置后仍收不到消息 | 确认 `allow_from`、进程已重启、微信端已发消息触发 `context_token` |
| 媒体无法解密 | 核对 `cdn_base_url`、网关返回的加密字段是否齐全 |
| 返回 errcode `-14` 等 | 多为会话过期，按日志提示暂停轮询后重新登录或稍后再试 |

---

## 相关链接

- 仓库内示例配置：[config.example.toml](../config.example.toml)  
- 使用指南中的 CLI 摘要：[usage.zh-CN.md](./usage.zh-CN.md)（「微信个人号配置 CLI」）  
- OpenClaw 同类插件（参考实现）：`openclaw-weixin`
