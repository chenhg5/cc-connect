package core

import (
	"strings"
	"testing"
)

func TestQueueMessageForBusySession_SkipPrefixesSuppressAck(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	sess := newQueuingSession("qs-skip-prefix")
	agent := &controllableAgent{nextSession: sess}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
	e.SetBusyAckSkipPrefixes([]string{"【问卷】", "cmd:【问卷】"})

	key := "test:skip-prefix-user"
	state := &interactiveState{agentSession: sess, platform: p, replyCtx: "ctx"}
	e.interactiveMu.Lock()
	e.interactiveStates[key] = state
	e.interactiveMu.Unlock()

	// Matching prefixes: queued silently — no busy ack reply.
	for _, content := range []string{
		"【问卷】[卡:3] Q1｜选项A",
		"cmd:【问卷】[卡:3] Q2｜选项B",
	} {
		msg := &Message{SessionKey: key, Content: content, ReplyCtx: "ctx"}
		if !e.queueMessageForBusySession(p, msg, key) {
			t.Fatalf("expected %q to be queued, got false", content)
		}
		if got := len(p.getSent()); got != 0 {
			t.Fatalf("after %q: platform replies = %d, want 0 (no busy ack for skipped prefix)", content, got)
		}
	}
	state.mu.Lock()
	depth := len(state.pendingMessages)
	state.mu.Unlock()
	if depth != 2 {
		t.Fatalf("queue depth = %d, want 2 (messages queued despite suppressed ack)", depth)
	}

	// Non-matching content still gets the busy ack (default behavior preserved).
	if !e.queueMessageForBusySession(p, &Message{SessionKey: key, Content: "regular question", ReplyCtx: "ctx"}, key) {
		t.Fatal("expected regular message to be queued")
	}
	sent := p.getSent()
	if len(sent) != 1 || !strings.Contains(sent[0], e.i18n.T(MsgMessageQueued)) {
		t.Fatalf("platform replies = %#v, want exactly one busy-ack", sent)
	}
}

// Without any configured prefixes every queued message is acknowledged,
// exactly as before the option existed.
func TestQueueMessageForBusySession_DefaultAcksEverything(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	sess := newQueuingSession("qs-ack-default")
	agent := &controllableAgent{nextSession: sess}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)

	key := "test:ack-default-user"
	state := &interactiveState{agentSession: sess, platform: p, replyCtx: "ctx"}
	e.interactiveMu.Lock()
	e.interactiveStates[key] = state
	e.interactiveMu.Unlock()

	msg := &Message{SessionKey: key, Content: "【问卷】should still ack without config", ReplyCtx: "ctx"}
	if !e.queueMessageForBusySession(p, msg, key) {
		t.Fatal("expected message to be queued")
	}
	sent := p.getSent()
	if len(sent) != 1 || !strings.Contains(sent[0], e.i18n.T(MsgMessageQueued)) {
		t.Fatalf("platform replies = %#v, want exactly one busy-ack", sent)
	}
}

func TestHasAnyPrefix(t *testing.T) {
	prefixes := []string{"a:", ""}
	cases := []struct {
		s    string
		want bool
	}{
		{"a:hello", true},
		{"a:", true},
		{"b:a:hello", false},
		{"hello", false},
		{"", false},
	}
	for _, c := range cases {
		if got := hasAnyPrefix(c.s, prefixes); got != c.want {
			t.Errorf("hasAnyPrefix(%q) = %v, want %v", c.s, got, c.want)
		}
	}
	if hasAnyPrefix("anything", nil) {
		t.Error("hasAnyPrefix with nil prefixes should be false")
	}
}
