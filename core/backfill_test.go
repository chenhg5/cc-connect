package core

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type historyPlatform struct {
	stubPlatformEngine
	history []ChannelHistoryMessage
	err     error
}

func (p *historyPlatform) ChannelHistory(_, beforeID string, limit int) ([]ChannelHistoryMessage, error) {
	if p.err != nil {
		return nil, p.err
	}
	var out []ChannelHistoryMessage
	for _, m := range p.history {
		if snowflakeAfter(beforeID, m.ID) {
			out = append(out, m)
		}
	}
	if len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out, nil
}

func TestSnowflakeAfter(t *testing.T) {
	if !snowflakeAfter("1559180740195778622", "1559180470745301053") {
		t.Fatal("later id should be after")
	}
	if snowflakeAfter("999", "1000") {
		t.Fatal("shorter id is older")
	}
}

func TestChannelBackfill_ShowsMissedMessagesInOrder(t *testing.T) {
	now := time.Now()
	p := &historyPlatform{history: []ChannelHistoryMessage{
		{ID: "100", Author: "zuiho", Content: "旧的，看过了", Time: now},
		{ID: "101", Author: "zuiho", Content: "感觉不太行，开多个职业思考一下", Time: now},
		{ID: "102", Author: "橘", IsBot: true, IsSelf: true, FromTurn: true, Content: "我自己的回复", Time: now},
		{ID: "103", Author: "橘", IsBot: true, IsSelf: true, Content: "⏰ ✅ `digest trending`\n\n⭐ GitHub Trending：rsa 工具上榜", Time: now},
		{ID: "104", Author: "龙娘", IsBot: true, Content: "❌ 错误: 启动 Agent 会话失败", Time: now},
		{ID: "105", Author: "zuiho", Content: "", Time: now},
	}}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	defer e.Stop()
	session := e.sessions.GetOrCreateActive("discord:c1")
	session.SetLastSeenMsgID("100")

	msg := &Message{ChannelID: "c1", MessageID: "106", Content: "你看看"}
	got, ok := e.channelBackfill(p, msg, session)
	if !ok {
		t.Fatal("backfill should succeed")
	}
	if strings.Contains(got, "旧的") || strings.Contains(got, "我自己的回复") {
		t.Fatalf("seen and own messages must be skipped:\n%s", got)
	}
	if !strings.Contains(got, "橘（你这个账号") || !strings.Contains(got, "rsa 工具上榜") {
		t.Fatalf("this bot's cron output never reached the agent and must be shown:\n%s", got)
	}
	i, j := strings.Index(got, "感觉不太行"), strings.Index(got, "龙娘（bot）: ❌")
	if i < 0 || j < 0 || i > j {
		t.Fatalf("missed messages missing or out of order:\n%s", got)
	}
	if !strings.Contains(got, "（空消息或只有附件）") {
		t.Fatalf("empty message should be shown:\n%s", got)
	}

	markSeen(session, msg)
	if session.GetLastSeenMsgID() != "106" {
		t.Fatalf("last seen = %q, want 106", session.GetLastSeenMsgID())
	}
	msg.SeenUpToID = "107"
	markSeen(session, msg)
	if session.GetLastSeenMsgID() != "107" {
		t.Fatalf("folded burst should advance last seen, got %q", session.GetLastSeenMsgID())
	}
}

func TestChannelBackfill_FirstTurnUsesRecentWindow(t *testing.T) {
	p := &historyPlatform{history: []ChannelHistoryMessage{
		{ID: "100", Author: "zuiho", Content: "昨天的", Time: time.Now().Add(-24 * time.Hour)},
		{ID: "101", Author: "zuiho", Content: "刚才的", Time: time.Now().Add(-time.Minute)},
	}}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	defer e.Stop()
	session := e.sessions.GetOrCreateActive("discord:c1")
	got, ok := e.channelBackfill(p, &Message{ChannelID: "c1", MessageID: "102"}, session)
	if !ok || strings.Contains(got, "昨天的") || !strings.Contains(got, "刚才的") {
		t.Fatalf("ok=%v got:\n%s", ok, got)
	}
}

func TestChannelBackfill_UnsupportedPlatformFallsBack(t *testing.T) {
	p := &stubPlatformEngine{n: "x"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	defer e.Stop()
	session := e.sessions.GetOrCreateActive("x:c1")
	if _, ok := e.channelBackfill(p, &Message{ChannelID: "c1", MessageID: "1"}, session); ok {
		t.Fatal("platform without history must report ok=false")
	}
}

func TestIsBotErrorNotice(t *testing.T) {
	for _, s := range []string{"❌ 错误: 启动 Agent 会话失败", " ⚠️ 这一轮卡住"} {
		if !isBotErrorNotice(s) {
			t.Errorf("%q should be an error notice", s)
		}
	}
	if isBotErrorNotice("本龙在呢") {
		t.Error("chat is not an error notice")
	}
}

func TestTurnIdleFor(t *testing.T) {
	e := NewEngine("test", &stubAgent{}, []Platform{&stubPlatformEngine{n: "x"}}, "", LangEnglish)
	defer e.Stop()
	st := &interactiveState{}
	st.lastEventAt.Store(time.Now().Add(-11 * time.Minute).UnixNano())
	e.interactiveMu.Lock()
	e.interactiveStates["k"] = st
	e.interactiveMu.Unlock()
	if got := e.turnIdleFor("k"); got < deadTurnAfter {
		t.Fatalf("idle = %v, want >= %v", got, deadTurnAfter)
	}
	if got := e.turnIdleFor("missing"); got != 0 {
		t.Fatalf("missing state idle = %v", got)
	}
}

func TestGateContextWiki(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/context" {
			http.NotFound(w, r)
			return
		}
		b, _ := json.Marshal(map[string]any{"wiki": []map[string]string{{"path": "~/wiki/bots/im-bridge.md", "desc": "cc-connect"}}})
		_, _ = w.Write(b)
	}))
	defer srv.Close()

	e := NewEngine("test", &stubAgent{}, []Platform{&stubPlatformEngine{n: "x"}}, "", LangEnglish)
	defer e.Stop()
	e.SetGate(srv.URL+"/decide?bot=longniang", 1000, "reply")
	if ep := e.gate.endpoint("/context"); ep != srv.URL+"/context" {
		t.Fatalf("endpoint = %q", ep)
	}
	sess := &Session{AgentSessionID: "a1"}
	if c := e.turnContext(sess, "discord:c1", "龙娘怎么了"); !strings.Contains(c, "~/wiki/bots/im-bridge.md") || strings.Contains(c, "进展总结") {
		t.Fatalf("context block:\n%s", c)
	}
	// Same agent context: nothing is sent twice.
	if c := e.turnContext(sess, "discord:c1", "龙娘怎么了"); c != "" {
		t.Fatalf("second turn resent:\n%s", c)
	}
	// --resume forked a new id: same conversation, nothing resent.
	sess.SetAgentSessionID("a2", "claudecode")
	if c := e.turnContext(sess, "discord:c1", "x"); c != "" {
		t.Fatalf("resume fork resent:\n%s", c)
	}
	// Compaction drops it from the agent's context: send again.
	sess.ResetTurnCtx()
	if c := e.turnContext(sess, "discord:c1", "x"); !strings.Contains(c, "im-bridge.md") {
		t.Fatalf("after compaction:\n%s", c)
	}
	// Id cleared (failed resume, /reasoning, /provider…): the next process is empty.
	sess.SetAgentSessionID("", "claudecode")
	if c := e.turnContext(sess, "discord:c1", "x"); !strings.Contains(c, "im-bridge.md") {
		t.Fatalf("after id cleared:\n%s", c)
	}
	// /switch to another conversation.
	sess.SetAgentInfo("other", "claudecode", "n")
	if c := e.turnContext(sess, "discord:c1", "x"); !strings.Contains(c, "im-bridge.md") {
		t.Fatalf("after switch:\n%s", c)
	}
}
