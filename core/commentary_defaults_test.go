package core

import (
	"strings"
	"testing"
	"time"
)

func TestProcessInteractiveEvents_QuietCommentaryOptOut(t *testing.T) {
	yes, no := true, false
	for _, mode := range []string{"quiet"} {
		for _, setting := range []struct {
			name  string
			value *bool
		}{
			{"absent", nil}, {"false", &no}, {"true", &yes},
		} {
			t.Run(mode+"/"+setting.name, func(t *testing.T) {
				p := &stubPlatformEngine{n: "test"}
				e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
				defer func() { _ = e.Stop() }()
				e.SetDisplayConfig(DisplayCfg{Mode: mode, CommentaryMessages: setting.value})
				e.SetStreamPreviewCfg(StreamPreviewCfg{Enabled: false})
				e.SetReplyFooterEnabled(false)
				e.SetShowContextIndicator(false)
				session := e.sessions.GetOrCreateActive("test:commentary-default")
				s := newControllableSession("commentary-default")
				state := &interactiveState{agentSession: s, platform: p, replyCtx: "ctx"}
				// Three updates reproduce the quiet-mode notification burst.
				for i := 0; i < 3; i++ {
					s.events <- Event{Type: EventCommentary, Content: "Progress update. "}
					s.events <- Event{Type: EventToolUse, ToolName: "shell"}
				}
				s.events <- Event{Type: EventText, Content: "Final answer"}
				s.events <- Event{Type: EventResult, Done: true}
				e.processInteractiveEvents(state, session, e.sessions, "test:commentary-default", "m", time.Now(), nil, nil, "ctx", 0)

				sent := p.getSent()
				history := session.GetHistory(0)
				if len(history) == 0 {
					t.Fatal("final history missing")
				}
				answer := history[len(history)-1].Content
				standalone := 0
				for _, text := range sent {
					if strings.TrimSpace(text) == "Progress update." {
						standalone++
					}
				}
				wantStandalone := 3
				if setting.value != nil && !*setting.value {
					wantStandalone = 0
				}
				if standalone != wantStandalone {
					t.Fatalf("standalone updates = %d, want %d; sent = %q", standalone, wantStandalone, sent)
				}
				if answer != "Final answer" {
					t.Fatalf("history = %q, want %q", answer, "Final answer")
				}
				if strings.Count(strings.Join(sent, "\n"), "Final answer") != 1 {
					t.Fatalf("final answer missing or duplicated: %q", sent)
				}
			})
		}
	}
}

func TestProcessInteractiveEvents_CommentaryAnswerCardPolicy(t *testing.T) {
	yes, no := true, false
	for _, tc := range []struct {
		name           string
		enabled        *bool
		wantStandalone bool
	}{
		{"absent", nil, true},
		{"true", &yes, true},
		{"false", &no, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &recordingStreamCardPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}, card: &recordingStreamCard{}}
			e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
			e.SetDisplayConfig(DisplayCfg{Mode: "quiet", CommentaryMessages: tc.enabled})
			e.SetReplyFooterEnabled(false)
			session := e.sessions.GetOrCreateActive("test:commentary-card")
			s := newControllableSession("commentary-card")
			state := &interactiveState{agentSession: s, platform: p, replyCtx: "ctx"}
			s.events <- Event{Type: EventCommentary, Content: "Working. "}
			s.events <- Event{Type: EventToolUse, ToolName: "shell"}
			s.events <- Event{Type: EventText, Content: "Done."}
			s.events <- Event{Type: EventResult, Done: true}
			e.processInteractiveEvents(state, session, e.sessions, "test:commentary-card", "m", time.Now(), nil, nil, "ctx", 0)
			sent := p.getSent()
			if tc.wantStandalone {
				if len(sent) != 1 || sent[0] != "Working." {
					t.Fatalf("standalone commentary = %q, want [\"Working.\"]", sent)
				}
			} else if len(sent) != 0 {
				t.Fatalf("opt-out sent standalone messages: %q", sent)
			}
			card := p.card.finalContent()
			if !strings.Contains(card, "Done.") || strings.Contains(card, "Working.") {
				t.Fatalf("answer card = %q, want only final answer", card)
			}
			_ = e.Stop()
		})
	}
}

func TestUnsolicitedReader_CommentaryOptOutPolicy(t *testing.T) {
	no := false
	p := &stubPlatformEngine{n: "test"}
	s := newControllableSession("optout-background-commentary")
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	e.SetDisplayConfig(DisplayCfg{CommentaryMessages: &no})
	const key = "test:optout-background-commentary"
	session := e.sessions.GetOrCreateActive(key)
	state := &interactiveState{agentSession: s, platform: p, replyCtx: "ctx"}
	e.interactiveStates[key] = state
	e.startUnsolicitedReader(state, session, e.sessions, key, "")
	s.events <- Event{Type: EventCommentary, Content: "Working. "}
	s.events <- Event{Type: EventText, Content: "Done."}
	s.events <- Event{Type: EventResult, Done: true}
	sent := waitForPlatformSend(p, 1, 2*time.Second)
	e.stopUnsolicitedReader(state)
	_ = e.Stop()
	if len(sent) != 1 || sent[0] != "Done." {
		t.Fatalf("background opt-out response = %q, want [\"Done.\"]", sent)
	}
}

func TestProcessCompressEvents_CommentaryOptOutPolicy(t *testing.T) {
	no := false
	for _, auto := range []bool{false, true} {
		e := newTestEngine()
		p := &stubPlatformEngine{n: "test"}
		e.SetDisplayConfig(DisplayCfg{CommentaryMessages: &no})
		s := newQueuingSession("optout-compress-commentary")
		state := &interactiveState{agentSession: s, platform: p}
		session := e.sessions.GetOrCreateActive("test:optout-compress-commentary")
		lockGen, _ := session.TryLock()
		s.events <- Event{Type: EventCommentary, Content: "Working. "}
		s.events <- Event{Type: EventText, Content: "Done."}
		s.events <- Event{Type: EventResult, Done: true}
		unlocked := false
		e.processCompressEvents(state, session, e.sessions, "test:optout-compress-commentary", p, nil, &unlocked, auto, lockGen)
		session.Unlock(lockGen)
		e.cancel()
		sent := p.getSent()
		if auto {
			if len(sent) != 0 {
				t.Fatalf("automatic compression sent messages: %q", sent)
			}
		} else if len(sent) != 1 || sent[0] != "Done." {
			t.Fatalf("manual compression opt-out response = %q, want [\"Done.\"]", sent)
		}
	}
}
