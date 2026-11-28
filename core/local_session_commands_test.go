package core

import (
	"strings"
	"testing"
)

func TestCmdListAndSwitchLocalSessionsWithoutAgentTranscript(t *testing.T) {
	agent := &stubListAgent{}
	p := &stubPlatformEngine{n: "plain"}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
	key := "weixin:user-1"
	first := e.sessions.NewSession(key, "test")
	second := e.sessions.NewSession(key, "test2")

	e.cmdList(p, &Message{SessionKey: key, ReplyCtx: "ctx"}, nil)
	if len(p.sent) != 1 || !strings.Contains(p.sent[0], "test") || !strings.Contains(p.sent[0], "test2") {
		t.Fatalf("/list omitted local sessions: %#v", p.sent)
	}
	if strings.Count(p.sent[0], "▶") != 1 {
		t.Fatalf("/list must mark one active local session: %q", p.sent[0])
	}

	p.sent = nil
	e.cmdSwitch(p, &Message{SessionKey: key, ReplyCtx: "ctx"}, []string{"test"})
	if got := e.sessions.ActiveSessionID(key); got != first.ID {
		t.Fatalf("/switch test activated %q; want %q (second=%q), reply=%#v", got, first.ID, second.ID, p.sent)
	}

	p.sent = nil
	e.cmdSwitch(p, &Message{SessionKey: key, ReplyCtx: "ctx"}, []string{"2"})
	if got := e.sessions.ActiveSessionID(key); got != second.ID {
		t.Fatalf("/switch 2 activated %q; want %q", got, second.ID)
	}
	agent.sessions = []AgentSessionInfo{{ID: "unrelated", Summary: "test helper"}}
	e.cmdSwitch(p, &Message{SessionKey: key, ReplyCtx: "ctx"}, []string{"test"})
	if got := e.sessions.ActiveSessionID(key); got != first.ID {
		t.Fatalf("exact local name matched agent summary: got %q, want %q", got, first.ID)
	}
	e.cmdSwitch(p, &Message{SessionKey: key, ReplyCtx: "ctx"}, []string{second.ID})
	if got := e.sessions.ActiveSessionID(key); got != second.ID {
		t.Fatalf("local session ID activated %q; want %q", got, second.ID)
	}

	p.sent = nil
	e.cmdList(p, &Message{SessionKey: "weixin:user-2", ReplyCtx: "ctx"}, nil)
	if len(p.sent) != 1 || strings.Contains(p.sent[0], "test2") {
		t.Fatalf("another user saw local sessions: %#v", p.sent)
	}
}

func TestRenderListCardIncludesLocalSessionWithoutTranscript(t *testing.T) {
	e := NewEngine("test", &stubListAgent{}, []Platform{&stubPlatformEngine{n: "plain"}}, "", LangEnglish)
	key := "weixin:user-1"
	e.sessions.NewSession(key, "named")
	card, err := e.renderListCard(key, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got := countCardActionValues(card, "act:/switch "); got != 1 {
		t.Fatalf("card switch actions = %d, want 1", got)
	}
}

func TestListNumberReplySwitchesToLocalSession(t *testing.T) {
	agent := &stubListAgent{}
	p := &stubPlatformEngine{n: "plain"}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
	key := "weixin:user-1"
	first := e.sessions.NewSession(key, "test")
	second := e.sessions.NewSession(key, "test2")

	for _, tc := range []struct {
		reply string
		want  string
	}{{"1", first.ID}, {"2", second.ID}} {
		// The card /list (and grouped text /list) arms numeric replies.
		e.setListSelection(key, &listSelectionState{mode: listSelectionGroups})
		p.sent = nil
		if !e.handleListSelectionInput(p, &Message{SessionKey: key, ReplyCtx: "ctx", Content: tc.reply}, tc.reply) {
			t.Fatalf("reply %q after /list was not handled", tc.reply)
		}
		if got := e.sessions.ActiveSessionID(key); got != tc.want {
			t.Fatalf("reply %q activated %q; want %q, sent=%#v", tc.reply, got, tc.want, p.sent)
		}
		if s := e.sessions.GetOrCreateActive(key); strings.HasPrefix(s.AgentSessionID, localSessionPrefix) {
			t.Fatalf("reply %q stored bogus agent session id %q", tc.reply, s.AgentSessionID)
		}
	}
}
