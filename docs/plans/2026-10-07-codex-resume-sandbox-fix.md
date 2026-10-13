# Codex 恢复会话的网络沙箱修复

2026-10-07。用户报告管理者无法连接团队 Unix socket，飞书凭据刷新也因 DNS 禁止而失败。

## 定位

管理者现有配置包含 `sandbox_network_access=true`，运行中的 app-server 命令行也带该参数；但报错轮次的原生 `turn_context` 记录仍是 `workspace-write`、`approval_policy=never`、`network_access=false`。因此只验证进程参数或服务在线不能证明执行沙箱已允许联网。

适配器原先仅将网络选项放到 CLI；thread/start、thread/resume 只单独传递可写目录，未保留返回的完整沙箱策略，turn/start 也未携带该策略。恢复历史线程时可能继续沿用历史轮次的限制。

## 修复

- 在 thread/start 和 thread/resume 的 config 中同时传递显式设置的网络选项；未配置时继续继承服务端默认值。
- 保存服务端响应中的完整 sandbox，每次 turn/start 原样传入 sandboxPolicy，保留可写目录、只读范围、网络与临时目录限制。
- 服务端返回更严格策略时照样保留，不由适配器放宽。旧服务端未返回 sandbox 时保持兼容，不编造权限策略。
- 不修改运行配置、授权模式或角色权限，不将工作区沙箱改为全权限模式。

协议依据：[OpenAI App Server 文档](https://learn.chatgpt.com/docs/app-server)的 turn/start 配置覆盖与 sandboxPolicy；工作区网络访问由 networkAccess 布尔字段表达。

## 验证

回归测试覆盖新建和恢复线程、显式允许/禁止网络、继承设置、只读模式、服务端收紧限制、旧服务端省略字段，以及连续两轮完整保留策略与审批模式。

真实 Codex 0.153.4 的 command/exec 在相同工作区及可写目录下验证：

| 网络策略 | DNS | 团队 Unix socket | 飞书 HTTPS |
| --- | --- | --- | --- |
| false | 临时解析失败 | Operation not permitted | 无法解析 |
| true | 正常 | HTTP 200 | HTTP 404，连接已到达服务端 |

该探测只读取 `/sessions` 并访问飞书站点，不派发任务、不读取凭据、不修改文档或设备，不调用模型执行业务任务。尚不能将探测成功表述为原业务任务已完成。

测试日志：`/tmp/cc-sandbox-policy-focused.log`、`/tmp/cc-sandbox-policy-all.log`、`/tmp/cc-sandbox-policy-race.log`、`/tmp/cc-sandbox-policy-build.log`。真实沙箱探测：`/tmp/cc-sandbox-policy-live-probe.json`。

定向回归、`go test ./...`、`go test -race ./agent/codex` 和构建均通过。

## 上线

2026-10-07 12:16 +08 已更新并重启 `cc-connect-team.service`，新 PID 2021147，4 个平台 ready、4 个 WebSocket 已连接、启动错误 0。运行配置与项目数据逐字节未变。

原管理者线程经真实 thread/resume 返回的策略已确认为 `workspaceWrite`、`networkAccess=true`、原可写目录保持不变；使用该返回策略执行只读探测，DNS 正常、团队 socket 返回 HTTP 200。探测后原会话文件哈希未变，未调用 turn/start 推进业务，也未派工或开展 Nerida 验证。

- 原线程恢复验证：`/tmp/cc-sandbox-policy-resume-probe.json`。
- 部署与健康记录：`/tmp/cc-sandbox-policy-deployment.json`、`/tmp/cc-sandbox-policy-health.json`。
- 二进制 SHA-256：`0394270880344a096110018a14f8791031a5da85dca63f2431b3d48859c146d7`。
- 回退目录：`/home/bytedance/.cc-connect-team/backups/sandbox-policy-20261007-121646/`，包含旧二进制、配置、服务定义与数据。
