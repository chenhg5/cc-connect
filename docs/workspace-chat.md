# Unified workspace chat

Unified workspace chat connects the main Web chat and WeCom direct messages to the same Codex App projects, native threads, settings, Turns, and interactions. Codex threads are authoritative for conversation history; cc-connect persists only coordination state in `data_dir/workspace_chat.db`.

This project is at `v0.1.0`. Workspace chat has one current REST contract, one WebSocket contract, one event envelope, and one database schema. Removed routes, fields, events, parsers, and database layouts are not kept as compatibility aliases. `/platform-sessions` remains a separate product domain for platform `session_key` conversations and does not reuse or wrap this protocol.

## Configuration

```toml
[workspace_chat]
enabled = true
transports = ["web", "wecom"]

[workspace_chat.wecom]
bot_id = "your-bot-id"
bot_secret = "your-bot-secret"
```

The control setup wizard generates this configuration atomically. At least one of `web` and `wecom` is required; WeCom requires its WebSocket Bot credentials. The Linux server does not run a local Codex Agent or read a server-side `CODEX_HOME`.

Each paired macOS Runtime reads its local Codex App state and owns one App Server connection. Runtime enables the experimental API and probes native settings, collaboration modes, paginated history, and realtime separately. An unavailable capability is returned with its reason; no removed RPC or event protocol is used.

## Projects and conversations

The project rail is grouped by device and is read-only. Each Runtime supplies order, projects, and roots from its valid local `CODEX_HOME/.codex-global-state.json`. Offline devices and invalid roots remain visible with their real reason and cannot be operated.

Clients submit only an opaque, server-issued `workspaceRef`. Every operation resolves that reference again and verifies that the native thread belongs to the canonical root. Browser JSON cannot supply `cwd`, server paths, sandbox roots, developer instructions, or unverified local attachment paths.

Within a workspace, the supported conversation operations are:

- list and switch native threads;
- create a draft for a new conversation;
- copy `codex://threads/{threadId}` after materialization.

Projects cannot be created, edited, or deleted here. Threads cannot be forked, named or renamed, pinned, archived, deleted, or searched.

## Draft materialization

An empty `thread/start` has no recoverable rollout before its first Turn. Therefore New does not create a native thread. It atomically creates a SQLite draft and selects it; the Web route is `/chat/{workspaceRef}/draft/{draftRef}`. New accepts no name.

The conversation actor materializes the draft on its first ordinary message by running `thread/start` and `turn/start` in sequence. Once App Server accepts the Turn, one database transaction binds the real thread, updates selection and submission state, and then publishes `thread_materialized`. Web replaces the URL with `/chat/{workspaceRef}/{threadId}`. Before this point there is no deep link or realtime session and the UI shows that both become available after the first Turn.

Pending input is retained only until App Server explicitly accepts it, then cleared. After a restart, any submission or settings update whose native outcome was not confirmed becomes `needs_retry`; cc-connect never resends it automatically.

## Native controls

A materialized conversation exposes the native model, normal effort, Default/Plan mode, Plan effort, permission profile, service tier, personality, reasoning summary, status, usage, active Turn, approvals, MCP elicitation, and structured user questions.

Models, efforts, service tiers, and permission profiles must be selected from values advertised by the App Server catalogs. Default and Plan must come from collaboration-mode masks. Personality and reasoning-summary values are the current App Server schema enums, while realtime voices come from the App Server voice catalog. Clients cannot invent identifiers. In the public patch, `effort` is only the normal Default-mode effort and `plan_effort` is the independent Plan-mode effort. `thread/settings/updated` is the only successful settings commit signal. In Plan mode, model or `plan_effort` changes update both the top-level settings and the collaboration-mode settings in the same request; returning to Default restores normal effort.

One `WorkspaceChatService` actor owns each `workspaceRef + conversationRef`. Operations on one conversation are serialized while different conversations may run concurrently. An idle thread accepts `turn_start`; an active thread accepts only explicit `turn_steer` with the expected Turn ID or `turn_interrupt` with the active Turn ID. Approvals and structured interactions retain their original JSON-RPC request ID and accept only decisions advertised by App Server. Secrets are not persisted or logged.

History is read exclusively through paginated `thread/turns/list` and `thread/items/list`. `thread/read(includeTurns=false)` is used only for exact metadata and canonical-cwd ownership validation; it never reads history. Web combines the authoritative pages with live events and displays known messages, reasoning summaries, plans, commands, file changes, MCP and dynamic tools, searches, errors, and generic raw details for unknown items. There is no fixed 200-item history request and no second history protocol.

## Web protocol and realtime

The canonical REST resources and WebSocket messages are documented in [Management API](management-api.md#unified-workspace-chat-resources). Web restores the exact `web:admin` selection from SQLite when opening `/chat`; a URL selection takes precedence. The thread snapshot includes metadata, materialized settings, status, usage, active Turn, pending interactions, capabilities, and a server-generated deep link.

Every WebSocket event uses one envelope containing `type`, `epoch`, `sequence`, workspace/conversation identifiers, optional thread/Turn/request identifiers, `payload`, `error`, and `occurred_at`. Native notifications use `type = "native_event"`; their payload preserves the App Server `method` and raw payload. Replay is bounded. A missing epoch or sequence produces `resync_required` followed by a new snapshot rather than silently dropping state.

Web realtime audio uses WebRTC SDP and is available only after a thread is materialized and the probed realtime capability is supported. Switching conversations, disconnecting WebSocket, unmounting the view, or stopping the service stops realtime and releases media resources. WeCom does not expose realtime voice.

## WeCom

WeCom workspace chat uses only the WebSocket long connection, with no VPN, tunnel, or public callback. Direct-message selection is stored under `wecom:user:{userId}`. Group conversations are rejected explicitly. Text, images, and files use the verified platform input path. AI Bot voice callbacks contain only `voice.content` transcription text, not an audio URL, AES key, format, or raw bytes, so they do not create an audio attachment. A normal message during an active Turn instructs the user to use `/steer` instead of changing semantics implicitly.

Numbered project, thread, settings, and interaction menus are persisted as snapshots. The corresponding selection command consumes that exact snapshot and revalidates the workspace, thread, catalog option, or pending request. See [WeCom setup](wecom.md#统一工作区对话) for the complete command table.

## Database lifecycle

`data_dir/workspace_chat.db` uses the current `PRAGMA user_version` as an exact schema identity; there is no migration runner and no old-table reader.

- If the database is absent, cc-connect creates and validates the current schema atomically.
- If a healthy database has a different `user_version`, cc-connect closes it, removes exactly `workspace_chat.db`, `workspace_chat.db-wal`, and `workspace_chat.db-shm`, then creates the current schema.
- It does not back up or import old selections, menus, drafts, submissions, interactions, delivery records, or settings intents.
- If SQLite cannot be read, either integrity check fails, or a current-version schema is malformed, startup fails and the file is preserved. Corruption is never treated as an upgrade.

Codex native threads live under `CODEX_HOME` and are unaffected when the coordination database is rebuilt.

## Explicitly excluded

Workspace chat does not implement fork, thread naming or renaming, pin, archive, delete, search, sections, goals, compact, review, terminal controls, Codex App project editing, or desktop-global management. These exclusions are product boundaries, not hidden or legacy protocol capabilities.
