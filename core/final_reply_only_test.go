package core

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

type quietTestPlatform struct{ *stubPlatformEngine }

func (*quietTestPlatform) FinalReplyOnly() bool { return true }

func TestFinalReplyOnly_ChannelClosePersistsHistory(t *testing.T) {
	for _, partial := range []string{"", "unfinished internal narration"} {
		t.Run(partial, func(t *testing.T) {
			p := &quietTestPlatform{&stubPlatformEngine{n: "test"}}
			storePath := filepath.Join(t.TempDir(), "sessions.json")
			e := NewEngine("test", &stubAgent{}, []Platform{p}, storePath, LangEnglish)
			t.Cleanup(func() { _ = e.Stop() })
			const key = "test:comment:crash"
			session := e.sessions.GetOrCreateActive(key)
			session.AddHistory("user", "explain this document")
			// A native session ID may arrive after the user history was saved.
			e.sessions.Save()
			session.SetAgentSessionID("late-native-session", "stub")
			events := make(chan Event, 1)
			if partial != "" {
				events <- Event{Type: EventText, Content: partial}
			}
			close(events)
			state := &interactiveState{
				agentSession: &resultAgentSession{events: events},
				platform:     p,
				replyCtx:     "comment",
			}
			e.interactiveStates[key] = state
			e.processInteractiveEvents(state, session, e.sessions, key, "crash", time.Now(), nil, nil, state.replyCtx, 0)
			got := p.getSent()
			if len(got) != 1 || !strings.Contains(got[0], "agent process exited before a final answer") {
				t.Fatalf("expected one failure notice without partial narration, got %q", got)
			}
			// Reload before Engine.Stop: shutdown must not hide a missing save.
			restored := NewSessionManager(storePath).GetOrCreateActive(key)
			if restored.ID != session.ID || restored.GetAgentSessionID() != "late-native-session" {
				t.Fatalf("session identity was not persisted: %#v", restored)
			}
			history := restored.GetHistory(0)
			wantLen := 1
			if partial != "" {
				wantLen++
			}
			if len(history) != wantLen || history[0].Content != "explain this document" {
				t.Fatalf("history lost after abnormal close: %#v", history)
			}
			if partial != "" && (history[1].Role != "assistant" || history[1].Content != partial) {
				t.Fatalf("partial assistant history lost: %#v", history)
			}
		})
	}
}

type quietUpdaterPlatform struct{ *mockUpdaterPlatform }

func (*quietUpdaterPlatform) FinalReplyOnly() bool { return true }

func TestFinalReplyOnly_UpdaterDoesNotPreviewOrAddLeadingSeparator(t *testing.T) {
	for _, boundary := range []EventType{EventThinking, EventToolUse} {
		t.Run(string(boundary), func(t *testing.T) {
			p := &quietUpdaterPlatform{&mockUpdaterPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}}}
			e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
			t.Cleanup(func() { _ = e.Stop() })
			e.streamPreview = StreamPreviewCfg{Enabled: true, MinDeltaChars: 1, IntervalMs: 100}
			as := newControllableSession("quiet-updater")
			t.Cleanup(func() { _ = as.Close() })
			as.events <- Event{Type: EventText, Content: "internal narration"}
			as.events <- Event{Type: boundary, Content: "thinking", ToolName: "Read"}
			as.events <- Event{Type: EventText, Content: "final answer"}
			as.events <- Event{Type: EventResult, Content: "internal narrationfinal answer", Done: true}
			state := &interactiveState{agentSession: as, platform: p, replyCtx: "comment"}
			session := e.sessions.GetOrCreateActive("test:updater")
			e.processInteractiveEvents(state, session, e.sessions, "test:updater", "updater", time.Now(), nil, nil, state.replyCtx, 0)
			if got := p.getSent(); !slices.Equal(got, []string{"final answer"}) {
				t.Fatalf("expected one final answer without a leading separator, got %q", got)
			}
			if got := p.getMessages(); len(got) != 0 {
				t.Fatalf("final-only platform emitted previews: %q", got)
			}
		})
	}
}

func TestCUJ_H6_FinalReplyOnlyDoesNotChangeIM(t *testing.T) {
	env := newCUJEnv(t)
	defer func() { _ = env.engine.Stop() }()
	env.engine.SetDisplayConfig(DisplayCfg{Mode: "full", ThinkingMessages: true, ToolMessages: true})
	events := []Event{
		{Type: EventText, Content: "内部进度说明"},
		{Type: EventThinking, Content: "内部思考"},
		{Type: EventToolUse, ToolName: "Read", ToolInput: "内部路径"},
		{Type: EventToolResult, ToolName: "Read", Content: "内部结果"},
		{Type: EventText, Content: "最终答复"},
		{Type: EventResult, Content: "内部进度说明最终答复", Done: true},
	}
	env.agent.mu.Lock()
	env.agent.nextSessionEvents = events
	env.agent.mu.Unlock()
	quiet := &quietTestPlatform{env.plat}
	env.engine.ReceiveMessage(quiet, &Message{SessionKey: "test:doc:1", Platform: "test", MessageID: "doc1", UserID: "alice", Content: "解释文档", ReplyCtx: "doc1"})
	env.waitFor("评论答复", 3*time.Second, func() bool { return len(env.plat.getSent()) >= 1 })
	for _, text := range env.plat.getSent() {
		if strings.Contains(text, "内部") {
			t.Fatalf("进度泄漏: %s", text)
		}
	}
	env.waitFor("首轮结束", 3*time.Second, func() bool { return len(env.activeSession("test:doc:1").GetHistory(0)) >= 2 })
	if got := env.plat.getSent(); !slices.Equal(got, []string{"最终答复"}) {
		t.Fatalf("comment must receive exactly one final answer: %v", got)
	}
	// A follow-up uses the same isolated conversation without changing output policy.
	env.agent.mu.Lock()
	as := env.agent.sessions[0]
	env.agent.mu.Unlock()
	as.mu.Lock()
	as.pendingEvents = events
	as.mu.Unlock()
	env.engine.ReceiveMessage(quiet, &Message{SessionKey: "test:doc:1", Platform: "test", MessageID: "doc2", UserID: "alice", Content: "Continue", ReplyCtx: "doc2"})
	env.waitFor("follow-up reply", 3*time.Second, func() bool {
		return len(env.activeSession("test:doc:1").GetHistory(0)) >= 4 && len(env.plat.getSent()) >= 2
	})
	if got := env.plat.getSent(); !slices.Equal(got, []string{"最终答复", "最终答复"}) {
		t.Fatalf("each comment turn must receive exactly one final answer: %v", got)
	}
	env.plat.clearSent()
	env.agent.mu.Lock()
	env.agent.nextSessionEvents = events
	env.agent.mu.Unlock()
	env.userSends("bob", "普通聊天仍显示进度")
	env.waitFor("IM 答复", 3*time.Second, func() bool { return env.sentContains("最终答复") })
	if !env.sentContains("内部思考") || !env.sentContains("内部路径") {
		t.Fatalf("普通会话进度被改动: %v", env.plat.getSent())
	}
}
