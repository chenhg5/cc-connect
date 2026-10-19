package core

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// maomao fork: message.sent names the message that triggered the turn (Extra.trigger_message_ids),
// so a receiver that posted that message can match the reply exactly.

func captureSentHooks(t *testing.T, e *Engine) func() []HookEvent {
	t.Helper()
	var mu sync.Mutex
	var got []HookEvent
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var ev HookEvent
		if err := json.NewDecoder(r.Body).Decode(&ev); err != nil {
			t.Errorf("decode hook body: %v", err)
		}
		mu.Lock()
		got = append(got, ev)
		mu.Unlock()
	}))
	t.Cleanup(srv.Close)
	e.SetHooks(NewHookManager("proj", []HookConfig{{Event: "message.sent", Type: "http", URL: srv.URL, Async: boolPtr(false)}}, "sh", "-c", ""))
	return func() []HookEvent {
		mu.Lock()
		defer mu.Unlock()
		return append([]HookEvent(nil), got...)
	}
}

func triggerIDs(t *testing.T, ev HookEvent) []string {
	t.Helper()
	raw, ok := ev.Extra["trigger_message_ids"].([]any)
	if !ok {
		t.Fatalf("extra.trigger_message_ids = %#v, want a list", ev.Extra["trigger_message_ids"])
	}
	out := make([]string, len(raw))
	for i, v := range raw {
		out[i], _ = v.(string)
	}
	return out
}

func TestMessageSentHook_CarriesTriggerMessageID(t *testing.T) {
	p := &stubMediaPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	got := captureSentHooks(t, e)
	sessionKey := "test:chan1"
	session := e.sessions.GetOrCreateActive(sessionKey)
	as := newControllableSession("s1")
	state := &interactiveState{agentSession: as, platform: p, replyCtx: "ctx-1"}
	e.interactiveStates[sessionKey] = state

	as.events <- Event{Type: EventResult, Content: "回板子的话", Done: true}
	e.processInteractiveEvents(state, session, e.sessions, sessionKey, "1560000000000000001", time.Now(), nil, nil, nil)

	evs := got()
	if len(evs) != 1 {
		t.Fatalf("message.sent hooks = %d, want 1", len(evs))
	}
	if ids := triggerIDs(t, evs[0]); len(ids) != 1 || ids[0] != "1560000000000000001" {
		t.Fatalf("trigger_message_ids = %v, want [1560000000000000001]", ids)
	}
	if evs[0].Content != "回板子的话" || evs[0].SessionKey != sessionKey {
		t.Fatalf("hook = %+v", evs[0])
	}
}

func TestMessageSentHook_ChannelClosePathCarriesTriggerMessageID(t *testing.T) {
	p := &stubMediaPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	got := captureSentHooks(t, e)
	sessionKey := "test:chan1"
	session := e.sessions.GetOrCreateActive(sessionKey)
	as := &closedChanSession{newControllableSession("s1")}
	state := &interactiveState{agentSession: as, platform: p, replyCtx: "ctx-1"}
	e.interactiveStates[sessionKey] = state

	as.events <- Event{Type: EventText, Content: "半截回复"}
	close(as.events)
	e.processInteractiveEvents(state, session, e.sessions, sessionKey, "m-close", time.Now(), nil, nil, nil)

	evs := got()
	if len(evs) != 1 {
		t.Fatalf("message.sent hooks = %d, want 1", len(evs))
	}
	if ids := triggerIDs(t, evs[0]); len(ids) != 1 || ids[0] != "m-close" {
		t.Fatalf("trigger_message_ids = %v, want [m-close]", ids)
	}
}

// closedChanSession: the test closes the events channel itself (agent process gone), so Close must not close it again.
type closedChanSession struct{ *controllableAgentSession }

func (s *closedChanSession) Close() error { s.alive = false; return nil }

func TestTriggerExtra_EmptyIDOmitsField(t *testing.T) {
	if x := triggerExtra(""); x != nil {
		t.Fatalf("triggerExtra(\"\") = %#v, want nil", x)
	}
	b, _ := json.Marshal(HookEvent{Event: HookEventMessageSent, Extra: triggerExtra("")})
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	if _, ok := m["extra"]; ok {
		t.Fatalf("empty trigger ID should omit extra: %s", b)
	}
}
