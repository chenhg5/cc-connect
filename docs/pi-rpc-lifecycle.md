# Pi RPC turn completion

Existing `rpc = true` configurations automatically use Pi's session-level
`agent_settled` event to finish ordinary turns. No additional completion option
is required. Automatic compaction and retries remain inside the original turn,
so a successful request produces its real answer without an earlier empty-response
placeholder or a second unsolicited reply (issue [#2161](https://github.com/chenhg5/cc-connect/issues/2161)).

## Compatibility

RPC mode requires **Pi 0.99.0 or a newer stable release**. Pi introduced
`agent_settled` in 0.80.4, but the prompt disposition needed to distinguish a
started run from a synchronously handled command was added in
[0.99.0](https://github.com/earendil-works/pi/blob/v0.99.0/packages/coding-agent/CHANGELOG.md).
This raises the minimum supported RPC version; JSON one-shot mode keeps its
existing process-exit completion behavior.

```sh
npm install -g @earendil-works/pi-coding-agent@latest
pi --version
```

Before starting each RPC session, cc-connect checks the configured executable
with `--version`, using its configured command arguments, environment, and working
directory. The probe has a five-second deadline and respects earlier cancellation.
Old versions, prereleases, failed probes, and ambiguous output are rejected with
upgrade guidance before RPC starts. A custom `cmd` wrapper must forward
`--version` and print the plain version, for example `0.99.0` or `1.5.0`.
There is no automatic fallback to the premature `agent_end` completion path.

Version guidance inherits the top-level `language` setting. A nonempty
`language` under `[projects.agent.options]` overrides that default and is
preserved when creating workspace agents. The version probe and RPC process
use the same startup configuration snapshot, even if the next session's
working directory changes while the probe is running.

## Waiting for acknowledgment and completion

Pi can compact before returning a prompt ACK, so ordinary ACK waits follow
the session context rather than a fixed short deadline. Cancellation and EOF
release the waiter. RPC deployments that need a wall-clock bound should set
`max_turn_time_mins`; its default is `0` (disabled). A peer that remains alive
without acknowledging or emitting `agent_settled` may otherwise keep the turn
open. There is no fallback to `agent_end`.

## Event ownership

- A prompt is recorded before it is written, including compaction before the
  first `agent_start`. A successful prompt ACK confirms acceptance, not completion.
- `agent_end`, even with `willRetry: false`, can precede overflow recovery or
  another automatic continuation. It updates usage but does not finish the turn.
- Compaction events and recoverable compaction errors do not finish a prompt.
  Assistant failures stay deferred until `agent_settled`; healthy assistant output
  clears transient errors. Terminal failure emits one error without a trailing
  result that could accidentally close the next queued turn.
- A matching `handled` ACK finishes a synchronous extension/input handler that
  did not start a run. If a run already started, cc-connect waits for its settled
  event. Independent runs started later by asynchronous extensions are separate
  unsolicited activity; `handled` does not promise that independent work finished.
- EOF releases pending ACK waiters and terminates an acknowledged running turn
  with one error. Closing the session cancels remaining waits.

This patch does not add native `/compact` or engine auto-compression support for
Pi. Extensions must follow Pi's standard command contract and must not fabricate
lifecycle records on stdout. Existing compression configuration and the separate
core reader-handoff work in [#2156](https://github.com/chenhg5/cc-connect/pull/2156)
are outside this change.

## Verification

The issue-specific tests use a fake external Pi peer with the production RPC
subprocess/parser, real Engine and SessionManager, and a recording platform.
They do not contact a model provider or a messaging service. Event barriers
control ordering; timers detect hangs. Regression coverage includes pre-start
and mid-run compaction, overflow recovery, retries, synchronous handled commands,
queued reply contexts, two independent bots, process exit, and cancellation.

```sh
go test ./agent/pi -count=1
go test -race ./agent/pi -run 'TestIssue2161Lifecycle_|TestPiIssue2161Engine_' -count=10
go test ./core -run TestCUJ -count=1
```
