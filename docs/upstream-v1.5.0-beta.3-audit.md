# Upstream v1.5.0-beta.3 audit

Audit date: 2026-08-14

Historical note: this table records the `v0.1.0-beta.1` release-gate decision. The previously deferred Feishu bundle was implemented in the 2026-08-15 follow-up; see [Upstream Feishu parity](upstream-feishu-parity-2026-08-15.md). The original decisions below remain unchanged so the release history is auditable.

cc-connect-next forked from official CC Connect v1.4.1 (`7fcad099`). Official v1.5.0-beta.3 is `ad196294`; the histories have diverged by 82 upstream commits, 23 successor commits, 190 changed files, and more than 26,000 added lines. The beta is therefore a patch source, not a safe merge target: importing it wholesale would replace independently reviewed migration and Feishu card-lifecycle code.

## Release-candidate decisions

| Upstream change | Decision for v0.1.0-beta.1 | Reason / implementation |
|---|---|---|
| `d484ae6` — admin-gate `/commands addexec` and `/cron addexec` | Reimplemented | Security boundary. Non-admin users can no longer register shell execution; listing and prompt-only management remain available. |
| `91c3d44` — time out blocked Codex app-server writes | Reimplemented | A request timeout now includes the stdin write and aborts the stuck transport. |
| `230ee3c` — resume preview after permission resolution | Adapted | Legacy previews restart as a fresh segment. Rich cards keep their single-card lifecycle and never freeze at the prompt boundary. |
| `5bda531` — prevent attachment filename collisions | Adapted | Next keeps the existing Agent interface, scopes files by trigger message ID, uses exclusive non-overwriting creates, and preserves the `.cc-connect-next` namespace. |
| `d7f06db` — create queue placeholder around idle reset | Already covered | Next creates the placeholder before attempting the session lock. A concurrent message also creates/adopts the placeholder, so the upstream loss window is absent. |
| `f943666` — hide Agent-emitted status footer | Reimplemented | `hide_agent_footer` is supported globally and per project; the recommended Feishu profile enables it. |
| `129753c` — topic-scoped workspace bindings | Deferred | This changes workspace-routing persistence, not the answer-card contract. It needs migration and real topic QA as one unit. |
| `c22d5e2` — on-demand quoted-file download | Deferred | Valuable, but it adds Feishu resource API reads and same-user privacy rules. It requires dedicated real-client/file-permission QA. |
| `94e2154` — bootstrap the first mention in an existing topic | Deferred with topic bundle | Depends on the topic/session semantics above and should not be imported alone. |
| `97b96ad`, `6fc59a3` — relay visibility and outbound bot mentions | Deferred | Bot-to-bot relay behavior is outside the first Feishu answer-card and migration release gate. |
| `760079b` — live Agent process idle timeout | Deferred, migration-gated | If an imported config uses this new field, migration fails before writing rather than silently ignoring its behavior. |
| `2973210` — Yuanbao platform | Deferred, migration-gated | The first beta does not advertise Yuanbao. A migrated project that uses it fails compatibility preflight before writing. |

## Policy for later upstream syncs

1. Compare from the last audited official tag, not from upstream `main`.
2. Classify each change as reimplemented, already covered, accepted verbatim, or deferred.
3. Preserve the Next identities (`cc-connect-next`, `.cc-connect-next`, independent services and sockets).
4. Treat Feishu routing, reply context, card lifecycle, and migration inventory as protected contracts.
5. Require focused regression tests before implementation, then full Go, race, release-local, archive, and real-client gates.
