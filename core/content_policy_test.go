package core

import (
	"strings"
	"testing"
	"time"
)

func TestHandleMessage_BannedWordsOnlyChecksAuthorText(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content string
		extra   string
		blocked bool
	}{
		{"author", "explain forbidden-keyword", "reference material", true},
		{"reference", "explain this passage", "quoted forbidden-keyword from another author", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newCUJEnv(t)
			defer func() { _ = env.engine.Stop() }()
			env.engine.SetBannedWords([]string{"forbidden-keyword"})
			env.engine.ReceiveMessage(env.plat, &Message{
				SessionKey: "test:author", Platform: "test", UserID: "author",
				MessageID: "policy", ReplyCtx: "ctx", Content: tc.content, ExtraContent: tc.extra,
			})
			want := "ok"
			if tc.blocked {
				want = env.engine.i18n.T(MsgBannedWordBlocked)
			}
			env.waitFor("content-policy response", 3*time.Second, func() bool { return env.sentContains(want) })
			env.agent.mu.Lock()
			sessions := append([]*cujAgentSession(nil), env.agent.sessions...)
			env.agent.mu.Unlock()
			if tc.blocked {
				if len(sessions) != 0 {
					t.Fatal("banned author text reached the agent")
				}
				return
			}
			if len(sessions) != 1 {
				t.Fatalf("reference context should reach one agent session; got %d", len(sessions))
			}
			prompts := sessions[0].getSentPrompts()
			if len(prompts) != 1 || !strings.Contains(prompts[0], tc.extra) || !strings.Contains(prompts[0], tc.content) {
				t.Fatalf("reference context or author text was lost: %v", prompts)
			}
		})
	}
}
