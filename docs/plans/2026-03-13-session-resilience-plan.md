# Session Resilience Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers-extended-cc:executing-plans to implement this plan task-by-task.

**Goal:** Eliminate silent session context loss in multi-workspace mode by normalizing paths, handling resume failures gracefully, surfacing context consumption to users, and adding diagnostic logging.

**Architecture:** Four independent changes layered bottom-up: path normalization (prevents mismatches), diagnostic logging (makes failures visible), resume fallback (auto-recovers from broken resumes), context indicator (gives users agency over compaction).

**Tech Stack:** Go, Claude Code CLI (stream-json protocol), slog structured logging

**Design doc:** `docs/plans/2026-03-13-session-resilience-design.md`

---

### Task 1: Add `normalizeWorkspacePath` helper

**Files:**
- Modify: `core/workspace_state.go`
- Create: `core/workspace_state_test.go` (add test cases)

**Step 1: Write the failing test**

Add to `core/workspace_state_test.go`:

```go
func TestNormalizeWorkspacePath(t *testing.T) {
	// Create a real temp directory for symlink tests
	tmp := t.TempDir()
	realDir := filepath.Join(tmp, "real-project")
	if err := os.Mkdir(realDir, 0o755); err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(tmp, "link-project")
	if err := os.Symlink(realDir, symlink); err != nil {
		t.Skip("symlinks not supported")
	}

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"trailing slash", realDir + "/", realDir},
		{"double slash", filepath.Join(tmp, "real-project") + "//", realDir},
		{"dot segment", filepath.Join(tmp, ".", "real-project"), realDir},
		{"dotdot segment", filepath.Join(tmp, "real-project", "subdir", ".."), realDir},
		{"symlink resolved", symlink, realDir},
		{"nonexistent uses Clean only", "/nonexistent/path/./foo/../bar", "/nonexistent/path/bar"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeWorkspacePath(tt.input)
			if got != tt.want {
				t.Errorf("normalizeWorkspacePath(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./core/ -run TestNormalizeWorkspacePath -v`
Expected: FAIL — `normalizeWorkspacePath` undefined

**Step 3: Write minimal implementation**

Add to `core/workspace_state.go`:

```go
import (
	"log/slog"
	"os"
	"path/filepath"
)

// normalizeWorkspacePath cleans and resolves a workspace path to prevent
// mismatches caused by trailing slashes, symlinks, or relative segments.
// If the path cannot be resolved (e.g. doesn't exist yet), falls back to
// filepath.Clean only.
func normalizeWorkspacePath(path string) string {
	cleaned := filepath.Clean(path)
	resolved, err := filepath.EvalSymlinks(cleaned)
	if err != nil {
		// Path doesn't exist yet — best effort
		return cleaned
	}
	if resolved != path {
		slog.Debug("workspace path normalized", "original", path, "normalized", resolved)
	}
	return resolved
}
```

**Step 4: Run test to verify it passes**

Run: `go test ./core/ -run TestNormalizeWorkspacePath -v`
Expected: PASS

**Step 5: Commit**

```bash
git add core/workspace_state.go core/workspace_state_test.go
git commit -m "feat: add normalizeWorkspacePath helper for consistent pool keys"
```

---

### Task 2: Apply path normalization at entry points

**Files:**
- Modify: `core/workspace_state.go` — `GetOrCreate`
- Modify: `core/engine.go:5656,5681` — `resolveWorkspace` return values
- Modify: `core/engine.go:993` — `getOrCreateWorkspaceAgent`

**Step 1: Write failing tests**

Add to `core/workspace_state_test.go`:

```go
func TestWorkspacePoolNormalizesKeys(t *testing.T) {
	tmp := t.TempDir()
	realDir := filepath.Join(tmp, "project")
	if err := os.Mkdir(realDir, 0o755); err != nil {
		t.Fatal(err)
	}

	pool := newWorkspacePool(15 * time.Minute)

	// Access with trailing slash
	ws1 := pool.GetOrCreate(realDir + "/")
	// Access without trailing slash
	ws2 := pool.GetOrCreate(realDir)

	if ws1 != ws2 {
		t.Error("trailing slash created a different workspace state")
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./core/ -run TestWorkspacePoolNormalizesKeys -v`
Expected: FAIL — two different states returned

**Step 3: Normalize in `GetOrCreate` and `Get`**

In `core/workspace_state.go`, modify `GetOrCreate`:

```go
func (p *workspacePool) GetOrCreate(workspace string) *workspaceState {
	workspace = normalizeWorkspacePath(workspace)
	p.mu.Lock()
	defer p.mu.Unlock()
	if s, ok := p.states[workspace]; ok {
		return s
	}
	s := newWorkspaceState(workspace)
	p.states[workspace] = s
	return s
}
```

Modify `Get` similarly:

```go
func (p *workspacePool) Get(workspace string) *workspaceState {
	workspace = normalizeWorkspacePath(workspace)
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.states[workspace]
}
```

**Step 4: Normalize in `resolveWorkspace` returns**

In `core/engine.go`, at lines 5656 and 5681 where workspace paths are returned, wrap with normalization:

At line 5656:
```go
return normalizeWorkspacePath(b.Workspace), b.ChannelName, nil
```

At line 5681:
```go
normalized := normalizeWorkspacePath(candidate)
e.workspaceBindings.Bind(projectKey, channelID, channelName, normalized)
slog.Info("workspace auto-bound by convention",
    "channel", channelName, "workspace", normalized)
return normalized, channelName, nil
```

**Step 5: Run tests**

Run: `go test ./core/ -run TestWorkspacePool -v`
Expected: PASS

**Step 6: Commit**

```bash
git add core/workspace_state.go core/workspace_state_test.go core/engine.go
git commit -m "feat: normalize workspace paths at pool and resolution entry points"
```

---

### Task 3: Add token usage fields to Event and parse in session

**Files:**
- Modify: `core/message.go:87-98` — add `InputTokens`, `OutputTokens` to `Event`
- Modify: `agent/claudecode/session.go:268-282` — parse usage from result JSON

**Step 1: Write failing test**

Add to a new file or existing test for claudecode session parsing. Since `handleResult` is on the unexported `claudeSession`, test via the event channel:

Create `agent/claudecode/session_test.go` test (or add to existing):

```go
func TestHandleResultParsesUsage(t *testing.T) {
	// Simulate a result event JSON with usage data
	raw := map[string]any{
		"type":       "result",
		"result":     "test response",
		"session_id": "sess-123",
		"usage": map[string]any{
			"input_tokens":  float64(50000),
			"output_tokens": float64(1500),
		},
	}

	cs := &claudeSession{
		events: make(chan core.Event, 1),
		ctx:    context.Background(),
		done:   make(chan struct{}),
	}
	cs.alive.Store(true)

	cs.handleResult(raw)

	evt := <-cs.events
	if evt.InputTokens != 50000 {
		t.Errorf("InputTokens = %d, want 50000", evt.InputTokens)
	}
	if evt.OutputTokens != 1500 {
		t.Errorf("OutputTokens = %d, want 1500", evt.OutputTokens)
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./agent/claudecode/ -run TestHandleResultParsesUsage -v`
Expected: FAIL — `InputTokens` field doesn't exist on Event

**Step 3: Add fields to Event**

In `core/message.go`, add to the `Event` struct:

```go
InputTokens  int // populated for EventResult — total input tokens this turn
OutputTokens int // populated for EventResult — output tokens this turn
```

**Step 4: Parse usage in handleResult**

In `agent/claudecode/session.go`, modify `handleResult`:

```go
func (cs *claudeSession) handleResult(raw map[string]any) {
	var content string
	if result, ok := raw["result"].(string); ok {
		content = result
	}
	if sid, ok := raw["session_id"].(string); ok && sid != "" {
		cs.sessionID.Store(sid)
	}

	var inputTokens, outputTokens int
	if usage, ok := raw["usage"].(map[string]any); ok {
		if v, ok := usage["input_tokens"].(float64); ok {
			inputTokens = int(v)
		}
		if v, ok := usage["output_tokens"].(float64); ok {
			outputTokens = int(v)
		}
	}

	evt := core.Event{
		Type:         core.EventResult,
		Content:      content,
		SessionID:    cs.CurrentSessionID(),
		Done:         true,
		InputTokens:  inputTokens,
		OutputTokens: outputTokens,
	}
	select {
	case cs.events <- evt:
	case <-cs.ctx.Done():
		return
	}
}
```

**Step 5: Run test to verify it passes**

Run: `go test ./agent/claudecode/ -run TestHandleResultParsesUsage -v`
Expected: PASS

**Step 6: Commit**

```bash
git add core/message.go agent/claudecode/session.go agent/claudecode/session_test.go
git commit -m "feat: parse token usage from Claude Code result events"
```

---

### Task 4: Track context percentage on interactiveState and append to messages

**Files:**
- Modify: `core/engine.go:192-202` — add `inputTokens` field to `interactiveState`
- Modify: `core/engine.go:1326-1352` — track tokens on EventResult
- Modify: `core/engine.go:1354+` — append `[ctx: XX%]` to relayed messages

**Step 1: Add field to interactiveState**

In `core/engine.go`, add to the `interactiveState` struct:

```go
type interactiveState struct {
	// ... existing fields ...
	inputTokens int // last known input_tokens from result event (context size proxy)
}
```

**Step 2: Update EventResult handler to track tokens and append indicator**

In `processInteractiveEvents`, in the `case EventResult:` block (around line 1326), after the existing session ID handling:

```go
case EventResult:
	if event.SessionID != "" {
		session.mu.Lock()
		session.AgentSessionID = event.SessionID
		session.mu.Unlock()
	}

	// Track context consumption
	if event.InputTokens > 0 {
		state.mu.Lock()
		state.inputTokens = event.InputTokens
		state.mu.Unlock()
	}

	fullResponse := event.Content
	if fullResponse == "" && len(textParts) > 0 {
		fullResponse = strings.Join(textParts, "")
	}
	if fullResponse == "" {
		fullResponse = e.i18n.T(MsgEmptyResponse)
	}

	// Append context indicator
	if event.InputTokens > 0 {
		pct := event.InputTokens * 100 / 200_000
		fullResponse += fmt.Sprintf("\n[ctx: %d%%]", pct)
	}

	// ... rest of existing EventResult handling
```

**Step 3: Also append indicator to intermediate messages (tool use, thinking)**

For every visible message sent during a turn, we need the indicator. The simplest approach: store the last known percentage on the state, and have a helper:

```go
func contextIndicator(inputTokens int) string {
	if inputTokens <= 0 {
		return ""
	}
	pct := inputTokens * 100 / 200_000
	return fmt.Sprintf("\n[ctx: %d%%]", pct)
}
```

Append `contextIndicator(state.inputTokens)` to every `e.send()` call in the event loop for EventThinking, EventToolUse, and EventResult. Read `state.inputTokens` under the state mutex that's already being acquired.

**Step 4: Run existing tests**

Run: `go test ./core/ -v -count=1`
Expected: PASS (no test changes needed — this is additive to message content)

**Step 5: Commit**

```bash
git add core/engine.go
git commit -m "feat: append context consumption indicator [ctx: XX%] to relayed messages"
```

---

### Task 5: Add system prompt instruction for Claude self-reporting

**Files:**
- Modify: `core/interfaces.go:36-81` — append context self-report instruction to `AgentSystemPrompt()`

**Step 1: Add instruction to system prompt**

At the end of the `AgentSystemPrompt()` return string, before the closing backtick, add:

```go
## Context awareness
At the end of every message you send, append your estimate of your context window consumption as: [ctx: ~XX%]
This helps the user decide when to run /compact. Be honest — if you're unsure, estimate conservatively.
```

**Step 2: Run existing tests**

Run: `go test ./core/ -v -count=1`
Expected: PASS

**Step 3: Commit**

```bash
git add core/interfaces.go
git commit -m "feat: instruct agent to self-report context usage for comparison logging"
```

---

### Task 6: Add dual-track context logging

**Files:**
- Modify: `core/engine.go` — EventResult handler, add structured log with both values

**Step 1: Parse self-reported percentage from response**

Add helper in `core/engine.go`:

```go
import "regexp"

var ctxSelfReportRe = regexp.MustCompile(`\[ctx:\s*~?(\d+)%\]`)

// parseSelfReportedCtx extracts the self-reported context percentage from a response.
// Returns -1 if not found.
func parseSelfReportedCtx(response string) int {
	m := ctxSelfReportRe.FindStringSubmatch(response)
	if m == nil {
		return -1
	}
	v, _ := strconv.Atoi(m[1])
	return v
}
```

**Step 2: Write test for parser**

```go
func TestParseSelfReportedCtx(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		{"some response\n[ctx: ~45%]", 45},
		{"response [ctx: 80%]", 80},
		{"no indicator", -1},
		{"[ctx: ~100%] mid-text", 100},
	}
	for _, tt := range tests {
		got := parseSelfReportedCtx(tt.input)
		if got != tt.want {
			t.Errorf("parseSelfReportedCtx(%q) = %d, want %d", tt.input, got, tt.want)
		}
	}
}
```

**Step 3: Run test to verify it fails, implement, verify pass**

Run: `go test ./core/ -run TestParseSelfReportedCtx -v`

**Step 4: Add structured logging in EventResult handler**

After computing the SDK percentage but before appending the indicator, add:

```go
if event.InputTokens > 0 {
	sdkPct := event.InputTokens * 100 / 200_000
	selfPct := parseSelfReportedCtx(fullResponse)
	slog.Info("context_usage",
		"session_key", sessionKey,
		"sdk_pct", sdkPct,
		"self_reported_pct", selfPct,
		"input_tokens", event.InputTokens,
		"output_tokens", event.OutputTokens,
	)
}
```

**Step 5: Strip Claude's self-reported indicator before appending the real one**

So we don't show duplicate indicators, strip the self-reported one from the response before appending the SDK-based one:

```go
// Strip self-reported indicator (we replace it with the accurate SDK one)
fullResponse = ctxSelfReportRe.ReplaceAllString(fullResponse, "")
fullResponse = strings.TrimRight(fullResponse, "\n ")
fullResponse += fmt.Sprintf("\n[ctx: %d%%]", sdkPct)
```

**Step 6: Commit**

```bash
git add core/engine.go core/engine_test.go
git commit -m "feat: dual-track context usage logging (SDK vs self-reported)"
```

---

### Task 7: Add diagnostic logging to session lifecycle

**Files:**
- Modify: `core/engine.go:1097-1119` — spawn/resume logging
- Modify: `core/engine.go:259-283` — reap logging
- Modify: `agent/claudecode/session.go:42-117` — spawn logging with JSONL path/size
- Modify: `agent/claudecode/claudecode.go:195-224` — log cwd at StartSession

**Step 1: Enhanced spawn logging in `getOrCreateInteractiveStateWith`**

Replace the existing log at line 1118 with:

```go
slog.Info("session spawned",
	"session_key", sessionKey,
	"agent_session", session.AgentSessionID,
	"is_resume", session.AgentSessionID != "",
	"elapsed", startElapsed,
)
```

**Step 2: Add JSONL file size logging in `claudecode.StartSession`**

In `agent/claudecode/claudecode.go`, in `StartSession`, before calling `newClaudeSession`, add:

```go
if sessionID != "" {
	// Log session file details for diagnostics
	homeDir, _ := os.UserHomeDir()
	absWorkDir, _ := filepath.Abs(a.workDir)
	if homeDir != "" {
		projectDir := findProjectDir(homeDir, absWorkDir)
		sessionFile := filepath.Join(projectDir, sessionID+".jsonl")
		if info, err := os.Stat(sessionFile); err == nil {
			slog.Info("session resume attempt",
				"session_id", sessionID,
				"jsonl_path", sessionFile,
				"jsonl_size_bytes", info.Size(),
				"work_dir", absWorkDir,
			)
		} else {
			slog.Warn("session file not found for resume",
				"session_id", sessionID,
				"expected_path", sessionFile,
				"work_dir", absWorkDir,
				"error", err,
			)
		}
	}
}
```

**Step 3: Enhanced reap logging in `runIdleReaper`**

In `core/engine.go`, in the reap loop (around line 271), add idle duration:

```go
reaped := e.workspacePool.ReapIdle()
for _, ws := range reaped {
	e.interactiveMu.Lock()
	for key, state := range e.interactiveStates {
		if state.workspaceDir == ws {
			state.mu.Lock()
			tokenCount := state.inputTokens
			state.mu.Unlock()
			slog.Info("session idle-reaped",
				"session_key", key,
				"workspace", ws,
				"last_ctx_pct", tokenCount*100/200_000,
				"input_tokens", tokenCount,
			)
			if state.agentSession != nil {
				state.agentSession.Close()
			}
			delete(e.interactiveStates, key)
		}
	}
```

**Step 4: Log stderr on session process failure**

In `agent/claudecode/session.go`, the `readLoop` already logs stderr on failure (line 125). Enhance it:

```go
slog.Error("claudeSession: process failed",
	"error", err,
	"stderr", stderrMsg,
	"work_dir", cs.workDir,
	"session_id", cs.CurrentSessionID(),
)
```

**Step 5: Run all tests**

Run: `go test ./core/ ./agent/claudecode/ -v -count=1`
Expected: PASS

**Step 6: Commit**

```bash
git add core/engine.go agent/claudecode/session.go agent/claudecode/claudecode.go
git commit -m "feat: add diagnostic logging for session spawn, resume, reap, and failure"
```

---

### Task 8: Resume failure fallback with user notification

**Files:**
- Modify: `core/engine.go:1097-1105` — retry logic in `getOrCreateInteractiveStateWith`

**Step 1: Write failing test**

Add to `core/engine_test.go`:

```go
func TestResumeFailureFallsBackToFreshSession(t *testing.T) {
	callCount := 0
	agent := &stubAgent{
		startSessionFunc: func(ctx context.Context, sessionID string) (core.AgentSession, error) {
			callCount++
			if sessionID != "" {
				// Simulate resume failure
				return nil, fmt.Errorf("Prompt is too long")
			}
			// Fresh session succeeds
			return &stubAgentSession{alive: true}, nil
		},
	}

	e := newTestEngine(t)
	e.agent = agent

	session := e.sessions.GetOrCreateActive("test:chan:user")
	session.AgentSessionID = "old-session-id"

	p := &stubPlatform{}
	state := e.getOrCreateInteractiveState("test:chan:user", p, nil, session)

	if state.agentSession == nil {
		t.Fatal("expected agentSession to be non-nil after fallback")
	}
	if callCount != 2 {
		t.Errorf("expected 2 StartSession calls (resume + fresh), got %d", callCount)
	}
	if session.AgentSessionID != "" {
		t.Errorf("expected AgentSessionID cleared, got %q", session.AgentSessionID)
	}
}
```

Note: This test may need adjustment to match the actual test helpers in the codebase. Check `core/engine_test.go` for the existing `stubAgent` and `newTestEngine` patterns and adapt accordingly.

**Step 2: Run test to verify it fails**

Run: `go test ./core/ -run TestResumeFailureFallsBackToFreshSession -v`
Expected: FAIL — current code doesn't retry

**Step 3: Implement retry logic**

Replace the error handling block in `getOrCreateInteractiveStateWith` (lines 1100-1104):

```go
startAt := time.Now()
agentSession, err := agent.StartSession(e.ctx, session.AgentSessionID)
startElapsed := time.Since(startAt)
if err != nil {
	if session.AgentSessionID != "" {
		// Resume failed — log diagnostics and retry with fresh session
		slog.Error("session resume failed, falling back to fresh session",
			"session_key", sessionKey,
			"failed_session_id", session.AgentSessionID,
			"error", err,
			"elapsed", startElapsed,
		)

		// Clear the stale session ID
		session.mu.Lock()
		session.AgentSessionID = ""
		session.mu.Unlock()

		// Notify user
		if p != nil {
			go func() {
				_ = p.Send(context.Background(), replyCtx,
					"⚠️ Session context was too large to resume — starting fresh. Project context is preserved in CLAUDE.md.")
			}()
		}

		// Retry with fresh session
		freshStart := time.Now()
		agentSession, err = agent.StartSession(e.ctx, "")
		freshElapsed := time.Since(freshStart)
		if err != nil {
			slog.Error("fresh session also failed",
				"session_key", sessionKey,
				"error", err,
				"elapsed", freshElapsed,
			)
			state = &interactiveState{platform: p, replyCtx: replyCtx, quiet: quietMode}
			e.interactiveStates[sessionKey] = state
			return state
		}
		slog.Info("fresh session started after resume failure",
			"session_key", sessionKey,
			"elapsed", freshElapsed,
		)
	} else {
		slog.Error("failed to start interactive session",
			"session_key", sessionKey,
			"error", err,
			"elapsed", startElapsed,
		)
		state = &interactiveState{platform: p, replyCtx: replyCtx, quiet: quietMode}
		e.interactiveStates[sessionKey] = state
		return state
	}
}
```

**Step 4: Run test to verify it passes**

Run: `go test ./core/ -run TestResumeFailureFallsBackToFreshSession -v`
Expected: PASS

**Step 5: Run full test suite**

Run: `go test ./core/ ./agent/claudecode/ -v -count=1`
Expected: PASS

**Step 6: Commit**

```bash
git add core/engine.go core/engine_test.go
git commit -m "feat: auto-recover from resume failure with fresh session and user notification"
```

---

### Task 9: Final integration verification

**Files:** None — verification only

**Step 1: Run full test suite**

Run: `go test ./... -count=1`
Expected: PASS

**Step 2: Verify build**

Run: `go build ./...`
Expected: no errors

**Step 3: Review all changes**

Run: `git log --oneline main..HEAD`

Verify the commit sequence matches the plan:
1. `normalizeWorkspacePath` helper
2. Apply normalization at entry points
3. Token usage fields + parsing
4. Context indicator on messages
5. System prompt self-report instruction
6. Dual-track logging
7. Diagnostic lifecycle logging
8. Resume failure fallback

**Step 4: Final commit (if any fixups needed)**

```bash
git add -A && git commit -m "fix: address issues found during integration verification"
```
