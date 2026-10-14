package core

import (
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
	if c := e.withTurnBlocks(s, "discord:c1", "x", "你好"); !strings.Contains(c, "] 修 gate") {
		t.Fatalf("first turn:\n%s", c)
	}
	s.SettleHandoff(false)
	if c := e.withTurnBlocks(s, "discord:c1", "x", "你好"); !strings.Contains(c, "] 修 gate") || !strings.HasSuffix(c, "你好") {
		t.Fatalf("retried turn:\n%s", c)
	}
	s.SettleHandoff(true)
	s.SettleHandoff(false) // a later failure must not resend it
	if c := e.withTurnBlocks(s, "discord:c1", "x", "你好"); c != "你好" {
		t.Fatalf("after delivery:\n%s", c)
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
