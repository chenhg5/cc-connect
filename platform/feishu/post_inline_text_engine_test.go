package feishu

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

// External agent execution and message delivery are replaced with local stubs.
type postInlineTextAgent struct {
	starts atomic.Int32
}

func (a *postInlineTextAgent) Name() string { return "post-mention-review" }
func (a *postInlineTextAgent) StartSession(context.Context, string) (core.AgentSession, error) {
	a.starts.Add(1)
	return nil, errors.New("unexpected agent startup in local-command regression")
}
func (a *postInlineTextAgent) ListSessions(context.Context) ([]core.AgentSessionInfo, error) {
	return nil, nil
}
func (a *postInlineTextAgent) Stop() error { return nil }

type postInlineTextSender struct {
	mu   sync.Mutex
	sent []string
}

func (p *postInlineTextSender) Name() string                    { return "feishu" }
func (p *postInlineTextSender) Start(core.MessageHandler) error { return nil }
func (p *postInlineTextSender) Stop() error                     { return nil }
func (p *postInlineTextSender) Reply(_ context.Context, _ any, content string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sent = append(p.sent, content)
	return nil
}
func (p *postInlineTextSender) Send(ctx context.Context, rc any, content string) error {
	return p.Reply(ctx, rc, content)
}
func (p *postInlineTextSender) replies() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.sent...)
}

// Splitting a command across adjacent text nodes must not insert newlines.
// The direct bot OpenID avoids depending on the separate placeholder fix.
func TestDispatchPostInlineText_CreatesNamedSessionWithoutAgent(t *testing.T) {
	for _, tc := range []struct {
		name     string
		elements string
		wantName string
	}{
		{"single text element", `{"tag":"text","text":" /new incident-triage"}`, "incident-triage"},
		{"command and name split", `{"tag":"text","text":" /new"},{"tag":"text","text":" incident-triage"}`, "incident-triage"},
		{"title split across elements", `{"tag":"text","text":" /new incident"},{"tag":"text","text":" triage"}`, "incident triage"},
	} {
		for _, wrapped := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/localeWrapped=%v", tc.name, wrapped), func(t *testing.T) {
				agent := &postInlineTextAgent{}
				sender := &postInlineTextSender{}
				engine := core.NewEngine("post-mention-review", agent, []core.Platform{sender}, "", core.LangEnglish)
				t.Cleanup(func() {
					if err := engine.Stop(); err != nil {
						t.Errorf("engine.Stop(): %v", err)
					}
				})
				const sessionKey = "feishu:oc_chat:ou_user"
				previous := engine.GetSessions().GetOrCreateActive(sessionKey)
				previous.SetName("existing-session")
				var admitted atomic.Int32
				var received string
				p := &Platform{platformName: "feishu", botOpenID: "ou_bot"}
				p.handler = func(_ core.Platform, msg *core.Message) {
					// Admission happens synchronously before an agent goroutine is
					// spawned. This assertion needs no sleep to detect fallthrough.
					received = msg.Content
					msg.OnAccepted = func() { admitted.Add(1) }
					engine.ReceiveMessage(sender, msg)
				}
				p.userNameCache.Store("ou_user", "Tester")
				p.chatNameCache.Store("oc_chat", "Group")
				content := `{"content":[[{"tag":"at","user_id":"ou_bot","user_name":"ExampleBot"},` + tc.elements + `]]}`
				if wrapped {
					content = `{"zh_cn":` + content + `}`
				}
				p.dispatchMessage(context.Background(), "post", content,
					nil,
					"om_post", sessionKey, "ou_user", "oc_chat", replyContext{}, "", 0)
				if received != " /new "+tc.wantName {
					t.Errorf("dispatch content = %q, want %q", received, " /new "+tc.wantName)
				}
				if got := admitted.Load(); got != 0 {
					t.Fatalf("command admitted to the agent path %d times", got)
				}
				active := engine.GetSessions().GetOrCreateActive(sessionKey)
				if active.ID == previous.ID || active.GetName() != tc.wantName {
					t.Errorf("active session = %s / %q, want a new session named %q", active.ID, active.GetName(), tc.wantName)
				}
				wantReply := fmt.Sprintf(core.NewI18n(core.LangEnglish).T(core.MsgNewSessionCreatedName), tc.wantName)
				if replies := sender.replies(); len(replies) != 1 || strings.Join(replies, "\n") != wantReply {
					t.Errorf("replies = %q, want exactly %q", replies, wantReply)
				}
			})
		}
	}
}
