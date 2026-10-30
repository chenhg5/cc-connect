# Ruliu 自定义机器人接入

本指南聚焦「企业内部开发 - 自定义机器人」模式：通过你配置的回调 URL 接收如流事件，通过机器人提供的 `Webhook` 发送消息，全部跑在企业可达的网络（公网域名或企业网关）上。

## 0. 准备工作

1. 登录 [https://qy.baidu.com](https://qy.baidu.com)，在企业群界面点击右上角「机器人」→「添加机器人」→「创建机器人」。
2. 记下机器人在群内生成的 `Webhook` 地址，它只在创建时展示一次；稍后用于 cc-connect 出站发送消息。
3. 在「接收消息服务器」页面配置回调地址、Token 和 EncodingAESKey，启用「用户在群聊中@机器人消息」权限，并保存。

## 1. 配置回调

- **URL**：你的 cc-connect 进程需要有一个稳定可达的外部回调地址，例如 `https://your.domain/ruliu/callback`；HTTPS 可以由反向代理或企业网关终止，再转发到本地 `http://127.0.0.1:8082/ruliu/callback`。
- **Token**：任意字符串，用于 `signature=md5(rn+timestamp+token)` 校验，cc-connect 会在 HTTP 请求里重新计算并比较。
- **EncodingAESKey**：22 位 Base64 字符串；解码后得到 AES-128 ECB key，用于解密请求 body 中的密文字符串。

### 接收 URL 验证

如流会向 `URL` 发起 `POST`，并在 query 参数里带上 `signature`/`timestamp`/`rn`/`echostr`。你的服务器必须：

1. `md5(rn + timestamp + token)`，和请求里的 `signature` 比对。
2. 校验通过后在 3s 内返回 `echostr` 内容（明文）。

## 2. 处理消息事件

只需订阅 `MESSAGE_RECEIVE` 事件即可：

1. 回调请求的原始 body 是一段密文字符串，先做 Base64URL 解码（末尾用 `=` 补齐），再用 `AES-128-ECB-PKCS7` 解密，得到 JSON。
2. 结构参考：
   ```json
   {
     "eventtype":"MESSAGE_RECEIVE",
     "agentid":123,
     "groupid":1609028,
     "message":{
       "header":{"fromuserid":"zhoumengqi","msgtype":"MIXED","messageid":166..., ...},
       "body":[{"type":"AT","robotid":...},{"type":"TEXT","content":"hi"}]
     }
   }
   ```
3. 只处理 `eventtype=MESSAGE_RECEIVE` 且 `message.header.msgtype`/`body` 有文本部分。如 body 数组里有多个 TEXT/AT，建议按顺序拼出纯文本后统一发送到 agent。
4. 如果回调不包含文本，直接返回状态码即可，避免重复触发。

## 3. 发送消息

通过创建机器人后生成的 `Webhook` URL 发 `POST`，示例：

```bash
curl -X POST 'https://openapi.im.baidu.com/your-ruliu-robot-webhook' \
  -H 'Content-Type:application/json' \
  -d '{
    "message":{
      "header":{
        "toid":[123456]
      },
      "body":[
        {"type":"TEXT","content":"hi, robot!"}
      ]
    }
  }'
```

常见字段：

- `message.header.toid`：目标群 ID 数组；数字类型必须是纯数字。
- `message.body`：按序包含 `TEXT`/`LINK`/`IMAGE` 等子对象；机器人会渲染文本和链接，图片仅支持 base64 字节（<1MB）。

## 4. cc-connect 配置参考

```toml
[[projects.platforms]]
type = "ruliu"
[projects.platforms.options]
webhook = "https://openapi.im.baidu.com/your-ruliu-robot-webhook"
token = "your-callback-token"
encoding_aes_key = "your-22-char-encoding-aes-key"
# port = "8082"
# callback_path = "/ruliu/callback"
# allow_from = "user1,user2"
```

确保防火墙/网关把外网 `https://your-domain/ruliu/callback` 转发到本地 `cc-connect` 进程（默认监听 `http://127.0.0.1:8082`）。

## 5. 启动与验证

1. 启动 `cc-connect`，查看日志确认：`platform started project=xxx platform=ruliu`。
2. 在如流后台点击「保存接收服务器」，观察 `/ruliu/callback` 是否收到验证请求；正确返回 `echostr` 表示回调生效。
3. 在群内 @ 机器人发送消息，或使用 `/命令`，判断 cc-connect 是否收到并将结果投回。

## 6. 常见问题

- **需要公网吗？** 是的，Webhook 默认需要公网地址，可通过企业网关/Cloudflare Tunnel/内网映射暴露。
- **Token 泄露怎么办？** 令牌用于签名校验，建议与 `cc-connect` 中配置一致，并避免在日志或错误输出中直接打印原值。
- **为什么收到的 body 带多个 TEXT？** 如流把每段文本、@ 和链接都拆成对象；cc-connect 会跳过 `AT` 和空白占位，保留 `TEXT.content`，并把 `LINK.label` 作为可读文本拼回去。
- **如何避免重复通知？** 使用 `message.header.messageid` 去重；cc-connect 平台层会对短时间内重复消息做过滤。
