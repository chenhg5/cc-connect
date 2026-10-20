package core

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMemoryIndexOnFreshContextOnly(t *testing.T) {
	dir := t.TempDir()
	idx := filepath.Join(dir, "MEMORY.md")
	os.WriteFile(idx, []byte("- [prefs](prefs/README.md) — 偏好\n- [works](works/README.md) — 作品\n"), 0o644)
	e := &Engine{}
	e.SetMemoryIndex(idx)
	s := &Session{}

	if got := e.withTurnBlocks(s, "k", "", "hi", false); got != "hi" {
		t.Fatalf("resumed context got a block: %q", got)
	}
	s.MarkFreshContext() // agent started without resume
	got := e.withTurnBlocks(s, "k", "", "hi", false)
	if !strings.HasPrefix(got, memoryIndexHead+" "+idx) || !strings.Contains(got, "- [works](works/README.md) — 作品\n\nhi") {
		t.Fatalf("fresh context block: %q", got)
	}
	s.SettleHandoff(true)
	if got := e.withTurnBlocks(s, "k", "", "hi", false); got != "hi" {
		t.Fatalf("second turn got the block again: %q", got)
	}

	s.MarkFreshContext()
	e.withTurnBlocks(s, "k", "", "hi", false)
	s.SettleHandoff(false) // that turn never reached the agent: the next one carries it
	if got := e.withTurnBlocks(s, "k", "", "hi", false); !strings.HasPrefix(got, memoryIndexHead) {
		t.Fatalf("undelivered block not carried to the next turn: %q", got)
	}

	// a handoff copying this turn drops the block (stale), keeps the user's words
	if got := stripStaleBlocks(e.memoryBlockFor(t, idx) + "\n\nhi"); got != "hi" {
		t.Fatalf("stripStaleBlocks kept the memory block: %q", got)
	}
}

// memoryBlockFor renders the block for a fresh session (test helper).
func (e *Engine) memoryBlockFor(t *testing.T, idx string) string {
	t.Helper()
	s := &Session{}
	s.MarkFreshContext()
	return e.memoryBlock(s)
}

func TestMemoryIndexOffByDefault(t *testing.T) {
	e := &Engine{} // 橘 (claudecode) sets no memory_index: Claude Code loads its own auto-memory
	s := &Session{}
	s.MarkFreshContext()
	if got := e.withTurnBlocks(s, "k", "", "hi", false); got != "hi" {
		t.Fatalf("block without memory_index: %q", got)
	}
}

func TestGateContextSendsBot(t *testing.T) {
	var body map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &body)
		w.Write([]byte(`{"wiki":[]}`))
	}))
	defer srv.Close()
	for url, want := range map[string]string{srv.URL + "/decide?bot=longniang": "longniang", srv.URL + "/decide": ""} {
		e := &Engine{ctx: t.Context()}
		e.gate = &gateState{url: url, timeout: time.Second}
		e.turnContext(&Session{}, "discord:1", "代理端口")
		if body["bot"] != want || body["text"] != "代理端口" {
			t.Fatalf("%s: body %v", url, body)
		}
	}
}

func TestAgentClosedHookEnv(t *testing.T) {
	env := eventToEnv(HookEvent{Event: HookEventAgentClosed, Project: "longniang", SessionKey: "discord:1",
		Extra: map[string]any{"agent_session_id": "01a1539c-95f0"}})
	joined := strings.Join(env, "\n")
	for _, want := range []string{"CC_HOOK_EVENT=agent.closed", "CC_HOOK_PROJECT=longniang", "CC_HOOK_AGENT_SESSION_ID=01a1539c-95f0"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %s in %v", want, env)
		}
	}
}

type closeOnlySession struct{ AgentSession }

func (closeOnlySession) CurrentSessionID() string { return "sid-1" }
func (closeOnlySession) Close() error             { return nil }

func TestAgentCloseEmitsHook(t *testing.T) {
	out := filepath.Join(t.TempDir(), "env")
	e := &Engine{}
	e.hooks = NewHookManager("longniang", []HookConfig{{Event: "agent.closed", Type: "command",
		Command: "echo $CC_HOOK_PROJECT $CC_HOOK_AGENT_SESSION_ID > " + out, Async: new(bool)}}, "/bin/sh", "-c", "")
	e.closeAgentSessionWithTimeout("discord:1", closeOnlySession{})
	b, _ := os.ReadFile(out)
	if strings.TrimSpace(string(b)) != "longniang sid-1" {
		t.Fatalf("hook got %q", b)
	}
}
