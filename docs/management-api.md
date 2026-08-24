# cc-connect control API

`cc-connect-control` is the only public Web endpoint and listens on `127.0.0.1:9820` by default. 1Panel/OpenResty terminates production HTTPS. The business process listens only on `/run/cc-connect/server.sock`, while macOS Runtime devices connect outbound over TLS/WebSocket.

At `v0.1.0`, the resources below are the sole current contract. Management tokens, query-string authentication, `cc-connect web`, and a public business-process TCP listener are removed.

## Authentication

- `GET|POST /api/v1/auth/setup`
- `POST /api/v1/auth/login`
- `POST /api/v1/auth/logout`
- `GET /api/v1/auth/session`

Passwords use Argon2id. Session tokens are stored only as digests. Cookies are `Secure`, `HttpOnly`, and `SameSite=Strict`; unsafe methods require both a same-origin `Origin` and `X-CSRF-Token`.

## Devices

- `GET /api/v1/devices`
- `POST /api/v1/devices/pairing-code`
- `PATCH|DELETE /api/v1/devices/{id}`
- `GET /api/v1/devices/{id}/logs?after={id}`
- `GET /api/v1/devices/{id}/logs/stream?after={id}`
- `POST /runtime/v1/pair`
- `GET /runtime/v1/install.sh`
- `GET|POST /runtime/v1/connect`

Pairing codes expire after ten minutes and are one-time. Subsequent Runtime connections use an Ed25519 private key stored in macOS Keychain. A contract fingerprint mismatch returns only `update_required`. Rename, revoke, connect, and disconnect events are persisted in `control.db`; device log streams replay NDJSON by monotonic `id`, and revoke immediately closes an active connection. The installer is embedded in the signed control binary rather than fetched from an unlocked branch.

## Deployments and service

- `GET|PUT /api/v1/deploy/dashboard`
- `GET|POST /api/v1/deploy/preflight-operations`
- `GET|POST /api/v1/deploy/runs`
- `GET /api/v1/deploy/runs/{id}/log?after={sequence}`
- `GET /api/v1/deploy/runs/{id}/stream?after={sequence}`
- `POST /api/v1/deploy/runs/{id}/cancel`
- `GET /api/v1/service/status`
- `POST /api/v1/service/restart`
- `GET /api/v1/service/logs?after={cursor}`
- `GET /api/v1/service/logs/stream?after={cursor}`

Updates and rollbacks are manual Web actions. control pins and verifies the `shusfun/cc-connect` repository, Release workflow, tag, Sigstore OIDC identity, manifest, and artifact SHA-256. There is no unsigned fallback. Update, rollback, and restart share one machine execution slot.

Deployment streams are cursor-replayable NDJSON backed by `control.db`; reconnect with the last confirmed `sequence`.

## Workspace chat

Workspace resources remain under `/api/v1/chat/*`. control authenticates the browser, then proxies over the private server Unix socket. Browsers submit only a global `workspaceRef`, never a device path, cwd, or local attachment path. See [Unified workspace chat](workspace-chat.md).

Normal endpoints return `{"ok":true,"data":...}`. Failures use a non-2xx status and `{"ok":false,"error":"..."}`. NDJSON streams are not wrapped.
