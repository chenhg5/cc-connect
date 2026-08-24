# cc-connect 控制面 API

`cc-connect-control` 是唯一公开 Web 入口，默认只监听 `127.0.0.1:9820`。生产环境由 1Panel/OpenResty 提供 HTTPS，业务进程只监听 `/run/cc-connect/server.sock`，Runtime 通过出站 TLS/WebSocket 连接 control。

`GET /api/v1/deploy/dashboard` 的 `deployment` 是部署能力权威来源，包含 `owner`、`available`、`reason`、`detail`、`update`、`rollback` 和 `restart`。systemd 与 container 两种所有者都支持 Web 签名更新和回滚；容器宿主执行器离线时返回 `reason=container_host_unavailable` 并禁用版本操作，server 重启仍由 control 负责。

项目处于 `v0.1.0`，以下是唯一现行契约。旧 management token、query token、CORS 登录、`cc-connect web` 和业务进程公开 TCP 不再支持。

## 认证

- `GET|POST /api/v1/auth/setup`：读取初始化状态或使用 bootstrap 输出的一次性 Token 设置管理员密码。
- `POST /api/v1/auth/login`：管理员密码登录。
- `POST /api/v1/auth/logout`：注销当前会话。
- `GET /api/v1/auth/session`：恢复 Cookie 会话并取得 CSRF Token。

密码使用 Argon2id。会话 Token 只保存摘要；Cookie 为 `Secure`、`HttpOnly`、`SameSite=Strict`。所有非安全方法必须同时通过同源 `Origin` 和 `X-CSRF-Token` 校验。

## 设备

- `GET /api/v1/devices`
- `POST /api/v1/devices/pairing-code`
- `PATCH|DELETE /api/v1/devices/{id}`
- `GET /api/v1/devices/{id}/logs?after={id}`
- `GET /api/v1/devices/{id}/logs/stream?after={id}`
- `POST /runtime/v1/pair`
- `GET /runtime/v1/install.sh`
- `GET|POST /runtime/v1/connect`

配对码十分钟有效且只可消费一次。Runtime 后续连接使用 macOS Keychain 中的 Ed25519 私钥完成 challenge-response；协议指纹不匹配只返回 `update_required`。重命名、撤销和连接/断开事件持久写入 `control.db`，设备日志流按自增 `id` 回放 NDJSON；撤销会立即关闭对应活动连接。安装脚本由当前已签名 control 二进制内嵌提供，不从未锁定分支读取。

## 部署与服务

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

更新和回滚只能由管理员在 Web 手动发起。原生安装由 control 直接验签；容器安装调用固定目标的宿主执行器，后者独立验证同一 Release 身份和 `ghcr.io/shusfun/cc-connect` 镜像签名。control 不持有 Docker Socket，宿主执行器不接受任意仓库、Compose 项目、service、路径或命令；不存在未签名 fallback。更新、回滚和重启共用一个机器级执行槽。

部署日志流是带持久游标的 NDJSON。重连时客户端提交最后确认的 `sequence`，服务端从 `control.db` 回放，运行结束后关闭流。

## 工作区聊天

工作区 REST/WS 仍位于 `/api/v1/chat/*`，由 control 完成 Cookie/CSRF/Origin 认证后通过私有 Unix Socket 转发给 `cc-connect-server`。浏览器只提交全局 `workspaceRef`，不提交设备路径、cwd 或本地附件路径。详细契约见[统一工作区对话](workspace-chat.zh-CN.md)。

普通接口使用 `{"ok":true,"data":...}`。失败使用非 2xx 状态和 `{"ok":false,"error":"..."}`。日志流使用 NDJSON，不包装为普通响应 envelope。
