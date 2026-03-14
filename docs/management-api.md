# cc-connect Management API Specification

> **Version:** 1.0-draft  
> **Status:** Draft — subject to change before implementation  
> **Last Updated:** 2026-03-10

---

## 1. Overview

The cc-connect Management API is an HTTP-based REST API that enables external applications (web dashboards, TUI clients, GUI desktop apps, Mac tray apps) to manage and monitor cc-connect instances. It complements the existing internal Unix socket API by providing a network-accessible, token-authenticated interface suitable for remote and local management tools.

### 1.1 Architecture

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

### 1.2 Design Principles

- **RESTful:** Resource-oriented URLs, standard HTTP methods
- **JSON:** All request/response bodies use `application/json`
- **Consistent envelope:** Every response uses `{"ok": true|false, "data"|"error": ...}`
- **Token auth:** Bearer token or query parameter for all endpoints

---

## 2. Configuration

### 2.1 Management Block

Add the following to `config.toml`:

```toml
[management]
enabled = true
port = 9820
token = "mgmt-secret"
```

| Field    | Type    | Default   | Description                                      |
|----------|---------|-----------|--------------------------------------------------|
| `enabled`| boolean | `false`   | Enable the Management API server                 |
| `port`   | integer | `9820`    | TCP port to listen on                            |
| `token`  | string  | (required)| Shared secret for authentication                 |

When `enabled` is `false`, the Management API is not started. The token should be a strong, random string (e.g. 32+ characters).

### 2.2 Base URL

All endpoints are relative to:

```
http://<host>:<port>/api/v1
```

Example: `http://localhost:9820/api/v1/status`

---

## 3. Authentication

Every request must include a valid token. Two methods are supported:

### 3.1 Bearer Token (Recommended)

```
Authorization: Bearer <token>
```

Example:

```bash
curl -H "Authorization: Bearer mgmt-secret" http://localhost:9820/api/v1/status
```

### 3.2 Query Parameter

```
GET /api/v1/status?token=mgmt-secret
```

> **Note:** Query parameter auth is provided for environments where setting headers is difficult. Prefer Bearer token for security (tokens in URLs may be logged).

### 3.3 Unauthorized Response

If the token is missing or invalid:

- **HTTP Status:** `401 Unauthorized`
- **Body:**

```json
{
  "ok": false,
  "error": "unauthorized: missing or invalid token"
}
```

---

## 4. Response Format

### 4.1 Success

```json
{
  "ok": true,
  "data": { ... }
}
```

### 4.2 Error

```json
{
  "ok": false,
  "error": "human-readable error message"
}
```

### 4.3 HTTP Status Codes

| Code | Meaning                                      |
|------|----------------------------------------------|
| 200  | Success                                      |
| 400  | Bad request (invalid body, missing params)   |
| 401  | Unauthorized (missing/invalid token)        |
| 404  | Resource not found (project, session, etc.) |
| 405  | Method not allowed                          |
| 500  | Internal server error                        |

---

## 5. Endpoint Reference

### 5.1 System

#### GET /api/v1/status

Returns system status and summary.

**Response:**

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

| Field                 | Type     | Description                                      |
|-----------------------|----------|--------------------------------------------------|
| `version`             | string   | cc-connect version (e.g. `v1.2.0`)              |
| `uptime_seconds`      | number   | Process uptime in seconds                        |
| `connected_platforms` | string[] | Platform types currently connected               |
| `projects_count`      | number   | Number of configured projects                    |
| `bridge_adapters`     | array   | External adapters connected via Bridge WebSocket |

---

#### POST /api/v1/restart

Triggers a graceful restart. The process will shut down cleanly and exec itself. A "restart successful" message may be sent to the session that initiated the restart (if applicable).

**Request body (optional):**

```json
{
  "session_key": "telegram:123:456",
  "platform": "telegram"
}
```

If provided, the restart notification will be sent to the specified session after the new process starts.

**Response:**

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

Reloads configuration from disk without restarting the process. New projects may be added; removed projects are stopped. Changed project settings take effect.

**Response:**

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

Returns the current configuration with secrets redacted. Useful for debugging and UI display.

**Query parameters:** None

**Response:**

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

Secrets (e.g. `api_key`, `token`, `app_secret`, `client_secret`) are replaced with `"***"`.

---

#### GET /api/v1/logs

Returns recent log entries.

**Query parameters:**

| Param   | Type   | Default | Description                          |
|---------|--------|---------|--------------------------------------|
| `level` | string | `info`  | Minimum level: `debug`, `info`, `warn`, `error` |
| `limit` | int    | `100`   | Max entries to return (1–1000)       |

**Response:**

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

### 5.2 Projects

#### GET /api/v1/projects

Lists all projects with a summary.

**Response:**

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

Returns detailed information for a single project.

**Path parameters:**

| Param  | Type   | Description        |
|--------|--------|--------------------|
| `name` | string | Project name       |

**Response:**

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

**Error (404):**

```json
{
  "ok": false,
  "error": "project not found: my-backend"
}
```

---

#### PATCH /api/v1/projects/{name}

Updates project settings. Only provided fields are updated.

**Request body:**

```json
{
  "quiet": true,
  "admin_from": "user1,user2,user3",
  "language": "zh",
  "disabled_commands": ["restart", "upgrade", "cron"]
}
```

| Field               | Type     | Description                                              |
|---------------------|----------|----------------------------------------------------------|
| `quiet`             | boolean  | Suppress thinking/tool progress messages                 |
| `admin_from`        | string   | Comma-separated user IDs for privileged commands; `"*"` = all |
| `language`          | string   | UI language: `en`, `zh`, `zh-TW`, `ja`, `es`             |
| `disabled_commands` | string[] | Commands to disable (e.g. `restart`, `upgrade`, `cron`)  |

**Response:**

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

### 5.3 Sessions

Sessions are conversation contexts within a project. A session is identified by a `session_key` (format: `platform:chatId:userId`) and optionally by an internal `id` for named sessions (e.g. `/new work` creates a named session).

#### GET /api/v1/projects/{name}/sessions

Lists sessions for a project.

**Response:**

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

Creates a new session.

**Request body:**

```json
{
  "session_key": "telegram:123:456",
  "name": "work"
}
```

| Field        | Type   | Required | Description                          |
|--------------|--------|----------|--------------------------------------|
| `session_key`| string | yes      | Platform routing key (e.g. `telegram:123:456`) |
| `name`       | string | no       | Human-readable session name           |

**Response:**

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

Returns session detail including message history.

**Path parameters:**

| Param  | Type   | Description                          |
|--------|--------|--------------------------------------|
| `name` | string | Project name                         |
| `id`   | string | Session ID or session_key            |

**Query parameters:**

| Param   | Type | Default | Description                    |
|---------|------|---------|--------------------------------|
| `history_limit` | int | 50 | Max history entries to return |

**Response:**

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

Deletes a session and its history.

**Response:**

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

Switches the active session for a given session_key (e.g. when a user has multiple named sessions).

**Request body:**

```json
{
  "session_key": "telegram:123:456",
  "session_id": "sess_xyz789"
}
```

| Field         | Type   | Required | Description                    |
|---------------|--------|----------|--------------------------------|
| `session_key` | string | yes      | Platform routing key           |
| `session_id`  | string | yes      | Session ID to make active      |

**Response:**

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

Sends a message to a session. The message is delivered to the agent as if the user had sent it via the platform.

**Request body:**

```json
{
  "session_key": "telegram:123:456",
  "message": "Review the latest commit"
}
```

| Field         | Type   | Required | Description                    |
|---------------|--------|----------|--------------------------------|
| `session_key`| string | yes      | Platform routing key           |
| `message`    | string | yes      | Text to send to the agent      |

**Response:**

```json
{
  "ok": true,
  "data": {
    "message": "message sent"
  }
}
```

---

### 5.4 Providers

Providers are API backends (e.g. Anthropic, OpenAI, custom endpoints) that supply the AI model for a project's agent.

#### GET /api/v1/projects/{name}/providers

Lists providers with active indicator.

**Response:**

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

Adds a new provider.

**Request body:**

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

| Field     | Type            | Required | Description                              |
|-----------|-----------------|----------|------------------------------------------|
| `name`    | string          | yes      | Provider identifier                      |
| `api_key` | string          | no*      | API key (*required if no `env`)          |
| `base_url`| string          | no       | Custom API endpoint                      |
| `model`   | string          | no       | Model override                           |
| `thinking`| string          | no       | `"disabled"` for providers without adaptive thinking |
| `env`     | object (k/v)    | no       | Extra environment variables              |

**Response:**

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

Removes a provider. The active provider cannot be removed; switch first.

**Response:**

```json
{
  "ok": true,
  "data": {
    "message": "provider removed"
  }
}
```

**Error (400):**

```json
{
  "ok": false,
  "error": "cannot remove active provider; switch to another first"
}
```

---

#### POST /api/v1/projects/{name}/providers/{provider}/activate

Switches the active provider.

**Response:**

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

Lists available models for the project's agent type.

**Response:**

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

Sets the model for the project.

**Request body:**

```json
{
  "model": "claude-3-5-sonnet-20241022"
}
```

**Response:**

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

### 5.5 Cron Jobs

#### GET /api/v1/cron

Lists all cron jobs, optionally filtered by project.

**Query parameters:**

| Param     | Type   | Description        |
|-----------|--------|--------------------|
| `project` | string | Filter by project  |

**Response:**

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

Adds a cron job. Either `prompt` or `exec` must be provided, not both.

**Request body (prompt job):**

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

**Request body (exec job):**

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

| Field        | Type    | Required | Description                                    |
|--------------|---------|----------|------------------------------------------------|
| `project`    | string  | no*      | Project name (*required if multiple projects) |
| `session_key`| string  | yes      | Target session for prompt jobs                 |
| `cron_expr`  | string  | yes      | Cron expression (5 or 6 fields)                |
| `prompt`     | string  | no*      | Prompt to send (*required if no `exec`)        |
| `exec`       | string  | no*      | Shell command (*required if no `prompt`)       |
| `work_dir`   | string  | no       | Working directory for exec                    |
| `description`| string  | no       | Human-readable label                           |
| `silent`     | boolean | no       | Suppress start notification                   |

**Response:**

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

Deletes a cron job.

**Response:**

```json
{
  "ok": true,
  "data": {
    "message": "cron job deleted"
  }
}
```

---

### 5.6 Heartbeat

Heartbeat runs periodic prompts in a session (e.g. "check inbox") to keep the agent aware of the environment.

#### GET /api/v1/projects/{name}/heartbeat

Returns heartbeat status.

**Response:**

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

Pauses heartbeat.

**Response:**

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

Resumes heartbeat.

**Response:**

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

Triggers heartbeat immediately (one-shot).

**Response:**

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

Sets the heartbeat interval.

**Request body:**

```json
{
  "minutes": 15
}
```

**Response:**

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

Lists connected bridge adapters (external platforms via WebSocket).

**Response:**

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

## 6. Error Handling Conventions

### 6.1 Standard Error Response

All errors use the same envelope:

```json
{
  "ok": false,
  "error": "human-readable message"
}
```

### 6.2 Common Errors

| HTTP | Error message example                          | Cause                          |
|------|-------------------------------------------------|--------------------------------|
| 400  | `"project is required (multiple projects)"`     | Missing required parameter     |
| 400  | `"either prompt or exec is required"`           | Invalid cron job body          |
| 401  | `"unauthorized: missing or invalid token"`      | Auth failure                   |
| 404  | `"project not found: xyz"`                      | Unknown project/session/cron   |
| 404  | `"session not found"`                           | Unknown session ID             |
| 405  | `"method not allowed"`                          | Wrong HTTP method              |
| 500  | `"internal error"`                              | Unexpected server error        |

### 6.3 Validation Errors

When request body validation fails:

```json
{
  "ok": false,
  "error": "invalid request: session_key is required"
}
```

---

## 7. Session Key Format

The `session_key` is a composite identifier used to route messages to the correct platform and chat:

```
<platform>:<chat_id>:<user_id>
```

Examples:

- `telegram:123456789:123456789` — Telegram user 123456789 in chat 123456789
- `feishu:ou_xxx:chat_yyy` — Feishu user in chat
- `slack:C01234:U05678` — Slack channel and user
- `discord:123456789:987654321` — Discord guild and user

For multi-workspace mode, the format may include a workspace prefix:

```
<workspace>:<platform>:<chat_id>:<user_id>
```

---

## 8. CORS

When the Management API is used by web dashboards, CORS headers should be configurable. A suggested config extension:

```toml
[management]
enabled = true
port = 9820
token = "mgmt-secret"
cors_origins = ["http://localhost:3000", "https://dashboard.example.com"]
```

If not configured, CORS may be disabled or use a default (e.g. `*` for same-origin only).

---

## 9. Changelog

| Version   | Date       | Changes                    |
|-----------|------------|----------------------------|
| 1.0-draft | 2026-03-10 | Initial specification      |

---

## 10. References

- [Bridge Protocol](bridge-protocol.md) — WebSocket protocol for external platform adapters
- [Usage Guide](usage.md) — End-user features and slash commands
- [config.example.toml](../config.example.toml) — Configuration template
