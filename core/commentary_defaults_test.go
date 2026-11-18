package core

import (
	"strings"
	"testing"
	"time"
)

func TestProcessInteractiveEvents_CommentaryOptInPreservesLegacyOutput(t *testing.T) {
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
				wantStandalone := 0
				if setting.value != nil && *setting.value {
					wantStandalone = 3
				}
				if standalone != wantStandalone {
					t.Fatalf("standalone updates = %d, want %d; sent = %q", standalone, wantStandalone, sent)
				}
				wantAnswer := "Final answer"
				if wantStandalone == 0 {
					wantAnswer = strings.Repeat("Progress update. ", 3) + wantAnswer
				}
				if answer != wantAnswer {
					t.Fatalf("history = %q, want %q", answer, wantAnswer)
				}
				if strings.Count(strings.Join(sent, "\n"), "Final answer") != 1 {
					t.Fatalf("final answer missing or duplicated: %q", sent)
				}
			})
		}
	}
}

func TestProcessInteractiveEvents_DefaultCommentaryUsesAnswerCard(t *testing.T) {
	for _, enabled := range []*bool{nil, new(bool)} {
		p := &recordingStreamCardPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}, card: &recordingStreamCard{}}
		e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
		e.SetDisplayConfig(DisplayCfg{Mode: "quiet", CommentaryMessages: enabled})
		e.SetReplyFooterEnabled(false)
		session := e.sessions.GetOrCreateActive("test:commentary-card")
		s := newControllableSession("commentary-card")
		state := &interactiveState{agentSession: s, platform: p, replyCtx: "ctx"}
		s.events <- Event{Type: EventCommentary, Content: "Working. "}
		s.events <- Event{Type: EventToolUse, ToolName: "shell"}
		s.events <- Event{Type: EventText, Content: "Done."}
		s.events <- Event{Type: EventResult, Done: true}
		e.processInteractiveEvents(state, session, e.sessions, "test:commentary-card", "m", time.Now(), nil, nil, "ctx", 0)
		if sent := p.getSent(); len(sent) != 0 {
			t.Fatalf("legacy answer card created standalone messages: %q", sent)
		}
		card := p.card.finalContent()
		if !strings.Contains(card, "Working. ") || !strings.Contains(card, "Done.") {
			t.Fatalf("legacy answer card lost text: %q", card)
		}
		_ = e.Stop()
	}
}

func TestUnsolicitedReader_DefaultCommentaryPreservesAnswer(t *testing.T) {
	for _, enabled := range []*bool{nil, new(bool)} {
		p := &stubPlatformEngine{n: "test"}
		s := newControllableSession("default-background-commentary")
		e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
		e.SetDisplayConfig(DisplayCfg{CommentaryMessages: enabled})
		const key = "test:default-background-commentary"
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
		if len(sent) != 1 || sent[0] != "Working. Done." {
			t.Fatalf("background legacy response = %q", sent)
		}
	}
}

func TestProcessCompressEvents_DefaultCommentaryPreservesManualAnswer(t *testing.T) {
	for _, enabled := range []*bool{nil, new(bool)} {
		for _, auto := range []bool{false, true} {
			e := newTestEngine()
			p := &stubPlatformEngine{n: "test"}
			e.SetDisplayConfig(DisplayCfg{CommentaryMessages: enabled})
			s := newQueuingSession("default-compress-commentary")
			state := &interactiveState{agentSession: s, platform: p}
			session := e.sessions.GetOrCreateActive("test:default-compress-commentary")
			lockGen, _ := session.TryLock()
			s.events <- Event{Type: EventCommentary, Content: "Working. "}
			s.events <- Event{Type: EventText, Content: "Done."}
			s.events <- Event{Type: EventResult, Done: true}
			unlocked := false
			e.processCompressEvents(state, session, e.sessions, "test:default-compress-commentary", p, nil, &unlocked, auto, lockGen)
			session.Unlock(lockGen)
			e.cancel()
			sent := p.getSent()
			if auto {
				if len(sent) != 0 {
					t.Fatalf("automatic compression sent messages: %q", sent)
				}
			} else if len(sent) != 1 || sent[0] != "Working. Done." {
				t.Fatalf("manual compression legacy response = %q", sent)
			}
		}
	}
}
