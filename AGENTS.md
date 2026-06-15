# CC-Connect Development Guide

## Project Overview

CC-Connect is a bridge that connects AI coding agents (Claude Code, Codex, Gemini CLI, Cursor, etc.) with messaging platforms (Feishu/Lark, Telegram, Discord, Slack, DingTalk, WeChat Work, QQ, LINE). Users interact with their coding agent through their preferred messaging app.

## Architecture

```
┌─────────────────────────────────────────────────┐
│                   cmd/cc-connect                │  ← entry point, CLI, daemon
├─────────────────────────────────────────────────┤
│                     config/                     │  ← TOML config parsing
├─────────────────────────────────────────────────┤
│                      core/                      │  ← engine, interfaces, i18n,
│                                                 │     cards, sessions, registry
├──────────────────────┬──────────────────────────┤
│     agent/           │      platform/           │
│  ├── claudecode/     │  ├── feishu/             │
│  ├── codex/          │  ├── telegram/           │
│  ├── cursor/         │  ├── discord/            │
│  ├── gemini/         │  ├── slack/              │
│  ├── iflow/          │  ├── dingtalk/           │
│  ├── opencode/       │  ├── wecom/              │
│  ├── acp/            │  ├── qq/                 │
│  └── qoder/          │  ├── qqbot/              │
│                      │  ├── line/               │
│                      │  └── weibo/              │
├──────────────────────┴──────────────────────────┤
│                     daemon/                     │  ← systemd/launchd service
└─────────────────────────────────────────────────┘
```

### Key Design Principles

**`core/` is the nucleus.** It defines all interfaces (`Platform`, `Agent`, `AgentSession`, etc.) and contains the `Engine` that orchestrates message flow. The core package must **never** import from `agent/` or `platform/`.

**Plugin architecture via registries.** Agents and platforms register themselves through `core.RegisterAgent()` and `core.RegisterPlatform()` in their `init()` functions. The engine creates instances via `core.CreateAgent()` / `core.CreatePlatform()` using string names from config.

**Dependency direction:**
```
cmd/ → config/, core/, agent/*, platform/*
agent/*   → core/   (never other agents or platforms)
platform/* → core/  (never other platforms or agents)
core/     → stdlib only (never agent/ or platform/)
```

### Core Interfaces

- **`Platform`** — messaging platform adapter (Start, Reply, Send, Stop)
- **`Agent`** — AI coding agent adapter (StartSession, ListSessions, Stop)
- **`AgentSession`** — a running bidirectional session (Send, RespondPermission, Events)
- **`Engine`** — the central orchestrator that routes messages between platforms and agents

Optional capability interfaces (implement only when needed):
- `CardSender` — rich card messages
- `InlineButtonSender` — inline keyboard buttons
- `ProviderSwitcher` — multi-model switching
- `DoctorChecker` — agent-specific health checks
- `AgentDoctorInfo` — CLI binary metadata for diagnostics

## Development Rules

### 1. No Hardcoding Platform or Agent Names in Core

The `core/` package must remain agnostic. Never write `if p.Name() == "feishu"` or `CreateAgent("claudecode", ...)` in core. Use interfaces and capability checks instead:

```go
// BAD — hardcodes platform knowledge in core
if p.Name() == "feishu" && supportsCards(p) {

// GOOD — capability-based check
if supportsCards(p) {
```

```go
// BAD — hardcodes agent type
agent, _ := CreateAgent("claudecode", opts)

// GOOD — derives from current agent
agent, _ := CreateAgent(e.agent.Name(), opts)
```

### 2. Prefer Interfaces Over Type Switches

When behavior differs across platforms/agents, define an optional interface in core and let implementations opt in:

```go
// In core/
type AgentDoctorInfo interface {
    CLIBinaryName() string
    CLIDisplayName() string
}

// In agent/claudecode/
func (a *Agent) CLIBinaryName() string  { return "claude" }
func (a *Agent) CLIDisplayName() string { return "Claude" }

// In core/ — query via interface, fallback gracefully
if info, ok := agent.(AgentDoctorInfo); ok {
    bin = info.CLIBinaryName()
}
```

### 3. Configuration Over Code

- Features that may vary per deployment should be configurable in `config.toml`
- Use `map[string]any` options for agent/platform factories to stay flexible
- Add new config fields with sensible defaults so existing configs don't break

### 4. High Cohesion, Low Coupling

- Each `agent/X/` package is self-contained: it handles process lifecycle, output parsing, and session management for agent X
- Each `platform/X/` package is self-contained: it handles API connection, message receiving/sending, and card rendering for platform X
- Cross-cutting concerns (i18n, cards, streaming, rate limiting) live in `core/`

### 5. Error Handling

- Always wrap errors with context: `fmt.Errorf("feishu: reply card: %w", err)`
- Never silently swallow errors; at minimum log them with `slog.Error` / `slog.Warn`
- Use `slog` (structured logging) consistently; never `log.Printf` or `fmt.Printf` for runtime logs
- Redact tokens/secrets in error messages using `core.RedactToken()`

### 6. Concurrency Safety

- Agent sessions are accessed from multiple goroutines; protect shared state with `sync.Mutex` or `atomic` types
- Use `context.Context` for cancellation propagation
- Channels should have clear ownership; document who closes them
- Prefer `sync.Once` for one-time teardown (`pendingPermission.resolve()`)

### 7. i18n

All user-facing strings must go through `core/i18n.go`:
- Define a `MsgKey` constant
- Add translations for all supported languages (EN, ZH, ZH-TW, JA, ES)
- Use `e.i18n.T(MsgKey)` or `e.i18n.Tf(MsgKey, args...)`

## Code Style

- Follow standard Go conventions (`gofmt`, `go vet`)
- Use `strings.EqualFold` for case-insensitive comparisons
- Avoid `init()` for anything other than platform/agent registration
- Keep functions focused; extract helpers when a function exceeds ~80 lines
- Naming: `New()` for constructors, `Get/Set` for accessors, avoid stuttering (`feishu.FeishuPlatform` → `feishu.Platform`)

## Testing

### Requirements

- All new features must include unit tests.
- **All bug fixes MUST include a regression test in the same PR.** A bug
  fix PR without a test that fails on the pre-fix code and passes on the
  fixed code will not be merged. Name regression tests so the bug is
  searchable later, e.g. `TestSwitchToAgentSession_PreservesHistory` for
  the cmdSwitch history-loss bug.
- Tests must pass before committing: `go test ./...`.
- Changes that touch a Critical User Journey (CUJ) — see
  `core/cuj_test.go` — should explicitly run `go test ./core/ -run TestCUJ`
  before opening the PR.

### Running Tests

```bash
# Full test suite
go test ./...

# Specific package
go test ./core/ -v

# Run specific test
go test ./core/ -run TestHandlePendingPermission -v

# Run Critical User Journey tests (recommended for any core/engine.go or
# core/session.go change)
go test ./core/ -run TestCUJ -v

# With race detector (CI)
go test -race ./...
```

### Test Patterns

- Use stub types for `Platform` and `Agent` in core tests (see `core/engine_test.go`).
- Test card rendering by inspecting the returned `*Card` struct, not JSON.
- For agent session tests, simulate event streams via channels.
- **For multi-step user behavior, add a CUJ test in `core/cuj_test.go`.**
  CUJ tests assert what a USER sees on the platform side across multiple
  actions (e.g. "create s1 → chat → /new s2 → /switch s1 → /history
  must show s1's content"). They exist because per-function unit tests
  can all pass while a user journey is still broken — the `/switch
  loses history` bug shipped in exactly that scenario despite full
  unit coverage of every individual function involved.

### Critical User Journeys (CUJ)

A CUJ test is a USER-perspective end-to-end scenario, not a developer-
perspective unit test. The current inventory of CUJs and their coverage
status lives in:

`projects/cc-connect/agents/qa-cursor/release-gate/CUJ-INVENTORY.md`
(in the spaceship agency workspace; the registered authoritative copy).

Rules for adding/updating CUJ tests in `core/cuj_test.go`:

1. Name: `TestCUJ_<group><id>_<short_camel_case>` (e.g. `TestCUJ_B3_SwitchPreservesHistory`).
2. Use real `SessionManager` + real `Engine`; mock only external boundaries (`Platform` sender, `Agent` process).
3. Drive the engine via `ReceiveMessage` — the same entrypoint platforms use, so engine/platform wiring is also covered.
4. Assert what the USER sees via `p.getSent()`, not internal struct fields.
5. ≥3 user actions per CUJ. A single-action assertion belongs in a unit test, not a CUJ.

When a user-reported bug maps to an existing CUJ, add a sub-case to that
CUJ rather than creating a new one.

## Selective Compilation

Each agent and platform is imported via a separate `plugin_*.go` file with a
build tag (e.g. `//go:build !no_feishu`). By default **all** agents and
platforms are compiled in.

### Include only specific agents/platforms

```bash
# Only Claude Code agent + Feishu and Telegram platforms
make build AGENTS=claudecode PLATFORMS_INCLUDE=feishu,telegram

# Multiple agents
make build AGENTS=claudecode,codex PLATFORMS_INCLUDE=feishu,telegram,discord
```

### Exclude specific agents/platforms

```bash
# Exclude some platforms you don't need
make build EXCLUDE=discord,dingtalk,qq,qqbot,line
```

### Direct build tag usage (without Make)

```bash
go build -tags 'no_discord no_dingtalk no_qq no_qqbot no_line' ./cmd/cc-connect
```

Available tags: `no_acp`, `no_claudecode`, `no_codex`, `no_copilot`, `no_cursor`, `no_gemini`,
`no_iflow`, `no_opencode`, `no_qoder`, `no_feishu`, `no_telegram`,
`no_discord`, `no_slack`, `no_dingtalk`, `no_wecom`, `no_weixin`, `no_qq`, `no_qqbot`,
`no_line`, `no_weibo`.

## Pre-Commit Checklist

1. **Build passes**: `go build ./...`
2. **Tests pass**: `go test ./...`
3. **CUJ tests pass** (for any change in `core/engine.go`, `core/session.go`, `core/cron.go`, `core/timer.go`, or command handlers): `go test ./core/ -run TestCUJ`
4. **Bug fix has a regression test**: a new test in this PR that fails on the pre-fix code and passes on the fix.
5. **No new hardcoded platform/agent names in core**: grep for platform names in `core/*.go`.
6. **i18n complete**: all new user-facing strings have translations for all languages.
7. **No secrets in code**: no API keys, tokens, or credentials in source files.

## Adding a New Platform

1. Create `platform/newplatform/newplatform.go`
2. Implement `core.Platform` interface (and optional interfaces as needed)
3. Register in `init()`: `core.RegisterPlatform("newplatform", factory)`
4. Create `cmd/cc-connect/plugin_platform_newplatform.go` with `//go:build !no_newplatform` tag
5. Add `newplatform` to `ALL_PLATFORMS` in `Makefile`
6. Add config example in `config.example.toml`
7. Add unit tests

## Adding a New Agent

1. Create `agent/newagent/newagent.go`
2. Implement `core.Agent` and `core.AgentSession` interfaces
3. Register in `init()`: `core.RegisterAgent("newagent", factory)`
4. Create `cmd/cc-connect/plugin_agent_newagent.go` with `//go:build !no_newagent` tag
5. Add `newagent` to `ALL_AGENTS` in `Makefile`
6. Optionally implement `AgentDoctorInfo` for `cc-connect doctor` support
7. Add config example in `config.example.toml`
8. Add unit tests
