# Pi RPC steering and completion

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

## Automatic steering

Set `busy_message_mode = "steer"` to let ordinary text, files and images
supplement the active task. `queue` remains the project default. Pi RPC uses the
same settled-turn and correlated-ACK layer in both modes; there is no separate
`rpc_completion` runtime mode. Remove that obsolete option from earlier
experimental configurations. JSON mode and adapters without the native
ACK-aware capability keep ordinary queue behavior.

```toml
[[projects]]
name = "my-project"
busy_message_mode = "steer"

[projects.agent]
type = "pi"

[projects.agent.options]
work_dir = "/path/to/project"
rpc = true
```

Recreate sessions after changing agent options. Workspace reconstruction keeps
RPC mode and the language used for version guidance. The minimum-version check
is a protocol prerequisite, not a substitute for testing custom extensions.

## Acknowledgment and fallback

RPC correlates every prompt with its response ID. The ACK disposition
means:

| Disposition | Meaning |
| --- | --- |
| `queued` | Pi accepted the input into its steering queue; this is not proof the model has consumed it. |
| `started` | Pi started a new run because the previous run had already ended. |
| `handled` | An extension or input handler consumed the input. |
| Missing or unknown | Protocol error; delivery is unconfirmed, never replayed, and the incompatible peer is stopped. |

Only a definitely unsent request or the exact native compaction preflight
rejection for plain text permits ordinary-message queue fallback. Pi 0.99.0 and 1.5.0
checks compaction before input handlers for plain text. A generic failed ACK
may follow extension side effects, so it is not automatically replayed. Slash
commands are also excluded from this preflight classification.

Ordinary prompt ACK waits follow the session context and RPC process lifetime.
Pi can perform automatic compaction, extension hooks and image normalization
before acknowledging the prompt, so there is no additional fixed ACK deadline
that could abort valid preflight work. Session cancellation and process exit
release the waiter; invalid ACK dispositions fail the incompatible session.

Automatic busy-steer ACK waits have a 30-second caller deadline, which leaves
the already-running task alive. The timeout bounds acceptance, not model
execution. For busy steering, timeout, cancellation
after sending, process exit before ACK, and partial writes are ambiguous:
they do not authorize automatic replay. Canceling an ACK wait does not kill
the current agent run. Correlation records remain until a late ACK or process
exit; the buffered response cannot block the stdout reader after its waiter
returns. An uncertain automatic delivery quarantines the session from further
normal input: use `/stop` or `/new` to recover. Pending queued input is reported
as undelivered. The original run remains alive and its output can still be
received.

Explicit `/ps` and `/btw` retain their upstream command behavior, including the
idle-session error. They keep their existing routing and feedback; RPC Send uses the shared ACK
contract. Automatic queue fallback and quarantine are for ordinary messages.
JSON mode is unchanged.

The busy-steer ACK deadline bounds response waiting, not a blocked synchronous OS pipe
write. Session cancellation terminates the subprocess and releases its pipe.
After a valid ACK, a peer that never sends either `agent_settled` or EOF still
needs the engine's `max_turn_time_mins` setting to bound the whole task. There
is no fallback to the unsafe `agent_end` terminal.

## Text and attachments

Automatic steering accepts ordinary text, files, and images, including messages
with no text. Images use the native RPC `images` field. Other files are saved and
represented by readable file references rather than inlined content. Each upload
uses an independent storage location, preserving same-named and earlier files.
An attachment preparation failure does not send a partially prepared input.

Ordinary RPC sends skip images with empty data or a MIME type that does not
start with `image/`, logging the image index, MIME type and data size. Text,
valid images and other files still reach Pi. If filtering leaves no content,
the empty prompt still follows the normal RPC acknowledgement/error path.
If an image fails these checks, automatic steering instead rejects the whole
input before writing any files or RPC command, allowing the engine to queue it
for an ordinary send after the current task finishes. File preparation and
transport failures remain errors.

One sender per session preserves engine arrival order. Inputs received during
session startup or an outstanding supplement ACK wait are buffered and sent as
steering once ready, without waiting for the original task to finish. Attachment
inputs do not force subsequent text into the next-turn queue. A waiting receipt
means buffered input; an accepted receipt requires the corresponding ACK.

Steering takes effect at Pi's next supported processing boundary within the same
task, not in another parallel agent. If the previous task has genuinely ended,
the input can start the next turn in the same session. Callback batching,
attachment collection windows, and reconstruction of a platform's original send
order are outside this feature. Voice and location handling are unchanged.

Successful file references remain available for later reads in the same session.
This change does not add per-turn deletion or a disk-retention policy. Deleting
files on an ACK would be unsafe because acceptance does not prove consumption.

## Completion and compression

For RPC, ordinary prompt ACKs, `agent_end`, and compaction start/end events do
not finish the logical run. `agent_settled` emits one terminal result or the
last unrecovered assistant error. A rejected supplement leaves the original
run open. Request generations protect a newer run from known stale completion
records. If a `started` ACK arrives before the old terminal and the adapter
coalesces both native runs, `ContinuesTurn` tells the engine to expect one
terminal event. If the old terminal was already emitted, the engine consumes
a separate turn for the new run. The ordered tests cover this contract without
claiming stock Pi reproduces the synthetic ordering. The wire has no remote
run ID, so fabricated lifecycle events cannot
be classified reliably.

RPC enables native `/compact` and `/compress` through the existing
engine compression interface. Native compact completes on its correlated
`compact` response. Review `auto_compress` settings when enabling RPC: Pi also
compacts internally, and a second scheduler may be unnecessary.

A `handled` ACK completes only a request that has not already started a run.
If `agent_start` arrived first, completion still waits for `agent_settled`.
Synchronous extensions returning `handled` must finish their own work before
returning. Fire-and-forget extensions, synthetic `agent_end`/`agent_settled`
records, and custom compaction wrappers require their own validated completion
contract; use native compression where possible.

## Verification

Tests replay ordered RPC records and use a fake persistent JSONL subprocess
through the real Pi adapter, Engine and SessionManager. Coverage includes the
plain `rpc = true` default, pre-start and mid-run compaction, overflow recovery,
retries, manual compression, handled extensions, queued reply contexts, two-bot
isolation, native images, same-named files, startup buffering, late ACKs,
cancellation, preflight work before a delayed ordinary ACK, missing dispositions,
version rejection, EOF and partial writes.
No model provider or production messaging service is contacted.

```sh
CI=true go test -tags no_web ./agent/pi ./core ./config -count=1
CI=true GORACE=atexit_sleep_ms=0 go test -race -tags no_web ./agent/pi ./core -count=1
CI=true go test -tags no_web ./core -run TestCUJ -count=1
```

The Engine fixtures use a local Unix socket for test control; environments
without that facility cannot run those fixtures unchanged. The application RPC
transport itself uses subprocess stdin/stdout, not that test-control socket.
