# cc-connect 管理 API 规范

> **版本：** 1.0-draft  
> **状态：** 草案 — 实现前可能变更  
> **最后更新：** 2026-03-10

---

## 1. 概述

cc-connect 管理 API 是基于 HTTP 的 REST API，供外部应用（Web 控制台、TUI 客户端、GUI 桌面应用、Mac 托盘应用等）管理和监控 cc-connect 实例。它是对现有内部 Unix 套接字 API 的补充，提供可通过网络访问、基于令牌认证的接口，适用于远程和本地管理工具。

### 1.1 架构

```
┌─────────────────────────────────────────────────────────────────────────┐
│                         cc-connect Process                               │
│                                                                          │
│  ┌──────────────────┐    ┌──────────────────┐    ┌──────────────────┐  │
│  │  Unix Socket API │    │  Management API   │    │  Bridge Server   │  │
│  │  (internal)      │    │  (HTTP :9820)     │    │  (WebSocket)     │  │
│  └────────┬─────────┘    └────────┬─────────┘    └────────┬─────────┘  │
│           │                       │                       │             │
│           └───────────────────────┼───────────────────────┘             │
│                                   │                                      │
│                          ┌────────┴────────┐                            │
│                          │  Core Engine(s)  │                            │
│                          │  Projects       │                            │
│                          │  Sessions      │                            │
│                          │  Cron/Heartbeat │                            │
│                          └─────────────────┘                            │
└─────────────────────────────────────────────────────────────────────────┘
                                    │
              ┌─────────────────────┼─────────────────────┐
              │                     │                     │
     ┌────────┴────────┐   ┌───────┴───────┐   ┌─────────┴─────────┐
     │  Web Dashboard  │   │  TUI Client   │   │  Mac Tray App      │
     └─────────────────┘   └───────────────┘   └────────────────────┘
```

### 1.2 设计原则

- **RESTful：** 资源导向的 URL，标准 HTTP 方法
- **JSON：** 所有请求/响应体使用 `application/json`
- **统一封装：** 每个响应均使用 `{"ok": true|false, "data"|"error": ...}`
- **令牌认证：** 所有端点支持 Bearer 令牌或查询参数认证

---

## 2. 配置

### 2.1 管理配置块

在 `config.toml` 中添加以下配置：

```toml
[management]
enabled = true
port = 9820
token = "mgmt-secret"
```

| 字段       | 类型    | 默认值   | 说明                                      |
|------------|---------|----------|-------------------------------------------|
| `enabled`  | boolean | `false`  | 是否启用管理 API 服务                     |
| `port`     | integer | `9820`   | 监听 TCP 端口                             |
| `token`    | string  | (必填)   | 认证用共享密钥                            |

当 `enabled` 为 `false` 时，管理 API 不会启动。令牌应为强随机字符串（建议 32 字符以上）。

### 2.2 基础 URL

所有端点相对于以下基础路径：

```
http://<host>:<port>/api/v1
```

示例：`http://localhost:9820/api/v1/status`

---

## 3. 认证

每个请求必须携带有效令牌。支持两种方式：

### 3.1 Bearer 令牌（推荐）

```
Authorization: Bearer <token>
```

示例：

```bash
curl -H "Authorization: Bearer mgmt-secret" http://localhost:9820/api/v1/status
```

### 3.2 查询参数

```
GET /api/v1/status?token=mgmt-secret
```

> **注意：** 查询参数认证适用于难以设置请求头的环境。出于安全考虑，建议使用 Bearer 令牌（URL 中的令牌可能被记录到日志）。

### 3.3 未授权响应

若令牌缺失或无效：

- **HTTP 状态：** `401 Unauthorized`
- **响应体：**

```json
{
  "ok": false,
  "error": "unauthorized: missing or invalid token"
}
```

---

## 4. 响应格式

### 4.1 成功

```json
{
  "ok": true,
  "data": { ... }
}
```

### 4.2 错误

```json
{
  "ok": false,
  "error": "human-readable error message"
}
```

### 4.3 HTTP 状态码

| 状态码 | 含义                                      |
|--------|-------------------------------------------|
| 200    | 成功                                      |
| 400    | 请求错误（无效请求体、缺少参数）          |
| 401    | 未授权（缺少/无效令牌）                   |
| 404    | 资源未找到（项目、会话等）                |
| 405    | 方法不允许                                |
| 500    | 服务器内部错误                            |

---

## 5. 端点参考

### 5.1 系统

#### GET /api/v1/status

返回系统状态与摘要信息。

**响应：**

```json
{
  "ok": true,
  "data": {
    "version": "v1.2.0",
    "uptime_seconds": 3600,
    "connected_platforms": ["feishu", "telegram"],
    "projects_count": 2,
    "bridge_adapters": [
      {
        "platform": "custom",
        "project": "my-backend",
        "capabilities": ["text", "images"]
      }
    ]
  }
}
```

| 字段                   | 类型     | 说明                                      |
|------------------------|----------|-------------------------------------------|
| `version`               | string   | cc-connect 版本（如 `v1.2.0`）            |
| `uptime_seconds`       | number   | 进程运行时长（秒）                        |
| `connected_platforms`  | string[] | 当前已连接的平台类型                      |
| `projects_count`       | number   | 已配置项目数量                            |
| `bridge_adapters`      | array    | 通过 Bridge WebSocket 连接的外部适配器    |

---

#### POST /api/v1/restart

触发优雅重启。进程将正常退出并重新 exec 自身。若适用，可能向发起重启的会话发送「重启成功」消息。

**请求体（可选）：**

```json
{
  "session_key": "telegram:123:456",
  "platform": "telegram"
}
```

若提供，新进程启动后将向指定会话发送重启通知。

**响应：**

```json
{
  "ok": true,
  "data": {
    "message": "restart initiated"
  }
}
```

---

#### POST /api/v1/reload

从磁盘重新加载配置，无需重启进程。可添加新项目；已移除的项目将被停止。项目配置变更将生效。

**响应：**

```json
{
  "ok": true,
  "data": {
    "message": "config reloaded",
    "projects_added": ["new-project"],
    "projects_removed": [],
    "projects_updated": ["my-backend"]
  }
}
```

---

#### GET /api/v1/config

返回当前配置，敏感信息已脱敏。适用于调试和 UI 展示。

**查询参数：** 无

**响应：**

```json
{
  "ok": true,
  "data": {
    "data_dir": "/home/user/.cc-connect",
    "language": "en",
    "projects": [
      {
        "name": "my-backend",
        "agent": {
          "type": "claudecode",
          "providers": [
            {
              "name": "anthropic",
              "api_key": "***",
              "base_url": "",
              "model": "claude-sonnet-4-20250514"
            }
          ]
        },
        "platforms": [
          {
            "type": "feishu",
            "options": {
              "app_id": "***",
              "app_secret": "***"
            }
          }
        ]
      }
    ]
  }
}
```

敏感信息（如 `api_key`、`token`、`app_secret`、`client_secret`）将被替换为 `"***"`。

---

#### GET /api/v1/logs

返回近期日志条目。

**查询参数：**

| 参数     | 类型   | 默认值  | 说明                                          |
|----------|--------|---------|-----------------------------------------------|
| `level`  | string | `info`  | 最低级别：`debug`、`info`、`warn`、`error`    |
| `limit`  | int    | `100`   | 返回条目上限（1–1000）                        |

**响应：**

```json
{
  "ok": true,
  "data": {
    "entries": [
      {
        "time": "2026-03-10T10:30:00Z",
        "level": "info",
        "message": "api server started",
        "attrs": {"socket": "/home/user/.cc-connect/run/api.sock"}
      }
    ]
  }
}
```

---

### 5.2 项目

#### GET /api/v1/projects

列出所有项目及摘要信息。

**响应：**

```json
{
  "ok": true,
  "data": {
    "projects": [
      {
        "name": "my-backend",
        "agent_type": "claudecode",
        "platforms": ["feishu", "telegram"],
        "sessions_count": 3,
        "heartbeat_enabled": true
      }
    ]
  }
}
```

---

#### GET /api/v1/projects/{name}

返回单个项目的详细信息。

**路径参数：**

| 参数   | 类型   | 说明        |
|--------|--------|-------------|
| `name` | string | 项目名称    |

**响应：**

```json
{
  "ok": true,
  "data": {
    "name": "my-backend",
    "agent_type": "claudecode",
    "platforms": [
      {
        "type": "feishu",
        "connected": true
      },
      {
        "type": "telegram",
        "connected": true
      }
    ],
    "sessions_count": 3,
    "active_session_keys": ["telegram:123:456", "feishu:ou_xxx:chat_xxx"],
    "heartbeat": {
      "enabled": true,
      "paused": false,
      "interval_mins": 30,
      "session_key": "telegram:123:456"
    },
    "settings": {
      "quiet": false,
      "admin_from": "user1,user2",
      "language": "en",
      "disabled_commands": ["restart", "upgrade"]
    }
  }
}
```

**错误（404）：**

```json
{
  "ok": false,
  "error": "project not found: my-backend"
}
```

---

#### PATCH /api/v1/projects/{name}

更新项目设置。仅更新提供的字段。

**请求体：**

```json
{
  "quiet": true,
  "admin_from": "user1,user2,user3",
  "language": "zh",
  "disabled_commands": ["restart", "upgrade", "cron"]
}
```

| 字段                 | 类型     | 说明                                                      |
|----------------------|----------|-----------------------------------------------------------|
| `quiet`              | boolean  | 是否隐藏思考过程/工具进度消息                             |
| `admin_from`         | string   | 特权命令用户 ID 列表（逗号分隔）；`"*"` 表示全部用户      |
| `language`           | string   | 界面语言：`en`、`zh`、`zh-TW`、`ja`、`es`                 |
| `disabled_commands`  | string[] | 要禁用的命令（如 `restart`、`upgrade`、`cron`）           |

**响应：**

```json
{
  "ok": true,
  "data": {
    "name": "my-backend",
    "settings": {
      "quiet": true,
      "admin_from": "user1,user2,user3",
      "language": "zh",
      "disabled_commands": ["restart", "upgrade", "cron"]
    }
  }
}
```

---

### 5.3 会话

会话是项目内的对话上下文。会话由 `session_key`（格式：`platform:chatId:userId`）标识，命名会话还可通过内部 `id` 标识（例如 `/new work` 会创建命名会话）。

#### GET /api/v1/projects/{name}/sessions

列出项目的会话列表。

**响应：**

```json
{
  "ok": true,
  "data": {
    "sessions": [
      {
        "id": "sess_abc123",
        "session_key": "telegram:123:456",
        "name": "work",
        "platform": "telegram",
        "active": true,
        "created_at": "2026-03-10T09:00:00Z",
        "updated_at": "2026-03-10T10:30:00Z",
        "history_count": 12
      }
    ]
  }
}
```

---

#### POST /api/v1/projects/{name}/sessions

创建新会话。

**请求体：**

```json
{
  "session_key": "telegram:123:456",
  "name": "work"
}
```

| 字段          | 类型   | 必填 | 说明                                      |
|---------------|--------|------|-------------------------------------------|
| `session_key` | string | 是   | 平台路由键（如 `telegram:123:456`）       |
| `name`        | string | 否   | 人类可读的会话名称                        |

**响应：**

```json
{
  "ok": true,
  "data": {
    "id": "sess_xyz789",
    "session_key": "telegram:123:456",
    "name": "work",
    "created_at": "2026-03-10T10:35:00Z"
  }
}
```

---

#### GET /api/v1/projects/{name}/sessions/{id}

返回会话详情，包含消息历史。

**路径参数：**

| 参数   | 类型   | 说明                          |
|--------|--------|-------------------------------|
| `name` | string | 项目名称                      |
| `id`   | string | 会话 ID 或 session_key        |

**查询参数：**

| 参数             | 类型 | 默认值 | 说明                    |
|------------------|------|--------|-------------------------|
| `history_limit`  | int  | 50     | 返回的历史条目上限      |

**响应：**

```json
{
  "ok": true,
  "data": {
    "id": "sess_abc123",
    "session_key": "telegram:123:456",
    "name": "work",
    "platform": "telegram",
    "active": true,
    "agent_session_id": "as_xxx",
    "created_at": "2026-03-10T09:00:00Z",
    "updated_at": "2026-03-10T10:30:00Z",
    "history": [
      {
        "role": "user",
        "content": "Hello",
        "timestamp": "2026-03-10T09:00:05Z"
      },
      {
        "role": "assistant",
        "content": "Hi! How can I help?",
        "timestamp": "2026-03-10T09:00:10Z"
      }
    ]
  }
}
```

---

#### DELETE /api/v1/projects/{name}/sessions/{id}

删除会话及其历史记录。

**响应：**

```json
{
  "ok": true,
  "data": {
    "message": "session deleted"
  }
}
```

---

#### POST /api/v1/projects/{name}/sessions/switch

切换指定 session_key 的活跃会话（例如用户有多个命名会话时）。

**请求体：**

```json
{
  "session_key": "telegram:123:456",
  "session_id": "sess_xyz789"
}
```

| 字段          | 类型   | 必填 | 说明                    |
|---------------|--------|------|-------------------------|
| `session_key` | string | 是   | 平台路由键              |
| `session_id`  | string | 是   | 要设为活跃的会话 ID     |

**响应：**

```json
{
  "ok": true,
  "data": {
    "message": "active session switched",
    "active_session_id": "sess_xyz789"
  }
}
```

---

#### POST /api/v1/projects/{name}/send

向会话发送消息。消息会像用户通过平台发送一样传递给 Agent。

**请求体：**

```json
{
  "session_key": "telegram:123:456",
  "message": "Review the latest commit"
}
```

| 字段          | 类型   | 必填 | 说明                    |
|---------------|--------|------|-------------------------|
| `session_key` | string | 是   | 平台路由键              |
| `message`     | string | 是   | 发送给 Agent 的文本     |

**响应：**

```json
{
  "ok": true,
  "data": {
    "message": "message sent"
  }
}
```

---

### 5.4 提供商

提供商是 API 后端（如 Anthropic、OpenAI、自定义端点），为项目的 Agent 提供 AI 模型。

#### GET /api/v1/projects/{name}/providers

列出提供商及其活跃状态。

**响应：**

```json
{
  "ok": true,
  "data": {
    "providers": [
      {
        "name": "anthropic",
        "active": true,
        "model": "claude-sonnet-4-20250514",
        "base_url": ""
      },
      {
        "name": "relay",
        "active": false,
        "model": "claude-sonnet-4-20250514",
        "base_url": "https://api.relay.example.com"
      }
    ],
    "active_provider": "anthropic"
  }
}
```

---

#### POST /api/v1/projects/{name}/providers

添加新提供商。

**请求体：**

```json
{
  "name": "relay",
  "api_key": "sk-xxx",
  "base_url": "https://api.relay.example.com",
  "model": "claude-sonnet-4-20250514",
  "thinking": "disabled",
  "env": {
    "CLAUDE_CODE_USE_BEDROCK": "1",
    "AWS_PROFILE": "bedrock"
  }
}
```

| 字段        | 类型           | 必填 | 说明                                          |
|-------------|----------------|------|-----------------------------------------------|
| `name`      | string         | 是   | 提供商标识符                                  |
| `api_key`   | string         | 否*  | API 密钥（*未提供 `env` 时为必填）            |
| `base_url`  | string         | 否   | 自定义 API 端点                               |
| `model`     | string         | 否   | 模型覆盖                                      |
| `thinking`  | string         | 否   | `"disabled"` 表示提供商不支持自适应思考      |
| `env`       | object (k/v)   | 否   | 额外环境变量                                  |

**响应：**

```json
{
  "ok": true,
  "data": {
    "name": "relay",
    "message": "provider added"
  }
}
```

---

#### DELETE /api/v1/projects/{name}/providers/{provider}

移除提供商。无法移除当前活跃的提供商，需先切换。

**响应：**

```json
{
  "ok": true,
  "data": {
    "message": "provider removed"
  }
}
```

**错误（400）：**

```json
{
  "ok": false,
  "error": "cannot remove active provider; switch to another first"
}
```

---

#### POST /api/v1/projects/{name}/providers/{provider}/activate

切换活跃提供商。

**响应：**

```json
{
  "ok": true,
  "data": {
    "active_provider": "relay",
    "message": "provider activated"
  }
}
```

---

#### GET /api/v1/projects/{name}/models

列出项目 Agent 类型可用的模型。

**响应：**

```json
{
  "ok": true,
  "data": {
    "models": [
      "claude-sonnet-4-20250514",
      "claude-3-5-sonnet-20241022",
      "claude-3-opus-20240229"
    ],
    "current": "claude-sonnet-4-20250514"
  }
}
```

---

#### POST /api/v1/projects/{name}/model

设置项目使用的模型。

**请求体：**

```json
{
  "model": "claude-3-5-sonnet-20241022"
}
```

**响应：**

```json
{
  "ok": true,
  "data": {
    "model": "claude-3-5-sonnet-20241022",
    "message": "model updated"
  }
}
```

---

### 5.5 定时任务

#### GET /api/v1/cron

列出所有定时任务，可按项目筛选。

**查询参数：**

| 参数      | 类型   | 说明        |
|-----------|--------|-------------|
| `project` | string | 按项目筛选  |

**响应：**

```json
{
  "ok": true,
  "data": {
    "jobs": [
      {
        "id": "cron_abc123",
        "project": "my-backend",
        "session_key": "telegram:123:456",
        "cron_expr": "0 6 * * *",
        "prompt": "Summarize GitHub trending",
        "exec": "",
        "work_dir": "",
        "description": "Daily GitHub Trending",
        "enabled": true,
        "silent": true,
        "created_at": "2026-03-10T08:00:00Z",
        "last_run": "2026-03-10T06:00:00Z",
        "last_error": ""
      }
    ]
  }
}
```

---

#### POST /api/v1/cron

添加定时任务。必须提供 `prompt` 或 `exec` 之一，不可同时提供。

**请求体（prompt 任务）：**

```json
{
  "project": "my-backend",
  "session_key": "telegram:123:456",
  "cron_expr": "0 6 * * *",
  "prompt": "Summarize GitHub trending",
  "description": "Daily GitHub Trending",
  "silent": true
}
```

**请求体（exec 任务）：**

```json
{
  "project": "my-backend",
  "session_key": "telegram:123:456",
  "cron_expr": "0 9 * * 1",
  "exec": "npm run weekly-report",
  "work_dir": "/path/to/project",
  "description": "Weekly Report",
  "silent": false
}
```

| 字段          | 类型    | 必填 | 说明                                        |
|---------------|---------|------|---------------------------------------------|
| `project`     | string  | 否*  | 项目名称（*多项目时为必填）                 |
| `session_key` | string  | 是   | prompt 任务的目标会话                       |
| `cron_expr`   | string  | 是   | Cron 表达式（5 或 6 个字段）                |
| `prompt`      | string  | 否*  | 要发送的 prompt（*未提供 `exec` 时为必填）  |
| `exec`        | string  | 否*  | Shell 命令（*未提供 `prompt` 时为必填）     |
| `work_dir`    | string  | 否   | exec 的工作目录                             |
| `description` | string  | 否   | 人类可读的标签                              |
| `silent`      | boolean | 否   | 是否隐藏启动通知                            |
| `session_mode` | string | 否   | `reuse`（默认）或 `new_per_run`：每次运行新建 agent 会话 |
| `timeout_mins` | int    | 否   | 单次调度最长等待：省略=30 分钟，`0`=不限制 |

**响应：**

```json
{
  "ok": true,
  "data": {
    "id": "cron_xyz789",
    "project": "my-backend",
    "session_key": "telegram:123:456",
    "cron_expr": "0 6 * * *",
    "prompt": "Summarize GitHub trending",
    "description": "Daily GitHub Trending",
    "enabled": true,
    "created_at": "2026-03-10T10:40:00Z"
  }
}
```

---

#### DELETE /api/v1/cron/{id}

删除定时任务。

**响应：**

```json
{
  "ok": true,
  "data": {
    "message": "cron job deleted"
  }
}
```

#### POST /api/v1/cron/{id}/run

立即触发一个已存在的 cron 任务。即使任务已禁用，仍然允许手动触发。

**响应：**

```json
{
  "ok": true,
  "data": {
    "id": "cron_xyz789",
    "status": "triggered"
  }
}
```

---

### 5.6 心跳

心跳在会话中定期执行 prompt（如「检查收件箱」），使 Agent 持续感知环境状态。

#### GET /api/v1/projects/{name}/heartbeat

返回心跳状态。

**响应：**

```json
{
  "ok": true,
  "data": {
    "enabled": true,
    "paused": false,
    "interval_mins": 30,
    "only_when_idle": true,
    "session_key": "telegram:123:456",
    "silent": true,
    "run_count": 42,
    "error_count": 0,
    "skipped_busy": 5,
    "last_run": "2026-03-10T10:00:00Z",
    "last_error": ""
  }
}
```

---

#### POST /api/v1/projects/{name}/heartbeat/pause

暂停心跳。

**响应：**

```json
{
  "ok": true,
  "data": {
    "message": "heartbeat paused"
  }
}
```

---

#### POST /api/v1/projects/{name}/heartbeat/resume

恢复心跳。

**响应：**

```json
{
  "ok": true,
  "data": {
    "message": "heartbeat resumed"
  }
}
```

---

#### POST /api/v1/projects/{name}/heartbeat/run

立即触发一次心跳（单次执行）。

**响应：**

```json
{
  "ok": true,
  "data": {
    "message": "heartbeat triggered"
  }
}
```

---

#### POST /api/v1/projects/{name}/heartbeat/interval

设置心跳间隔。

**请求体：**

```json
{
  "minutes": 15
}
```

**响应：**

```json
{
  "ok": true,
  "data": {
    "interval_mins": 15,
    "message": "interval updated"
  }
}
```

---

### 5.7 Bridge

#### GET /api/v1/bridge/adapters

列出已连接的 Bridge 适配器（通过 WebSocket 连接的外部平台）。

**响应：**

```json
{
  "ok": true,
  "data": {
    "adapters": [
      {
        "platform": "custom",
        "project": "my-backend",
        "capabilities": ["text", "images", "files"],
        "connected_at": "2026-03-10T09:00:00Z"
      }
    ]
  }
}
```

---

## 6. 错误处理约定

### 6.1 标准错误响应

所有错误使用相同封装格式：

```json
{
  "ok": false,
  "error": "human-readable message"
}
```

### 6.2 常见错误

| HTTP | 错误消息示例                                  | 原因                          |
|------|-----------------------------------------------|-------------------------------|
| 400  | `"project is required (multiple projects)"`   | 缺少必填参数                  |
| 400  | `"either prompt or exec is required"`         | 定时任务请求体无效            |
| 401  | `"unauthorized: missing or invalid token"`    | 认证失败                      |
| 404  | `"project not found: xyz"`                    | 未知项目/会话/定时任务       |
| 404  | `"session not found"`                         | 未知会话 ID                   |
| 405  | `"method not allowed"`                       | HTTP 方法错误                 |
| 500  | `"internal error"`                           | 服务器意外错误                |

### 6.3 校验错误

当请求体验证失败时：

```json
{
  "ok": false,
  "error": "invalid request: session_key is required"
}
```

---

## 7. Session Key 格式

`session_key` 是用于将消息路由到正确平台和会话的复合标识符：

```
<platform>:<chat_id>:<user_id>
```

示例：

- `telegram:123456789:123456789` — Telegram 用户 123456789，会话 123456789
- `feishu:ou_xxx:chat_yyy` — 飞书用户与会话
- `slack:C01234:U05678` — Slack 频道与用户
- `discord:123456789:987654321` — Discord 服务器与用户

多工作区模式下，格式可能包含工作区前缀：

```
<workspace>:<platform>:<chat_id>:<user_id>
```

---

## 8. CORS

当管理 API 被 Web 控制台调用时，CORS 头应可配置。建议的配置扩展：

```toml
[management]
enabled = true
port = 9820
token = "mgmt-secret"
cors_origins = ["http://localhost:3000", "https://dashboard.example.com"]
```

若未配置，CORS 可能被禁用或使用默认值（例如仅同源时为 `*`）。

---

## 9. 更新日志

| 版本       | 日期       | 变更                    |
|------------|------------|-------------------------|
| 1.0-draft  | 2026-03-10 | 初始规范                |

---

## 10. 参考

- [Bridge 协议](bridge-protocol.md) — 外部平台适配器的 WebSocket 协议
- [使用指南](usage.md) — 终端用户功能与斜杠命令
- [config.example.toml](../config.example.toml) — 配置模板
