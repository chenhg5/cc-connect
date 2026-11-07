package core

import (
	"strings"
	"testing"
	"time"
)

type quietTestPlatform struct{ *stubPlatformEngine }

func (*quietTestPlatform) FinalReplyOnly() bool { return true }

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
