package core

import (
	"strings"
	"testing"
	"time"
)

// TestResolveUnsolicitedReply_NoReplyMarker covers the NO_REPLY semantics
// applied to background (unsolicited) turns: a bare marker delivers nothing,
// a trailing marker is stripped from otherwise normal content.
func TestResolveUnsolicitedReply_NoReplyMarker(t *testing.T) {
	cases := []struct {
		in         string
		want       string
		wantSilent bool
	}{
		{"NO_REPLY", "", true},
		{"  no_reply\n", "", true},
		{"Done.\nNO_REPLY", "Done.", false},
		{"normal reply", "normal reply", false},
		{"", "", false},
	}
	for _, tc := range cases {
		got, silent := resolveUnsolicitedReply(tc.in)
		if got != tc.want || silent != tc.wantSilent {
			t.Errorf("resolveUnsolicitedReply(%q) = (%q, %v), want (%q, %v)", tc.in, got, silent, tc.want, tc.wantSilent)
		}
	}
}

// TestUnsolicitedReader_SuppressesBareNoReply is the regression test for
// background turns leaking the literal "NO_REPLY" marker to the platform.
// Before the fix, runUnsolicitedReader forwarded EventResult.Content as-is,
// so a background task notification answered with NO_REPLY was pushed to
// the chat verbatim. A follow-up turn with real content must still be sent,
// and a trailing marker must be stripped from it.
func TestUnsolicitedReader_SuppressesBareNoReply(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	sess := newControllableSession("unsol-silent")
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	defer e.Stop()

	sessions := e.sessions
	session := sessions.GetOrCreateActive("test:ch1:u1")

	state := &interactiveState{
		agentSession:     sess,
		platform:         p,
		replyCtx:         "ctx",
		eventsNeedResync: false,
	}
	iKey := "test:ch1:u1"
	e.interactiveMu.Lock()
	e.interactiveStates[iKey] = state
	e.interactiveMu.Unlock()

	e.startUnsolicitedReader(state, session, sessions, iKey, "")
	defer e.stopUnsolicitedReader(state)

	sess.events <- Event{Type: EventResult, Content: "NO_REPLY", Done: true}
	// Second turn: real content followed by a trailing marker.
	sess.events <- Event{Type: EventResult, Content: "Task finished.\nNO_REPLY", Done: true}

	waitForPlatformSend(p, 1, 5*time.Second)
	// Give the reader a moment to (wrongly) emit anything else.
	time.Sleep(100 * time.Millisecond)
	sent := p.getSent()
	if len(sent) != 1 {
		t.Fatalf("expected exactly 1 delivered message, got %d: %v", len(sent), sent)
	}
	if strings.Contains(sent[0], "NO_REPLY") {
		t.Errorf("NO_REPLY marker leaked to platform: %q", sent[0])
	}
	if !strings.Contains(sent[0], "Task finished.") {
		t.Errorf("real content not delivered, got %q", sent[0])
	}
}
