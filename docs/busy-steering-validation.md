# Busy steering and asynchronous answer validation

Validation date: 2026-10-29 (upstream refresh; earlier observations retain their dates). The automated checks below use this public PR branch,
integrated with upstream `main` at `b2a6e6e7`. Live observations come from a
separate private derivative. These are different artifacts and different claims.

## Public branch: automated validation

Environment: Windows, Go 1.25.12. Tests use a real Engine and SessionManager with
external platform/agent fixtures. The Codex subprocess tests use the real
app-server transport and adapter against a simulated JSON-RPC peer. Pi tests use
its RPC fixture; no installed or authenticated Pi instance is required.

Covered scenarios:

- Queue remains the default; enabling steer admits plain-text follow-ups into an
  active turn. Unsupported adapters and attachments retain their queue/Send paths.
- Startup-time text is offered to the active turn without a second process or
  `turn/start`. Pending attachments preserve FIFO order. Recall during an RPC
  cannot remove the next queued attachment. An explicit rejection stays queued
  and is not retried on each subsequent agent event.
- Accepted input is recorded. Unknown delivery records the user input plus an
  explicit unconfirmed-delivery notice; it is not automatically replayed.
- `/ps` and `/btw` do not start another turn at the remote-completion boundary.
- Optional questions do not capture ordinary task corrections. Buttons and
  `/answer` explicitly identify answers. `/answer` is recognized before the
  generic unknown-command fallback.
- Open questions survive completed turns, released execution state and storage
  reload. Answering a historical card selects its original conversation within
  the same platform/user namespace. Another busy conversation is not interrupted.
- Idle answers continue the original model thread. Failed validation, unsafe
  teardown and failed resume do not silently redirect these answers to a blank
  thread. This guard is scoped to answer continuation; it is not a general
  lifecycle overhaul.
- Repeated and uncertain answers are deduplicated across restarts. Card success
  waits for actual Send/steer acknowledgement, not queue admission. The exact
  clicked Feishu message is patched; synchronous question callbacks retain their
  existing behavior.

Commands:

```sh
go test ./agent/codex ./config ./core ./platform/feishu ./agent/pi -run 'TestAppServerSession_|TestCUJ|TestAsync|TestBusySteering|TestOnCardAction|TestSteer|TestCmdPs|TestLoad_.*BusyMessage|TestSendRPC|TestSend_TextSupplement|TestPiRPC' -count=1 -timeout 90s
go vet ./agent/codex ./config ./core ./platform/feishu
go build -tags no_web ./cmd/cc-connect
git diff --check
```

Before/after regression evidence: restoring the previous busy-steering function
fails the timeout/transport history expectations; restoring the previous Feishu
callback fails the no-optimistic-success regression. The candidate passes both.

No repository-wide suite, frontend build, packaging or race-detector run was
performed locally. CI evidence must name the checked head and jobs; an earlier
green run does not validate the refreshed head.

## Limited combination check with #2046

Temporarily overlaid only the Codex production and adapter-test changes from
#2046 at `2112f3ec`, ran the targeted steering/async-answer/A8/Pi checks, then
restored the public branch byte-for-byte. That implementation is **not included**
in this PR. Unrelated daemon/cloud-web fixture changes from #2046 were not overlaid.

The checks passed, including a subprocess scenario where default-mode input is
queued while busy and starts a second turn only after remote completion. This
does not support a blanket claim that #2046 necessarily changes the core queue
default. If both approaches are retained, their separate internal steering paths
still merit consolidation and review. This is not a race proof or live-provider
acceptance of the combined tree.

The new queue fixture initially raced synthetic completion ahead of the Send
acknowledgement, including without the overlay. It was corrected to sequence
that acknowledgement explicitly and revalidated before drawing this conclusion.

## Private derivative: real user-facing observations

Environment: Chinese-language Windows, Codex desktop CLI `0.163.0-alpha.11`,
app-server over stdio, `gpt-6-astra` with high reasoning effort, and existing Chrome
tabs for Feishu and a custom private Web client. The deployed derivative includes
additional launcher/lifecycle fixes, cross-entrance routing and private Web
integration that are **not part of this PR**.

Observed through actual UI operations and checked against model-turn/history data:

1. A task started in Feishu; a correction from the custom Web client changed its
   answer during the same model turn, without creating a replacement model thread.
2. A native async question appeared in Feishu. The model continued independent
   work. An ordinary Web correction added a separate marker while the question
   remained unanswered. Selecting a Feishu option applied the answer while
   retaining the marker.
3. In a follow-up test, the model asked a shape question and immediately completed
   using a default. Clicking the alternative after completion created exactly one
   new turn in the same model conversation and updated only the requested choice.
   The Feishu card reflected submission, and the Web history showed the same result.
4. Repeating that answer from the Web client, including after service restart,
   created no additional model turn and did not change the saved history length.
5. A separate earlier explicit file-delivery check from the custom Web client back
   to the original Feishu chat succeeded. That validates private outbound routing,
   **not a file-delivery change in this PR**. A zero-byte file was rejected by
   Feishu; an empty-text UTF-8 BOM file was subsequently accepted.

These observations support the interaction contract, not exact-build acceptance
of this public branch. The stock upstream Web UI, other providers/platforms,
real Pi, and real network-failure injection were not live-tested here. Public
fault-handling claims rely on the automated fixtures above.

Private Web/permission code, business data, hostnames, deployment configuration, credentials,
deployment scripts, screenshots and raw production logs are excluded. The private
service was not replaced with the public PR binary for this validation.

## 2026-10-29 refresh

Merged upstream main at `b2a6e6e7` without rewriting the public branch history.
Both the existing A8 steering/answer CUJs and upstream's R1 reasoning CUJ are
retained. Two upstream transport tests now assert the PR's `transportErr` field
rather than a synthetic RPC rejection; production delivery semantics are unchanged.
The targeted command above additionally covers upstream app-server EOF, terminal
backpressure, late start-response, child-thread isolation and large-resume tests.

Chrome tests on the updated private derivative repeated existing-context resume,
in-turn corrections from Feishu and its custom Web client, explicit async card
answers, one continuation after completion, and duplicate-answer suppression.
The saved model conversation remained the same; a duplicate answer left the
history count unchanged. These observations are still not live acceptance of
the exact public binary. The private UI showed a contradictory connection label
and one unreproduced automated-fill truncation; neither is represented as fixed
or included in this public PR.
