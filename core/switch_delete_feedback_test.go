package core

import (
	"strings"
	"sync"
	"testing"
	"time"
)

func TestFailedSwitchPreservesRunningStateAndReportsMiss(t *testing.T) {
	for _, pending := range []bool{false, true} {
		for _, card := range []bool{false, true} {
			t.Run(map[bool]string{false: "text", true: "card"}[card]+map[bool]string{false: "/deleted", true: "/pending"}[pending], func(t *testing.T) {
				p := &stubPlatformEngine{n: "test"}
				a := &stubListAgent{sessions: []AgentSessionInfo{{ID: "target", Summary: "Target"}}}
				e := NewEngine("test", a, []Platform{p}, "", LangEnglish)
				key := "test:user"
				current := e.sessions.SwitchToAgentSession(key, "old", "stub", "Old")
				live := newControllableSession("old")
				state := &interactiveState{agentSession: live, platform: p, replyCtx: "ctx", busySession: current}
				e.interactiveStates[key] = state
				if pending {
					e.sessions.deletionWaitTimeout = 20 * time.Millisecond
					entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
					var once sync.Once
					finish := func() { once.Do(func() { close(release) }) }
					t.Cleanup(func() { finish(); <-done })
					go func() {
						defer close(done)
						_ = e.sessions.DeleteAgentSessionIfInactive("target", "caller", func() error { close(entered); <-release; return nil })
					}()
					select {
					case <-entered:
					case <-time.After(time.Second):
						t.Fatal("deletion never started")
					}
				} else if err := e.sessions.DeleteAgentSessionIfInactive("target", "caller", func() error { return nil }); err != nil {
					t.Fatal(err)
				}
				if card {
					e.executeCardAction("/switch", "target", key)
				} else {
					e.cmdSwitch(p, &Message{SessionKey: key, ReplyCtx: "ctx"}, []string{"target"})
				}
				e.interactiveMu.Lock()
				retained := e.interactiveStates[key] == state
				e.interactiveMu.Unlock()
				if !retained || !live.Alive() || live.closeFinished.Load() || current != e.sessions.GetOrCreateActive(key) {
					t.Fatal("failed switch aborted or replaced the current conversation")
				}
				want := e.i18n.Tf(MsgSwitchNoMatch, "target")
				if pending {
					want = e.i18n.Tf(MsgError, ErrAgentSessionDeletionPending)
				}
				deadline := time.Now().Add(time.Second)
				for !strings.Contains(strings.Join(p.getSent(), "\n"), want) && time.Now().Before(deadline) {
					time.Sleep(time.Millisecond)
				}
				if !strings.Contains(strings.Join(p.getSent(), "\n"), want) {
					t.Fatalf("failed switch missing feedback: %v", p.getSent())
				}
			})
		}
	}
}

func TestSuccessfulSwitchRetiresCurrentInteraction(t *testing.T) {
	for _, card := range []bool{false, true} {
		t.Run(map[bool]string{false: "text", true: "card"}[card], func(t *testing.T) {
			p := &stubPlatformEngine{n: "test"}
			a := &stubListAgent{sessions: []AgentSessionInfo{{ID: "target", Summary: "Target"}}}
			e := NewEngine("test", a, []Platform{p}, "", LangEnglish)
			key := "test:user"
			current := e.sessions.SwitchToAgentSession(key, "old", "stub", "Old")
			live := newControllableSession("old")
			e.interactiveStates[key] = &interactiveState{agentSession: live, platform: p, replyCtx: "ctx", busySession: current}
			if card {
				e.executeCardAction("/switch", "target", key)
			} else {
				e.cmdSwitch(p, &Message{SessionKey: key, ReplyCtx: "ctx"}, []string{"target"})
			}
			if e.sessions.GetOrCreateActive(key).GetAgentSessionID() != "target" {
				t.Fatal("switch failed")
			}
			e.interactiveMu.Lock()
			state := e.interactiveStates[key]
			e.interactiveMu.Unlock()
			if state != nil {
				t.Fatal("successful switch retained old live session")
			}
		})
	}
}
