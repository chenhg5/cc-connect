# Webhook 与管理 API 空 token 升级说明

Webhook 可以触发 Agent 提示词和 shell 命令；管理 API 可以读取配置并控制运行中的实例。启用任一入口后，cc-connect 现在默认要求该入口配置独立的认证 token。旧部署若启用了入口却留空 token，升级后进程会在启动时退出。

| 配置 | 启动结果 | 监听地址 | 认证 |
| --- | --- | --- | --- |
| 入口未启用 | 正常 | 不监听 | 不适用 |
| 已启用、有 token、`insecure = false`（默认） | 正常 | 与原来一样监听所有接口 | 必须提供 token |
| 已启用、空 token、`insecure = false` | 报错退出 | 不启动该入口 | 不适用 |
| 已启用、`insecure = true` | 正常 | 仅 `127.0.0.1`，即使同时配置 token | 有 token 时仍需认证；空 token 时不认证 |

## 升级前操作

1. 检查每个部署的 `[webhook]` 与 `[management]` 配置。每个已启用的入口都需要各自的 token；未配置等同空 token。未启用的入口无需调整。
2. 如需远程访问，为每个入口设置高强度 token，同步更新 webhook 调用方、管理客户端、反向代理和健康检查。两者均可使用 `Authorization: Bearer <token>`。不要将 token 放进 URL 或日志。配置支持 `${ENV_VAR}` 环境变量展开。
3. 仅本机开发确需无认证时，在相应入口显式设置 `insecure = true`，改用 `http://127.0.0.1:<端口>`。不要通过公开代理转发该无认证入口。`localhost` 可能解析为 IPv6 `::1`，而此模式只绑定 IPv4 loopback。
4. 用新配置重启，验证目标入口可达；有 token 的模式下，缺失或错误 token 返回 HTTP 401，正确 token 可进入处理器。

远程访问配置示例：

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

仅本机无认证开发配置示例：

```toml
[webhook]
enabled = true
insecure = true

[management]
enabled = true
insecure = true
```

`insecure = true` 始终把监听地址限制为 IPv4 loopback，包括同时配置 token 的情况。直接使用 Go API 的调用方要处理 `NewWebhookServer` 或 `NewManagementServer` 在空 token 时返回 nil；只有显式使用 `NewWebhookServerInsecure` 或 `NewManagementServerInsecure` 才启用仅本机模式。

需要回滚时，优先补 token 或暂时关闭入口。回退旧二进制可能重新开放远程无认证访问。

[English guide](security-empty-token-migration.md) · [管理 API 参考](management-api.md)
