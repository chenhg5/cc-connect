package core

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type usageHookCollector struct {
	mu     sync.Mutex
	events []HookEvent
	srv    *httptest.Server
}

func newUsageHookCollector(t *testing.T) *usageHookCollector {
	t.Helper()
	c := &usageHookCollector{}
	c.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var ev HookEvent
		if err := json.NewDecoder(r.Body).Decode(&ev); err != nil {
			t.Errorf("hook body: %v", err)
			return
		}
		c.mu.Lock()
		c.events = append(c.events, ev)
		c.mu.Unlock()
	}))
	t.Cleanup(c.srv.Close)
	return c
}

func (c *usageHookCollector) all() []HookEvent {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]HookEvent(nil), c.events...)
}

// runLimitedTurn runs one foreground turn that ends in result.
func runLimitedTurn(t *testing.T, reply string, result Event) (*stubPlatformEngine, *usageHookCollector) {
	t.Helper()
	p := &stubPlatformEngine{n: "weixin"}
	sess := newQueuingSession("limited")
	e := NewEngine("test", &controllableAgent{nextSession: sess}, []Platform{p}, "", LangEnglish)
	t.Cleanup(func() { _ = e.Stop() })
	c := newUsageHookCollector(t)
	e.SetHooks(NewHookManager("test", []HookConfig{{Event: "*", Type: "http", URL: c.srv.URL}}, "sh", "-c", ""))
	e.SetUsageLimitReply(reply)

	key := "weixin:dm:owner"
	session := e.sessions.GetOrCreateActive(key)
	state := &interactiveState{agentSession: sess, platform: p, replyCtx: "ctx"}
	e.interactiveMu.Lock()
	e.interactiveStates[key] = state
	e.interactiveMu.Unlock()

	sess.events <- result
	sendDone := make(chan error, 1)
	sendDone <- nil
	done := make(chan struct{})
	go func() {
		e.processInteractiveEvents(state, session, e.sessions, key, "wx-msg-1", time.Now(), nil, sendDone, "ctx", 0)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("processInteractiveEvents did not complete")
	}
	time.Sleep(200 * time.Millisecond) // async hooks
	return p, c
}

const refusal = "You've hit your session limit · resets 7:30am (Asia/Shanghai)"

var limited = Event{Type: EventResult, Content: refusal, Done: true, UsageLimit: &UsageLimit{
	ResetsAt: time.Date(2026, 10, 23, 23, 30, 0, 0, time.UTC),
	Prompt:   "Please analyze the attached file(s).\n\n(Files saved locally, please read them: /obsun/.cc-connect/attachments/wx-msg-1/a.pdf)",
}}

func limitHooks(c *usageHookCollector) []HookEvent {
	var out []HookEvent
	for _, ev := range c.all() {
		if ev.Event == HookEventUsageLimited {
			out = append(out, ev)
		}
	}
	return out
}

// With usage_limit_reply set, the user gets that text instead of the refusal,
// and turn.usage_limited carries what a hook needs to hand the message on.
func TestUsageLimitedTurn_RepliesAndReportsHook(t *testing.T) {
	const reply = "这边额度用完了，已转给飞书秘书处理"
	p, c := runLimitedTurn(t, reply, limited)

	if sent := p.getSent(); len(sent) != 1 || sent[0] != reply {
		t.Fatalf("sent = %q, want only %q", sent, reply)
	}
	evs := limitHooks(c)
	if len(evs) != 1 {
		t.Fatalf("turn.usage_limited events = %d, want 1 (all: %+v)", len(evs), c.all())
	}
	x := evs[0].Extra
	if x["in_reply_to"] != "wx-msg-1" || x["prompt"] != limited.UsageLimit.Prompt || x["limit_text"] != refusal ||
		x["resets_at"] != "2026-10-23T23:30:00Z" || x["reply"] != reply {
		t.Fatalf("extra = %v", x)
	}
	if evs[0].Platform != "weixin" || evs[0].SessionKey != "weixin:dm:owner" {
		t.Fatalf("event = %+v", evs[0])
	}
}

// Without usage_limit_reply the refusal text goes out as before; the hook
// still fires.
func TestUsageLimitedTurn_KeepsRefusalWithoutConfiguredReply(t *testing.T) {
	p, c := runLimitedTurn(t, "", limited)
	if sent := p.getSent(); len(sent) != 1 || sent[0] != refusal {
		t.Fatalf("sent = %q, want the refusal", sent)
	}
	evs := limitHooks(c)
	if len(evs) != 1 {
		t.Fatalf("turn.usage_limited events = %d, want 1", len(evs))
	}
	if _, ok := evs[0].Extra["reply"]; ok {
		t.Fatalf("extra has a reply although none is configured: %v", evs[0].Extra)
	}
}

// An ordinary reply neither changes text nor fires the hook.
func TestUsageLimitedTurn_OrdinaryReplyUntouched(t *testing.T) {
	p, c := runLimitedTurn(t, "这边额度用完了，已转给飞书秘书处理",
		Event{Type: EventResult, Content: "here is the summary", Done: true})
	if sent := p.getSent(); len(sent) != 1 || sent[0] != "here is the summary" {
		t.Fatalf("sent = %q", sent)
	}
	if n := len(limitHooks(c)); n != 0 {
		t.Fatalf("turn.usage_limited events = %d, want 0", n)
	}
}

// A command hook gets the whole event, Extra included, as JSON on stdin.
func TestCommandHook_GetsEventJSONOnStdin(t *testing.T) {
	out := filepath.Join(t.TempDir(), "event.json")
	async := false
	hm := NewHookManager("test", []HookConfig{{Event: string(HookEventUsageLimited), Type: "command",
		Command: fmt.Sprintf("cat > '%s'", out), Async: &async}}, "sh", "-c", "")
	hm.Emit(HookEvent{Event: HookEventUsageLimited, Extra: map[string]any{"in_reply_to": "wx-msg-1", "prompt": "看这个"}})
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read hook output: %v", err)
	}
	var ev HookEvent
	if err := json.Unmarshal(raw, &ev); err != nil {
		t.Fatalf("stdin was not the event JSON: %q (%v)", raw, err)
	}
	if ev.Extra["in_reply_to"] != "wx-msg-1" || !strings.Contains(fmt.Sprint(ev.Extra["prompt"]), "看这个") {
		t.Fatalf("stdin event = %+v", ev)
	}
}
