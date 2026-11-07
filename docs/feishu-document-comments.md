# Feishu document comment mentions

Enable this optional entry point to mention the bot beside a document passage or
spreadsheet cell. The configured agent receives the comment, its quoted content,
recent replies and a bounded resource preview. Its final answer is posted by the
same Feishu application, without forwarding the discussion to an IM chat.

## Configuration

```toml
[[projects.platforms]]
type = "feishu"

[projects.platforms.options]
app_id = "${FEISHU_APP_ID}"
app_secret = "${FEISHU_APP_SECRET}"
enable_document_comments = true
document_context_max_chars = 12000
allow_from = "*"
```

`enable_document_comments` defaults to `false`. The optional content budget
defaults to 12,000 Unicode code points and accepts values from 1 to 100,000.
Quoted text and earlier replies have separate fixed bounds of 2,000 and 6,000
code points respectively; at most 20 earlier replies are included.

Subscribe to `drive.notice.comment_add_v1` in the same application's event
configuration, publish the application changes, and grant the application
comment read/write and resource read permissions for the resources it needs.
Application scopes and access to each individual resource are separate checks.
Do not use another application's token to reply as this bot.

The entry point works through the existing WebSocket or webhook dispatcher.
It uses the pinned official SDK's custom event support and HTTP client; no SDK
upgrade, separate listener, Feishu CLI or external skill is required.

When several projects share an application, enable document comments only in
the project that should handle them. `allow_from` applies to the real comment
author. IM chat filters such as `allow_chat` and `group_only` cannot route a
document comment because the event has no IM chat ID.

## Context and reply behavior

- Only explicit mentions of the current bot are processed. Resolved comments,
  the bot's own replies, unauthorized senders and duplicate triggering replies
  are ignored. A new mention in the same thread remains a new turn.
- Sessions are isolated by application, resource type, resource token and comment
  ID: `<platform>:doc:<app_id>:<file_type>:<file_token>:<comment_id>`. Workspace
  bindings also preserve the comment thread's scope.
- Docx content is read within the configured prompt budget. Sheets preview the
  header and rows near the quoted cell. If sheet positioning metadata is absent,
  a single-sheet workbook can be resolved automatically; a multi-sheet workbook
  exposes candidates instead of guessing the first sheet.
- Other resource types are not rejected by an adapter allowlist. Their comments,
  quotes and resource metadata are supplied; further content reads depend on
  tools available to the configured agent. Resource preview errors are included
  as context instead of dropping an otherwise readable comment.
- Inline answers are written to the original comment thread. Feishu does not
  permit threaded replies to whole-document comments; those answers are new
  whole-document comments on the same resource, prefixed with the original
  comment ID. They are associated answers, not inline replies.
- This entry point exposes final text output only. IM cards, tool output and
  routine intermediate agent text are not posted as document replies. Normal
  IM display settings are preserved. Progress receipts and business task-state
  integrations are outside this feature.

All API calls use the existing application client, including its configured
Feishu/Lark base URL and tenant-token handling. Unicode answers are split into
bounded replies. Comment state is checked again before sending, and read/write
errors are logged. A readable comment may receive an error reply; if the comment
channel is also inaccessible, the error is logged without silently switching to
a private-message channel.

## Validation

The adapter tests cover mention filtering, author checks, deduplication,
pagination, isolated session recovery, resource types, preview failures,
Unicode reply splitting and whole-document reply behavior. SDK contract tests
exercise WebSocket payloads and signed encrypted webhooks. A core user-journey
test covers a comment, its follow-up and an ordinary IM conversation to ensure
that final-only output does not change IM progress rendering.

For a real deployment, authorize a dedicated docx or Sheet, mention the bot in
a comment and read the resulting reply back through the comment API. Confirm
the bot identity, resource/parent comment, answer content and follow-up session.
Configure the agent's required credentials and tools independently.
