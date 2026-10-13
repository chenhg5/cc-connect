# 公开输出实时显示与适配器一致性修复

2026-10-07。针对“模型输出慢”和 traex / Codex 执行卡不一致的反馈，修复桥接层的输出等待与呈现差异。模型生成首段公开文字之前的计算、排队时间不属于本次修复的显示延迟。

## 修复

- Codex app-server 握手不再关闭 `item/agentMessage/delta`。公开 commentary 按消息 ID 更新同一说明段，final_answer 流式显示且以完成事件中的正文作为最终交付；工具开始和回合结束不再成为显示公开文字的前置条件。
- 兼容 phase 延后出现和没有 delta 的完整消息。turn/start 响应和通知先后顺序不再清空已收到的文字，已结束的回合不会被迟到响应重新激活。事件队列满时等待消费或取消，避免静默丢字。
- Codex 命令结果增量实时累积，完成事件有完整结果时替换；完成事件省略结果时保留已有输出。traex 的 ACP 更新同样保留省略字段，并支持初始报告已完成的调用、rawOutput 和明确的空结果。
- 统一执行卡的工具语义名称：运行命令显示 `command_execution`，读文件显示 `read_file`，修改显示 `file_change`。命令从 command/cmd 字段提取，避免把整条命令当成加粗工具名称，或把 shell 命令显示成一段传输 JSON。
- 公开文字和命令保留在原执行卡，最终结果单独交付；不增加命令编号或执行记录编号。内部 reasoning / ACP agent_thought_chunk 不进入公开执行卡。
- 卡片首次正文到达时立即更新。连续增量合并到约 500ms 的刷新间隔，并补上末段定时刷新；即使模型随后暂停，也会发布最新文字。内存中的详情数据即时更新，磁盘和飞书写入合并到刷新时进行。阅读快照、输入草稿和原审批区域仍受保护，结束时取消延后刷新。

## 验证

修复前复现了四项回归：公开文字必须等完成事件、两种命令事件显示不同、只更新完成状态会清空输出、最后一段文字没有后续事件就不显示。修复后通过。

- 协议测试覆盖公开文字即时到达、完整消息补齐、去重、最终正文、握手启用 delta、回合开始竞态、过期增量、队列背压与取消、命令增量和省略结果。
- ACP 测试覆盖省略字段、rawOutput、初始完整工具结果、标准思考事件的隔离。
- `TestCUJ_PublicOutputAdaptersShareOneExecutionPresentation` 比较两条代表性事件序列，验证相同说明、命令、完整结果和独立最终交付。
- 原详情阅读、并发更新/点击、补充草稿、审批和容量边界测试继续通过。新增测试验证模型暂停时仍显示尾段，以及结束后不再触发延后写入。
- 全量 `go test ./...`、显式 `go test ./core -run TestCUJ`、相关 race 检查、`go build ./...` 和运行二进制构建通过。记录位于 `/tmp/cc-stream-{all-final,cuj,race,build,target-tests}.log`。

协议依据：[Codex app-server](https://learn.chatgpt.com/docs/app-server)、[ACP tool calls](https://agentclientprotocol.com/protocol/v1/tool-calls)。测试使用协议样本和本地模拟服务；不代表两家模型推理耗时相同，也不代替飞书桌面/手机实际体验验收。

设计参考仍为 [v0.14.5 交互稿](https://bytedance.larkoffice.com/page/Jnjbmp8l2d5nxGaEmKgcc3vnnSg)。本次没有重新发布原型或操作真实审批。

## 部署

2026-10-07T14:48:26.625490+08:00 在无运行/排队/待决策任务、无活动执行卡且团队服务无 agent 子进程时完成重启。服务 PID 2181505；4 个平台 ready，4 个 WebSocket 连接，启动错误 0，控制 socket HTTP 200。配置和项目数据与备份一致。

安装二进制 SHA-256：`ad15155b13e0df7ac68c6da09d91d3af3d64ca37369152788355e12fe40ae12e`。备份：`/home/bytedance/.cc-connect-team/backups/output-stream-20261007-144823`。部署与健康记录：`/tmp/cc-stream-deployment.json`、`/tmp/cc-stream-health.json`。

新的执行回合使用修复；历史卡片不会批量改写。私聊通知精确定位原审批卡仍是已有待验收问题，本次没有宣称解决该跳转。

飞书方案已同步并回读确认至 revision 67：[方案文档](https://bytedance.larkoffice.com/docx/SFZtddQ2ronWJ8xGRg5c7vRZnQe)。保留原型链接与未完成的客户端验收边界。
