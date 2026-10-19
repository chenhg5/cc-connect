package core

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type handoffStubAgent struct {
	stubAgent
	handoffs map[string]*SessionHandoff
}

func (a *handoffStubAgent) ReadSessionHandoff(id string) (*SessionHandoff, error) {
	if h, ok := a.handoffs[id]; ok {
		return h, nil
	}
	return nil, errNotFound
}

var errNotFound = &stubErr{"not found"}

type stubErr struct{ s string }

func (e *stubErr) Error() string { return e.s }

func TestFreshOnColdCache(t *testing.T) {
	a := &handoffStubAgent{handoffs: map[string]*SessionHandoff{
		"old":   {Path: "/x/old.jsonl", LastActivity: time.Now().Add(-2 * time.Hour), ContextTokens: 150000},
		"warm":  {LastActivity: time.Now().Add(-10 * time.Minute), ContextTokens: 150000},
		"small": {LastActivity: time.Now().Add(-3 * time.Hour), ContextTokens: 20000},
	}}
	e := NewEngine("test", a, []Platform{&stubPlatformEngine{n: "x"}}, "", LangEnglish)
	defer e.Stop()

	s := &Session{AgentSessionID: "old"}
	if n := e.freshOnColdCache(a, s); n != "" {
		t.Fatalf("disabled but got %q", n)
	}
	e.SetColdCacheFresh(true, time.Hour, 80000)
	for _, id := range []string{"warm", "small", "missing"} {
		s := &Session{AgentSessionID: id}
		if n := e.freshOnColdCache(a, s); n != "" || s.GetAgentSessionID() != id {
			t.Fatalf("%s: should resume, got %q", id, n)
		}
	}
	n := e.freshOnColdCache(a, s)
	if !strings.Contains(n, "15.0 万") || !strings.Contains(n, "2.0 小时") {
		t.Fatalf("notice = %q", n)
	}
	if s.GetAgentSessionID() != "" || s.HandoffFrom != "old" {
		t.Fatalf("agent id %q handoff %q", s.GetAgentSessionID(), s.HandoffFrom)
	}
	// The first turn of the fresh context carries the handoff, once.
	if b := e.handoffBlock(s); !strings.Contains(b, "/x/old.jsonl") || !strings.Contains(b, "缓存已过期") {
		t.Fatalf("handoff block:\n%s", b)
	}
	if b := e.handoffBlock(s); b != "" {
		t.Fatalf("handoff sent twice:\n%s", b)
	}
}

func TestFreshOnImageBloat(t *testing.T) {
	a := &handoffStubAgent{handoffs: map[string]*SessionHandoff{
		// recent and small in tokens: the cold-cache rule keeps it, the image rule must not
		"heavy": {Path: "/x/heavy.jsonl", LastActivity: time.Now().Add(-time.Minute), ContextTokens: 60000, ImageBytes: 8_300_000},
		"light": {LastActivity: time.Now().Add(-time.Minute), ContextTokens: 60000, ImageBytes: 1_000_000},
	}}
	e := NewEngine("test", a, []Platform{&stubPlatformEngine{n: "x"}}, "", LangEnglish)
	defer e.Stop()
	e.SetColdCacheFresh(true, time.Hour, 80000)

	s := &Session{AgentSessionID: "heavy"}
	if n := e.freshOnImageBloat(a, s); n != "" || s.GetAgentSessionID() != "heavy" {
		t.Fatalf("disabled but got %q", n)
	}
	e.SetImageFresh(true, 3_000_000)
	light := &Session{AgentSessionID: "light"}
	if n := e.freshOnImageBloat(a, light); n != "" || light.GetAgentSessionID() != "light" {
		t.Fatalf("light: should resume, got %q", n)
	}
	n := e.freshOnImageBloat(a, s)
	if !strings.Contains(n, "8.3 MB") {
		t.Fatalf("notice = %q", n)
	}
	if s.GetAgentSessionID() != "" || s.HandoffFrom != "heavy" {
		t.Fatalf("agent id %q handoff %q", s.GetAgentSessionID(), s.HandoffFrom)
	}
	if b := e.handoffBlock(s); !strings.Contains(b, "/x/heavy.jsonl") || !strings.Contains(b, "图片") {
		t.Fatalf("handoff block:\n%s", b)
	}
}

func TestFormatHandoffKeepsNewestWithinBudget(t *testing.T) {
	h := &SessionHandoff{Path: "/p.jsonl", CompactSummary: "压缩摘要"}
	for i := 0; i < 20; i++ {
		h.Tail = append(h.Tail, HistoryEntry{Role: "user", Content: strings.Repeat("前", 2000) + "问" + string(rune('A'+i))})
		h.Tail = append(h.Tail, HistoryEntry{Role: "assistant", Content: "答" + string(rune('A'+i)) + strings.Repeat("后", 2000)})
	}
	b := formatHandoff("old", "用户发了 /new", h)
	if !strings.Contains(b, "压缩摘要") || !strings.Contains(b, "问T") || !strings.Contains(b, "答T") {
		t.Fatalf("newest turn or summary missing")
	}
	if strings.Contains(b, "问A") {
		t.Fatal("oldest turn should be dropped")
	}
	// User turns keep their end (the user's own words), replies their start.
	if strings.Index(b, "问T") < strings.Index(b, "答S") {
		t.Fatal("not in time order")
	}
	if n := len([]rune(b)); n > handoffTailRunes+500 {
		t.Fatalf("handoff too long: %d runes", n)
	}
}

func TestCmdNewHandsOffOldSession(t *testing.T) {
	a := &handoffStubAgent{handoffs: map[string]*SessionHandoff{
		"old": {Path: "/x/old.jsonl", Tail: []HistoryEntry{{Role: "user", Content: "修 gate"}}},
	}}
	p := &stubPlatformEngine{n: "x"}
	e := NewEngine("test", a, []Platform{p}, "", LangEnglish)
	defer e.Stop()
	old := e.sessions.GetOrCreateActive("discord:c1")
	old.SetAgentSessionID("old", "stub")
	old.SetLastSeenMsgID("123")
	e.cmdNew(p, &Message{SessionKey: "discord:c1", ReplyCtx: "ctx"}, nil)
	// /new again before any turn: the pending handoff is not lost.
	e.cmdNew(p, &Message{SessionKey: "discord:c1", ReplyCtx: "ctx"}, nil)
	s := e.sessions.GetOrCreateActive("discord:c1")
	if s == old || s.GetLastSeenMsgID() != "" {
		t.Fatalf("fresh session should backfill its own window, last seen = %q", s.GetLastSeenMsgID())
	}
	// A send that fails puts the handoff back for the next turn.
	if c := e.withTurnBlocks(s, "discord:c1", "x", "你好", true); !strings.Contains(c, "] 修 gate") {
		t.Fatalf("first turn:\n%s", c)
	}
	s.SettleHandoff(false)
	if c := e.withTurnBlocks(s, "discord:c1", "x", "你好", true); !strings.Contains(c, "] 修 gate") || !strings.HasSuffix(c, "你好") {
		t.Fatalf("retried turn:\n%s", c)
	}
	s.SettleHandoff(true)
	s.SettleHandoff(false) // a later failure must not resend it
	if c := e.withTurnBlocks(s, "discord:c1", "x", "你好", true); c != "你好" {
		t.Fatalf("after delivery:\n%s", c)
	}
}

// A timer message (TurnCtx unset) that opens the fresh context still carries
// the handoff, but not the teammate status or wiki pages.
func TestHandoffWithoutTurnCtx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := json.Marshal(map[string]any{"wiki": []map[string]string{{"path": "~/wiki/bots/im-bridge.md", "desc": "cc-connect"}}})
		_, _ = w.Write(b)
	}))
	defer srv.Close()
	a := &handoffStubAgent{handoffs: map[string]*SessionHandoff{
		"old": {Path: "/x/old.jsonl", Tail: []HistoryEntry{{Role: "user", Content: "修 gate"}}},
	}}
	e := NewEngine("test", a, []Platform{&stubPlatformEngine{n: "x"}}, "", LangEnglish)
	defer e.Stop()
	e.SetGate(srv.URL+"/decide?bot=maomao", 1000, "reply")
	dir := t.TempDir()
	e.team = &teamState{dir: dir, name: "橘", inject: true, seen: map[string]*teamSession{}}
	writeTeamFile(t, dir, teamFile{Project: "longniang", Name: "龙娘", Updated: time.Now(), Sessions: []teamEntry{
		{Key: "discord:111", Chat: "general", Task: "修 bug", Busy: true, Since: time.Now()},
	}})

	s := &Session{}
	s.SetHandoffFrom("old", "缓存已过期")
	noBlocks := func(c string) bool {
		return !strings.Contains(c, "im-bridge.md") && !strings.Contains(c, "龙娘")
	}
	c := e.withTurnBlocks(s, "discord:111", "", "定时提醒", false)
	if !strings.Contains(c, "] 修 gate") || !strings.HasSuffix(c, "定时提醒") || !noBlocks(c) {
		t.Fatalf("timer turn:\n%s", c)
	}
	// Send failed: the next turn, timer or not, carries it again.
	s.SettleHandoff(false)
	if c := e.withTurnBlocks(s, "discord:111", "", "定时提醒", false); !strings.Contains(c, "] 修 gate") || !noBlocks(c) {
		t.Fatalf("retried timer turn:\n%s", c)
	}
	s.SettleHandoff(true)
	if c := e.withTurnBlocks(s, "discord:111", "", "定时提醒", false); c != "定时提醒" {
		t.Fatalf("after delivery:\n%s", c)
	}
	// The timer turns marked no wiki page or board as sent.
	if c := e.withTurnBlocks(s, "discord:111", "x", "你好", true); strings.Contains(c, "] 修 gate") || noBlocks(c) {
		t.Fatalf("first turnCtx turn:\n%s", c)
	}
}

func TestHandoffFromSurvivesSave(t *testing.T) {
	path := t.TempDir() + "/s.json"
	sm := NewSessionManager(path)
	sm.GetOrCreateActive("discord:1").SetHandoffFrom("old", "用户发了 /new")
	sm.Save()
	if id, reason := NewSessionManager(path).GetOrCreateActive("discord:1").TakeHandoffFrom(); id != "old" || reason == "" {
		t.Fatalf("handoff after reload = %q %q", id, reason)
	}
}

func TestFormatHandoffDropsStaleBlocks(t *testing.T) {
	h := &SessionHandoff{Tail: []HistoryEntry{{Role: "user", Content: "（队友状态，系统自动附上，不用回应）\n龙娘：忙\n\n（可能相关的 wiki 页，需要时去读）\n- a.md\n\n（频道里你上次看到之后的消息，按时间顺序）\n[10:00] 龙娘: 做完了\n\n你看看"}}}
	b := formatHandoff("old", "x", h)
	if strings.Contains(b, "队友状态") || strings.Contains(b, "a.md") || !strings.Contains(b, "龙娘: 做完了") || !strings.Contains(b, "你看看") {
		t.Fatalf("handoff:\n%s", b)
	}
	// The removed progress summary and an earlier handoff, both multi-paragraph.
	old := "（本频道进展总结已大改，以这一版为准）\n## 正在做\n- 修 gate\n\n## 已完成\n- x\n\n已移除：\n- y\n\n" +
		formatHandoff("older", "x", &SessionHandoff{Tail: []HistoryEntry{{Role: "user", Content: "很旧的话"}}}) + "\n\n真正的问题"
	if got := strings.TrimSpace(stripStaleBlocks(old)); got != "真正的问题" {
		t.Fatalf("stripped = %q", got)
	}
}

// A turn that keeps viewing images is interrupted after the tool result that
// takes the context past the image_fresh limit (maomao fork).
func TestImageBloatMidTurnInterrupts(t *testing.T) {
	run := func(t *testing.T, enabled bool, imageBytes int) (*Engine, *stubPlatformEngine, *Session, *controllableAgentSession) {
		a := &handoffStubAgent{handoffs: map[string]*SessionHandoff{
			"s1": {Path: "/x/s1.jsonl", ImageBytes: imageBytes},
		}}
		p := &stubPlatformEngine{n: "x"}
		e := NewEngine("test", a, []Platform{p}, "", LangEnglish)
		t.Cleanup(func() { _ = e.Stop() })
		e.SetImageFresh(enabled, 2_000_000)
		key := "x:c1"
		session := e.sessions.GetOrCreateActive(key)
		session.SetAgentSessionID("s1", a.Name())
		as := newControllableSession("s1")
		state := &interactiveState{agentSession: as, platform: p, replyCtx: "ctx", agent: a}
		e.interactiveStates[key] = state
		as.events <- Event{Type: EventToolUse, ToolName: "ViewImage", ToolInput: "/tmp/a.png"}
		as.events <- Event{Type: EventToolResult, ToolName: "ViewImage", ToolResult: "/tmp/a.png"}
		as.events <- Event{Type: EventResult, Content: "看完了", Done: true}
		e.processInteractiveEvents(state, session, e.sessions, key, "m1", time.Now(), nil, nil, "ctx")
		return e, p, session, as
	}

	t.Run("over limit", func(t *testing.T) {
		e, p, s, as := run(t, true, 2_900_000)
		select {
		case <-as.closed: // the turn was interrupted: its agent session is closed
		default:
			t.Fatal("agent session not closed")
		}
		if _, ok := e.interactiveStates["x:c1"]; ok {
			t.Fatal("interactive state kept")
		}
		if s.GetAgentSessionID() != "" || s.HandoffFrom != "s1" {
			t.Fatalf("agent id %q handoff %q", s.GetAgentSessionID(), s.HandoffFrom)
		}
		want := "♻️ 这一轮图片累计约 2.9 MB，超过上限，已打断并换新会话（不带图）。接着说就行"
		sent := p.getSent()
		if len(sent) == 0 || sent[len(sent)-1] != want {
			t.Fatalf("sent = %#v", sent)
		}
		// Not re-run: the turn's final reply was never read and nothing new started.
		if strings.Contains(strings.Join(sent, "\n"), "看完了") || len(e.interactiveStates) != 0 {
			t.Fatalf("turn continued or re-ran: sent %#v states %d", sent, len(e.interactiveStates))
		}
		if b := e.handoffBlock(s); !strings.Contains(b, "/x/s1.jsonl") || !strings.Contains(b, "已打断") {
			t.Fatalf("handoff block:\n%s", b)
		}
	})
	for _, c := range []struct {
		name       string
		enabled    bool
		imageBytes int
	}{{"under limit", true, 1_000_000}, {"image_fresh off", false, 9_000_000}} {
		t.Run(c.name, func(t *testing.T) {
			e, p, s, as := run(t, c.enabled, c.imageBytes)
			select {
			case <-as.closed:
				t.Fatal("agent session closed")
			default:
			}
			if s.GetAgentSessionID() != "s1" || s.HandoffFrom != "" {
				t.Fatalf("agent id %q handoff %q", s.GetAgentSessionID(), s.HandoffFrom)
			}
			if _, ok := e.interactiveStates["x:c1"]; !ok {
				t.Fatal("interactive state dropped")
			}
			if got := strings.Join(p.getSent(), "\n"); !strings.Contains(got, "看完了") || strings.Contains(got, "♻️") {
				t.Fatalf("sent = %q", got)
			}
		})
	}
}
