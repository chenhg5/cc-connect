# DSH web adapter

This adapter connects to the legacy DSH web RPC protocol: JSON
`client-request` / `server-response` envelopes on `/api/session.*`,
`/api/respond` interaction receipts, and `/api/events.mux` WebSocket events.
The server and cc-connect must share access to the configured `work_dir`
for file attachments.

## Permission isolation

`mode` configures new sessions. Live `/mode` changes use the requesting
session's `LiveModeSwitcher`; changing the agent default does not mutate
another live session. Explicit modes must be applied successfully before
session startup returns.

The server must provide `command.execute` and the session-scoped
`/permission` command. A missing command or rejected selection fails
explicitly. The adapter never writes `permission.defaultPreset` through
`settings.update`, because that would change other projects' defaults on
a shared server. `plan` additionally requires a configured `read-only`
permission preset in DSH.

## Delivery and recovery

Both event queues apply cancellation-aware backpressure. Text, reasoning,
and tool events are retained when the IM consumer is slow. Closing a
session cancels blocked producers before waiting for them and closing the
event channel.

Approval and question requests remain pending until `/api/respond`
returns a valid `accepted: true` receipt, allowing a failed delivery to
be retried. Workspace agents preserve the server URL, agent preset,
model, reasoning effort, permission mode, and turn timeout.

Model selection matches catalog routes without splitting model IDs on
slashes. A bare ID must be unique across providers; ambiguous IDs require
`provider/model`. Explicit selection failures stop startup rather than
silently running a different model.

## Verification and version boundary

Run the adapter's protocol regressions, including slow-consumer output,
session-scoped modes, retryable interactions, and model routing:

```sh
go test -race ./agent/dsh -count=1
```

The optional live test requires a compatible running server and model
credentials:

```sh
DSH_SMOKE_URL=http://127.0.0.1:3080 \
DSH_SMOKE_WORKDIR=/absolute/project/path \
go test -tags smoke ./agent/dsh -run TestSmoke_RoundTrip -v
```

Mock protocol tests do not establish compatibility with every published
DSH release. In particular, the published `@deepseek-ai/dsh@0.2.1-rc.2`
uses the newer `/api/remote.mux` Remote stream protocol and authenticated
API peers; it is not supported by this legacy transport. A local protocol
probe against that installed release exchanged the launch token through
`/connect` (302), received 404 for an authenticated `/api/session.list`
request, and successfully opened `/api/remote.mux` (101). This verifies the
transport mismatch without making a model request. The earlier
`0.1.5-rc.3` package also includes
the newer gateway. Verify the installed server's actual protocol before
deploying this adapter; do not assume a successful package installation
means its wire protocol matches.

## Remaining Claude Code parity work

This is a reliability follow-up to upstream PR #1676, not a complete
Claude Code capability match. Native history retrieval, session deletion,
context reporting and compaction, command/skill discovery, persistent
approval choices, and the newer authenticated Remote API still need
implementation and live contract tests. The existing smoke test exercises
a single conversation only; restart/resume and two-topic permission flows
also need validation against a compatible public DSH build.
