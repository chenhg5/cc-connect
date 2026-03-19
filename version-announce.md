v1.2.2-beta.3 发布

主要更新：
1. 多用户模式 - 支持 per-user 限流、ACL 权限控制和审计日志 (#150)
2. 图片发送 - 6 大平台统一图片发送支持 (#222)
3. MiniMax M2.7 - 默认模型升级至 M2.7 (#211)
4. 新增命令 - /whoami、/btw、/dir
5. Workspace 持久化 - 多 workspace 模式下 session 自动保存
6. 中断支持 - 可发送 Ctrl+C 中断正在运行的 agent (#198)
7. 消息队列 - agent 忙碌时自动排队，不丢消息
8. QQ Bot Markdown 支持 (#172)
9. CORS 支持 (#196)

其他优化：Cron 任务可静音、Relay 超时返回部分结果、完整的国际化翻译等。

感谢贡献者：@sean2077 @0xsegfaulted @octo-patch @windli2018 @jenvan @huangdijia @kevinWangSheng @xxb @chenhg5 @Deeka Wong @Shawn 等

下载：
GitHub: https://github.com/chenhg5/cc-connect/releases/tag/v1.2.2-beta.3
Gitee: https://gitee.com/cg33/cc-connect/releases/tag/v1.2.2-beta.3

npm 安装：npm install -g cc-connect@beta

如有问题请反馈。