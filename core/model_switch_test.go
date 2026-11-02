package core

import (
	"fmt"
	"strings"
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

// A safeguards fallback in the middle of a turn is announced to the user as its
// own message, ahead of the reply that the fallback model produced, and is not
// mixed into the reply text or the session history.
func TestProcessInteractiveEvents_AnnouncesModelSwitch(t *testing.T) {
	p := &stubPlatformEngine{n: "feishu"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	e.SetDisplayConfig(DisplayCfg{Mode: "quiet", ToolMessages: false})
	sessionKey := "feishu:model-switch"
	session := e.sessions.GetOrCreateActive(sessionKey)
	agentSession := newControllableSession("model-switch")
	state := &interactiveState{
		agentSession: agentSession,
		platform:     p,
		replyCtx:     "ctx-model-switch",
	}
	e.interactiveStates[sessionKey] = state

	agentSession.events <- Event{Type: EventModelSwitch, ModelSwitch: opusCyberFallback()}
	agentSession.events <- Event{Type: EventText, Content: "answer from the fallback model"}
	agentSession.events <- Event{Type: EventResult, Content: "answer from the fallback model", Done: true}

	e.processInteractiveEvents(state, session, e.sessions, sessionKey, "m-model-switch", time.Now(), nil, nil, state.replyCtx, 0)

	sent := p.getSent()
	want := []string{
		"⚠️ `claude-opus-5-5`'s safeguards declined this request [cyber], so `claude-opus-4-8` is answering instead and the rest of this session stays on it. Send `/model claude-opus-5-5` to switch back.",
		"answer from the fallback model",
	}
	if fmt.Sprint(sent) != fmt.Sprint(want) {
		t.Fatalf("sent = %#v\nwant %#v", sent, want)
	}
	if got := session.GetHistory(0); len(got) != 1 || got[0].Content != "answer from the fallback model" {
		t.Fatalf("history = %#v, want only the reply", got)
	}
}

// A fallback can also happen in a turn nobody typed (a background task
// notification); the unsolicited reader relays it too.
func TestUnsolicitedReader_RelaysModelSwitch(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	sess := newControllableSession("unsol-model-switch")
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	defer func() { _ = e.Stop() }()

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

	sent := waitForPlatformSend(p, 1, 5*time.Second)
	if len(sent) != 1 || !strings.Contains(sent[0], "`claude-opus-4-8` is answering instead") {
		t.Fatalf("sent = %#v, want the model switch notice", sent)
	}
}

func TestModelSwitchNotice(t *testing.T) {
	e := NewEngine("test", &stubAgent{}, nil, "", LangEnglish)

	local := opusCyberFallback()
	local.SessionWide = false
	local.Category = ""
	if got, want := e.modelSwitchNotice(local), "⚠️ `claude-opus-5-5`'s safeguards declined part of this turn; `claude-opus-4-8` answered that part instead. The session model is unchanged."; got != want {
		t.Fatalf("local notice = %q\nwant %q", got, want)
	}

	for name, ms := range map[string]*ModelSwitch{
		"nil":        nil,
		"no target":  {From: "claude-opus-5-5", SessionWide: true},
		"no source":  {To: "claude-opus-4-8", SessionWide: true},
		"same model": {From: "claude-opus-4-8", To: "claude-opus-4-8", SessionWide: true},
	} {
		if got := e.modelSwitchNotice(ms); got != "" {
			t.Errorf("%s: notice = %q, want none", name, got)
		}
	}

	e.i18n.SetLang(LangChinese)
	if got, want := e.modelSwitchNotice(opusCyberFallback()), "⚠️ `claude-opus-5-5` 的安全机制拦下了这条请求 [cyber]，改由 `claude-opus-4-8` 回答，本会话后面也会一直用它。发送 `/model claude-opus-5-5` 可切回。"; got != want {
		t.Fatalf("zh notice = %q\nwant %q", got, want)
	}
}
