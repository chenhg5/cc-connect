# L-0404: Claude permission bridge design

## Goal

Keep Claude Code's native permission model authoritative while making the
Telegram Secretary uninterrupted for approved research tools.  Do not create a
cc-connect-specific tool-rule language.

## Native-rule passthrough

Add an ordered `allowed_tools` agent option and pass each value unchanged to
Claude Code as `--allowedTools`.  The Secretary configuration permits `Skill`,
`Workflow`, and `WebSearch`.  `WebFetch` is configured only with Claude's
domain-scoped rule syntax; it has no global default.

Claude Code evaluates these rules before it asks the stdio permission bridge.
Existing `acceptEdits` behavior remains intact for all other tools.

## Background permission lifecycle

When a Claude control request arrives after a foreground turn has completed,
cc-connect creates the same pending-permission state and Telegram card used by
the foreground event loop.  A single pending request is active per session;
additional background requests wait in FIFO order.  TTL begins only when a
request becomes the displayed active card, never while it waits in the queue.
Each active card expires after a configurable, bounded TTL (default: 60
seconds).  Expiry sends one deny response to Claude and activates the next
queued request.  Session close, reset, stop, and cancel deny each unresolved
stdio request at most once and clear the active request and queue.

## /yolo alias

Recognize `/yolo` only when an active permission request exists.  It delegates
to the existing `allow all` path, setting the current live session's shared
`approveAll` flag and approving the active request.  The same flag
automatically approves queued requests when they become active and every later
unresolved request in that live session.  It never selects Claude Code's
`bypassPermissions` mode.  New/reset/stopped/cancelled sessions start without
this state.  Queue length is bounded.

## Tests

1. Claude adapter CLI arguments preserve ordered `allowed_tools` values.
2. Secretary config holds only the three unscoped research rules; WebFetch is
   domain-scoped.
3. Unsolicited permission events create a Telegram approval request rather than
   immediately denying; their TTL denies exactly once and advances FIFO work.
4. `/yolo` enables existing session-scoped allow-all only with a pending
   request, and does not alter the adapter permission mode.

## Non-goals

- No `auto_approve_tools` setting or cc-connect-specific rule parser.
- No default arbitrary WebFetch, Bash, or MCP approval.
- No mapping from `/yolo` to `bypassPermissions`.
