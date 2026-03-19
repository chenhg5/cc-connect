# Multi-Workspace Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers-extended-cc:executing-plans to implement this plan task-by-task.

**Goal:** Enable a single cc-connect bot to serve multiple workspaces, routing messages to different Claude Code sessions based on Slack channel.

**Architecture:** Engine-level multiplexing. Add workspace resolution to Engine.handleMessage that maps channel → workspace directory, spawns/resumes per-workspace agent subprocesses, and manages idle reaping. Gated behind `mode = "multi-workspace"` in ProjectConfig so existing single-workspace projects are unaffected.

**Tech Stack:** Go, Slack API (conversations.info for channel name resolution), JSON file persistence

**Design Doc:** `docs/plans/2026-03-12-multi-workspace-design.md`

---

### Task 1: Add config fields

**Files:**
- Modify: `config/config.go` (ProjectConfig struct ~line 103, validate() ~line 177)
- Modify: `config.example.toml` (add multi-workspace example)

**Step 1: Add Mode and BaseDir to ProjectConfig**

In `config/config.go`, add two fields to ProjectConfig (after line 105):

```go
type ProjectConfig struct {
	Name             string           `toml:"name"`
	Mode             string           `toml:"mode,omitempty"`     // "" or "multi-workspace"
	BaseDir          string           `toml:"base_dir,omitempty"` // parent dir for workspaces
	Agent            AgentConfig      `toml:"agent"`
	Platforms        []PlatformConfig `toml:"platforms"`
	Quiet            *bool            `toml:"quiet,omitempty"`
	DisabledCommands []string         `toml:"disabled_commands,omitempty"`
}
```

**Step 2: Add validation for multi-workspace mode**

In `config/config.go` validate(), after the existing platform checks (~line 196), add:

```go
if proj.Mode == "multi-workspace" {
	if proj.BaseDir == "" {
		return fmt.Errorf("project %q: multi-workspace mode requires base_dir", proj.Name)
	}
	if _, ok := proj.Agent.Options["work_dir"]; ok {
		return fmt.Errorf("project %q: multi-workspace mode conflicts with agent work_dir (use base_dir instead)", proj.Name)
	}
}
```

**Step 3: Add example to config.example.toml**

Add a commented multi-workspace example section showing the config pattern from the design doc.

**Step 4: Run existing tests**

Run: `go build ./...`
Expected: Compiles cleanly

**Step 5: Commit**

```bash
git add config/config.go config.example.toml
git commit -m "feat: add multi-workspace mode and base_dir config fields"
```

---

### Task 2: Workspace binding persistence

**Files:**
- Create: `core/workspace_binding.go`
- Create: `core/workspace_binding_test.go`

**Step 1: Write tests for WorkspaceBindingManager**

```go
package core

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWorkspaceBindingManager_SaveLoad(t *testing.T) {
	dir := t.TempDir()
	storePath := filepath.Join(dir, "bindings.json")

	mgr := NewWorkspaceBindingManager(storePath)
	mgr.Bind("project:claude", "C123", "my-channel", "/home/user/workspace/my-channel")

	b := mgr.Lookup("project:claude", "C123")
	if b == nil {
		t.Fatal("expected binding, got nil")
	}
	if b.ChannelName != "my-channel" {
		t.Errorf("expected channel name 'my-channel', got %q", b.ChannelName)
	}
	if b.Workspace != "/home/user/workspace/my-channel" {
		t.Errorf("expected workspace path, got %q", b.Workspace)
	}

	// Reload from disk
	mgr2 := NewWorkspaceBindingManager(storePath)
	b2 := mgr2.Lookup("project:claude", "C123")
	if b2 == nil {
		t.Fatal("expected binding after reload, got nil")
	}
	if b2.Workspace != "/home/user/workspace/my-channel" {
		t.Errorf("expected workspace path after reload, got %q", b2.Workspace)
	}
}

func TestWorkspaceBindingManager_Unbind(t *testing.T) {
	dir := t.TempDir()
	storePath := filepath.Join(dir, "bindings.json")

	mgr := NewWorkspaceBindingManager(storePath)
	mgr.Bind("project:claude", "C123", "chan", "/path")
	mgr.Unbind("project:claude", "C123")

	if b := mgr.Lookup("project:claude", "C123"); b != nil {
		t.Error("expected nil after unbind")
	}
}

func TestWorkspaceBindingManager_ListByProject(t *testing.T) {
	dir := t.TempDir()
	mgr := NewWorkspaceBindingManager(filepath.Join(dir, "bindings.json"))
	mgr.Bind("project:claude", "C1", "chan1", "/path1")
	mgr.Bind("project:claude", "C2", "chan2", "/path2")
	mgr.Bind("project:other", "C3", "chan3", "/path3")

	list := mgr.ListByProject("project:claude")
	if len(list) != 2 {
		t.Errorf("expected 2 bindings, got %d", len(list))
	}
}
```

**Step 2: Run tests to verify they fail**

Run: `go test ./core/ -run TestWorkspaceBinding -v`
Expected: FAIL (types not defined)

**Step 3: Implement WorkspaceBindingManager**

```go
package core

import (
	"encoding/json"
	"log/slog"
	"os"
	"sync"
	"time"
)

// WorkspaceBinding maps a channel to a workspace directory.
type WorkspaceBinding struct {
	ChannelName string    `json:"channel_name"`
	Workspace   string    `json:"workspace"`
	BoundAt     time.Time `json:"bound_at"`
}

// WorkspaceBindingManager persists channel→workspace mappings.
// Top-level key is "project:<name>", second-level key is channel ID.
type WorkspaceBindingManager struct {
	mu        sync.RWMutex
	bindings  map[string]map[string]*WorkspaceBinding // projectKey → channelID → binding
	storePath string
}

func NewWorkspaceBindingManager(storePath string) *WorkspaceBindingManager {
	m := &WorkspaceBindingManager{
		bindings:  make(map[string]map[string]*WorkspaceBinding),
		storePath: storePath,
	}
	if storePath != "" {
		m.load()
	}
	return m
}

func (m *WorkspaceBindingManager) Bind(projectKey, channelID, channelName, workspace string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.bindings[projectKey] == nil {
		m.bindings[projectKey] = make(map[string]*WorkspaceBinding)
	}
	m.bindings[projectKey][channelID] = &WorkspaceBinding{
		ChannelName: channelName,
		Workspace:   workspace,
		BoundAt:     time.Now(),
	}
	m.saveLocked()
}

func (m *WorkspaceBindingManager) Unbind(projectKey, channelID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if proj := m.bindings[projectKey]; proj != nil {
		delete(proj, channelID)
		if len(proj) == 0 {
			delete(m.bindings, projectKey)
		}
	}
	m.saveLocked()
}

func (m *WorkspaceBindingManager) Lookup(projectKey, channelID string) *WorkspaceBinding {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if proj := m.bindings[projectKey]; proj != nil {
		return proj[channelID]
	}
	return nil
}

func (m *WorkspaceBindingManager) ListByProject(projectKey string) map[string]*WorkspaceBinding {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make(map[string]*WorkspaceBinding)
	if proj := m.bindings[projectKey]; proj != nil {
		for k, v := range proj {
			result[k] = v
		}
	}
	return result
}

func (m *WorkspaceBindingManager) saveLocked() {
	if m.storePath == "" {
		return
	}
	data, err := json.MarshalIndent(m.bindings, "", "  ")
	if err != nil {
		slog.Error("workspace bindings: marshal error", "err", err)
		return
	}
	if err := AtomicWriteFile(m.storePath, data, 0o644); err != nil {
		slog.Error("workspace bindings: save error", "err", err)
	}
}

func (m *WorkspaceBindingManager) load() {
	data, err := os.ReadFile(m.storePath)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Error("workspace bindings: load error", "err", err)
		}
		return
	}
	if err := json.Unmarshal(data, &m.bindings); err != nil {
		slog.Error("workspace bindings: unmarshal error", "err", err)
	}
}
```

Note: This follows the exact same pattern as `core/relay.go` RelayManager persistence.

**Step 4: Run tests to verify they pass**

Run: `go test ./core/ -run TestWorkspaceBinding -v`
Expected: PASS

**Step 5: Commit**

```bash
git add core/workspace_binding.go core/workspace_binding_test.go
git commit -m "feat: add WorkspaceBindingManager for channel-to-workspace persistence"
```

---

### Task 3: Workspace state and idle reaper

**Files:**
- Create: `core/workspace_state.go`
- Create: `core/workspace_state_test.go`

**Step 1: Write tests for workspacePool**

```go
package core

import (
	"context"
	"testing"
	"time"
)

func TestWorkspacePool_GetOrCreate(t *testing.T) {
	pool := newWorkspacePool(15 * time.Minute)

	state1 := pool.GetOrCreate("/workspace/a")
	state2 := pool.GetOrCreate("/workspace/a")
	state3 := pool.GetOrCreate("/workspace/b")

	if state1 != state2 {
		t.Error("expected same state for same workspace")
	}
	if state1 == state3 {
		t.Error("expected different state for different workspace")
	}
}

func TestWorkspacePool_Touch(t *testing.T) {
	pool := newWorkspacePool(15 * time.Minute)
	state := pool.GetOrCreate("/workspace/a")

	before := state.LastActivity()
	time.Sleep(10 * time.Millisecond)
	state.Touch()
	after := state.LastActivity()

	if !after.After(before) {
		t.Error("expected lastActivity to advance after Touch()")
	}
}

func TestWorkspacePool_ReapIdle(t *testing.T) {
	pool := newWorkspacePool(50 * time.Millisecond)
	pool.GetOrCreate("/workspace/a")

	time.Sleep(100 * time.Millisecond)
	reaped := pool.ReapIdle()

	if len(reaped) != 1 || reaped[0] != "/workspace/a" {
		t.Errorf("expected [/workspace/a] reaped, got %v", reaped)
	}

	if s := pool.Get("/workspace/a"); s != nil {
		t.Error("expected workspace removed after reap")
	}
}
```

**Step 2: Run tests to verify they fail**

Run: `go test ./core/ -run TestWorkspacePool -v`
Expected: FAIL

**Step 3: Implement workspacePool and workspaceState**

```go
package core

import (
	"sync"
	"time"
)

// workspaceState holds the runtime state for a single workspace.
type workspaceState struct {
	mu           sync.Mutex
	workspace    string
	sessions     *SessionManager
	lastActivity time.Time
}

func newWorkspaceState(workspace string, sessions *SessionManager) *workspaceState {
	return &workspaceState{
		workspace:    workspace,
		sessions:     sessions,
		lastActivity: time.Now(),
	}
}

func (ws *workspaceState) Touch() {
	ws.mu.Lock()
	ws.lastActivity = time.Now()
	ws.mu.Unlock()
}

func (ws *workspaceState) LastActivity() time.Time {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	return ws.lastActivity
}

// workspacePool manages a set of workspace states with idle reaping.
type workspacePool struct {
	mu         sync.RWMutex
	states     map[string]*workspaceState // workspace path → state
	idleTimeout time.Duration
}

func newWorkspacePool(idleTimeout time.Duration) *workspacePool {
	return &workspacePool{
		states:      make(map[string]*workspaceState),
		idleTimeout: idleTimeout,
	}
}

func (p *workspacePool) Get(workspace string) *workspaceState {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.states[workspace]
}

func (p *workspacePool) GetOrCreate(workspace string) *workspaceState {
	p.mu.Lock()
	defer p.mu.Unlock()
	if s, ok := p.states[workspace]; ok {
		return s
	}
	s := newWorkspaceState(workspace, nil) // SessionManager set later by Engine
	p.states[workspace] = s
	return s
}

// ReapIdle removes and returns workspace paths that have been idle longer than idleTimeout.
func (p *workspacePool) ReapIdle() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	cutoff := time.Now().Add(-p.idleTimeout)
	var reaped []string
	for path, state := range p.states {
		if state.LastActivity().Before(cutoff) {
			reaped = append(reaped, path)
			delete(p.states, path)
		}
	}
	return reaped
}

func (p *workspacePool) All() map[string]*workspaceState {
	p.mu.RLock()
	defer p.mu.RUnlock()
	result := make(map[string]*workspaceState, len(p.states))
	for k, v := range p.states {
		result[k] = v
	}
	return result
}
```

**Step 4: Run tests to verify they pass**

Run: `go test ./core/ -run TestWorkspacePool -v`
Expected: PASS

**Step 5: Commit**

```bash
git add core/workspace_state.go core/workspace_state_test.go
git commit -m "feat: add workspacePool for managing per-workspace agent state"
```

---

### Task 4: Channel name resolution in Slack platform

**Files:**
- Modify: `platform/slack/slack.go` (~line 25 replyContext, ~line 102 handleEvent)
- Modify: `core/interfaces.go` (Platform interface — check if ChannelNameResolver needed)

**Step 1: Add ChannelNameResolver interface**

In `core/interfaces.go`, add a new optional interface:

```go
// ChannelNameResolver is an optional interface for platforms that can resolve
// channel IDs to human-readable names.
type ChannelNameResolver interface {
	ResolveChannelName(channelID string) (string, error)
}
```

**Step 2: Implement in Slack platform**

In `platform/slack/slack.go`, add a channel name cache and resolver:

```go
// Add to Platform struct:
channelNameCache map[string]string
channelCacheMu   sync.RWMutex

// Initialize in New() or Start():
p.channelNameCache = make(map[string]string)

// Add method:
func (p *Platform) ResolveChannelName(channelID string) (string, error) {
	p.channelCacheMu.RLock()
	if name, ok := p.channelNameCache[channelID]; ok {
		p.channelCacheMu.RUnlock()
		return name, nil
	}
	p.channelCacheMu.RUnlock()

	info, err := p.client.GetConversationInfo(&slack.GetConversationInfoInput{
		ChannelID: channelID,
	})
	if err != nil {
		return "", fmt.Errorf("slack: resolve channel name for %s: %w", channelID, err)
	}

	p.channelCacheMu.Lock()
	p.channelNameCache[channelID] = info.Name
	p.channelCacheMu.Unlock()

	return info.Name, nil
}
```

**Step 3: Build to verify compilation**

Run: `go build ./...`
Expected: Compiles cleanly

**Step 4: Commit**

```bash
git add core/interfaces.go platform/slack/slack.go
git commit -m "feat: add ChannelNameResolver interface and Slack implementation"
```

---

### Task 5: Engine multi-workspace fields and constructor

**Files:**
- Modify: `core/engine.go` (Engine struct ~line 119, NewEngine ~line 200)

**Step 1: Add multi-workspace fields to Engine struct**

After `eventIdleTimeout` (~line 168), add:

```go
// Multi-workspace mode
multiWorkspace    bool
baseDir           string
workspaceBindings *WorkspaceBindingManager
workspacePool     *workspacePool
initFlows         map[string]*workspaceInitFlow // channelID → init state
initFlowsMu       sync.Mutex
```

Add the init flow state struct:

```go
type workspaceInitFlow struct {
	state    string // "awaiting_url", "awaiting_confirm"
	repoURL  string
	cloneTo  string
}
```

**Step 2: Add SetMultiWorkspace method**

```go
func (e *Engine) SetMultiWorkspace(baseDir, bindingStorePath string) {
	e.multiWorkspace = true
	e.baseDir = baseDir
	e.workspaceBindings = NewWorkspaceBindingManager(bindingStorePath)
	e.workspacePool = newWorkspacePool(15 * time.Minute)
	e.initFlows = make(map[string]*workspaceInitFlow)
	go e.runIdleReaper()
}
```

**Step 3: Implement idle reaper goroutine**

```go
func (e *Engine) runIdleReaper() {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-e.ctx.Done():
			return
		case <-ticker.C:
			reaped := e.workspacePool.ReapIdle()
			for _, ws := range reaped {
				// Stop interactive states for this workspace
				e.interactiveMu.Lock()
				for key, state := range e.interactiveStates {
					if state.workspaceDir == ws {
						if state.agentSession != nil {
							state.agentSession.Close()
						}
						delete(e.interactiveStates, key)
					}
				}
				e.interactiveMu.Unlock()
				slog.Info("workspace idle-reaped", "workspace", ws)
			}
		}
	}
}
```

Note: This requires adding `workspaceDir string` to the `interactiveState` struct (~line 174).

**Step 4: Build to verify compilation**

Run: `go build ./...`
Expected: Compiles cleanly

**Step 5: Commit**

```bash
git add core/engine.go
git commit -m "feat: add multi-workspace fields, SetMultiWorkspace, and idle reaper to Engine"
```

---

### Task 6: Workspace resolution logic

**Files:**
- Modify: `core/engine.go`

**Step 1: Implement resolveWorkspace method**

Add this method to Engine. It returns the workspace path or empty string if init flow is needed:

```go
// resolveWorkspace resolves a channel to a workspace directory.
// Returns (workspacePath, channelName, error).
// If workspacePath is empty, the init flow should be triggered.
func (e *Engine) resolveWorkspace(p Platform, channelID string) (string, string, error) {
	projectKey := "project:" + e.name

	// Step 1: Check existing binding
	if b := e.workspaceBindings.Lookup(projectKey, channelID); b != nil {
		// Verify workspace directory still exists
		if _, err := os.Stat(b.Workspace); err != nil {
			slog.Warn("bound workspace directory missing, removing binding",
				"workspace", b.Workspace, "channel", channelID)
			e.workspaceBindings.Unbind(projectKey, channelID)
			return "", b.ChannelName, nil
		}
		return b.Workspace, b.ChannelName, nil
	}

	// Step 2: Resolve channel name for convention match
	channelName := ""
	if resolver, ok := p.(ChannelNameResolver); ok {
		name, err := resolver.ResolveChannelName(channelID)
		if err != nil {
			slog.Warn("failed to resolve channel name", "channel", channelID, "err", err)
		} else {
			channelName = name
		}
	}

	if channelName == "" {
		return "", "", nil
	}

	// Step 3: Convention match — check if base_dir/<channel-name> exists
	candidate := filepath.Join(e.baseDir, channelName)
	if info, err := os.Stat(candidate); err == nil && info.IsDir() {
		// Auto-bind with feedback
		e.workspaceBindings.Bind(projectKey, channelID, channelName, candidate)
		slog.Info("workspace auto-bound by convention",
			"channel", channelName, "workspace", candidate)
		return candidate, channelName, nil
	}

	return "", channelName, nil
}
```

**Step 2: Build to verify**

Run: `go build ./...`
Expected: Compiles cleanly

**Step 3: Commit**

```bash
git add core/engine.go
git commit -m "feat: add resolveWorkspace method for channel-to-directory mapping"
```

---

### Task 7: Init flow conversation handler

**Files:**
- Modify: `core/engine.go`

**Step 1: Implement handleInitFlow**

This handles the conversational flow when no workspace is bound for a channel:

```go
// handleWorkspaceInitFlow manages the conversational workspace setup.
// Returns true if the message was consumed by the init flow.
func (e *Engine) handleWorkspaceInitFlow(p Platform, msg *Message, channelID, channelName string) bool {
	e.initFlowsMu.Lock()
	flow, exists := e.initFlows[channelID]
	e.initFlowsMu.Unlock()

	content := strings.TrimSpace(msg.Content)

	if !exists {
		// Check if user is providing a /workspace init command — handled elsewhere
		if strings.HasPrefix(content, "/") {
			return false
		}

		// Start new init flow
		e.initFlowsMu.Lock()
		e.initFlows[channelID] = &workspaceInitFlow{state: "awaiting_url"}
		e.initFlowsMu.Unlock()

		e.reply(p, msg.ReplyCtx, fmt.Sprintf(
			"No workspace found for this channel. Send me a git repo URL to clone, or use `/workspace init <url>`."))
		return true
	}

	switch flow.state {
	case "awaiting_url":
		// Validate it looks like a git URL
		if !looksLikeGitURL(content) {
			e.reply(p, msg.ReplyCtx, "That doesn't look like a git URL. Please provide a URL like `https://github.com/org/repo` or `git@github.com:org/repo.git`.")
			return true
		}
		repoName := extractRepoName(content)
		cloneTo := filepath.Join(e.baseDir, repoName)

		e.initFlowsMu.Lock()
		flow.repoURL = content
		flow.cloneTo = cloneTo
		flow.state = "awaiting_confirm"
		e.initFlowsMu.Unlock()

		e.reply(p, msg.ReplyCtx, fmt.Sprintf(
			"I'll clone `%s` to `%s` and bind it to this channel. OK? (yes/no)", content, cloneTo))
		return true

	case "awaiting_confirm":
		lower := strings.ToLower(content)
		if lower != "yes" && lower != "y" {
			e.initFlowsMu.Lock()
			delete(e.initFlows, channelID)
			e.initFlowsMu.Unlock()
			e.reply(p, msg.ReplyCtx, "Cancelled. Send a repo URL anytime to try again.")
			return true
		}

		e.reply(p, msg.ReplyCtx, fmt.Sprintf("Cloning `%s` to `%s`...", flow.repoURL, flow.cloneTo))

		if err := gitClone(flow.repoURL, flow.cloneTo); err != nil {
			e.initFlowsMu.Lock()
			delete(e.initFlows, channelID)
			e.initFlowsMu.Unlock()
			e.reply(p, msg.ReplyCtx, fmt.Sprintf("Clone failed: %v\nSend a repo URL to try again.", err))
			return true
		}

		projectKey := "project:" + e.name
		e.workspaceBindings.Bind(projectKey, channelID, channelName, flow.cloneTo)

		e.initFlowsMu.Lock()
		delete(e.initFlows, channelID)
		e.initFlowsMu.Unlock()

		e.reply(p, msg.ReplyCtx, fmt.Sprintf(
			"Clone complete. Bound workspace `%s` to this channel. Ready.", flow.cloneTo))
		return true
	}

	return false
}

func looksLikeGitURL(s string) bool {
	return strings.HasPrefix(s, "https://") ||
		strings.HasPrefix(s, "http://") ||
		strings.HasPrefix(s, "git@") ||
		strings.HasPrefix(s, "ssh://")
}

func extractRepoName(url string) string {
	// Handle both https://github.com/org/repo.git and git@github.com:org/repo.git
	url = strings.TrimSuffix(url, ".git")
	parts := strings.Split(url, "/")
	if len(parts) > 0 {
		return parts[len(parts)-1]
	}
	// Handle git@ format
	if idx := strings.LastIndex(url, ":"); idx != -1 {
		remainder := url[idx+1:]
		parts = strings.Split(remainder, "/")
		if len(parts) > 0 {
			return parts[len(parts)-1]
		}
	}
	return "workspace"
}

func gitClone(repoURL, dest string) error {
	cmd := exec.Command("git", "clone", repoURL, dest)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %w", strings.TrimSpace(string(output)), err)
	}
	return nil
}
```

**Step 2: Build to verify**

Run: `go build ./...`
Expected: Compiles cleanly

**Step 3: Commit**

```bash
git add core/engine.go
git commit -m "feat: add workspace init flow for cloning repos and binding channels"
```

---

### Task 8: Wire multi-workspace routing into handleMessage

**Files:**
- Modify: `core/engine.go` (handleMessage ~line 574, getOrCreateInteractiveState)

**Step 1: Add workspace routing at the top of handleMessage**

After the banned words check (~line 611) and before command dispatch (~line 613), insert multi-workspace resolution:

```go
// Multi-workspace resolution
var resolvedWorkspace string
if e.multiWorkspace {
	channelID := extractChannelID(msg.SessionKey)
	workspace, channelName, err := e.resolveWorkspace(p, channelID)
	if err != nil {
		slog.Error("workspace resolution failed", "err", err)
		e.reply(p, msg.ReplyCtx, fmt.Sprintf("Workspace resolution error: %v", err))
		return
	}
	if workspace == "" {
		// No workspace — handle init flow (unless it's a /workspace command)
		if !strings.HasPrefix(content, "/workspace") {
			if e.handleWorkspaceInitFlow(p, msg, channelID, channelName) {
				return
			}
		}
		// If init flow didn't consume and no workspace, only /workspace commands work
		if !strings.HasPrefix(content, "/workspace") {
			return
		}
	} else {
		resolvedWorkspace = workspace
		// Touch the workspace pool for idle tracking
		if ws := e.workspacePool.Get(workspace); ws != nil {
			ws.Touch()
		}

		// Auto-bind feedback for convention matches (first message only)
		if ws := e.workspacePool.Get(workspace); ws == nil {
			e.reply(p, msg.ReplyCtx, fmt.Sprintf(
				"Found `%s` matching this channel. Binding workspace and starting session... Ready.", workspace))
		}
	}
}
```

**Step 2: Add extractChannelID helper**

```go
func extractChannelID(sessionKey string) string {
	// Format: "platform:channelID:userID" or "platform:channelID"
	parts := strings.SplitN(sessionKey, ":", 3)
	if len(parts) >= 2 {
		return parts[1]
	}
	return ""
}
```

**Step 3: Modify getOrCreateInteractiveState for multi-workspace**

The existing `getOrCreateInteractiveState` method creates agent sessions using `e.agent`. For multi-workspace, it needs to create an agent session with the resolved workspace's `work_dir`. Find `getOrCreateInteractiveState` and modify it:

- Add `resolvedWorkspace` parameter
- When `e.multiWorkspace` is true, use a workspace-scoped key and pass the workspace dir to the agent
- Key `interactiveStates` by `workspace + ":" + sessionKey` instead of just `sessionKey`
- Set `state.workspaceDir` on the interactive state

This requires the Agent interface to support dynamic work dirs. Add a method:

```go
// In core/interfaces.go, add optional interface:
type WorkDirSetter interface {
	SetWorkDir(dir string)
}
```

And in `agent/claudecode/claudecode.go`:
```go
func (a *Agent) SetWorkDir(dir string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.workDir = dir
}

func (a *Agent) GetWorkDir() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.workDir
}
```

However, since a single Agent instance is shared, we can't just SetWorkDir — it would race. Instead, for multi-workspace we need **per-workspace Agent instances**. Store them in the workspaceState:

Add to `workspaceState`:
```go
type workspaceState struct {
	mu           sync.Mutex
	workspace    string
	sessions     *SessionManager
	agent        Agent           // per-workspace agent clone
	lastActivity time.Time
}
```

Add a method to Engine for creating per-workspace agents:

```go
func (e *Engine) getOrCreateWorkspaceAgent(workspace string) (Agent, *SessionManager, error) {
	ws := e.workspacePool.GetOrCreate(workspace)
	ws.mu.Lock()
	defer ws.mu.Unlock()

	if ws.agent != nil {
		return ws.agent, ws.sessions, nil
	}

	// Create agent clone with this workspace's work_dir
	agentOpts := make(map[string]any)
	// Copy relevant options from the original agent if it's a claudecode agent
	if wds, ok := e.agent.(interface{ Options() map[string]any }); ok {
		for k, v := range wds.Options() {
			agentOpts[k] = v
		}
	}
	agentOpts["work_dir"] = workspace

	agent, err := CreateAgent("claudecode", agentOpts)
	if err != nil {
		return nil, nil, fmt.Errorf("create workspace agent: %w", err)
	}

	// Wire providers if original agent has them
	if ps, ok := e.agent.(ProviderSwitcher); ok {
		if ps2, ok := agent.(ProviderSwitcher); ok {
			ps2.SetProviders(ps.GetProviders())
		}
	}

	// Create per-workspace session manager
	sessionFile := filepath.Join(filepath.Dir(e.sessions.storePath),
		fmt.Sprintf("%s_ws_%x.json", e.name, sha256Short(workspace)))
	sessions := NewSessionManager(sessionFile)

	ws.agent = agent
	ws.sessions = sessions
	return agent, sessions, nil
}

func sha256Short(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:4])
}
```

**Step 4: Update processInteractiveMessage to use workspace agent**

In the multi-workspace path, before calling `getOrCreateInteractiveState`, swap in the workspace's agent and session manager. The cleanest approach: modify `processInteractiveMessage` to accept optional overrides, or modify `getOrCreateInteractiveState` to accept an agent parameter.

Modify `getOrCreateInteractiveState` signature to optionally accept a workspace agent:

```go
func (e *Engine) getOrCreateInteractiveState(sessionKey string, p Platform, replyCtx any, session *Session, agent ...Agent) *interactiveState {
	// ... existing logic, but use agent[0] if provided instead of e.agent
}
```

**Step 5: Build to verify**

Run: `go build ./...`
Expected: Compiles cleanly

**Step 6: Commit**

```bash
git add core/engine.go core/interfaces.go core/workspace_state.go agent/claudecode/claudecode.go
git commit -m "feat: wire multi-workspace routing into handleMessage with per-workspace agents"
```

---

### Task 9: Workspace commands

**Files:**
- Modify: `core/engine.go` (handleCommand ~line 1320)

**Step 1: Add workspace command handler**

In the `handleCommand` switch statement, add cases for workspace commands:

```go
case "workspace":
	if !e.multiWorkspace {
		e.reply(p, msg.ReplyCtx, "Workspace commands are only available in multi-workspace mode.")
		return true
	}
	e.handleWorkspaceCommand(p, msg, args)
	return true
```

**Step 2: Implement handleWorkspaceCommand**

```go
func (e *Engine) handleWorkspaceCommand(p Platform, msg *Message, args string) {
	channelID := extractChannelID(msg.SessionKey)
	projectKey := "project:" + e.name

	parts := strings.Fields(args)
	subCmd := ""
	if len(parts) > 0 {
		subCmd = parts[0]
	}

	switch subCmd {
	case "":
		// Show current binding
		b := e.workspaceBindings.Lookup(projectKey, channelID)
		if b == nil {
			e.reply(p, msg.ReplyCtx, "No workspace bound to this channel.")
		} else {
			e.reply(p, msg.ReplyCtx, fmt.Sprintf("Workspace: `%s`\nBound: %s",
				b.Workspace, b.BoundAt.Format(time.RFC3339)))
		}

	case "init":
		if len(parts) < 2 {
			e.reply(p, msg.ReplyCtx, "Usage: `/workspace init <git-url>`")
			return
		}
		repoURL := parts[1]
		if !looksLikeGitURL(repoURL) {
			e.reply(p, msg.ReplyCtx, "That doesn't look like a git URL.")
			return
		}

		repoName := extractRepoName(repoURL)
		cloneTo := filepath.Join(e.baseDir, repoName)

		// Check if already exists
		if _, err := os.Stat(cloneTo); err == nil {
			// Directory exists, just bind
			channelName := ""
			if resolver, ok := p.(ChannelNameResolver); ok {
				channelName, _ = resolver.ResolveChannelName(channelID)
			}
			e.workspaceBindings.Bind(projectKey, channelID, channelName, cloneTo)
			e.reply(p, msg.ReplyCtx, fmt.Sprintf(
				"Directory `%s` already exists. Bound workspace to this channel. Ready.", cloneTo))
			return
		}

		e.reply(p, msg.ReplyCtx, fmt.Sprintf("Cloning `%s` to `%s`...", repoURL, cloneTo))

		if err := gitClone(repoURL, cloneTo); err != nil {
			e.reply(p, msg.ReplyCtx, fmt.Sprintf("Clone failed: %v", err))
			return
		}

		channelName := ""
		if resolver, ok := p.(ChannelNameResolver); ok {
			channelName, _ = resolver.ResolveChannelName(channelID)
		}
		e.workspaceBindings.Bind(projectKey, channelID, channelName, cloneTo)
		e.reply(p, msg.ReplyCtx, fmt.Sprintf(
			"Clone complete. Bound workspace `%s` to this channel. Ready.", cloneTo))

	case "unbind":
		e.workspaceBindings.Unbind(projectKey, channelID)
		// Clean up workspace pool state
		if ws := e.workspacePool.Get(channelID); ws != nil {
			// Stop any running sessions
		}
		e.reply(p, msg.ReplyCtx, "Workspace unbound from this channel.")

	case "list":
		bindings := e.workspaceBindings.ListByProject(projectKey)
		if len(bindings) == 0 {
			e.reply(p, msg.ReplyCtx, "No workspaces bound.")
			return
		}
		var sb strings.Builder
		sb.WriteString("Bound workspaces:\n")
		for chID, b := range bindings {
			name := b.ChannelName
			if name == "" {
				name = chID
			}
			sb.WriteString(fmt.Sprintf("• #%s → `%s`\n", name, b.Workspace))
		}
		e.reply(p, msg.ReplyCtx, sb.String())

	default:
		e.reply(p, msg.ReplyCtx,
			"Usage: `/workspace [init <url> | unbind | list]`")
	}
}
```

**Step 3: Build to verify**

Run: `go build ./...`
Expected: Compiles cleanly

**Step 4: Commit**

```bash
git add core/engine.go
git commit -m "feat: add /workspace commands (init, unbind, list, status)"
```

---

### Task 10: Wire multi-workspace in main.go

**Files:**
- Modify: `cmd/cc-connect/main.go` (~line 139 project setup loop)

**Step 1: Add multi-workspace setup after engine creation**

After `engine := core.NewEngine(...)` (~line 195), add:

```go
if proj.Mode == "multi-workspace" {
	baseDir := proj.BaseDir
	if strings.HasPrefix(baseDir, "~/") {
		home, _ := os.UserHomeDir()
		baseDir = filepath.Join(home, baseDir[2:])
	}
	// Ensure base dir exists
	if err := os.MkdirAll(baseDir, 0o755); err != nil {
		slog.Error("failed to create base_dir", "path", baseDir, "err", err)
		continue
	}
	bindingStore := filepath.Join(cfg.DataDir, "workspace_bindings.json")
	engine.SetMultiWorkspace(baseDir, bindingStore)
	slog.Info("multi-workspace mode enabled", "project", proj.Name, "base_dir", baseDir)
}
```

**Step 2: Build to verify**

Run: `go build ./...`
Expected: Compiles cleanly

**Step 3: Commit**

```bash
git add cmd/cc-connect/main.go
git commit -m "feat: wire multi-workspace mode setup in main.go"
```

---

### Task 11: Integration testing

**Files:**
- Create: `core/multi_workspace_test.go`

**Step 1: Write integration test for full resolution flow**

```go
package core

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMultiWorkspaceResolution_ConventionMatch(t *testing.T) {
	baseDir := t.TempDir()
	bindingDir := t.TempDir()

	// Create a directory matching a channel name
	channelDir := filepath.Join(baseDir, "test-channel")
	os.MkdirAll(channelDir, 0o755)

	// Create engine with multi-workspace
	agent := &mockAgent{}
	engine := NewEngine("test", agent, nil, "", LangEN)
	engine.SetMultiWorkspace(baseDir, filepath.Join(bindingDir, "bindings.json"))

	// Create a mock platform with channel name resolution
	mockP := &mockPlatformWithChannelResolver{
		channelNames: map[string]string{"C123": "test-channel"},
	}

	workspace, channelName, err := engine.resolveWorkspace(mockP, "C123")
	if err != nil {
		t.Fatal(err)
	}
	if workspace != channelDir {
		t.Errorf("expected %s, got %s", channelDir, workspace)
	}
	if channelName != "test-channel" {
		t.Errorf("expected test-channel, got %s", channelName)
	}

	// Verify binding was saved
	b := engine.workspaceBindings.Lookup("project:test", "C123")
	if b == nil {
		t.Fatal("expected binding to be saved")
	}
}

func TestMultiWorkspaceResolution_NoMatch(t *testing.T) {
	baseDir := t.TempDir()
	bindingDir := t.TempDir()

	agent := &mockAgent{}
	engine := NewEngine("test", agent, nil, "", LangEN)
	engine.SetMultiWorkspace(baseDir, filepath.Join(bindingDir, "bindings.json"))

	mockP := &mockPlatformWithChannelResolver{
		channelNames: map[string]string{"C456": "unknown-channel"},
	}

	workspace, _, err := engine.resolveWorkspace(mockP, "C456")
	if err != nil {
		t.Fatal(err)
	}
	if workspace != "" {
		t.Errorf("expected empty workspace for unmatched channel, got %s", workspace)
	}
}

func TestMultiWorkspaceResolution_MissingDirRemovesBinding(t *testing.T) {
	baseDir := t.TempDir()
	bindingDir := t.TempDir()

	agent := &mockAgent{}
	engine := NewEngine("test", agent, nil, "", LangEN)
	engine.SetMultiWorkspace(baseDir, filepath.Join(bindingDir, "bindings.json"))

	// Bind to a non-existent directory
	engine.workspaceBindings.Bind("project:test", "C789", "deleted-channel", "/nonexistent/path")

	mockP := &mockPlatformWithChannelResolver{
		channelNames: map[string]string{"C789": "deleted-channel"},
	}

	workspace, _, err := engine.resolveWorkspace(mockP, "C789")
	if err != nil {
		t.Fatal(err)
	}
	if workspace != "" {
		t.Errorf("expected empty workspace for missing dir, got %s", workspace)
	}

	// Binding should be removed
	b := engine.workspaceBindings.Lookup("project:test", "C789")
	if b != nil {
		t.Error("expected binding to be removed for missing directory")
	}
}

// Mock types - adapt to match existing test mocks in the project
type mockPlatformWithChannelResolver struct {
	mockPlatform // embed existing mock if available
	channelNames map[string]string
}

func (m *mockPlatformWithChannelResolver) ResolveChannelName(channelID string) (string, error) {
	if name, ok := m.channelNames[channelID]; ok {
		return name, nil
	}
	return "", fmt.Errorf("unknown channel %s", channelID)
}
```

Note: The mock types will need to be adapted based on existing test helpers in the codebase. Check `core/*_test.go` for existing mock patterns.

**Step 2: Run tests**

Run: `go test ./core/ -run TestMultiWorkspace -v`
Expected: PASS

**Step 3: Run full test suite**

Run: `go test ./...`
Expected: All existing tests still pass

**Step 4: Commit**

```bash
git add core/multi_workspace_test.go
git commit -m "test: add integration tests for multi-workspace resolution"
```

---

### Task 12: Update config.example.toml and verify build

**Files:**
- Modify: `config.example.toml`

**Step 1: Add multi-workspace example section**

Find the projects section and add a complete multi-workspace example:

```toml
# Multi-workspace mode: single bot, multiple workspaces
# Channel name maps to ~/workspace/<channel-name> automatically.
# Use /workspace init <url> to clone and bind a new repo.
#
# [[projects]]
# name = "claude"
# mode = "multi-workspace"
# base_dir = "~/workspace"
#
# [projects.agent]
# type = "claudecode"
# permission_mode = "yolo"
#
# [[projects.platforms]]
# type = "slack"
# [projects.platforms.options]
# bot_token = "xoxb-..."
# app_token = "xapp-..."
```

**Step 2: Final build and test**

Run: `go build ./... && go test ./...`
Expected: Clean build and all tests pass

**Step 3: Commit**

```bash
git add config.example.toml
git commit -m "docs: add multi-workspace example to config.example.toml"
```
