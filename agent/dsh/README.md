# DSH web adapter

Connect cc-connect to a running DeepSeek Harness web server on the same
machine. The authenticated Remote API is verified against the public
`@deepseek-ai/dsh@0.2.1-rc.2` package. The adapter also retains #1676's
legacy `/api/session.*` and `/api/events.mux` transport for older deployments.

## Configuration

Start `dsh web` and use the launch token it prints:

```toml
[projects.agent]
type = "dsh"

[projects.agent.options]
work_dir = "/absolute/project/path"
base_url = "http://127.0.0.1:3080"
token = "<launch token>"
mode = "default"
```

`DSH_TOKEN` supplies the token when the option is absent. Pasting the full
launch URL into `base_url` also works: its token is extracted and removed
from the URL before logging. Supplying a token selects the authenticated
Remote transport; without a token the adapter uses the legacy transport.
Use the new launch token after restarting DSH. The adapter does not start
or stop the shared DSH server.

Optional `model` accepts an exact `provider/model` route or an unambiguous
bare ID from the server catalog. `reasoning_effort` uses that model's
advertised options. Invalid explicit selections fail session startup.
`agent_preset` selects the native preset for new sessions. `timeout_mins`
limits ordinary turns and native commands; `0` disables that watchdog.

## Behavior

- Each chat has its own authenticated peer and socket. It receives only its
  own output and answers only its own approval/question requests.
- Both event queues apply cancellation-aware backpressure, retaining text,
  reasoning, tool events, and turn completion under a slow consumer.
- Reconnecting resumes the native conversation instead of passing a used ID
  to `session/create`, which would create a different conversation in modern
  DSH. Initial history snapshots are kept out of the next outgoing reply.
- `/mode` uses acknowledged session-scoped permission commands. `default`,
  `yolo`, and `plan` select `workspace-write`, `danger-full-access`, and
  `read-only`. The corresponding preset must exist. No server-wide defaults
  are changed. `plan` restricts filesystem access; it does not toggle DSH's
  separate collaboration-plan state.
- Failed approval/question responses remain retryable. Pending approvals can
  be recovered after reconnecting; unrelated peers do not settle them.
- Native history respects surface replacements from edits and compaction.
  Context usage reads DSH's native token and context-pressure projections.
- `/compact` and other registered native slash commands execute through
  `commands/execute`. Unknown slash commands remain normal model input.
- Standard `.dsh/skills`, `.agents/skills`, and user skill roots are exposed
  to cc-connect's skill discovery. Platform instructions and cc-connect's
  session environment are included in the first submitted prompt.
- Workspace agents preserve URL, token, preset, model, effort, mode, and
  timeout. `/stop` cancels the current turn and keeps the session usable.
- Deleting a session uses DSH's native archive action. Its durable log is
  retained, and archived sessions are excluded from lists and resume checks.

The authenticated transport adds native history, context reporting, archive,
and command execution. Legacy servers retain their original capabilities.
Attachments saved as files require shared access to `work_dir`; images are
submitted through the server's image intake.

DSH's approval protocol grants **allow once** or deny. The adapter does not
pretend this is Claude Code's persistent tool allowlist (`/allow`). Permission
presets remain available through `/mode`. Real account billing/model quality
and IM delivery belong to their external services rather than these tests.

## Verification

```sh
go test -race ./agent/dsh -count=20
go vet ./agent/dsh
golangci-lint run --build-tags smoke ./agent/dsh
```

The reproducible live suite starts the installed public DSH CLI with an
isolated home and a local Messages API fixture. Only the external model is
simulated; DSH authentication, sessions, tools, approval handling, persistence,
skills, projections, and commands run in the real DSH process:

```sh
DSH_CLI=/absolute/path/to/dsh python3 agent/dsh/testdata/remote_smoke.py
```

The suite covers multiple turns, model/effort selection, bridge restart and
resume, native history/context, two-topic permission isolation, allow/deny,
pending approval recovery, questions, stop followed by another turn, native
compaction, skill discovery, image input, and archive/list consistency.

For an already-running server with a real model account:

```sh
DSH_SMOKE_URL=http://127.0.0.1:3080 \
DSH_TOKEN='<launch token>' \
DSH_SMOKE_WORKDIR=/absolute/project/path \
go test -tags smoke ./agent/dsh -run TestSmoke_RoundTrip -v
```
