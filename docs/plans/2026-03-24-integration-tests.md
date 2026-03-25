# Integration Test Plan

Integration tests verify real agent-platform interactions using actual agent binaries
with a mock platform. Tests are gated by `//go:build integration` and excluded from
normal CI. Run with:

```bash
go test -tags=integration ./tests/integration/...
```

## Philosophy

- **Real agents, mocked platform**: Agents run as real subprocesses; platform is mocked
  to record and verify all messages without network dependencies.
- **Agent pooling**: Agent instances are reused across tests to avoid per-test startup
  overhead (Claude Code cold start ~3-6s).
- **Resilient assertions**: Use case-insensitive substring matching, generous timeouts,
  and skip agents that fail due to auth/infra issues (e.g., OpenCode needs GitLab token).

---

## Implemented Cases

### Session Management
- [x] `TestNewSession_ClaudeCode` — New session spawns, agent responds
- [x] `TestNewSession_Codex` — Same for Codex
- [x] `TestListSessions_ShowsActiveSessions` — `/list` shows active sessions
- [x] `TestSwitchSession` — `/switch` changes active session
- [x] `TestStopCommand` — `/stop` interrupts active session
- [x] `TestNewSessionClearsContext` — After `/new`, prior context is cleared
- [x] `TestHistoryCommand` — `/history` returns conversation history
- [x] `TestConcurrentSessionIsolation` — Two sessions don't cross-talk

### Agent Interaction
- [x] `TestEventParsing_ThinkToolUse` — Tool calls (echo) are parsed and produce output
- [x] `TestMarkdownLongTextChunking` — Long responses chunked correctly
- [x] `TestPermissionModeSwitch` — `/mode yolo` and `/mode default` work
- [x] `TestAgentCodex` — Codex agent responds
- [x] `TestAgentCursor` — Cursor agent responds (⚠️ may respond in locale)
- [x] `TestAgentGemini` — ⚠️ Fails due to quota exhaustion in CI env
- [x] `TestAgentOpencode` — ⚠️ Requires GitLab auth, skipped
- [x] `TestSharedCasesAcrossAgents` — Same prompts validated across agents
- [x] `TestLongTextChunking` — Very long user input (5k+ chars) processed

### Commands & Built-ins
- [x] `TestShellCommand` — `/shell` executes (skips if `admin_from` not set)
- [x] `TestProviderSwitch` — `/provider list` works

### i18n
- [x] `TestLanguageSwitch` — `/lang zh` changes language (⚠️ may skip due to locale)
- [x] `TestEmptyMessage` — Whitespace-only messages handled gracefully

### Message Handling
- [x] `TestImageAttachmentRouting` — Image-bearing messages routed (⚠️ needs real image)

---

## Planned Cases (not yet implemented)

### Multi-Agent / Provider
- [ ] `TestSessionResume` — Send `/new`, reconnect to same session key, verify context preserved
- [ ] `TestProviderSwitchActual` — `/provider switch <name>` actually changes provider mid-session
- [ ] `TestModelSwitch` — `/model <name>` changes model; responses reflect new model
- [ ] `TestConcurrentMultiAgent` — Two different agent types active simultaneously

### Commands & Built-ins
- [ ] `TestCustomCommand` — Register and invoke a custom command
- [ ] `TestAliasCommand` — Create/use alias; verify substitution
- [ ] `TestDirCommand` — `/dir` navigates workspace; agent respects new directory
- [ ] `TestSearchCommand` — `/search <query>` invokes search

### Permission & Safety
- [ ] `TestPermissionPromptBypass` — `yolo` mode bypasses permission prompts; `default` shows them
- [ ] `TestSensitiveMessageRedaction` — Tokens/secrets in user input are redacted
- [ ] `TestBannedWordBlocking` — Banned-word messages rejected with feedback

### Message Handling
- [ ] `TestFileAttachmentRouting` — File attachments reach agent
- [ ] `TestVoiceMessageHandling` — Voice messages transcribed/processed or gracefully rejected
- [ ] `TestMarkdownParsing` — Markdown in agent responses rendered correctly

### Rate Limiting & Performance
- [ ] `TestIncomingRateLimit` — Rapid messages rate-limited; excess queued/rejected
- [ ] `TestOutgoingRateLimit` — Rapid agent output respects platform limits
- [ ] `TestSlowAgentTimeout` — Slow agent (>idle timeout) flagged or session reaped

### i18n
- [ ] `TestMultiLanguageResponses` — Same prompt in different language configs produces localized responses

### Error & Edge Cases
- [ ] `TestBadAgentOutput` — Malformed agent output handled gracefully (no panic)
- [ ] `TestAgentCrashRecovery` — Agent dies mid-session; engine detects, notifies, allows respawn
- [ ] `TestVeryLongAgentResponse` — Extremely long response (>50k chars); chunking works, no OOM
- [ ] `TestConcurrentSessionCreation` — Rapid session creation; keys unique, no state leakage

### ACP / Relay
- [ ] `TestACPMessageRelay` — ACP message relayed to agent; response returns via correct channel
- [ ] `TestRelaySessionKeyPreservation` — `CC_SESSION_KEY` propagated through relay; session continuity maintained

---

## Notes

- **Timeout guidelines**: Simple prompts ("say hi") — 30s; tool use — 60s; slow agents
  (gemini, opencode) — 90s; long output — 120s.
- **Skip vs Fail**: Auth/infra failures (`Skip`) are expected in some environments;
  code bugs should `Fatal`. Use `t.Skipf("reason")` for expected env issues.
- **Agent pool reuse**: The pool key includes `workDir`, so tests using the same workDir
  share the same agent instance. Use `t.TempDir()` for isolation.
- **Parallel tests**: Use `t.Parallel()` for independent tests. Avoid parallel subtests
  that share session keys to prevent race conditions.
