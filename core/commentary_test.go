package core

import (
	"strings"
	"testing"
	"time"
)

func TestProcessInteractiveEvents_CommentaryBypassesProgressCards(t *testing.T) {
	for _, cardMode := range []string{"default", "rich", "streaming"} {
		for _, thinking := range []bool{true, false} {
			t.Run(cardMode+"/thinking="+map[bool]string{true: "shown", false: "hidden"}[thinking], func(t *testing.T) {
				base := &stubCompactProgressPlatform{
					stubPlatformEngine: stubPlatformEngine{n: "test"}, style: "card", supportPayload: true,
				}
				var p Platform = base
				getSent := base.getSent
				if cardMode == "streaming" {
					stream := &recordingStreamCardPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}, card: &recordingStreamCard{}}
					p, getSent = stream, stream.getSent
				}
				e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
				e.SetDisplayConfig(DisplayCfg{Mode: "full", CardMode: cardMode, ThinkingMessages: thinking, ToolMessages: true})
				session := e.sessions.GetOrCreateActive("test:commentary")
				s := newControllableSession("commentary")
				state := &interactiveState{agentSession: s, platform: p, replyCtx: "ctx"}
				done := make(chan struct{})
				go func() {
					e.processInteractiveEvents(state, session, e.sessions, "test:commentary", "m", time.Now(), nil, nil, "ctx", 0)
					close(done)
				}()
				t.Cleanup(func() {
					e.cancel()
					select {
					case <-done:
					case <-time.After(2 * time.Second):
						t.Error("event processor did not stop")
					}
				})
				s.events <- Event{Type: EventThinking, Content: "internal planning"}
				s.events <- Event{Type: EventCommentary, Content: "Progress requested via /ps"}
				deadline := time.Now().Add(time.Second)
				for !strings.Contains(strings.Join(getSent(), "\n"), "Progress requested via /ps") {
					if time.Now().After(deadline) {
						t.Fatalf("no standalone progress before turn completion; sent=%v", getSent())
					}
					time.Sleep(time.Millisecond)
				}
				s.events <- Event{Type: EventCommentary, Content: "NO_REPLY"}
				s.events <- Event{Type: EventCommentary, Content: "   "}
				s.events <- Event{Type: EventText, Content: "Final answer"}
				s.events <- Event{Type: EventResult, Done: true}
				select {
				case <-done:
				case <-time.After(2 * time.Second):
					t.Fatal("turn did not complete")
				}
				sent := strings.Join(getSent(), "\n")
				if strings.Count(sent, "Progress requested via /ps") != 1 || strings.Contains(sent, "internal planning") || strings.Contains(sent, "NO_REPLY") {
					t.Fatalf("standalone messages duplicated progress or exposed reasoning/marker: %q", sent)
				}
				history := session.GetHistory(0)
				if len(history) == 0 || history[len(history)-1].Content != "Final answer" {
					t.Fatalf("final history contains progress: %+v", history)
				}
			})
		}
	}
}

func TestUnsolicitedReader_RelaysCommentaryBeforeCompletion(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	s := newControllableSession("commentary")
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	t.Cleanup(func() { _ = e.Stop() })
	const key = "test:background-commentary"
	session := e.sessions.GetOrCreateActive(key)
	state := &interactiveState{agentSession: s, platform: p, replyCtx: "ctx"}
	e.interactiveStates[key] = state
	e.startUnsolicitedReader(state, session, e.sessions, key, "")
	defer e.stopUnsolicitedReader(state)

	s.events <- Event{Type: EventThinking, Content: "internal planning"}
	s.events <- Event{Type: EventCommentary, Content: "Background progress"}
	sent := waitForPlatformSend(p, 1, 2*time.Second)
	if len(sent) != 1 || sent[0] != "Background progress" {
		t.Fatalf("progress must arrive without a terminal result: %v", sent)
	}
}

func TestProcessInteractiveEvents_QuietCommentaryOptOut(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		t.Run(map[bool]string{true: "enabled", false: "disabled"}[enabled], func(t *testing.T) {
			p := &stubPlatformEngine{n: "test"}
			e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
			defer func() { _ = e.Stop() }()
			e.SetDisplayConfig(DisplayCfg{Mode: "quiet", CommentaryMessages: &enabled})
			session := e.sessions.GetOrCreateActive("test:quiet-commentary")
			s := newControllableSession("quiet-commentary")
			state := &interactiveState{agentSession: s, platform: p, replyCtx: "ctx"}
			done := make(chan struct{})
			go func() {
				e.processInteractiveEvents(state, session, e.sessions, "test:quiet-commentary", "m", time.Now(), nil, nil, "ctx", 0)
				close(done)
			}()
			for i := 0; i < 3; i++ {
				s.events <- Event{Type: EventCommentary, Content: "Progress update"}
				s.events <- Event{Type: EventToolUse, ToolName: "shell"}
			}
			s.events <- Event{Type: EventText, Content: "Final answer"}
			s.events <- Event{Type: EventResult, Done: true}
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("turn did not complete")
			}
			sent := strings.Join(p.getSent(), "\n")
			want := 0
			if enabled {
				want = 3
			}
			if got := strings.Count(sent, "Progress update"); got != want {
				t.Fatalf("progress count = %d, want %d; sent=%q", got, want, sent)
			}
			if strings.Count(sent, "Final answer") != 1 {
				t.Fatalf("final answer missing/duplicated: %q", sent)
			}
		})
	}
}

func TestProcessCompressEvents_CommentaryOnlyForManualCompression(t *testing.T) {
	for _, auto := range []bool{false, true} {
		t.Run(map[bool]string{false: "manual", true: "auto"}[auto], func(t *testing.T) {
			e := newTestEngine()
			defer e.cancel()
			p := &stubPlatformEngine{n: "test"}
			agentSession := newQueuingSession("compress")
			state := &interactiveState{agentSession: agentSession, platform: p}
			session := e.sessions.GetOrCreateActive("test:compress-commentary")
			lockGen, _ := session.TryLock()
			defer session.Unlock(lockGen)
			agentSession.events <- Event{Type: EventCommentary, Content: "Compressing progress"}
			agentSession.events <- Event{Type: EventResult, Done: true}
			unlocked := false
			e.processCompressEvents(state, session, e.sessions, "test:compress-commentary", p, nil, &unlocked, auto, lockGen)
			got := strings.Count(strings.Join(p.getSent(), "\n"), "Compressing progress")
			want := 1
			if auto {
				want = 0
			}
			if got != want {
				t.Fatalf("progress count = %d, want %d", got, want)
			}
		})
	}
}
