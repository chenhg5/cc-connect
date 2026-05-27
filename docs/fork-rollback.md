# Session Fork & Rollback

> [中文版](./fork-rollback.zh-CN.md)

cc-connect supports session forking and rollback for agents that implement the `SessionForker` interface (currently Claude Code only).

---

## /fork — Branch a conversation

Create a new session that branches from the current one at a specific point. The original session remains untouched — the fork inherits the conversation history up to the chosen turn.

### Usage

| Command | Description |
|---------|-------------|
| `/fork` | Show a card with the last 10 turns to pick a fork point |
| `/fork [name]` | Fork the entire session with a custom name |
| `/fork [name] N` | Fork from turn N (remove the last N turns) with a custom name |

When no name is provided, an auto-name is generated: **`fork 1 of <currentName>`**, **`fork 2 of <currentName>`**, etc.

### Card-based selection (Feishu, Slack, Telegram)

On platforms that support interactive cards, `/fork` (with no arguments) shows an in-place card popup listing the last 10 turns. Each turn shows a preview of the user message. Select a turn or "Entire session" to fork from that point.

```
┌─ Fork from which turn? ────────────────┐
│                                         │
│  🔽 Select a turn:                      │
│    倒数第1轮: 南沙这边想做个副业...        │
│    倒数第2轮: 有什么风险吗                │
│    倒数第3轮: 代办不是还要跑腿哦啊         │
│    ...                                  │
│    整个会话                              │
│                                         │
└─────────────────────────────────────────┘
```

After selecting, the card updates in-place to show the result:

```
┌─ Fork from which turn? ────────────────┐
│                                         │
│  ✅ Fork created: fork 1 of 意识探讨 (s8)│
│                                         │
│  [← Back]                               │
└─────────────────────────────────────────┘
```

### Example

```
User:  /fork
Bot:   Shows card with turns → user picks "倒数第3轮"
Bot:   ✅ Fork created: fork 1 of 南沙副业 (s9), from turn 3
```

Or directly with arguments:

```
User:  /fork 意识探索 2
Bot:   ✅ Fork created: 意识探索 (s10), from turn 2
```

---

## /rollback — Remove recent turns

Remove the last N turns from the **current** session. This truncates the agent's JSONL history so the model only sees the remaining context.

### Usage

| Command | Description |
|---------|-------------|
| `/rollback` | Show a card with the last 10 turns to pick a rollback point |
| `/rollback N` | Remove the last N turns directly |

### Card-based selection

On supported platforms, `/rollback` (with no arguments) shows an in-place card popup. After selecting, the card updates to show how many turns were removed and how many remain.

```
┌─ Rollback ─────────────────────────────┐
│                                         │
│  ✅ Rolled back 3 turns, 4 remaining    │
│                                         │
│  [← Back]                               │
└─────────────────────────────────────────┘
```

### Example

```
User:  /rollback 2
Bot:   ✅ Rolled back 2 turns, 8 remaining
```

After rollback, the agent's context window only contains the truncated history. Any subsequent messages continue from that point.

---

## Key differences

| | `/fork` | `/rollback` |
|---|---|---|
| Creates new session? | Yes (side session) | No (modifies current) |
| Original session? | Preserved unchanged | Truncated |
| Can switch to it? | Yes, via `/switch` | — |
| Auto-name? | `fork N of <name>` | — |

---

## Session name preservation

Both fork and rollback preserve the current session name in the agent's JSONL file. Rollback truncation may remove the `custom-title` entry from the end of the file — cc-connect automatically re-writes the title after truncation so VSCode / CLI always show the correct name.

---

## Changelog

| Date | What changed |
|------|-------------|
| 2026-05-22 | **🚀 Fork result card: added back button** — after selecting a turn, the result card now has a "← Back" button (matching rollback behavior) |
| 2026-05-22 | **🔧 Fork card: synchronous mode** — `/fork-prompt` card action now executes the fork synchronously (same pattern as `/rollback`), eliminating the async `RefreshCard` approach that silently failed. Result card appears immediately after selection |
| 2026-05-22 | **🐛 Rollback: session name preservation** — rollback truncation now re-writes the current `custom-title` to the JSONL file, preventing VSCode from reverting to a stale/old title |
| 2026-05-21 | **🚀 Fork auto-naming** — `/fork` without a name auto-generates `fork 1 of <currentName>`, counting existing forks to avoid collisions |
| 2026-05-21 | **🚀 Card-based selection** — `/fork` and `/rollback` with no arguments show in-place card popups with turn previews (on Feishu, Slack, Telegram) |
| 2026-05-21 | **🚀 Help card: parameter descriptions** — `/fork` and `/rollback` entries in the help card now show parameter hints like `/fork [名称] [N]` and `/rollback [N]` |
| 2026-05-21 | **🚀 ListRecentTurns** — new `SessionForker.ListRecentTurns()` method extracts user message summaries from the JSONL, filtering out tool_result entries |