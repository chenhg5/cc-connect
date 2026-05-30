# 大象 Bridge（daxiangbridge）接入指南

本文档说明如何让 **cc-connect** 通过 `daxiangbridge` 平台接入已经部署好的大象 Bridge 服务。

> 这个平台不是直接连大象官方接口，而是 **cc-connect 作为 bridge client**，通过 WebSocket 连到上游 bridge 服务，再由上游服务把大象侧消息转成 bridge 协议消息。

---

## 适用场景

适合下面这种拓扑：

- 你已经有一套运行中的大象 bridge 服务
- bridge 服务对外暴露 WebSocket 地址，例如 `ws://host/ws`
- 你希望 cc-connect 作为 Agent 侧执行端接入这套桥

一句话：**大象消息先到 bridge，再由 bridge 转给 cc-connect；cc-connect 的回复再通过 bridge 回写到大象。**

---

## 工作方式

`daxiangbridge` 平台启动后会做 4 件事：

1. 主动连接配置里的 `ws_url`
2. 发送 `client.register` 注册帧
3. 用 `client_id + client_secret + timestamp` 生成 AES-GCM 凭证完成鉴权
4. 收到 `client.registered` 后进入工作状态

之后：

- bridge 下发 `bridge.event.message`
- cc-connect 转成标准 `core.Message`
- Agent 产出的最终回复、流式回复、权限确认，再编码成 bridge 帧回发

相关实现：

- `platform/daxiangbridge/daxiangbridge.go`
- `platform/daxiangbridge/client.go`
- `platform/daxiangbridge/inbound.go`
- `platform/daxiangbridge/outbound.go`

---

## 前置要求

你需要准备好以下信息：

- `ws_url`：bridge 的 WebSocket 地址
- `client_id`：分配给这个 cc-connect 实例的 client 标识
- `client_secret`：32 位 hex 字符串，对应 16 字节 AES-128 密钥
- `bot_id`：这个 client 所服务的大象 bot ID

生成一个可用的 `client_secret`：

```bash
openssl rand -hex 16
```

输出示例：

```text
0123456789abcdef0123456789abcdef
```

---

## 配置示例

在 `config.toml` 里新增一个项目平台：

```toml
[[projects]]
name = "my-daxiang-project"

[projects.agent]
type = "claudecode"

[projects.agent.options]
work_dir = "/path/to/project"
mode = "default"

[[projects.platforms]]
type = "daxiangbridge"

[projects.platforms.options]
ws_url = "ws://your-bridge-host/ws"
client_id = "ccconnect-prod-01"
client_secret = "0123456789abcdef0123456789abcdef"
bot_id = 123456
```

最少必须配置的只有 4 个字段：

- `ws_url`
- `client_id`
- `client_secret`
- `bot_id`

如果缺任意一个，平台会启动失败。

---

## 字段说明

| 字段 | 必填 | 说明 |
|------|------|------|
| `ws_url` | 是 | bridge 服务的 WebSocket 地址，通常形如 `ws://host/ws` 或 `wss://host/ws` |
| `client_id` | 是 | 当前 cc-connect 实例的 client 身份标识 |
| `client_secret` | 是 | 32 位 hex 字符串，用于生成 AES-GCM 注册凭证 |
| `bot_id` | 是 | 这个 client 对应的大象 bot ID |

注意：

- `client_secret` 必须是 **32 个十六进制字符**
- 当前实现会校验长度，不符合会直接报错
- `bot_id` 必须是数值型

---

## 启动方式

配置好后正常启动 cc-connect：

```bash
cc-connect
```

如果你是源码运行：

```bash
go run ./cmd/cc-connect
```

连接成功后，`daxiangbridge` 会：

- 建立 WebSocket 长连接
- 自动注册
- 按固定节奏发送 `ping`
- 断线后自动重连

当前内置默认值：

- `pingInterval = 30s`
- `minBackoff = 3s`
- `maxBackoff = 60s`

见 `platform/daxiangbridge/client.go:41`

---

## 会话与路由语义

这里要特别注意：

### 1. `client` 是什么

这里的 `client` 指的是：

**一个通过 WebSocket 连到 bridge 的 cc-connect 实例**。

不是大象用户，也不是聊天 session。

### 2. 会区分用户 session 吗

**会区分消息所属 session，但连接路由不是按 session 分。**

bridge 下发消息时，帧里会带：

- `requestId`
- `sessionId`
- `conversationId`
- `messageId`
- `fromUserId`
- `fromUserName`

cc-connect 收到后会把：

- `SessionKey = frame.SessionID`
- `ChatName = conversationId`
- `ReplyCtx` 里保存 `requestId/sessionId/conversationId`

这意味着：

- 两个大象用户同时找同一个 bot 聊天
- 可以在 **同一个 cc-connect 连接** 上被正确区分
- 回复也会按各自 `requestId/sessionId` 回去，不会串

但当前 bridge 选在线连接的维度通常是 **bot/client 级别**，不是“用户一个连接”。

所以：

- **支持同一 bot 下多个用户并发会话**
- **不支持按用户把流量拆到多个 cc-connect 长连接做 session 级路由**

---

## 当前限制

### 1. 目前只支持私聊消息

入站标准化里明确要求：

- `chatType` 必须是 `private`
- 空文本消息会被拒绝

也就是当前实现默认只吃 private 消息。

见：

- `platform/daxiangbridge/inbound.go:21`
- `platform/daxiangbridge/inbound.go:24`

### 2. 依赖上游 bridge 服务

`daxiangbridge` 不是独立接大象，而是依赖上游 bridge 服务：

- 注册鉴权
- 会话分发
- 权限回传
- 最终消息落回大象

都由 bridge 服务负责承接。

### 3. 一个 bot 通常对应一个在线 client

当前语义下，更稳定的方式是：

- 一个 `bot_id`
- 对应一个在线 cc-connect client

如果你给同一个 `bot_id` 起多个 client，最终选中哪条连接，要看上游 bridge 的路由实现，不建议默认依赖这种行为做负载均衡。

---

## 联调建议

推荐按这个顺序验证：

### 1. 先验证 bridge 服务可连

确认 `ws_url` 可访问，例如：

```bash
wscat -c ws://your-bridge-host/ws
```

如果是内网服务，先确认网络和端口通。

### 2. 再启动 cc-connect

启动后看日志里是否出现成功注册、开始收消息。

### 3. 用大象侧发一条私聊消息

验证是否能进入 cc-connect，并触发 Agent。

### 4. 验证回复是否成功回写

重点检查：

- 最终回复是否能回到原 conversation
- 流式回复是否正常
- 权限请求是否能往返

---

## 常见问题

### 1. 启动时报 `ws_url, client_id, client_secret, and bot_id are required`

说明必填字段没配全。

### 2. 启动时报 `client_secret must be a 32-char hex string`

说明 `client_secret` 格式不对。

正确形式：

```text
0123456789abcdef0123456789abcdef
```

不要带空格，不要带 `0x`。

### 3. 连接反复重试

优先检查：

- `ws_url` 是否正确
- bridge 服务是否在线
- `client_id / client_secret / bot_id` 是否和上游配置一致
- 是否被上游鉴权拒绝

### 4. 收到消息但不回复

优先检查：

- Agent 是否正常启动
- cc-connect 当前项目是否选中了正确 agent
- 上游 bridge 是否正确处理 `agent.reply.final` / 流式帧

### 5. 群消息不生效

当前实现只支持 `private`。

---

## 相关文件

- 平台实现：`platform/daxiangbridge/`
- 配置示例：`config.example.toml`
- 通用 bridge 协议：`docs/bridge-protocol.md`
- 中文协议文档：`docs/bridge-protocol.zh-CN.md`

---

## 最小可运行配置

如果你只想先跑通，直接从这个最小例子开始：

```toml
[[projects]]
name = "daxiang-demo"

[projects.agent]
type = "claudecode"

[projects.agent.options]
work_dir = "/path/to/project"
mode = "default"

[[projects.platforms]]
type = "daxiangbridge"

[projects.platforms.options]
ws_url = "ws://127.0.0.1:18180/ws"
client_id = "ccconnect-demo-01"
client_secret = "0123456789abcdef0123456789abcdef"
bot_id = 10001
```

先保证这 4 个参数和上游 bridge 服务一致，再看消息链路。