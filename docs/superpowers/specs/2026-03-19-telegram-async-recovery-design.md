# Telegram Async Recovery Design

**Date:** 2026-03-19
**Status:** Approved
**Branch:** codex/telegram-async-recovery

## Problem

Telegram currently fails synchronously in `Platform.Start()` when bot authentication or initial connectivity fails. `core.Engine.Start()` logs the platform failure and keeps the process alive if other platforms started, but Telegram is never retried. This creates two problems:

1. **Lost availability after startup failure** - a transient Telegram outage or bad network path during process boot leaves Telegram permanently offline until manual restart
2. **No reusable recovery contract** - other platforms may need the same "start now, become ready later, recover in background" behavior, but `core` has no capability-based way to support it

The required behavior is:

- `cc-connect` should continue serving other platforms even if Telegram cannot connect at startup
- Telegram should keep retrying in the background until it becomes usable
- The solution should be extensible so future platforms can opt into the same lifecycle without platform-name checks in `core`

## Design

### State Overview

```text
Start
  -> Connecting
  -> Ready
  -> Unavailable
  -> Ready
  -> Stop

Stop is terminal for the process lifetime.
Late callbacks or stale connection cleanup after Stop must be ignored.
Stale cleanup from an older connection generation must not override a newer Ready state.
```

### 1. Add a Capability for Asynchronous Platform Readiness

**Location:** `core/interfaces.go`

Add a lifecycle callback contract that platforms may implement when "started" and "ready" are different states:

```go
type PlatformLifecycleHandler interface {
	OnPlatformReady(p Platform)
	OnPlatformUnavailable(p Platform, err error)
}

type AsyncRecoverablePlatform interface {
	Platform
	SetLifecycleHandler(h PlatformLifecycleHandler)
}
```

Semantics:

- `Start(handler)` remains the entry point and still returns an error for unrecoverable setup failures
- Platforms implementing `AsyncRecoverablePlatform` may return `nil` from `Start()` after launching a background connection loop, even if they are not yet ready to receive messages
- Such platforms notify `core` through `OnPlatformReady` when a live connection is established and inbound processing is active
- They notify `core` through `OnPlatformUnavailable` when a previously ready connection is lost or when a connection attempt fails after startup

This keeps `core` platform-agnostic. The engine reacts only to capabilities and callbacks, never to hardcoded platform names.

### 2. Teach Engine to Handle Deferred Platform Readiness

**Location:** `core/engine.go`

`Engine` will implement `PlatformLifecycleHandler`.

#### Startup path

During `Engine.Start()`:

- Before calling `p.Start(...)`, detect whether the platform implements `AsyncRecoverablePlatform`
- If yes, install the engine as its lifecycle handler
- Keep the existing `p.Start(...)` call and existing error aggregation behavior

#### Ready hook

Move post-start platform initialization into a helper, for example:

```go
func (e *Engine) onPlatformReady(p Platform)
```

That helper is responsible for:

- logging platform readiness
- registering commands when the platform implements `CommandRegistrar`
- setting card navigation when the platform implements `CardNavigable`

`Engine.Start()` will call this helper immediately for normal platforms that start synchronously. `OnPlatformReady()` will call the same helper for async platforms when they actually become ready.

#### Idempotency

`onPlatformReady()` must be safe to call multiple times for the same platform. This is required because async platforms can reconnect and may emit multiple ready notifications over process lifetime.

The engine therefore needs per-platform readiness bookkeeping, keyed by the platform instance, so it can:

- avoid duplicate "platform started" side effects while already ready
- allow a later `OnPlatformReady()` after `OnPlatformUnavailable()` to re-run the needed setup if the platform disconnected and recovered

Recommended state model inside `Engine`:

- `platformReady map[Platform]bool`
- `stopping atomic.Bool` or equivalent guarded flag
- protected by a dedicated mutex

#### Unavailable hook

`OnPlatformUnavailable()` should:

- update readiness state to false
- log that the platform became unavailable
- not stop the engine
- not affect other platforms

This preserves the current partial-availability model.

#### Shutdown coordination

Lifecycle callbacks may arrive redundantly or close together during reconnect churn. `Engine` must therefore treat both callbacks as advisory, idempotent state transitions.

Additional shutdown rules:

- once `Engine.Stop()` begins, lifecycle callbacks from async platforms must become no-ops
- the engine must not re-mark a platform as ready after shutdown has started
- callback handlers must tolerate late delivery from a platform goroutine that was already in flight when stop began

Recommended engine state:

- set an explicit stopping flag before platform teardown begins
- cancel the engine context before stopping platforms so late callbacks observe shutdown immediately
- early-return from `OnPlatformReady()` and `OnPlatformUnavailable()` when either the stopping flag is set or the engine context is done

### 3. Refactor Telegram into a Background Connection Loop

**Location:** `platform/telegram/telegram.go`

Telegram will be the first platform implementing `AsyncRecoverablePlatform`.

#### New responsibilities

- `Start(handler)` stores the message handler, creates a cancellable context, starts a background connection loop, and returns `nil`
- `connectLoop()` repeatedly attempts to establish a working bot connection with exponential backoff
- `runConnection()` owns one connected lifecycle from `NewBotAPIWithClient(...)` through update consumption until disconnection or stop

#### Connection lifecycle

`runConnection()` should:

1. Create the bot client using the configured HTTP client
2. Allocate a monotonically increasing connection generation ID for this attempt
3. Store the bot on the platform only after successful client creation, tagged with that generation
4. Log successful connection with bot username
5. Drain pending updates as today
6. Start `GetUpdatesChan`
7. Notify lifecycle handler with `OnPlatformReady(p)`
8. Consume updates until:
   - stop context is canceled
   - update channel closes unexpectedly
   - a recoverable runtime error occurs during long-polling setup or update stream handling

When the lifecycle ends:

- stop receiving updates for the current bot
- clear or replace connection state safely only if this connection generation is still the current one
- notify `OnPlatformUnavailable(p, err)` only if:
  - the shutdown was not caused by process stop, and
  - this connection generation is still current

This generation guard prevents stale cleanup from an older failed connection attempt from overwriting a newer live connection.

#### Backoff behavior

Use exponential backoff with reset-after-stable-connection behavior, following existing reconnect patterns in the repository:

- initial backoff: `1s`
- growth: double each attempt
- max backoff: `30s`
- if the connection stayed healthy for a meaningful interval, reset backoff to `1s`

This avoids tight retry loops while allowing fast recovery after transient outages.

#### Concurrency/state safety

Telegram currently reads `p.bot` from send/reply paths and from the update loop. Background reconnect makes this concurrent.

Add synchronization around mutable connection state:

- protect `bot`, `cancel`, and lifecycle handler access with a mutex
- protect the current connection generation with the same mutex or an atomic
- read the current bot under lock only through a small shared accessor, for example `currentBot() (*tgbotapi.BotAPI, uint64, error)`

Every code path that touches the bot instance must use the same guarded accessor, including:

- `Reply`
- `Send`
- `SendImage`
- `SendFile`
- `SendWithButtons`
- `SendPreviewStart`
- `UpdateMessage`
- typing indicator support
- `downloadFile`
- callback-query handling paths that answer or edit Telegram messages
- any helper that inspects bot identity such as group-message targeting logic

If Telegram is not ready when one of these paths runs, return a contextual error such as `telegram: bot not connected` or skip processing safely when the code path is inbound-only. Helpers must fail fast on disconnected or stale bot state; they should not retry internally. This keeps reconnect behavior deterministic and avoids mixing request semantics with connection management.

#### Stop semantics

`Stop()` must make the platform permanently inert for the remaining process lifetime:

- cancel the background recovery loop context
- stop receiving updates on the current bot, if any
- clear active connection state under lock
- prevent any later retry iteration from emitting lifecycle callbacks after stop has begun

Late goroutines may still wake up briefly, but they must observe the canceled context and exit without mutating visible platform state.

Recommended stop ordering:

1. set a stopping flag under lock
2. cancel the recovery context
3. snapshot the current bot reference/generation
4. stop receiving updates for that bot
5. clear published connection state if the snapped generation is still current

### 4. Preserve Existing Core Semantics for Non-Async Platforms

No existing platform must be forced into the async model.

Specifically:

- platforms that do not implement `AsyncRecoverablePlatform` keep today's behavior
- `Engine.Start()` still reports errors when a normal platform fails to start
- if all platforms fail synchronously, process startup still fails as today

The new capability is opt-in, not a behavioral rewrite of `Platform.Start()`.

## Error Handling

- All lifecycle logs and errors must include project and platform context where available
- Telegram retry logs should distinguish:
  - initial connection failure
  - connection lost after previously being ready
  - retry scheduling/backoff duration
- Errors returned from send/reply paths while disconnected should be wrapped with Telegram context
- `OnPlatformUnavailable()` should tolerate nil or repeated errors without panicking

## Testing

### Core tests

**File:** `core/engine_test.go`

Add tests covering:

1. **Async platform start does not fail engine startup**
   - stub async platform `Start()` returns `nil` without becoming ready
   - `Engine.Start()` succeeds
   - command registration is not attempted yet

2. **Ready callback triggers deferred initialization**
   - async platform emits `OnPlatformReady()`
   - command registration happens exactly once
   - card navigation handler is installed

3. **Repeated ready callback is idempotent**
   - two ready callbacks without an intervening unavailable event
   - registration side effects still happen once

4. **Unavailable then ready re-initializes**
   - platform becomes ready, then unavailable, then ready again
   - engine transitions readiness correctly
   - deferred initialization can run again after recovery if needed

5. **Callbacks after engine stop are ignored**
   - async platform delivers `OnPlatformReady()` or `OnPlatformUnavailable()` after `Engine.Stop()` has started
   - engine does not mutate readiness state or run deferred initialization

6. **Duplicate lifecycle notifications are safe**
   - repeated unavailable notifications or repeated ready notifications during reconnect churn
   - engine remains consistent and side effects stay bounded

7. **Engine stop blocks late readiness**
   - `Engine.Stop()` sets stopping state before platform teardown
   - a late ready callback during stop is ignored
   - deferred initialization does not run after shutdown has started

### Telegram tests

**File:** `platform/telegram/telegram_test.go`

Refactor Telegram slightly to make connection logic testable without real network calls. The cleanest seam is a package-level factory or helper method for bot creation and stream startup.

Add tests covering:

1. **Start launches background recovery**
   - first connect attempt fails
   - `Start()` still returns `nil`
   - background loop schedules another attempt

2. **Ready callback fires on successful connect**
   - connection succeeds on a later attempt
   - lifecycle handler receives `OnPlatformReady`

3. **Stop terminates retry loop**
   - start platform
   - fail several attempts
   - call `Stop()`
   - verify no further retries are made

4. **Disconnected send paths fail safely**
   - invoke reply/send helpers while no bot is connected
   - assert contextual error instead of panic

5. **Late connect result after stop is inert**
   - a connection attempt completes after `Stop()` begins
   - no ready callback is emitted
   - no live bot remains published on the platform

6. **All bot-touching helpers use guarded access**
   - exercise helper paths such as download or callback reply logic while disconnected
   - verify they fail safely instead of dereferencing nil or stale bot state

7. **Stale generation cleanup cannot clobber newer connection**
   - an older `runConnection()` instance exits after a newer generation is already ready
   - old cleanup does not clear the new bot or emit unavailable for the newer live connection

8. **Stop preserves inert terminal state**
   - stop the platform during retry backoff and during an active connection
   - verify backoff loop exits and no later callback or backoff-reset logic mutates state

### Verification

Run at minimum:

```bash
go test ./core ./platform/telegram
go test ./...
```

## Non-Goals

- Converting all existing platforms to async readiness in this change
- Introducing platform-name-specific logic in `core`
- Adding user-facing Telegram notifications about reconnect status
- Generalizing retry/backoff policy into a global scheduler shared by all platforms

## Implementation Order

1. Add lifecycle capability interfaces in `core/interfaces.go`
2. Update `Engine` to consume the capability and handle deferred readiness
3. Add or refactor engine tests for async lifecycle behavior
4. Refactor Telegram into a background connection loop with safe disconnected behavior
5. Add Telegram regression tests
6. Run focused tests, then full test suite
