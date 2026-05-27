---
name: cc-connect-fork-wechat-post
description: cc-connect fork 版微信朋友圈文案
metadata:
  type: project
---

# cc-connect fork 版微信朋友圈文案

2026-05-22 发布

## 文案（完整版）

无需官方订阅，任意国产模型 API，手机远程操控本地 Claude Code！🚀

简单说：你在手机聊天框里发消息，本地 AI Agent 就开始干活，结果直接在聊天里看。代码审查、资料研究、数据分析、自动化任务……AI 能干的事，手机上都能指挥。

我的增强版相比原版增加了：

🔹 `/fork` 会话分叉——聊到一半想换个方向？从当前对话某个轮次开一条分支，原对话不受影响。飞书/Slack/Telegram 直接弹卡片选轮次，还能自动命名。

🔹 `/rollback` 会话回撤——Agent 走偏了？一键删掉最近几轮对话，上下文截断，从那个点重新开始。同样支持卡片选择。

🔹 cc-connect 与 VSCode Agent 插件 session 命名同步——在飞书 `/name` 改了会话名，VSCode 侧立即同步，不再出现两边名字不一致的问题。

安装（需 Go 1.22+）：

```bash
git clone https://github.com/zhangyanbo2007/cc-connect.git
cd cc-connect && make build
sudo cp cc-connect /usr/local/bin/
cc-connect
```

支持 10+ Agent（Claude Code、Codex、Cursor、Gemini CLI……），12+ 聊天平台（飞书、钉钉、Telegram、Slack、Discord、企业微信、微博、微信个人号、QQ……），大部分不需要公网 IP。

想试 fork/rollback 功能？看这里：
https://github.com/zhangyanbo2007/cc-connect

## 精简版（适合朋友圈直接发）

无需官方订阅，任意国产模型API, 手机远程操控本地 Claude Code！📱➡️💻

cc-connect 把 AI Agent 桥接到飞书/微信/钉钉/Telegram 等聊天工具，手机发消息=电脑上 AI 开始干活。

我的增强版相比原版增加了：
✅ `/fork` 分叉对话——换个方向不影响原对话
✅ `/rollback` 回撤对话——走偏了就撤回来重来
✅ cc-connect与VSCode-Agent插件session命名同步

安装：git clone → make build → cc-connect
详见：github.com/zhangyanbo2007/cc-connect

10+ Agent × 12+ 聊天平台，大部分无需公网IP 🌐