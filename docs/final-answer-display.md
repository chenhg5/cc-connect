# Final-answer display mode / 仅显示最终回答

Set `mode = "final"` under `[display]`, or under `[projects.display]` for a project override. This mode sends each foreground or background turn's answer as plain messages, without streaming cards, previews, thinking, tool messages, footers or system notices. Errors and permission prompts are still sent. Typing indicators and done reactions remain. The `/quiet` command does not change this mode.

If a turn contains several segments, the last non-empty segment is used when the result does not supply an answer. A trailing tool call or thinking block alone does not erase the answer. Notices such as auto-compress, idle reset and model switching are not sent. Unsafe resume and automatic denial of a background tool call without an active turn are logged at Warn.

在 `[display]` 中设置 `mode = "final"`，也可以在 `[projects.display]` 中单独覆盖项目设置。前台与后台轮次只发最终回答，不发流式卡片、预览、思考、工具消息、页脚或系统提示。错误和权限请求照常发送，输入中提示及完成表情保留。`/quiet` 不会切换此模式。

如果结果没有正文，使用最后一段非空文本。末尾的工具调用或思考不会抹掉已有回答。自动压缩、空闲重置或模型切换通知不发到聊天；无法安全恢复会话，以及无活动轮次时自动拒绝的后台工具调用，均记录为 Warn 日志。
