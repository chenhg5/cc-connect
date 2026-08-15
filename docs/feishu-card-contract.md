# Feishu answer-card contract

This document is the implementation contract for cc-connect-next's default `card_mode = "rich"`. It is intentionally stricter than a visual description: it defines what may enter the Feishu payload, how one turn changes state, and which tests must keep the behavior stable.

## One turn, one quoted card

Every accepted interactive turn starts with its own Card 2.0 message. The card is created as a reply to that turn's triggering Feishu message, including queued turns; later updates target the same card rather than creating progress messages. The only successful terminal exception is an answer containing a resolved native @ mention: Feishu cards do not emit the bot mention event, so a tracked quoted `MsgTypeText` answer replaces the lifecycle card.

| Agent state | Visible status | Visible body |
|---|---|---|
| Accepted, before the first event | Localized “thinking” status | No blank placeholder |
| Reasoning event | Anonymous reasoning/tool counts | No reasoning text |
| Tool event | Localized “calling tools” status and counts | No tool details |
| First answer text | Localized “answering” status; prior progress disappears | Answer text only |
| Completed | Localized Done status | Final answer only |
| Failed before an answer | Localized generic failure | No runtime error details |
| Failed after partial answer | Localized failure | The already-visible safe partial answer |
| Bare `NO_REPLY` | The optimistic card is recalled; no answer or Done state remains | No answer body; Feishu may show its own recall notice |
| Triggering message recalled | The lifecycle card is deleted silently | No partial answer is persisted to assistant history |
| Completed with a resolved native @ | The process card is replaced by one tracked quoted text answer | Native Feishu mention event plus final answer; no stale card |

The initial card is non-empty and is sent before waiting for reasoning, tools, or answer text. A queued turn uses its own stored reply context, so it never quotes an earlier question by accident.

Rendering configuration is snapshotted when a turn begins. A management hot reload therefore cannot switch an already-created Rich Card into the legacy completion path; the new configuration takes effect when the next queued or newly accepted turn begins.

Immediate feedback is intentionally higher priority than retroactive invisibility. The runtime cannot know before the first Agent event that a turn will eventually end as `NO_REPLY`; deferring all cards until that decision would restore the long blank wait this card contract is designed to remove. cc-connect-next therefore recalls the optimistic card for `NO_REPLY`. No answer or completion reaction remains, but the Feishu client may render a recall notice.

## Streaming and fallback

When Feishu returns a CardKit `card_id`, answer deltas update the `main_text` element with a monotonic sequence number. Full-card state transitions share the same sequence, so a delayed frame cannot overwrite a newer one. Rich cards share the public `[stream_preview]` contract with legacy previews: `enabled` and `disabled_platforms` gate all non-terminal reasoning-count, tool-count, and answer-body updates, while `interval_ms`, `min_delta_chars`, and `max_chars` control answer-body frames. Disabling preview still keeps the immediate accepted-state card and the terminal Done/error update, but suppresses every intermediate frame. The final answer is never truncated by `max_chars`.

Some Agent backends, including the default `codex exec --json` path, may emit a complete answer as one text event immediately followed by the terminal result. Feishu keeps the successfully rendered `answering` phase visible for at least 900 milliseconds before applying Done in that case, allowing CardKit's native text animation to become perceptible instead of being overwritten by a back-to-back terminal patch. Naturally incremental answers that have already spent that long in the answering phase receive no additional delay, and cancellation or shutdown never waits for the dwell window.

If CardKit creation or element streaming is unavailable, cc-connect-next safely falls back to updating the inline card in the same quoted message. A CardKit rate-limit response is treated as an unrendered frame, not a successful stream update; the full-card fallback must succeed before that answer body is recorded as visible. Tables beyond Feishu's per-card component budget are rendered as fenced text in that same card rather than creating overflow answer messages.

If the terminal full-card update itself fails, cc-connect-next creates one completed replacement card with a deletable message handle, then removes the stale lifecycle card. If initial lifecycle-card creation failed, final delivery retries through that same tracked replacement-card path. Answer delivery never degrades into untracked multi-part replies: a recall racing the replacement cleans up every created card handle and prevents history commit. If the platform cannot create the tracked replacement either, the turn fails closed without sending an undeletable answer or committing it to history. If a turn fails earlier or its Agent event channel closes unexpectedly, only the last body confirmed by a successful Feishu create/update call may be retained on the failure card or in assistant history. Text held back by disabled previews, throttling, or a failed update is never treated as visible. If the failure-state update also fails, the fallback reply contains only that confirmed safe partial plus localized static failure copy; raw provider/process errors are never substituted into chat-visible text.

When `resolve_mentions = true`, streaming and safe-partial card bodies resolve `@DisplayName` against the triggering chat for correct visual rendering. Explicit `mention_map` entries override same-name group members and are validated as bot `ou_` open IDs. A completed answer with any successfully resolved native mention does not pretend that Card 2.0 can notify the target: cc-connect-next prepares the `MsgTypeText` at-tag, sends a tracked quoted text replacement, then removes the lifecycle card. If that send fails, the existing card completion path remains available so the user is not left without an answer. A recall racing a successful replacement deletes its exact message handle before history is committed.

Remote markdown images are uploaded once and reused by URL. A failed fetch or Feishu upload enters a one-minute backoff instead of a permanent denylist; after that window the next card that references the URL retries it. This avoids per-frame retry storms while allowing transient timeouts, rate limits, and network failures to recover without restarting the process.

During shutdown, cc-connect-next first interrupts any final remote-image resolution for active turns, then uses the still-live platform connection to deliver each terminal Done/error card within the bounded shutdown window. Platform teardown happens only after those lifecycle updates finish or the deadline expires.

## Privacy boundary

Rich-card progress carries event kinds and anonymous counts only. The renderer never receives or emits:

- reasoning text;
- tool names, arguments, results, or runtime errors;
- model, effort, token, context, or working-directory metadata;
- reply footers or expandable/collapsible detail panels.

This is omission, not a collapsed disclosure: private details do not exist in the card JSON and therefore cannot be expanded by another viewer. The starter configuration also enables `smart + emoji + code` reference rendering for Codex and Claude on Feishu, shortening local file references without exposing redundant absolute-path presentation.

The recommended profile also sets `hide_agent_footer = true`. Before rendering,
the engine removes only a strict, complete Agent-emitted status footer containing
all three model/token/context metrics; ordinary prose or partial metric mentions
remain untouched. This complements `reply_footer = false`, which controls the
footer generated by cc-connect-next itself.

`EventText` transport chunks are reconstructed as logical lines before that
decision. A footer split across several chunks is therefore never exposed in an
intermediate CardKit frame. Conversely, a chunk that happens to end with a
footer-shaped substring is retained when later text proves that the logical line
is ordinary prose; transport framing alone is never treated as a line ending.

## Locale and completion

Lifecycle copy is defined for English, Simplified Chinese, Traditional Chinese, Japanese, and Spanish. Each accepted turn snapshots the locale selected by its own triggering message, so concurrent sessions cannot make a card switch language mid-turn; a queued turn receives its own fresh snapshot. A configured `done_emoji` is added to the triggering message only after a visible successful answer; it is suppressed for `NO_REPLY`, recalled triggers, and failures.

## Executable verification

Run the focused contract tests:

```bash
go test ./platform/feishu -run 'TestBuildRichCard|TestRichCardLifecycle' -count=1
go test ./core -run 'TestProcessInteractiveEvents_RichCard|TestProcessInteractiveEvents_QueuedRichCards' -count=1
go test ./core -run 'TestProcessInteractiveEvents_CapturesRichCardLocalePerTurn|TestHandleMessageRecallDeletesRichCardWithoutPersistingPartialOutput|TestEngine_Stop.*RichCard' -count=1
go test ./core -run TestCUJ -count=1
```

These tests cover payload privacy, all supported locales, per-turn locale isolation, CardKit creation and monotonic updates, exact quoted replies, queued-turn isolation, configured mention resolution and tracked terminal text replacement, partial-answer failure handling, shutdown finalization, recalled-trigger cleanup, stale-card cleanup and generic failure fallback, and removal of the lasting `NO_REPLY` answer card. A real Feishu client check is still required before a release is described as visually verified, because client rendering, mention events, file permissions, and platform permissions are external to the repository.
