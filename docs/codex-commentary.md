# Codex progress messages / Codex 进度消息

Codex sends user-facing progress as standalone messages by default, even in
quiet mode or when thinking is hidden. You can disable them in the web admin's
global display settings or in the global or per-project TOML configuration:

```toml
[display]
commentary_messages = false
```

For a project override, use the same key under its `[projects.display]`.
Disabling commentary does not suppress the final answer. Explicit
`partial_answer` and `final_answer` remain in the answer channel. When older
Codex versions omit phase data, text before a tool or approval can be reported
as progress. A turn can produce several updates; progress that precedes an
approval appears before the approval prompt.

In Feishu, tool and thinking logs remain in the progress card. If the card
cannot be created, those logs do not fall back to individual messages; the
final answer and any enabled standalone Codex commentary are separate.
Manual compression can show commentary; automatic compression is silent.
The final answer in history and relay responses does not include commentary.

默认开启独立进度消息，quiet 模式和隐藏思考不影响它。可在管理界面的全局显示设置关闭，
也可以在全局 `[display]` 或对应项目的 `[projects.display]` 设置
`commentary_messages = false`。关闭不影响最终答案。旧版 Codex 没有阶段标记时，
工具调用或审批前的一段文本可能作为进度发送，每轮可能有多条更新。
飞书的思考与工具日志仍留在进度卡；建卡失败不会改为逐条发送这些日志。
手动压缩可以发进度，自动压缩保持静默。最终历史正文和机器人间 relay 不包含进度。
