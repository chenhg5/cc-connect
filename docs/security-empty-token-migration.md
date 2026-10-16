# Webhook and Management API token migration

The webhook can trigger agent prompts and shell commands. The Management API can expose configuration and control the instance. When either endpoint is enabled, cc-connect now requires its own authentication token by default. A previously enabled endpoint with an empty token makes the process exit during startup.

| Configuration | Startup | Bind address | Authentication |
| --- | --- | --- | --- |
| Disabled | Continues | No listener | Not applicable |
| Enabled, token set, `insecure = false` (default) | Continues | All interfaces, as before | Required |
| Enabled, token empty, `insecure = false` | Exits with an error | No endpoint listener | Not applicable |
| Enabled, `insecure = true` | Continues | `127.0.0.1` only, even with a token | Required if token set; otherwise absent |

## Before upgrading

1. Check every deployment's `[webhook]` and `[management]` blocks. Each enabled endpoint needs its own token; an unset token is empty. Disabled endpoints need no change.
2. For remote access, configure a strong token for each endpoint and update webhook senders, Management API clients, reverse proxies, and health checks. Use `Authorization: Bearer <token>`. Keep tokens out of URLs and logs. Configuration supports `${ENV_VAR}` expansion.
3. For local development that truly needs no token, explicitly set `insecure = true` on that endpoint and use `http://127.0.0.1:<port>`. Do not forward this unauthenticated listener through a public proxy. `localhost` may resolve to IPv6 `::1`, which this mode does not bind.
4. Restart with the new configuration. Verify the intended endpoint is reachable, an absent or wrong token gets HTTP 401 in authenticated mode, and the correct token reaches the handler.

Remote configuration example:

```toml
[webhook]
enabled = true
port = 9111
token = "${CC_WEBHOOK_TOKEN}"

[management]
enabled = true
port = 9820
token = "${CC_MANAGEMENT_TOKEN}"
```

Local-only, unauthenticated development example:

```toml
[webhook]
enabled = true
insecure = true

[management]
enabled = true
insecure = true
```

`insecure = true` always narrows the listener to IPv4 loopback, including when a token is present. Public Go callers of `NewWebhookServer` or `NewManagementServer` must handle a nil return when the token is empty; the explicit `NewWebhookServerInsecure` and `NewManagementServerInsecure` constructors opt into local-only operation.

For rollback, prefer adding tokens or disabling the endpoints. Rolling back to an older binary can reopen unauthenticated remote access.

[中文说明](security-empty-token-migration.zh-CN.md) · [Management API reference](management-api.md)
