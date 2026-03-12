# Feishu Delete Card Design

**Date:** 2026-03-11

**Goal:** Let Feishu users enter `/delete` with no arguments to open a card-based multi-select delete flow, while keeping explicit delete arguments such as `/delete 1,2,3` working as direct command execution.

## Scope

- Only `/delete` with no arguments activates card selection mode.
- The normal `/list` card remains unchanged.
- Explicit delete arguments continue to bypass the card flow.
- Card flow is only for card-capable platforms; non-card platforms keep usage text.

## Interaction Flow

1. User sends `/delete`.
2. If the platform supports cards, cc-connect renders a delete-mode session list card.
3. Each session row exposes a single right-side button that toggles selected/unselected state.
4. The card footer provides:
   - `删除已选`
   - `取消`
   - pagination controls
5. `删除已选` opens a confirmation card listing the selected sessions.
6. The confirmation card provides:
   - `确认删除`
   - `返回继续选择`
7. On confirmation, cc-connect deletes the selected sessions, skips the active session, clears the temporary selection state, and renders a result card.

## State Model

Delete-mode state should be tracked per `sessionKey`, separate from the agent interactive process state. The minimum state needed is:

- whether delete mode is active
- current page in delete mode
- selected session IDs
- whether the user is currently on the confirmation card

Session IDs, not row numbers, must be the source of truth after selection, so cross-page selection remains stable even if list ordering changes between renders.

## Card Actions

- `act:/delete-mode open`
- `act:/delete-mode toggle <session-id>`
- `act:/delete-mode page <n>`
- `act:/delete-mode confirm`
- `act:/delete-mode back`
- `act:/delete-mode submit`
- `act:/delete-mode cancel`

All delete-mode actions should update the card in place on Feishu. They should not dispatch as plain user commands.

## Error Handling

- Empty selection cannot proceed to confirmation; the card should stay in delete mode with a hint.
- Deleting the active session must be reported as blocked, not silently ignored.
- If a selected session no longer exists, report it in the result card rather than failing the whole batch.
- Cancel must always clear delete-mode state.

## Testing

- Rendering delete-mode cards with selected/unselected rows
- In-place toggle behavior and page persistence
- Confirmation card content
- Submit path deleting only selected session IDs
- Active-session protection in card-driven batch delete
- Explicit `/delete 1,3-5,8` continuing to work outside card mode
