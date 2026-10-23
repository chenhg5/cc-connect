package core

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func opusCyberFallback() *ModelSwitch {
	return &ModelSwitch{
		From:        "claude-opus-5-5",
		To:          "claude-opus-4-8",
		Reason:      "refusal",
		Category:    "cyber",
		SessionWide: true,
	}
}

const (
	wantSessionNotice   = "⚠️ `claude-opus-5-5`'s safeguards declined this request [cyber], so `claude-opus-4-8` is answering instead and the rest of this session stays on it."
	wantCommands        = "Send `/model switch claude-opus-5-5` to switch back, or `/new` to start a new session."
	wantRestoringNotice = "⚠️ `claude-opus-5-5`'s safeguards declined this request [cyber], so `claude-opus-4-8` is answering this turn. The session switches back to `claude-opus-5-5` after this reply; the declined request is not sent again."
	wantRestored        = "✅ Switched back to `claude-opus-5-5`."
)

// liveModelSession is a controllable agent session that can switch models in
// place, as Claude Code does with set_model.
type liveModelSession struct {
	*controllableAgentSession
	platform *stubPlatformEngine
	err      error

	mu    sync.Mutex
	calls []string
	// sentBefore records how many platform messages were out when each
	// switch was requested.
	sentBefore []int
}

func (s *liveModelSession) SetLiveModel(_ context.Context, model string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, model)
	s.sentBefore = append(s.sentBefore, len(s.platform.getSent()))
	return s.err
}

func (s *liveModelSession) switches() ([]string, []int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.calls...), append([]int(nil), s.sentBefore...)
}

type modelSwitchHarness struct {
	e       *Engine
	p       *stubPlatformEngine
	session *Session
	state   *interactiveState
	key     string
}

func newModelSwitchHarness(t *testing.T, p Platform, base *stubPlatformEngine, agentSession AgentSession) *modelSwitchHarness {
	t.Helper()
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	t.Cleanup(func() { _ = e.Stop() })
	e.SetDisplayConfig(DisplayCfg{Mode: "quiet", ToolMessages: false})
	key := "feishu:model-switch"
	state := &interactiveState{agentSession: agentSession, platform: p, replyCtx: "ctx-model-switch"}
	e.interactiveStates[key] = state
	return &modelSwitchHarness{e: e, p: base, session: e.sessions.GetOrCreateActive(key), state: state, key: key}
}

// runTurn feeds a turn in which the agent falls back mid-turn and then answers.
func (h *modelSwitchHarness) runTurn(events chan Event, ms *ModelSwitch) {
	events <- Event{Type: EventModelSwitch, ModelSwitch: ms}
	events <- Event{Type: EventText, Content: "answer from the fallback model"}
	events <- Event{Type: EventResult, Content: "answer from the fallback model", Done: true}
	h.e.processInteractiveEvents(h.state, h.session, h.e.sessions, h.key, "m-model-switch", time.Now(), nil, nil, h.state.replyCtx, 0)
}

func assertSent(t *testing.T, got, want []string) {
	t.Helper()
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("sent = %#v\nwant %#v", got, want)
	}
}

// Default (notify): the user is told when the fallback happens and given the
// ways back, ahead of the reply that the fallback model produced. Nothing is
// switched automatically, and the notice stays out of the reply text and the
// session history.
func TestProcessInteractiveEvents_AnnouncesModelSwitch(t *testing.T) {
	p := &stubPlatformEngine{n: "feishu"}
	agentSession := &liveModelSession{controllableAgentSession: newControllableSession("model-switch"), platform: p}
	h := newModelSwitchHarness(t, p, p, agentSession)

	h.runTurn(agentSession.events, opusCyberFallback())

	assertSent(t, p.getSent(), []string{wantSessionNotice + "\n" + wantCommands, "answer from the fallback model"})
	if calls, _ := agentSession.switches(); len(calls) != 0 {
		t.Fatalf("SetLiveModel calls = %v, want none in notify mode", calls)
	}
	if got := h.session.GetHistory(0); len(got) != 1 || got[0].Content != "answer from the fallback model" {
		t.Fatalf("history = %#v, want only the reply", got)
	}
}

// restore: the session is switched back once the reply is out, and the user
// is told both when it falls back and when it is back.
func TestModelFallbackRestore_SwitchesBackAfterReply(t *testing.T) {
	p := &stubPlatformEngine{n: "feishu"}
	agentSession := &liveModelSession{controllableAgentSession: newControllableSession("model-restore"), platform: p}
	h := newModelSwitchHarness(t, p, p, agentSession)
	h.e.SetModelFallbackAction("restore")

	h.runTurn(agentSession.events, opusCyberFallback())

	assertSent(t, p.getSent(), []string{wantRestoringNotice, "answer from the fallback model", wantRestored})
	calls, sentBefore := agentSession.switches()
	if fmt.Sprint(calls) != "[claude-opus-5-5]" {
		t.Fatalf("SetLiveModel calls = %v, want [claude-opus-5-5]", calls)
	}
	if sentBefore[0] != 2 {
		t.Fatalf("switched back with %d messages out, want 2 (after the reply)", sentBefore[0])
	}
	if h.state.lastModelRestoreAt.IsZero() {
		t.Fatal("lastModelRestoreAt not recorded")
	}
}

// A fallback that recurs within the loop window of the last switch back stays
// in place; the user gets the choices instead. After the window it is
// switched back again.
func TestModelFallbackRestore_LoopProtection(t *testing.T) {
	cases := []struct {
		name        string
		lastRestore time.Duration
		wantCalls   string
		wantSent    []string
	}{
		{
			name:        "within window",
			lastRestore: 5 * time.Minute,
			wantCalls:   "[]",
			wantSent: []string{
				"⚠️ `claude-opus-5-5`'s safeguards declined another request [cyber] within 30 minutes of switching back, so this time the session stays on `claude-opus-4-8`.\n" + wantCommands,
				"answer from the fallback model",
			},
		},
		{
			name:        "after window",
			lastRestore: 31 * time.Minute,
			wantCalls:   "[claude-opus-5-5]",
			wantSent:    []string{wantRestoringNotice, "answer from the fallback model", wantRestored},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := &stubPlatformEngine{n: "feishu"}
			agentSession := &liveModelSession{controllableAgentSession: newControllableSession("model-loop"), platform: p}
			h := newModelSwitchHarness(t, p, p, agentSession)
			h.e.SetModelFallbackAction("restore")
			h.state.lastModelRestoreAt = time.Now().Add(-tc.lastRestore)

			h.runTurn(agentSession.events, opusCyberFallback())

			assertSent(t, p.getSent(), tc.wantSent)
			if calls, _ := agentSession.switches(); fmt.Sprint(calls) != tc.wantCalls {
				t.Fatalf("SetLiveModel calls = %v, want %s", calls, tc.wantCalls)
			}
		})
	}
}

func TestModelFallbackRestore_FailureOffersChoices(t *testing.T) {
	p := &stubPlatformEngine{n: "feishu"}
	agentSession := &liveModelSession{controllableAgentSession: newControllableSession("model-restore-fail"), platform: p, err: errors.New("set_model rejected")}
	h := newModelSwitchHarness(t, p, p, agentSession)
	h.e.SetModelFallbackAction("restore")

	h.runTurn(agentSession.events, opusCyberFallback())

	assertSent(t, p.getSent(), []string{
		wantRestoringNotice,
		"answer from the fallback model",
		"⚠️ Could not switch back to `claude-opus-5-5` (set_model rejected); the session stays on `claude-opus-4-8`.\n" + wantCommands,
	})
	if !h.state.lastModelRestoreAt.IsZero() {
		t.Fatal("lastModelRestoreAt recorded for a failed switch")
	}
}

// An agent session that cannot switch in place gets the notify behaviour even
// in restore mode.
func TestModelFallbackRestore_AgentWithoutLiveSwitchNotifies(t *testing.T) {
	p := &stubPlatformEngine{n: "feishu"}
	agentSession := newControllableSession("model-no-live")
	h := newModelSwitchHarness(t, p, p, agentSession)
	h.e.SetModelFallbackAction("restore")

	h.runTurn(agentSession.events, opusCyberFallback())

	assertSent(t, p.getSent(), []string{wantSessionNotice + "\n" + wantCommands, "answer from the fallback model"})
}

// Card platforms get the ways back as buttons that run /model and /new, plus
// a button that keeps the fallback model.
func TestModelFallbackNotify_CardButtons(t *testing.T) {
	p := &stubCardPlatform{stubPlatformEngine: stubPlatformEngine{n: "feishu"}}
	agentSession := newControllableSession("model-card")
	h := newModelSwitchHarness(t, p, &p.stubPlatformEngine, agentSession)

	h.runTurn(agentSession.events, opusCyberFallback())

	p.mu.Lock()
	cards := append([]*Card(nil), p.sentCards...)
	p.mu.Unlock()
	if len(cards) != 1 {
		t.Fatalf("sent cards = %d, want 1", len(cards))
	}
	var buttons []string
	var text string
	for _, el := range cards[0].Elements {
		switch v := el.(type) {
		case CardMarkdown:
			text += v.Content
		case CardActions:
			for _, b := range v.Buttons {
				buttons = append(buttons, b.Text+" => "+b.Value)
			}
		}
	}
	if text != wantSessionNotice {
		t.Fatalf("card text = %q\nwant %q", text, wantSessionNotice)
	}
	want := []string{
		"Switch back to claude-opus-5-5 => cmd:/model switch claude-opus-5-5",
		"New session => cmd:/new",
		"Keep current => act:/model-fallback-keep claude-opus-4-8",
	}
	if fmt.Sprint(buttons) != fmt.Sprint(want) {
		t.Fatalf("buttons = %#v\nwant %#v", buttons, want)
	}

	kept := h.e.handleCardNav("act:/model-fallback-keep claude-opus-4-8", h.key)
	if kept == nil || !strings.Contains(kept.RenderText(), "Staying on `claude-opus-4-8`.") {
		t.Fatalf("keep action card = %#v, want the kept confirmation", kept)
	}
}

func TestModelFallbackNotify_InlineButtons(t *testing.T) {
	p := &stubInlineButtonPlatform{stubPlatformEngine: stubPlatformEngine{n: "telegram"}}
	agentSession := newControllableSession("model-inline")
	h := newModelSwitchHarness(t, p, &p.stubPlatformEngine, agentSession)

	h.runTurn(agentSession.events, opusCyberFallback())

	if p.buttonContent != wantSessionNotice+"\n"+wantCommands {
		t.Fatalf("button message = %q", p.buttonContent)
	}
	want := "[[{Switch back to claude-opus-5-5 cmd:/model switch claude-opus-5-5} {New session cmd:/new}]]"
	if got := fmt.Sprint(p.buttonRows); got != want {
		t.Fatalf("buttons = %s\nwant %s", got, want)
	}
}

// A fallback can also happen in a turn nobody typed (a background task
// notification); the unsolicited reader relays it and switches back too.
func TestUnsolicitedReader_RelaysModelSwitch(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	sess := &liveModelSession{controllableAgentSession: newControllableSession("unsol-model-switch"), platform: p}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	defer func() { _ = e.Stop() }()
	e.SetModelFallbackAction("restore")

	sessions := e.sessions
	iKey := "test:ch1:u1"
	session := sessions.GetOrCreateActive(iKey)
	state := &interactiveState{agentSession: sess, platform: p, replyCtx: "ctx"}
	e.interactiveMu.Lock()
	e.interactiveStates[iKey] = state
	e.interactiveMu.Unlock()

	e.startUnsolicitedReader(state, session, sessions, iKey, "")
	defer e.stopUnsolicitedReader(state)

	sess.events <- Event{Type: EventModelSwitch, ModelSwitch: opusCyberFallback()}
	sess.events <- Event{Type: EventResult, Content: "background result", Done: true}

	sent := waitForPlatformSend(p, 3, 5*time.Second)
	assertSent(t, sent, []string{wantRestoringNotice, "background result", wantRestored})
}

func TestModelSwitchNotices(t *testing.T) {
	p := &stubPlatformEngine{n: "feishu"}
	agentSession := newControllableSession("model-local")
	h := newModelSwitchHarness(t, p, p, agentSession)

	local := opusCyberFallback()
	local.SessionWide = false
	local.Category = ""
	h.e.relayModelSwitch(p, "ctx", h.key, h.state, local)
	for _, ms := range []*ModelSwitch{
		nil,
		{From: "claude-opus-5-5", SessionWide: true},
		{To: "claude-opus-4-8", SessionWide: true},
		{From: "claude-opus-4-8", To: "claude-opus-4-8", SessionWide: true},
	} {
		h.e.relayModelSwitch(p, "ctx", h.key, h.state, ms)
	}
	assertSent(t, p.getSent(), []string{"⚠️ `claude-opus-5-5`'s safeguards declined part of this turn; `claude-opus-4-8` answered that part instead. The session model is unchanged."})

	h.e.i18n.SetLang(LangChinese)
	h.e.relayModelSwitch(p, "ctx", h.key, h.state, opusCyberFallback())
	got := p.getSent()
	if want := "⚠️ `claude-opus-5-5` 的安全机制拦下了这条请求 [cyber]，改由 `claude-opus-4-8` 回答，本会话后面也会一直用它。\n发送 `/model switch claude-opus-5-5` 切回，或发送 `/new` 新开会话。"; got[len(got)-1] != want {
		t.Fatalf("zh notice = %q\nwant %q", got[len(got)-1], want)
	}
}

func TestSetModelFallbackAction(t *testing.T) {
	e := NewEngine("test", &stubAgent{}, nil, "", LangEnglish)
	defer func() { _ = e.Stop() }()
	for in, want := range map[string]string{"": "notify", "notify": "notify", " Restore ": "restore", "restore": "restore"} {
		e.SetModelFallbackAction(in)
		if e.modelFallbackAction != want {
			t.Errorf("SetModelFallbackAction(%q) = %q, want %q", in, e.modelFallbackAction, want)
		}
	}
}
