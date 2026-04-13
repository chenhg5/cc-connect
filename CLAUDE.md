# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Build, Test, Lint

```bash
# Build (requires web assets first — make handles this)
make build                    # full build including web UI
make build-noweb              # Go-only, skips web assets

# Run
./cc-connect                  # requires config.toml (see config.example.toml)

# Test
go test ./...                 # unit tests (fast, no build tags)
go test ./core/ -v            # single package
go test ./core/ -run TestFoo  # single test
go test -race ./...           # with race detector (CI uses this)

# Test tiers (via Make)
make test-fast                # unit + smoke (<2 min, every push)
make test-full                # + regression (<10 min, PR requirement)
make test-release             # + performance benchmarks

# Lint
make lint                     # golangci-lint (errcheck, govet, staticcheck, unused, gosimple, ineffassign)
                              # CI runs lint on changed files only (--new-from-rev)
```

Web UI is React+Vite+Tailwind in `web/`. CI builds it with `pnpm install --frozen-lockfile && pnpm build`. The `make build` target handles this automatically.

## Project Overview

CC-Connect bridges AI coding agents (Claude Code, Codex, Gemini CLI, Cursor, etc.) with messaging platforms (Feishu/Lark, Telegram, Discord, Slack, DingTalk, WeChat Work, QQ, LINE). Users interact with their coding agent through their preferred messaging app.

## Architecture

```
cmd/cc-connect     ← CLI entry point, subcommands, daemon management
config/            ← TOML config parsing and validation
core/              ← engine, interfaces, session mgmt, i18n, cards, streaming, cron
agent/*/           ← one package per AI agent (claudecode, codex, cursor, gemini, etc.)
platform/*/        ← one package per messaging platform (feishu, slack, telegram, etc.)
daemon/            ← systemd/launchd/schtasks service management
web/               ← embedded React UI (Vite + Tailwind)
tests/             ← integration, e2e (smoke/regression), performance benchmarks
```

### Dependency Direction (strict)

```
cmd/ → config/, core/, agent/*, platform/*
agent/*    → core/   (never other agents or platforms)
platform/* → core/   (never other platforms or agents)
core/      → stdlib only (never agent/ or platform/)
```

### Plugin Architecture

Agents and platforms register via `core.RegisterAgent()` / `core.RegisterPlatform()` in `init()` functions. Each has a `plugin_*.go` file in `cmd/cc-connect/` with a build tag (`//go:build !no_feishu`). The engine creates instances from config strings via `core.CreateAgent()` / `core.CreatePlatform()`.

### Message Flow

1. Platform calls `Start(handler)` with the engine's `MessageHandler`
2. `Engine.handleMessage()` receives platform + message — applies rate limiting, alias resolution, workspace routing
3. Engine resolves or creates a session (keyed by `platform:channel:user`), acquires `session.TryLock()`
4. If session is busy, message is queued (up to 5); dequeued after current turn completes
5. Agent session is started/resumed via `agent.StartSession()`, message sent via `agentSession.Send()`
6. Agent streams events back via `agentSession.Events()` channel (types: `system`, `assistant`, `tool_use`, `tool_result`, `result`)
7. `streamPreview` provides incremental text delivery while the agent works (throttled updates via `UpdateMessage()`)
8. Final result is sent to platform via `platform.Reply()`/`platform.Send()`, session unlocked, history persisted

### Key Subsystems

- **SessionManager** (`core/session.go`) — maps `userKey → session ID`, serializes turns with `TryLock()/Unlock()`, persists sessions to JSON on disk
- **Thread Router** (`core/thread_router.go`) — forks sessions for parallel platform threads within the same base session
- **Observer** (`core/observer.go`) — watches local Claude Code JSONL session logs, polls every 2s, forwards activity to platforms that implement `ObserverTarget`
- **Bridge** (`core/bridge.go`) — global WebSocket server for external platform adapters to integrate without code changes
- **Streaming** (`core/streaming.go`) — incremental text delivery with throttling; platforms opt in via config
- **Cron** (`core/cron.go`) — scheduled task execution with per-session mode control
- **Permission flow** — `EventPermissionRequest` blocks on a pending channel until user responds via `/allow` or `/deny`; resolved by `pendingPermission.resolve()`
- **Multi-workspace** — workspace pool multiplexes agents across directories via `workspaceBindings`

### Core Interfaces

```go
Platform         // Start, Reply, Send, Stop — messaging platform adapter
Agent            // StartSession, ListSessions, Stop — AI agent adapter
AgentSession     // Send, RespondPermission, Events, Close — bidirectional session
```

Optional capability interfaces (implement only when needed):
- `CardSender` — rich card messages
- `InlineButtonSender` — inline keyboard buttons
- `FormattingInstructionProvider` — platform-specific formatting (e.g. Slack mrkdwn)
- `ReplyContextReconstructor` — recreate reply context from session key (needed for cron)
- `ProviderSwitcher` — multi-model switching
- `DoctorChecker` / `AgentDoctorInfo` — health checks and CLI metadata
- `ObserverTarget` — receive terminal session observations

## CLI Subcommands

`cc-connect` (no args) runs the main bridge. Key subcommands:
- `doctor` — health checks for agent/platform setup
- `daemon install|uninstall|start|stop|restart|status|logs` — OS service management
- `config` / `config-example` — config inspection
- `cron` — scheduled task management
- `sessions` — session inspection TUI
- `send` / `react` / `unreact` — programmatic message/reaction sending
- `relay` — bot-to-bot communication
- `provider` — model provider switching
- `update` / `check-update` — self-update

## Development Rules

### No Hardcoding Platform or Agent Names in Core

`core/` must remain agnostic. Use interfaces and capability checks:

```go
// BAD
if p.Name() == "feishu" { ... }

// GOOD — capability-based
if cs, ok := p.(CardSender); ok { ... }
```

### Prefer Interfaces Over Type Switches

Define optional interfaces in core, implement in agent/platform packages:

```go
// core/
type AgentDoctorInfo interface {
    CLIBinaryName() string
}

// core/ usage — query via interface, fallback gracefully
if info, ok := agent.(AgentDoctorInfo); ok {
    bin = info.CLIBinaryName()
}
```

### Error Handling

- Wrap with context: `fmt.Errorf("feishu: reply card: %w", err)`
- Never swallow errors; log with `slog.Error` / `slog.Warn`
- Use `slog` consistently; never `log.Printf` or `fmt.Printf` for runtime logs
- Redact secrets with `core.RedactToken()`

### Concurrency Safety

- Protect shared state with `sync.Mutex` or `atomic`; sessions are accessed from multiple goroutines
- Use `context.Context` for cancellation; `sync.Once` for one-time teardown
- Channels: document who closes them

### i18n

All user-facing strings go through `core/i18n.go`:
- Define a `MsgKey` constant
- Add translations for EN, ZH, ZH-TW, JA, ES
- Use `e.i18n.T(MsgKey)` or `e.i18n.Tf(MsgKey, args...)`

## Code Style

- Standard Go (`gofmt`, `go vet`); `strings.EqualFold` for case-insensitive comparisons
- `init()` only for platform/agent registration
- Naming: `New()` constructors, no stuttering (`feishu.Platform` not `feishu.FeishuPlatform`)

## Testing Patterns

- Stub types for `Platform` and `Agent` in core tests (see `core/engine_test.go`)
- Test card rendering by inspecting `*Card` structs, not JSON
- Agent session tests simulate event streams via channels
- Integration tests in `tests/integration/` — mock platforms, test session lifecycle
- E2E tests gated by build tags: `smoke`, `regression`, `performance`

## Selective Compilation

```bash
# Include only specific agents/platforms
make build AGENTS=claudecode PLATFORMS_INCLUDE=feishu,telegram

# Exclude specific ones
make build EXCLUDE=discord,dingtalk,qq,qqbot,line
```

Available tags: `no_acp`, `no_claudecode`, `no_codex`, `no_cursor`, `no_gemini`,
`no_iflow`, `no_opencode`, `no_qoder`, `no_feishu`, `no_telegram`,
`no_discord`, `no_slack`, `no_dingtalk`, `no_wecom`, `no_weixin`, `no_qq`, `no_qqbot`,
`no_line`, `no_web`.

## Adding a New Platform

1. Create `platform/newplatform/newplatform.go` implementing `core.Platform`
2. Register in `init()`: `core.RegisterPlatform("newplatform", factory)`
3. Create `cmd/cc-connect/plugin_platform_newplatform.go` with `//go:build !no_newplatform`
4. Add to `ALL_PLATFORMS` in `Makefile`
5. Add config example in `config.example.toml`
6. Add unit tests

## Adding a New Agent

1. Create `agent/newagent/newagent.go` implementing `core.Agent` and `core.AgentSession`
2. Register in `init()`: `core.RegisterAgent("newagent", factory)`
3. Create `cmd/cc-connect/plugin_agent_newagent.go` with `//go:build !no_newagent`
4. Add to `ALL_AGENTS` in `Makefile`
5. Optionally implement `AgentDoctorInfo` for `cc-connect doctor`
6. Add config example in `config.example.toml`
7. Add unit tests

## CI Pipeline

GitHub Actions (`.github/workflows/ci.yml`): lint → unit-test (with race detector + coverage) → smoke-test → regression-test → performance-test. Runs on push to main and PRs. Web assets must build before Go compilation. Lint uses `golangci-lint` with `--new-from-rev` for incremental checks on PRs.
