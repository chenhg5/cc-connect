package core

import (
	"errors"
	"strings"
	"testing"
	"time"
)

const episodeReply = "额度用完了，约 {reset_at} 恢复；消息会排队"

// limitEnv runs several foreground turns through one engine, so state kept
// across turns (the limit episode) is exercised.
type limitEnv struct {
	t       *testing.T
	e       *Engine
	p       *stubPlatformEngine
	sess    *queuingAgentSession
	state   *interactiveState
	session *Session
	hooks   *usageHookCollector
	key     string
}

func newLimitEnv(t *testing.T, reply string) *limitEnv {
	t.Helper()
	le := &limitEnv{t: t, p: &stubPlatformEngine{n: "feishu"}, sess: newQueuingSession("limited"), key: "feishu:dm:owner"}
	le.e = NewEngine("test", &controllableAgent{nextSession: le.sess}, []Platform{le.p}, "", LangEnglish)
	t.Cleanup(func() { _ = le.e.Stop() })
	le.hooks = newUsageHookCollector(t)
	le.e.SetHooks(NewHookManager("test", []HookConfig{{Event: "*", Type: "http", URL: le.hooks.srv.URL}}, "sh", "-c", ""))
	le.e.SetUsageLimitReply(reply)
	le.session = le.e.sessions.GetOrCreateActive(le.key)
	le.state = &interactiveState{agentSession: le.sess, platform: le.p, replyCtx: "ctx"}
	le.e.interactiveMu.Lock()
	le.e.interactiveStates[le.key] = le.state
	le.e.interactiveMu.Unlock()
	return le
}

// turn runs one turn made of events and returns what the chat got.
func (le *limitEnv) turn(events ...Event) []string {
	le.t.Helper()
	le.p.clearSent()
	for _, ev := range events {
		le.sess.events <- ev
	}
	sendDone := make(chan error, 1)
	sendDone <- nil
	done := make(chan struct{})
	go func() {
		le.e.processInteractiveEvents(le.state, le.session, le.e.sessions, le.key, "om-1", time.Now(), nil, sendDone, "ctx", 0)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		le.t.Fatal("processInteractiveEvents did not complete")
	}
	return le.p.getSent()
}

func limitedAt(resets time.Time) Event {
	return Event{Type: EventResult, Content: refusal, Done: true, UsageLimit: &UsageLimit{ResetsAt: resets}}
}

// clockOf is how a reply shows a reset time: "15:04" today, else with the date.
func clockOf(t time.Time) string {
	t, now := t.Local(), time.Now()
	if t.YearDay() == now.YearDay() && t.Year() == now.Year() {
		return t.Format("15:04")
	}
	return t.Format("01-02 15:04")
}

// One notice per episode: the first refusal gets the reply with the reset
// time, the next refusals get nothing, an answered turn ends the episode and
// the next refusal is announced again.
func TestUsageLimitReply_OncePerEpisode(t *testing.T) {
	le := newLimitEnv(t, episodeReply)
	resets := time.Now().Add(2 * time.Hour).Truncate(time.Minute)
	want := strings.ReplaceAll(episodeReply, "{reset_at}", clockOf(resets))

	if sent := le.turn(limitedAt(resets)); len(sent) != 1 || sent[0] != want {
		t.Fatalf("first refusal sent %q, want only %q", sent, want)
	}
	queued := le.e.i18n.T(MsgUsageLimitQueued)
	if sent := le.turn(limitedAt(resets)); len(sent) != 1 || sent[0] != queued {
		t.Fatalf("second refusal in the same episode sent %q, want the queued line %q", sent, queued)
	}
	if sent := le.turn(Event{Type: EventResult, Content: "here is the summary", Done: true}); len(sent) != 1 || sent[0] != "here is the summary" {
		t.Fatalf("answered turn sent %q", sent)
	}
	if sent := le.turn(limitedAt(resets)); len(sent) != 1 || sent[0] != want {
		t.Fatalf("refusal after an answered turn sent %q, want %q again", sent, want)
	}

	time.Sleep(200 * time.Millisecond) // async hooks
	evs := limitHooks(le.hooks)
	if len(evs) != 3 {
		t.Fatalf("turn.usage_limited events = %d, want one per refusal (3)", len(evs))
	}
	// Hooks run asynchronously, so their order is not the turns' order.
	announced, silent := 0, 0
	for _, ev := range evs {
		r, ok := ev.Extra["reply"]
		switch {
		case !ok:
			silent++
		case r == want:
			announced++
		default:
			t.Fatalf("refusal reports reply %v, want %q or none", r, want)
		}
	}
	if announced != 2 || silent != 1 {
		t.Fatalf("refusals reporting the reply / none = %d / %d, want 2 / 1", announced, silent)
	}
}

// Once the reset time the notice announced has passed, a refusal starts a
// new episode even without an answered turn in between.
func TestUsageLimitReply_NewEpisodeAfterAnnouncedReset(t *testing.T) {
	le := newLimitEnv(t, episodeReply)
	past := time.Now().Add(-time.Minute)
	for i := 0; i < 2; i++ {
		if sent := le.turn(limitedAt(past)); len(sent) != 1 || !strings.HasPrefix(sent[0], "额度用完了，约 ") {
			t.Fatalf("refusal %d after the announced reset sent %q, want a new notice", i+1, sent)
		}
	}
}

// A silent refusal leaks nothing, not even refusal text that streamed in
// before the result — it answers with one short queued line instead of
// deleting the card (Lark would show that deletion as a retraction).
func TestUsageLimitReply_SilentRefusalSendsQueuedLine(t *testing.T) {
	le := newLimitEnv(t, episodeReply)
	resets := time.Now().Add(time.Hour)
	le.turn(Event{Type: EventText, Content: refusal}, limitedAt(resets))
	sent := le.turn(Event{Type: EventText, Content: refusal}, limitedAt(resets))
	if len(sent) != 1 || sent[0] != le.e.i18n.T(MsgUsageLimitQueued) {
		t.Fatalf("silent refusal sent %q, want only the queued line", sent)
	}
	for _, s := range sent {
		if strings.Contains(s, "session limit") {
			t.Fatalf("silent refusal leaked the agent's refusal: %q", s)
		}
	}
}

// Without usage_limit_reply every refusal goes out as the agent wrote it.
func TestUsageLimitReply_UnsetKeepsEveryRefusal(t *testing.T) {
	le := newLimitEnv(t, "")
	resets := time.Now().Add(time.Hour)
	for i := 0; i < 2; i++ {
		if sent := le.turn(limitedAt(resets)); len(sent) != 1 || sent[0] != refusal {
			t.Fatalf("refusal %d sent %q, want the refusal text", i+1, sent)
		}
	}
}

// Without a reset time from the agent the reply takes the one in the
// refusal text, and says 稍后 when there is none.
func TestUsageLimitReply_ResetTimeFallbacks(t *testing.T) {
	le := newLimitEnv(t, episodeReply)
	sent := le.turn(Event{Type: EventResult, Content: "You've hit your session limit · resets 8:30pm (Asia/Shanghai)", Done: true, UsageLimit: &UsageLimit{}})
	if len(sent) != 1 || sent[0] != strings.ReplaceAll(episodeReply, "{reset_at}", "20:30") {
		t.Fatalf("sent %q, want the reset time from the text", sent)
	}

	le2 := newLimitEnv(t, episodeReply)
	sent = le2.turn(Event{Type: EventResult, Content: "You've hit your usage limit", Done: true, UsageLimit: &UsageLimit{}})
	if len(sent) != 1 || sent[0] != strings.ReplaceAll(episodeReply, "{reset_at}", usageLimitUnknownReset) {
		t.Fatalf("sent %q, want the unknown-time wording", sent)
	}
}

func TestUsageLimitResetClock(t *testing.T) {
	loc := time.FixedZone("UTC+8", 8*3600)
	now := time.Date(2026, 10, 25, 4, 0, 0, 0, loc)
	cases := []struct {
		name  string
		limit *UsageLimit
		text  string
		want  string
	}{
		{"reported, today", &UsageLimit{ResetsAt: time.Date(2026, 10, 24, 20, 30, 0, 0, time.UTC)}, "", "04:30"},
		{"reported, another day", &UsageLimit{ResetsAt: time.Date(2026, 10, 27, 0, 30, 0, 0, time.UTC)}, "", "10-27 08:30"},
		{"reported wins over text", &UsageLimit{ResetsAt: time.Date(2026, 10, 24, 20, 30, 0, 0, time.UTC)}, "resets 9am", "04:30"},
		{"text am", &UsageLimit{}, "You've hit your session limit · resets 8:30am (Asia/Shanghai)", "08:30"},
		{"text pm no minutes", nil, "resets 7pm", "19:00"},
		{"text noon", nil, "resets 12pm", "12:00"},
		{"text midnight", nil, "resets 12:15am", "00:15"},
		{"no time", nil, "You've hit your weekly limit", ""},
		{"not a clock", nil, "resets 13:00pm", ""},
	}
	for _, c := range cases {
		if got := usageLimitResetClock(c.limit, c.text, now); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

// A timer run whose turn was refused fails with a UsageLimitError carrying
// the reset time, so the job's last_error says the prompt went unanswered;
// an answered run on the same session succeeds again.
func TestExecuteTimerJob_UsageLimitedTurnIsJobError(t *testing.T) {
	plat := &cujReplyCtxPlatform{stubPlatformEngine: &stubPlatformEngine{n: "test"}}
	agent := &cujAgent{}
	e := NewEngine("test", agent, []Platform{plat}, t.TempDir()+"/sessions.json", LangEnglish)
	t.Cleanup(func() { _ = e.Stop() })
	e.SetUsageLimitReply(episodeReply)

	resets := time.Date(2026, 10, 25, 0, 30, 0, 0, time.UTC)
	agent.setNextSessionEvents([]Event{limitedAt(resets)}, 0)
	err := e.ExecuteTimerJob(&TimerJob{ID: "t1", Project: "test", SessionKey: "test:erin", Prompt: "[mesh 消息] 来自 x：hi"})
	var ul *UsageLimitError
	if !errors.As(err, &ul) {
		t.Fatalf("refused run returned %v, want a UsageLimitError", err)
	}
	if !ul.ResetsAt.Equal(resets) || !strings.HasPrefix(err.Error(), "usage limit reached; resets ") {
		t.Fatalf("error = %q (resets %v), want the reset time %v", err, ul.ResetsAt, resets)
	}

	if err := e.ExecuteTimerJob(&TimerJob{ID: "t2", Project: "test", SessionKey: "test:erin", Prompt: "[mesh 消息] 来自 x：again"}); err != nil {
		t.Fatalf("answered run returned %v, want nil", err)
	}
}

// Turns the agent runs on its own follow the same once-per-episode rule.
func TestUsageLimitReply_SilentRefusalFinalizesCardWithQueuedLine(t *testing.T) {
	p := &stubRichCardSilentPlatform{stubPlatformEngine: stubPlatformEngine{n: "feishu"}}
	sess := newQueuingSession("rich")
	e := NewEngine("test", &controllableAgent{nextSession: sess}, []Platform{p}, "", LangEnglish)
	t.Cleanup(func() { _ = e.Stop() })
	e.SetDisplayConfig(DisplayCfg{Mode: "full", CardMode: "rich", ThinkingMessages: true, ThinkingMaxLen: 300, ToolMaxLen: 500, ToolMessages: true})
	e.SetUsageLimitReply(episodeReply)
	key := "feishu:dm:rich"
	session := e.sessions.GetOrCreateActive(key)
	state := &interactiveState{agentSession: sess, platform: p, replyCtx: "ctx"}
	e.interactiveMu.Lock()
	e.interactiveStates[key] = state
	e.interactiveMu.Unlock()
	resets := time.Now().Add(time.Hour)
	run := func() {
		sess.events <- Event{Type: EventText, Content: refusal}
		sess.events <- limitedAt(resets)
		e.processInteractiveEvents(state, session, e.sessions, key, "om-rich", time.Now(), nil, nil, "ctx", 0)
	}

	run() // announced
	_, _, updatesBefore, deletesBefore := p.snapshot()
	run() // silent
	_, _, updates, deletes := p.snapshot()
	var sawQueued bool
	for _, u := range updates[len(updatesBefore):] {
		if strings.Contains(u, "hit your session limit") {
			t.Fatalf("silent refusal finalized a card with the refusal: %q", u)
		}
		if strings.Contains(u, e.i18n.T(MsgUsageLimitQueued)) {
			sawQueued = true
		}
	}
	if !sawQueued {
		t.Fatalf("silent refusal did not finalize the card with the queued line (updates %q)", updates[len(updatesBefore):])
	}
	if deletes != deletesBefore {
		t.Fatalf("silent refusal deleted its card (deletes %d -> %d)", deletesBefore, deletes)
	}
}
