# Feishu: add instructions to the active turn

While a task is running, ordinary messages enter a FIFO queue for later turns. To add instructions to the current turn, click **Add to this turn** (`补充到本轮`) on its queue receipt, or send `/steer <instructions>`.

This release adds Feishu card controls backed by Codex App Server. The default `codex exec` backend continues to support ordinary queued messages.

## Configuration

Set these options in the existing Codex project's configuration:

```toml
[projects.agent.options]
backend = "app_server"
app_server_url = "stdio"
```

Keep the project's existing working directory, model, and permission settings. Restart cc-connect and establish a session. The installed Codex CLI must support App Server's `turn/steer`; `stdio` communicates with a local process.

The Feishu app must enable the `card.action.trigger` interactive-card callback. Messages and card callbacks must reach the same cc-connect instance.

## Queue receipt buttons

```text
You: Investigate the login failure.
Bot: Working…

You: Do not change code yet; report the cause first.
Bot: Queued
     Do not change code yet; report the cause first.
     [Add to this turn] [Cancel queued message]
```

- **Add to this turn** (`补充到本轮`) submits that message and its attachments to the exact turn recorded when it was queued. Acceptance removes it from the queue, so it does not run again as a separate turn.
- **Cancel queued message** (`取消排队`) removes that waiting message. The running task continues.

Only the user who started the active task and sent the queued message can use its controls. Callbacks also validate the originating session and chat. Another group member cannot use the card to change that task.

## Direct commands

These commands all use native steering with the Codex App Server backend:

```text
/steer Check whether the token has expired first.
/补充 先检查 token 是否过期。
/ps Check whether the token has expired first.
```

They target your active turn in the current session. Missing turns, unsupported backends, and explicit rejections produce a status reply; direct command input is never automatically converted into queued work.

Codex exec does not support these commands for same-turn input. In particular, `/ps` will not start a concurrent exec process. Send an ordinary message to queue it instead. Other agents retain their existing `/ps` behavior.

## Outcomes

| Status | Behavior |
| --- | --- |
| Queued | Waits for the current turn; the owner may add it to that turn or cancel it. |
| Added to this turn | Codex accepted the input. It will not run again from the queue. |
| Original turn ended | A still-pending message remains queued. It is never redirected into a different active turn. |
| Supplement rejected | A card-submitted message keeps its original FIFO position unless it was recalled during submission. A recalled message stays removed. Direct commands are not queued. |
| Submission outcome unknown | Input might have been accepted. This message is withheld from automatic execution; inspect the task before deciding whether to resend it. |
| Removed from queue | That pending message will not run. The active task continues. |
| Expired or unauthorized | The card is obsolete, the message has left the queue, the session changed, or the user does not match. No input is submitted. |

Acceptance means the turn received the input; a tool already running may continue until it finishes. Use `/stop` to stop the task. Repeating an already-processed button returns its existing outcome without submitting again. Recalling a message cannot retract input that Codex has already accepted.

Queue entries and card-action state are held in memory. Restarting loses pending queue entries and invalidates old controls. Inspect the task's result before resending work that still needs to run.

Quoting your own message does not automatically steer in this release. Quotes provide context; a button or command explicitly requests steering.

## Reproducible local preview

![Local preview of queued, accepted, ended, and unknown outcomes](images/feishu-steer-preview.jpg)

The preview exports actual Chinese cards from `core.queuedTaskCard` for queued, accepted, ended, unknown, and cancelled states. All content is synthetic; no Feishu account or network connection is used.

From the repository root:

```sh
CC_STEER_PREVIEW_DIR="$PWD/.artifacts/steer-preview" \
  go test ./core -run TestExportSteerCardPreview -count=1
```

Open `feishu-steer-preview.html` in the output directory. The adjacent `feishu-steer-cards.json` contains the card data used for titles, buttons, and notes. The preview uses a fixed light theme and supports desktop and mobile widths.

The page explicitly says **本地卡片预览，非飞书客户端截图** (“Local card preview, not a Feishu client screenshot”). It demonstrates generated card content and local layout. Actual Feishu rendering and callback delivery require separate testing with a connected app.
