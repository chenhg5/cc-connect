# Feishu: reply without a mention in one-person groups

[Feishu setup guide](feishu.md) · [简体中文](feishu-solo-chat.md) · [Русский](feishu-solo-chat.ru.md)

Enable this option under the matching Feishu platform's `[projects.platforms.options]`:

```toml
solo_chat_reply_all = true  # default: false
```

The option applies only when Feishu reports exactly one person and one bot in the group. A message without mentions can trigger the bot; a message mentioning only someone else does not gain a trigger. Chat inspection needs `im:chat:readonly`, and Feishu must grant and publish `im:message.group_msg` to deliver unmentioned group messages. The option does not override other group routing rules.

- New membership lookups run only after `allow_chat` and `allow_from` allow the message. An unknown bot identity or failed lookup does not enable the exception.
- The first lookup waits up to 5 seconds. Failed lookups are retried only after a 5-second cooldown. Only one lookup per chat is in flight; concurrent unmentioned messages with unknown membership do not trigger and are not replayed afterward.
- Successful lookup results, including nonmatching groups, are cached for 30 seconds. Membership changes may briefly use a stale result.
- With thread isolation and `attachment_wait = "mention"`, admission by this option alone does not start, extend or release the attachment wait. Unmentioned attachments eligible for staging follow the existing thread rules.
