# Web 控制面部署

## 制品与主机

正式 Release 包含 Linux amd64/arm64 的 `cc-connect-control`、`cc-connect-server`，以及 macOS amd64/arm64 的 `cc-connect-runtime`。`manifest.json` 和所有制品 SHA-256 由 GitHub OIDC/Sigstore provenance 签署；安装和更新均拒绝未签名制品。

Linux 主机需要 systemd、`cosign`、`jq`、`sha256sum`、`tar` 和 `openssl`。macOS Runtime 主机需要已登录的 Codex CLI/Codex App、`cosign`、`curl`、`jq` 和 `launchctl`。本项目不使用 Docker。

## 首次 bootstrap

在 Linux 用 GitHub CLI 下载同一 tag 的完整 Release（包括 bootstrap、六个二进制归档、manifest、bundle 和 1Panel 模板），然后执行：

```bash
gh release download v0.1.0 --repo shusfun/cc-connect --dir release-v0.1.0
sudo ./release-v0.1.0/bootstrap.sh --release-dir ./release-v0.1.0
```

脚本会验签并创建：

- `/opt/cc-connect/releases`：当前版本和最近两个成功版本槽；
- `/var/lib/cc-connect/control`：`control.db`、部署记录和日志；
- `/var/lib/cc-connect/app`：业务配置、`workspace_chat.db` 和附件暂存；
- `/run/cc-connect`：私有 Unix Socket。

systemd 只管理 `cc-connect-control`。control 以唯一生命周期所有者身份监管 server，后者只监听 `/run/cc-connect/server.sock`。

首次启动只监听 `127.0.0.1:9820`，bootstrap 会输出一次性设置 Token。建立 SSH 转发：

```bash
ssh -L 9820:127.0.0.1:9820 user@server
```

访问 `http://127.0.0.1:9820/setup`，用一次性 Token 创建至少 12 位的管理员密码。Token 文件在 control 首次读取后删除，Token 使用后也不能再次设置管理员。

## 1Panel/OpenResty

设置公开 HTTPS 地址后，使用 Release 中的 `openresty-1panel.conf`（仓库副本为 [OpenResty 模板](../deploy/openresty-1panel.conf)）配置现有站点反代。模板覆盖 WebSocket、50 MiB 请求体和长连接超时。control 不修改 DNS、证书或其他站点。

## Web 初始化六步

1. loopback 设置页创建管理员；
2. 保存无路径、无 query 的公开 HTTPS URL，并复制 1Panel/OpenResty 配置；
3. 生成十分钟有效的配对码，运行 control 内置的 Runtime 安装器；
4. 等待 Runtime 校验本机 Codex CLI/认证、Codex App 状态和至少一个有效项目目录；
5. 配置 Web 工作区聊天，可选填写企业微信 WebSocket Bot ID、Secret 和允许用户；
6. control 原子生成私有 server 配置并启动业务进程。

任一步失败都会显示真实错误。业务配置写入或 server 启动失败时回滚配置，不产生“页面成功但服务未启动”的状态。

## Runtime 配对

在初始化向导创建配对码，然后在 macOS 执行页面提供的命令，等价于：

```bash
curl -fsSL https://cc.example.com/runtime/v1/install.sh -o cc-connect-runtime-install.sh
sh cc-connect-runtime-install.sh \
  --server https://cc.example.com \
  --code <pairing-code> \
  --tag v0.1.0
```

Runtime 私钥只保存在 macOS Keychain。launchd 常驻进程通过出站 TLS/WebSocket 连接 control，无需 VPN、内网穿透或公网回调。多台 Mac 可在运维中心分别配对、重命名、撤销并查看持久连接日志；项目和 Codex thread 始终以对应设备本地状态为权威。Runtime 会在本地 Codex 项目 catalog 变化后更新服务器的只读目录快照，不上传路径或对话正文。

## 更新与回滚

更新和回滚只从 Web 运维中心手动发起。更新流程锁定 tag、commit 和 manifest，检查签名、磁盘、配置、活动 Turn/交互/realtime，再暂存在线 Runtime。离线设备标记待更新，不阻断服务器切换。

control 在切换前备份 `control.db` 并写入 activation 记录。提交点前取消只终止本次运行，不停止业务进程；从停止 server 起进入不可取消阶段。候选 control 先开放 Runtime 入口，等待本轮在线设备以新协议代际重连，再发送 `runtime/update/confirm`。Runtime 在激活前写入 `pending-activation.json`，未在时限内收到确认会自行恢复上一槽。候选 control/server 健康、数据库提交和 Runtime 确认全部成功后才完成部署；任一步失败时，systemd `ExecStopPost` helper 恢复上一 control/server 槽、`control.db` 和已切换 Runtime。更新、回滚和重启共用一个执行槽。

Web 只允许回滚到上一成功版本。目标版本不能读取当前 control schema 时明确拒绝。界面不提供停止、卸载、清空配置或删除持久目录操作。

## 恢复与诊断

服务状态与日志在 Web 运维中心查看。部署日志使用持久 NDJSON 游标，刷新后会从最后序列继续回放。

SSH 诊断只读取状态：

```bash
systemctl status cc-connect-control.service
journalctl -u cc-connect-control.service -n 200 --no-pager
```

不要手工改写 `current` symlink 或删除 activation/backup。若自动恢复失败，保留 `/var/lib/cc-connect/control/activation.json`、对应 backup 和 journal 后再处理。
