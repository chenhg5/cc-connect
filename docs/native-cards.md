# Platform-Native Cards

Scripts and agents can send a card authored in the platform's own format
(currently Feishu/Lark card JSON) and later replace it in place, for example
to turn a "running" status card into a "done" card.

```bash
# Send: prints the card's message id
id=$(cc-connect send --card /tmp/build-card.json --uuid build-1234)

# Replace the same card later through the local API
curl --unix-socket ~/.cc-connect/run/api.sock -X POST http://unix/cards/update \
  -H 'Content-Type: application/json' \
  -d "{\"message_id\":\"$id\",\"card\":$(cat /tmp/build-done-card.json)}"
```

- `POST /send` accepts `card` (a JSON object) instead of `message`; a card is
  sent unchanged and cannot be combined with `message`, `tts_text` or
  attachments, nor with `at_users` / `at_all` (put mentions inside the card).
  The response is `{"status":"ok","message_id":"..."}`; `cc-connect send
  --card` prints the id and exits non-zero if the response carries none.
- `uuid` (with `card` only, at most 50 characters) is passed to the platform
  as its deduplication key, so a retried send with the same key does not post
  a second card. On Feishu the key is honoured for about an hour; reusing it
  after that window sends another card.
- `POST /cards/update` takes `message_id` and `card`, plus optional `project`
  and `session_key` (needed only when a project has several card-capable
  platforms; the platform is resolved from it the same way `/send` does,
  workspace-prefixed keys included). It shares `/send`'s body size limit and
  outgoing rate limit.
- Platforms that cannot send or update raw cards answer `501 Not Implemented`.
