# PR Draft

## Title

Feature: normalize local agent references and render them readably on IM platforms

## Summary

This PR adds a new opt-in references feature for `cc-connect`:

- normalize local file / directory / code-position references from agent output
- render them readably before sending to IM platforms

Current initial support:

- agents: `codex`, `claudecode`
- platforms: `feishu`, `weixin`

## Config

```toml
[projects.references]
normalize_agents = ["all"]
render_platforms = ["all"]
display_path = "relative"
marker_style = "emoji"
enclosure_style = "code"
```

## Included

- project-level references config and validation
- platform-independent reference normalization
- platform rendering adaptation for initial supported platforms
- `/show <reference>` command for viewing files / directories / snippets from local references
- integration into:
  - agent thinking
  - final response
  - stream preview
  - progress compact / progress card
- regression tests

## Explicitly not included

- local file click-to-open behavior
- non-initial agents / platforms
- applying rendering to system messages
- applying rendering to raw tool results
- parsing already-rendered front-end display forms such as `📄 foo.ts`

## Testing

- `go test ./... -count=1`
- real Feishu validation:
  - Codex
  - Claude Code
  - final reply
  - progress/card
  - `/show`:
    - file head
    - line context
    - `line:col` context
    - range
    - directory listing
- real Weixin validation:
  - Codex short-task reply
  - Claude Code short-task reply
  - Claude Code short progress-style task
- preset matrix validation on both Feishu and Weixin:
  - `absolute-none-none`
  - `relative-emoji-code`
  - `basename-ascii-bracket`
  - `dirname-basename-none-angle`
  - `smart-emoji-fullwidth`
  - `dirname-basename-ascii-code`

Note:

- Weixin has a separate long-task `sendMessage` / session-timeout issue under investigation.
- This PR's manual validation on Weixin therefore focused on short, high-coverage prompts to verify reference rendering itself.

## Suggested PR screenshots

- Feishu before / after:
  - raw baseline under the deep workspace
  - recommended `relative-emoji-code`
- `/show` examples on Feishu:
  - `/show ui/recovery_contact_form.tsx`
  - `/show ui/recovery_contact_form.tsx:11`
  - `/show svc/recovery_session_reconciler.go:12:2`
  - `/show svc/recovery_session_reconciler_test.go:8-17`
  - `/show svc/`

## Notes

- default behavior remains unchanged unless `[projects.references]` is configured
- system messages and tool results remain raw by design
- `/show` is a privileged command and remains independent from references rendering
- tests were adjusted to avoid machine-specific filesystem dependencies
- current recommended default remains:

```toml
[projects.references]
normalize_agents = ["all"]
render_platforms = ["all"]
display_path = "relative"
marker_style = "emoji"
enclosure_style = "code"
```

- matrix comparison suggests:
  - `relative-emoji-code` is the best general default
  - `dirname-basename-ascii-code` is the strongest non-emoji alternative
  - `smart-emoji-fullwidth` is shorter but too lossy for a default
