package claudecode

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

type sdkReplySession struct {
	parser *claudeSession
	raw    string
	closed sync.Once
}

func (s *sdkReplySession) Send(string, string, []core.ImageAttachment, []core.FileAttachment) error {
	s.parser.handleReadLoopLine(s.raw)
	return nil
}
func (*sdkReplySession) RespondPermission(string, core.PermissionResult) error { return nil }
func (s *sdkReplySession) Events() <-chan core.Event                           { return s.parser.events }
func (s *sdkReplySession) CurrentSessionID() string                            { return s.parser.CurrentSessionID() }
func (s *sdkReplySession) Alive() bool                                         { return s.parser.Alive() }
func (s *sdkReplySession) Close() error {
	s.closed.Do(func() { s.parser.alive.Store(false); s.parser.cancel(); close(s.parser.events) })
	return nil
}

type sdkReplyAgent struct{ session *sdkReplySession }

func (*sdkReplyAgent) Name() string { return "sdk-fixture" }
func (a *sdkReplyAgent) StartSession(context.Context, string) (core.AgentSession, error) {
	return a.session, nil
}
func (*sdkReplyAgent) ListSessions(context.Context) ([]core.AgentSessionInfo, error) { return nil, nil }
func (*sdkReplyAgent) Stop() error                                                   { return nil }

type sdkReplyPlatform struct{ sent chan string }

func (*sdkReplyPlatform) Name() string                    { return "fixture" }
func (*sdkReplyPlatform) Start(core.MessageHandler) error { return nil }
func (p *sdkReplyPlatform) Reply(_ context.Context, _ any, text string) error {
	p.sent <- text
	return nil
}
func (p *sdkReplyPlatform) Send(_ context.Context, _ any, text string) error {
	p.sent <- text
	return nil
}
func (*sdkReplyPlatform) Stop() error { return nil }

func TestSDKFailureReasonReachesUserWithoutProviderPayload(t *testing.T) {
	for _, tc := range []struct{ name, result, want string }{
		{"prompt_too_long", "Prompt is too long", "Prompt is too long"},
		{"credit_balance", "Credit balance is too low", "Credit balance is too low"},
		{"api_status", "API Error: 400 (private provider body)", "HTTP 400"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const secret = "synthetic-credential-must-not-be-delivered"
			data, err := json.Marshal(map[string]any{"type": "result", "subtype": "success", "is_error": true, "session_id": "sdk-result-fixture", "result": tc.result + " access_token=" + secret + " " + strings.Repeat("private_payload ", 1000)})
			if err != nil {
				t.Fatal(err)
			}
			s := &sdkReplySession{parser: newSDKResultSession(t), raw: string(data)}
			p := &sdkReplyPlatform{sent: make(chan string, 16)}
			e := core.NewEngine("sdk-reply", &sdkReplyAgent{session: s}, []core.Platform{p}, filepath.Join(t.TempDir(), "sessions.json"), core.LangEnglish)
			t.Cleanup(func() { _ = e.Stop() })
			if err := e.Start(); err != nil {
				t.Fatal(err)
			}
			e.ReceiveMessage(p, &core.Message{Platform: p.Name(), SessionKey: "fixture:chat:root:message", MessageID: "message", UserID: "fixture", Content: "local diagnostic check", ReplyCtx: "fixture"})
			deadline := time.After(5 * time.Second)
			for {
				select {
				case text := <-p.sent:
					if !strings.Contains(text, tc.want) {
						continue
					}
					if strings.Contains(text, secret) || strings.Contains(text, "private_payload") || len(text) > 512 {
						t.Fatal("SDK failure reply included provider payload or exceeded its bound")
					}
					return
				case <-deadline:
					t.Fatalf("Engine did not deliver actionable SDK reason %q", tc.want)
				}
			}
		})
	}
}
