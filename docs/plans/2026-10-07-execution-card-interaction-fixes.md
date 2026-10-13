# 执行卡交互问题修复

2026-10-07。用户反馈命令行间距过大、更新时不能打开详情、私聊不能定位审批原卡，以及有效审批被提示失效。

## 已修复

- 执行卡正文纵向间距设为 4px，命令点击区域去掉上下内边距。说明与命令仍在同一卡连续展示，未增加按钮、命令编号或分卡标题。
- 卡片更新持锁期间，详情和审批点击等待当前写入完成，不再被 TryLock 直接拒绝。原生回调在 400ms 内先确认接收，后续更新在后台完成；保留消息归属、阅读版本、请求 token 和处理人校验。共享连接先过滤非归属事件，避免确认其他会话的点击。
- 管理者与成员应用下的 open_id 不同。审批前通过飞书通讯录接口，以 union_id 验证并转换到成员应用下的处理人身份；管理者私聊仍使用管理者应用下的身份。无法验证身份时拒绝执行，不以姓名或点击者自报身份授权。
- 审批处理失败不再统一误报为“分页数据失效”。身份转换失败且尚无卡片时也会结束等待，避免空指针崩溃。

## 定位证据

用户实际审批点击发生在卡片展示后的 8–66 秒内，早于请求结束，确非到期。管理者保存的 requester 与成员收到的 callback operator open_id 不同，而同一用户的 union_id 一致。真实只读 API 探测完成身份转换，结果与实际回调身份一致。

详情与审批回归通过阻塞实际出站卡片更新来复现；修复前点击分别报“更新中”和“分页失效”，修复后及时确认回调并完成详情显示或审批转交。

## 尚未修复：私聊精确定位

实际通知中的群 ID 和 message_position 与原审批卡相符。但当前链接采用 `/client/chat/open?openChatId=...&position=...`；官方 AppLink 文档只保证打开聊天，没有对 position 定位的公开保证。不能把消息发送成功或审批身份修复当作跳转已修复。

已向用户询问实际点击是进入正确群但未定位、链接错误，还是打开其他消息，尚待回复。此版本保留原链接行为；精确定位仍需客户端复现与修复，不能纳入已完成验收。

参考：[打开聊天官方文档](https://open.feishu.cn/document/common-capabilities/applink-protocol/supported-protocol/open-a-chat-page)、[官方 CLI 消息链接转换实现](https://github.com/larksuite/cli/blob/main/shortcuts/im/convert_lib/content_convert.go)。

## 验证与上线

- `go test ./...`、显式 `go test ./core -run TestCUJ -count=1`、相关 core/feishu race 回归及 `go build ./...` 均通过。
- 回归覆盖更新期间打开详情与审批、跨应用发起者身份、无权用户与过期 token 拒绝、共享连接过滤、无卡片时的失败处理。
- 等当前业务任务和执行卡全部结束后，于 13:04 +08 更新并重启 `cc-connect-team.service`。PID 2110289，4 个平台 ready，4 个 WebSocket 已连接，启动错误 0，控制 socket HTTP 200。
- 配置和项目数据与备份逐字节相同；未修改权限或代替用户审批。旧审批不会因更新重新变为有效，需新的实际请求。
- 新二进制 SHA-256：`13011563ebeacc8d96c76c7a96776e5f91dddc68e1594846951d27f693ced918`。
- 回退目录：`/home/bytedance/.cc-connect-team/backups/card-interaction-20261007-130405/`，含旧二进制、配置、服务定义及停机数据快照。
- 测试日志：`/tmp/cc-card-bugs-all-final.log`、`/tmp/cc-card-bugs-cuj-final.log`、`/tmp/cc-card-bugs-race-final.log`、`/tmp/cc-card-bugs-build-final.log`；部署和健康记录：`/tmp/cc-card-bugs-deployment.json`、`/tmp/cc-card-bugs-health.json`。

飞书桌面和移动客户端的真实显示、点击体验仍需验收；精确定位问题仍开放。
