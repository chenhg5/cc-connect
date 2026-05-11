# Deployment

## Operations

| 项目 | 详情 |
|------|------|
| 进程管理 | systemd |
| 服务文件 | `/etc/systemd/system/cc-connect.service` |
| 工作目录 | `/root/.cc-connect` |
| 源码目录 | `/data/CodeHouse/cc-connect` |
| 监听端口 | 无固定业务监听端口；作为消息平台代理/长连接桥运行 |
| 启动 | `sudo systemctl start cc-connect.service` |
| 停止 | `sudo systemctl stop cc-connect.service` |
| 重启 | `sudo systemctl restart cc-connect.service` |
| 状态 | `sudo systemctl status cc-connect.service --no-pager` |
| 日志 | `journalctl -u cc-connect.service -f` 或 `/root/.cc-connect/logs/cc-connect.log` |
| 健康检查 | `sudo systemctl is-active cc-connect.service`；依赖 `codex-router.service` |
| 自动部署 | 无；本机手动构建并替换 `/usr/local/lib/node_modules/cc-connect/bin/cc-connect` |
| 更新后操作 | `go build -tags no_web -o cc-connect ./cmd/cc-connect && sudo install -m 0755 cc-connect /usr/local/lib/node_modules/cc-connect/bin/cc-connect && sudo systemctl restart cc-connect.service` |

### 环境变量

| 变量 | 用途 | 必需 |
|------|------|------|
| `CC_LOG_FILE` | 文件日志路径 | 是 |
| `CC_LOG_MAX_SIZE` | 日志轮转大小 | 是 |
| `HOME` | 运行用户主目录 | 是 |
| `CODEX_HOME` | Codex 配置目录 | 是 |
| `http_proxy` / `https_proxy` | 本机代理 | 否 |
