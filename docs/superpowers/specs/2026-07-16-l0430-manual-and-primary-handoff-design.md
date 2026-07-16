# L-0430 Manual Receipt and Primary Secretary Handoff Design

## Goal

Use Telegram as Boss's receipt inbox, not as a universal agent router. A RESULT is retained until Boss receives it; Boss may either receive it manually or hand the current original directly to the cc-connect primary secretary.

## Inbox card

Every pending receipt card displays the envelope metadata plus the absolute original `result.md` path.

The card keeps `展开原信` for paged reading and has two state-changing actions:

- `收件`: record Boss receipt and remove the Inbox card; do not invoke an agent.
- `交主秘书`: read the original RESULT and pass its full source bytes as the primary secretary's agent input; on success it also records receipt and removes the Inbox card.

## External secretary workflow

No external-routing or export button is added. Boss may copy the expanded original or use the displayed absolute result path when manually handing work to a Codex, Claude Code, Cursor, or other local secretary instance. After doing so, Boss clicks `收件` to clear the Inbox entry.

## State and safety

`AcknowledgedAt` means the card was received and removed. A separate primary-handoff record captures recipient/session/time only when `交主秘书` succeeds. Both actions are idempotent. A failed card deletion or failed primary handoff keeps the card pending and does not write acknowledgement, preventing lost letters.

The original archive RESULT is authoritative for Telegram reading, manual external work, `/letter`, and primary-secretary handoff. It is intentionally re-read for each operation, so a pursuit edit is immediately visible and there is no copied-file retention or hash protocol. The file modification timestamp is the receipt generation: an unreceived update replaces the existing card in place; an update after receipt creates a fresh pending card. Callback data includes that timestamp, so stale buttons cannot act on newer content.

## Verification

Tests cover original-path rendering without a hash, manual receipt without agent invocation, primary handoff receiving the current source bytes and deleting the card, and compensation for delete/handoff failures.
