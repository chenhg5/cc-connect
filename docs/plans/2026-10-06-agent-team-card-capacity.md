# 执行卡容量与飞书限制对齐

2026-10-06。修正执行过程中提前生成大量 TXT 附件的问题。

## 行为

- 取消单个执行块 12 KB、整页 18 KB 和顶层 80 个元素的内部阈值。
- 按完整卡片 JSON 和兼容消息请求的实际序列化字节数检查 30,000 字节上限，包含标题、中文 UTF-8、JSON 转义及交互控件；按嵌套组件累计检查 200 个元素上限。
- 整页装不下时，将完整执行块按顺序放入同话题的续卡。仅单个执行块独占一张卡仍超限时，保留完整内容为 TXT 附件。
- 运行中的执行块保留独立卡片空间；多个已完成执行块可共享卡片。后端修订较长结果时优先续卡，不将整页转成附件。
- 提问或审批控件放不下时单独续卡，不挤占可展示的执行记录。

## 官方依据

- [CardKit 更新卡片](https://open.feishu.cn/document/cardkit-v1/card/update)：30 KB、200 个组件／元素。
- [回复消息](https://open.feishu.cn/document/server-docs/im-v1/message/reply)：卡片消息请求不超过 30 KB。
- [卡片 JSON 2.0 结构](https://open.feishu.cn/document/uAjLw4CM/ukzMukzMukzM/feishu-cards/card-json-v2-structure)：组件计数包含嵌套的 plain_text 等元素。

30 KB 使用十进制 30,000 字节；以实际序列化请求计量，正文可用容量会少于 30,000 字节。CardKit data 字符串字段的较大长度上限不作为卡片容量上限。

## 验证

原实现下新增回归失败，修复后通过。覆盖 26 KB 单块不转附件、修订结果按完整块续卡、嵌套组件计数、中文与转义、30,000／30,001 字节边界、200／201 元素边界、审批续卡及清除控件。已有超大单块附件和运行中块位置测试通过。

通过 `go test ./...`、`go test ./core -run TestCUJ -count=1`、`go test -race ./platform/feishu -run TestNative -count=1` 和 `go build ./...`。飞书交付测试使用 HTTP 边界模拟，没有向真实群发送测试附件。

测试日志：`/tmp/cc-card-limits-{regression-before,native,all,cuj,race,build}.log`。

[飞书方案](https://bytedance.larkoffice.com/docx/SFZtddQ2ronWJ8xGRg5c7vRZnQe) 已同步并回读验证。

## 激活

2026-10-06 22:26 +08，在最新执行轮次完成后更新 `cc-connect-team.service`。服务 active，4 个平台 ready、4 个 WebSocket 连接成功，启动错误 0。部署过程中配置和项目数据逐字节未变；会话数据一并备份，没有对真实群执行任务结束或暂停。

- SHA-256：`a37d799dc818a9241f221638a228baf0c1474b325fad1e1c62a6a1f8ec302116`
- 回退备份：`/home/bytedance/.cc-connect-team/backups/card-capacity-20261006-222603/`
- 部署记录：`/tmp/cc-card-limits-deployment.json`
- 健康记录：`/tmp/cc-card-limits-health.json`

真实客户端的接近容量边界卡片展示仍待用户使用验证；未为验证向群内发送大段测试内容。
