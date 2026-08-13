# Feishu answer-card contract

This document is the implementation contract for cc-connect-next's default `card_mode = "rich"`. It is intentionally stricter than a visual description: it defines what may enter the Feishu payload, how one turn changes state, and which tests must keep the behavior stable.

## One turn, one quoted card

Every accepted interactive turn gets its own Card 2.0 message. The card is created as a reply to that turn's triggering Feishu message, including queued turns; later updates target the same card rather than creating progress messages.

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

The initial card is non-empty and is sent before waiting for reasoning, tools, or answer text. A queued turn uses its own stored reply context, so it never quotes an earlier question by accident.

Immediate feedback is intentionally higher priority than retroactive invisibility. The runtime cannot know before the first Agent event that a turn will eventually end as `NO_REPLY`; deferring all cards until that decision would restore the long blank wait this card contract is designed to remove. cc-connect-next therefore recalls the optimistic card for `NO_REPLY`. No answer or completion reaction remains, but the Feishu client may render a recall notice.

## Streaming and fallback

When Feishu returns a CardKit `card_id`, answer deltas update the `main_text` element with a monotonic sequence number. Full-card state transitions share the same sequence, so a delayed frame cannot overwrite a newer one. Updates are coalesced on a short interval to keep the typing effect smooth without violating Feishu rate limits.

If CardKit creation or element streaming is unavailable, cc-connect-next safely falls back to updating the inline card in the same quoted message. Tables beyond Feishu's per-card component budget are rendered as fenced text in that same card rather than creating overflow answer messages.

If the terminal full-card update itself fails, cc-connect-next removes the stale lifecycle card before sending the readable answer as a normal reply. If a failure-state update fails, the fallback reply contains only any already-visible safe assistant partial plus localized static failure copy; raw provider/process errors are never substituted into chat-visible text.

## Privacy boundary

Rich-card progress carries event kinds and anonymous counts only. The renderer never receives or emits:

- reasoning text;
- tool names, arguments, results, or runtime errors;
- model, effort, token, context, or working-directory metadata;
- reply footers or expandable/collapsible detail panels.

This is omission, not a collapsed disclosure: private details do not exist in the card JSON and therefore cannot be expanded by another viewer. The starter configuration also enables `smart + emoji + code` reference rendering for Codex and Claude on Feishu, shortening local file references without exposing redundant absolute-path presentation.

## Locale and completion

Lifecycle copy is defined for English, Simplified Chinese, Traditional Chinese, Japanese, and Spanish. The active conversation locale selects the copy. A configured `done_emoji` is added to the triggering message only after a visible successful answer; it is suppressed for `NO_REPLY` and failures.

## Executable verification

Run the focused contract tests:

```bash
go test ./platform/feishu -run 'TestBuildRichCard|TestRichCardLifecycle' -count=1
go test ./core -run 'TestProcessInteractiveEvents_RichCard|TestProcessInteractiveEvents_QueuedRichCards' -count=1
go test ./core -run TestCUJ -count=1
```

These tests cover payload privacy, all supported locales, CardKit creation and monotonic updates, exact quoted replies, queued-turn isolation, partial-answer failure handling, stale-card cleanup and generic failure fallback, and removal of the lasting `NO_REPLY` answer card. A real Feishu client check is still required before a release is described as visually verified, because client rendering and platform permissions are external to the repository.
