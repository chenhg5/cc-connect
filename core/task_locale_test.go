package core

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

type localeRichPlatform struct{ *stubRichCardSilentPlatform }

func (p *localeRichPlatform) BuildRichCardLocalized(status CardStatus, _ string, steps []ToolStep, body string, _ bool, footer string, lang Language) string {
	return fmt.Sprintf("locale=%s status=%s steps=%d body=%s footer=%s", lang, status, len(steps), body, footer)
}

func TestCUJ_L2_RunningTaskKeepsLocaleWhileCommandsChange(t *testing.T) {
	p := &localeRichPlatform{&stubRichCardSilentPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}}}
	s := newQueuingSession("locale-task")
	a := &controllableAgent{nextSession: s}
	e := NewEngine("locale", a, []Platform{p}, t.TempDir()+"/sessions.json", LangAuto)
	e.SetReplyFooterEnabled(true)
	e.SetDisplayConfig(DisplayCfg{Mode: "full", CardMode: "rich", ToolMessages: true, ToolMaxLen: 500})
	defer e.cancel()
	// Drive the actual callback entry used by platforms, not only its public wrapper.
	e.handleMessage(p, &Message{SessionKey: "test:alice", Content: "检查工具", ReplyCtx: "ctx"})
	wait := func(condition func() bool) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			if condition() {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatal("timed out")
	}
	wait(func() bool { s.sendMu.Lock(); defer s.sendMu.Unlock(); return len(s.sendCalls) > 0 })
	s.events <- Event{Type: EventToolUse, ToolName: "Bash", ToolInput: "pwd"}
	wait(func() bool { starts, _, _, _ := p.snapshot(); return len(starts) == 1 })
	e.ReceiveMessage(p, &Message{SessionKey: "test:alice", Content: "/help", ReplyCtx: "help"})
	e.ReceiveMessage(p, &Message{SessionKey: "test:bob", Content: "/help", ReplyCtx: "bob"})
	s.events <- Event{Type: EventResult, Content: "验证完成", Done: true}
	wait(func() bool {
		_, _, updates, _ := p.snapshot()
		for _, u := range updates {
			if strings.Contains(u, "status=done") {
				return true
			}
		}
		return false
	})
	starts, _, updates, _ := p.snapshot()
	if len(starts) != 1 {
		t.Fatalf("tool messages created %d cards", len(starts))
	}
	last := updates[len(updates)-1]
	if !strings.Contains(last, "locale=zh") || !strings.Contains(last, "⏱ 用时") {
		t.Fatalf("task locale changed: %s", last)
	}
}

func TestRichCard_StopAndErrorFinalizeWithTaskLocale(t *testing.T) {
	for _, mode := range []string{"error", "stop", "closed"} {
		t.Run(mode, func(t *testing.T) {
			p := &localeRichPlatform{&stubRichCardSilentPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}}}
			e := NewEngine("locale", &stubAgent{}, []Platform{p}, "", LangEnglish)
			e.SetReplyFooterEnabled(true)
			e.SetDisplayConfig(DisplayCfg{Mode: "full", CardMode: "rich", ToolMessages: true, ToolMaxLen: 500})
			defer e.cancel()
			s := newControllableSession("stop-locale")
			state := &interactiveState{language: LangChinese, platform: p, replyCtx: "ctx", agentSession: s}
			session := e.sessions.GetOrCreateActive("test:alice")
			done := make(chan struct{})
			go func() {
				defer close(done)
				e.processInteractiveEvents(state, session, e.sessions, "test:alice", "m", time.Now(), nil, nil, "ctx", 0)
			}()
			s.events <- Event{Type: EventToolUse, ToolName: "Bash", ToolInput: "pwd"}
			deadline := time.Now().Add(3 * time.Second)
			for {
				starts, _, _, _ := p.snapshot()
				if len(starts) > 0 {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("no card")
				}
				time.Sleep(time.Millisecond)
			}
			switch mode {
			case "stop":
				state.markStopped()
			case "closed":
				s.events <- Event{Type: EventText, Content: "部分回复"}
				close(s.events)
			default:
				s.events <- Event{Type: EventError, Error: fmt.Errorf("failure")}
			}
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("turn did not stop")
			}
			_, _, updates, _ := p.snapshot()
			if len(updates) == 0 {
				t.Fatal("card not finalized")
			}
			last := updates[len(updates)-1]
			if !strings.Contains(last, "locale=zh") || !strings.Contains(last, "⏱ 用时") {
				t.Fatal(last)
			}
			if mode == "stop" && !strings.Contains(last, "status=stopped") {
				t.Fatal(last)
			}
			if mode == "closed" {
				if !strings.Contains(last, "status=error") || !strings.Contains(last, "部分回复") {
					t.Fatal(last)
				}
				if sent := p.getSent(); len(sent) != 0 {
					t.Fatalf("duplicate plain replies after card finalization: %v", sent)
				}
				if history := session.GetHistory(1); len(history) != 1 || history[0].Content != "部分回复" {
					t.Fatalf("partial response history was lost: %v", history)
				}
			}
		})
	}
}

var _ Platform = (*localeRichPlatform)(nil)
