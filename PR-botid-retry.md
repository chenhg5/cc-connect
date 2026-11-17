# fix(feishu): retry bot open_id discovery instead of permanently disabling group chat filtering

> **Baseline:** branched from `main` (`580df5ea`), a single commit touching only
> `platform/feishu/`. No unrelated changes are bundled in.

## Symptom

When `cc-connect` starts in WebSocket mode and the very first Feishu API call fails,
the bot's own `open_id` is never learned — for the entire lifetime of the process.
The only trace is a single `WARN` at startup:

```
level=WARN msg="feishu: failed to get bot open_id, group chat filtering disabled"
```

The process then keeps running, looks healthy, serves DMs correctly, and gives no
further indication that anything is wrong. In the incident that prompted this PR the
daemon ran degraded for **43 minutes** until it was manually restarted.

## Reproduction conditions

Any transient failure of the *first* `/open-apis/bot/v3/info` call reproduces it:

- the process is launched at boot / by `launchd` / by systemd **before** the network
  stack or DNS resolver is ready;
- a laptop wakes from sleep and the supervisor restarts the service before Wi-Fi
  reassociates (this is the observed case — macOS `launchd` + wake-from-sleep);
- a brief DNS outage, captive portal, or proxy hiccup at the moment of startup.

The window is small, but the consequence is permanent, so over a long-running
deployment it is hit reliably.

## Real-world log

```
time=2026-08-25T13:52:32.693+08:00 level=WARN
  msg="feishu: failed to get bot open_id, group chat filtering disabled"
  error="api call: Post \"https://open.feishu.cn/open-apis/auth/v3/tenant_access_token/internal\": dial tcp: lookup ..."
```

## Root cause

`Platform.Start` (`platform/feishu/feishu.go`) treats bot identity discovery as a
one-shot, best-effort operation:

```go
if !p.shouldUseWebhookMode() {
    if openID, err := p.fetchBotOpenID(); err != nil {
        slog.Warn(p.platformName+": failed to get bot open_id, group chat filtering disabled", "error", err)
    } else {
        p.mu.Lock()
        p.botOpenID = openID
        p.mu.Unlock()
        slog.Info(p.platformName+": bot identified", "open_id", openID)
    }
}
```

There is no retry. A transient error is converted into a permanent capability loss —
a classic startup race, where the recovery path (the network coming up a few seconds
later) exists but is never taken.

## Security impact — this fails **open**, not closed

`p.botOpenID` is not merely used to *match* mentions; its emptiness disables the
mention gate altogether. From `onMessage`:

```go
if chatType == "group" && !p.groupReplyAll && p.getBotOpenID() != "" {
    if !isBotMentioned(msg.Mentions, p.getBotOpenID()) {
        // ... drop the message
    }
}
```

When `getBotOpenID()` returns `""` the entire condition is skipped, so **every message
in every group chat is dispatched to the agent**, including messages that never
mentioned the bot. For a deployment that relies on `@bot` as the activation gate, this
means unrelated group chatter silently triggers agent runs — burning tokens, and
exposing conversation content the operator did not intend to route to an agent.

This PR does not change that fallback behaviour (a separate decision about whether an
unknown bot identity should fail closed), but it does shrink the exposure window from
"until someone notices and restarts" to "until the network comes back".

Two other consumers of `getBotOpenID()` degrade more benignly and are likewise
untouched: `filterQuotedFilesForUser` fails closed (drops quoted files), and
`stripMentions` merely leaves the `@bot` prefix in the forwarded text.

## Fix

When the eager fetch fails, start a background goroutine that retries with
exponential backoff until it succeeds or the platform shuts down:

- backoff starts at **2s**, doubles, and is capped at **60s**; retries are unbounded,
  because the condition being waited on (network availability) has no deadline;
- on success the value is written under the existing `p.mu` and an `INFO` line records
  the recovery, so operators can tell degraded-then-recovered from degraded-forever;
- the goroutine owns a `context.CancelFunc` stored on the `Platform` and cancelled
  from `Stop()`, so it cannot leak and an in-flight HTTP request is cancelled too;
- `startBotOpenIDRetry` is idempotent — a second call while a loop is running is a
  no-op rather than a duplicate goroutine;
- `fetchBotOpenID` now takes a `context.Context` instead of hard-coding
  `context.Background()`, which is what makes cancellation actually reach the request.

**Webhook mode is deliberately unchanged.** The existing comment explains why startup
there must not depend on a bot-info call, and that reasoning still holds — the retry
only applies to the WebSocket path that already performed eager discovery.

## Scope of change

| File | Change |
| --- | --- |
| `platform/feishu/feishu.go` | +101 / −4 — three new `Platform` fields, two new methods, `fetchBotOpenID` takes a ctx, `Start`/`Stop` wiring |
| `platform/feishu/bot_openid_retry_test.go` | new — 4 tests |

No behaviour changes on the success path: when the first fetch works, the code runs
exactly as before and no goroutine is started.

## Testing

**Unit tests** — `go test -count=1 -race ./platform/feishu/...` is green on a clean
`main` checkout (34.7s), as are `gofmt -l` and `go vet ./platform/feishu/`:

- `TestStartBotOpenIDRetry_RecoversAfterFailure` — `httptest` server fails the first
  two `/open-apis/bot/v3/info` calls, then succeeds; asserts `botOpenID` is populated
  and that it took ≥3 attempts.
- `TestStartBotOpenIDRetry_OnlyOneGoroutine` — two `start` calls, one `stop`; asserts
  API traffic ceases, proving no duplicate goroutine.
- `TestStopBotOpenIDRetry_StopsGoroutine` — always-failing server; asserts the attempt
  counter freezes after `stop`, and that `stop` is idempotent.
- `TestStop_CancelsBotOpenIDRetry` — asserts `Platform.Stop()` cancels the loop.

Both new code paths were mutation-checked: neutering the goroutine launch fails two
tests, and neutering the `cancel()` call fails two others — the tests are not vacuous.

**End-to-end against the live Feishu API.** The built binary was started behind a
`CONNECT` proxy that was *not yet listening*, reproducing the original failure, then
the proxy was brought up mid-backoff:

```
15:08:53 WARN  feishu: failed to get bot open_id, group chat filtering disabled until retry succeeds
               error="... proxyconnect tcp: dial tcp 127.0.0.1:18080: connect: connection refused"
15:08:55 WARN  feishu: bot open_id retry failed  attempt=1 next_delay=4s
15:08:59 WARN  feishu: bot open_id retry failed  attempt=2 next_delay=8s
15:09:07 WARN  feishu: bot open_id retry failed  attempt=3 next_delay=16s
        <-- proxy started here -->
15:09:23 INFO  feishu: bot identified after retry, group chat filtering restored
               open_id=ou_b3b9... attempt=4
```

Backoff progression (2s → 4s → 8s → 16s) and recovery both behave as designed. On the
previous binary this scenario stays degraded indefinitely.

**Happy path unaffected:** the new binary was installed over the running service and
restarted via `launchctl`; startup logs the unchanged `feishu: bot identified` line
and no retry goroutine is created.

## Note on scope of verification

This PR does not touch `daemon/`. On non-Linux hosts a whole-repo `go build ./...`
currently trips over a duplicate `CheckLinger` declaration in that package, which
predates and is unrelated to this change, so verification here is reported at package
level: `go test -count=1 -race ./platform/feishu/...` passes on a clean `main`
baseline.
