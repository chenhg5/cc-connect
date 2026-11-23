package feishu

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// Exercise the real Feishu receive path and Engine/SessionManager together.
// Only the external message sender and agent process are replaced. This CUJ
// lives beside the adapter because core must not import platform packages.
func TestCUJ_A_FeishuGroupReplyAllChats(t *testing.T) {
	p := newGroupReplyAllTestPlatform(t, "feishu", map[string]any{
		"group_reply_all": true, "group_reply_all_chats": "oc_allowed",
		"group_chat_history_share": true, "allow_from": "ou_alice",
	}, nil)
	sender := &groupReplyAllCUJSender{base: p, replies: make(chan string, 16)}
	p.self = sender
	engine := core.NewEngine("reply-all-cuj", &groupReplyAllCUJAgent{}, []core.Platform{sender}, filepath.Join(t.TempDir(), "sessions.json"), core.LangEnglish)
	engine.SetSkipGit(true)
	engine.SetDisplayConfig(core.DisplayCfg{Mode: "quiet"})
	engine.SetReplyFooterEnabled(false)
	if err := sender.Start(engine.ReceiveMessage); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := engine.Stop(); err != nil {
			t.Errorf("engine.Stop: %v", err)
		}
	})

	awaitReply := func(want string) string {
		t.Helper()
		timer := time.NewTimer(3 * time.Second)
		defer timer.Stop()
		for {
			select {
			case reply := <-sender.replies:
				if strings.Contains(reply, want) {
					return reply
				}
			case <-timer.C:
				t.Fatalf("no visible reply containing %q", want)
				return ""
			}
		}
	}
	assertSilent := func() {
		t.Helper()
		select {
		case reply := <-sender.replies:
			t.Fatalf("unexpected visible reply: %q", reply)
		case <-time.After(100 * time.Millisecond):
		}
	}

	// Action 1: a selected group gets an answer without @mention.
	sendGroupHistoryText(t, p, "m1", "ou_alice", "selected group question", "oc_allowed", "", false)
	awaitReply("selected group question")
	// Action 2: ordinary chatter in a different group stays silent.
	sendGroupHistoryText(t, p, "m2", "ou_alice", "other group background", "oc_main", "", false)
	assertSilent()
	// Action 3: explicitly mention the bot there; the answer includes that
	// group's pending context, with nothing leaked from the selected group.
	sendGroupHistoryText(t, p, "m3", "ou_alice", "summarize this group", "oc_main", "", true)
	reply := awaitReply("summarize this group")
	if !strings.Contains(reply, "other group background") || strings.Contains(reply, "selected group question") {
		t.Fatalf("group context was lost or crossed chat boundaries: %q", reply)
	}
	// Action 4: an unauthorized user cannot trigger the selected group.
	sendGroupHistoryText(t, p, "m4", "ou_other", "unauthorized question", "oc_allowed", "", false)
	assertSilent()
	// Action 5: the authorized user can continue without re-mentioning.
	sendGroupHistoryText(t, p, "m5", "ou_alice", "selected group follow-up", "oc_allowed", "", false)
	awaitReply("selected group follow-up")

	// Let turn teardown finish before closing the fake agent's event channel.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		busy := false
		for _, session := range engine.GetSessions().AllSessions() {
			busy = busy || session.Busy()
		}
		if !busy {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("agent turns did not finish")
}

type groupReplyAllCUJSender struct {
	base    *Platform
	replies chan string
}

func (p *groupReplyAllCUJSender) Name() string { return p.base.Name() }
func (p *groupReplyAllCUJSender) Start(handler core.MessageHandler) error {
	p.base.mu.Lock()
	p.base.handler = handler
	p.base.mu.Unlock()
	return nil
}
func (p *groupReplyAllCUJSender) Reply(ctx context.Context, _ any, content string) error {
	select {
	case p.replies <- content:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (p *groupReplyAllCUJSender) Send(ctx context.Context, replyCtx any, content string) error {
	return p.Reply(ctx, replyCtx, content)
}
func (p *groupReplyAllCUJSender) Stop() error { return nil }

type groupReplyAllCUJAgent struct{ nextID atomic.Uint64 }

func (a *groupReplyAllCUJAgent) Name() string { return "reply-all-cuj" }
func (a *groupReplyAllCUJAgent) StartSession(context.Context, string) (core.AgentSession, error) {
	return &groupReplyAllCUJSession{
		id: fmt.Sprintf("cuj-%d", a.nextID.Add(1)), events: make(chan core.Event, 16),
	}, nil
}
func (a *groupReplyAllCUJAgent) ListSessions(context.Context) ([]core.AgentSessionInfo, error) {
	return nil, nil
}
func (a *groupReplyAllCUJAgent) Stop() error { return nil }

type groupReplyAllCUJSession struct {
	id     string
	events chan core.Event
	mu     sync.Mutex
	closed bool
}

func (s *groupReplyAllCUJSession) Send(prompt, _ string, _ []core.ImageAttachment, _ []core.FileAttachment) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return fmt.Errorf("test agent session is closed")
	}
	s.events <- core.Event{Type: core.EventResult, Content: "Answer: " + prompt, SessionID: s.id, Done: true}
	return nil
}
func (s *groupReplyAllCUJSession) RespondPermission(string, core.PermissionResult) error { return nil }
func (s *groupReplyAllCUJSession) Events() <-chan core.Event                             { return s.events }
func (s *groupReplyAllCUJSession) CurrentSessionID() string                              { return s.id }
func (s *groupReplyAllCUJSession) Alive() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.closed
}
func (s *groupReplyAllCUJSession) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		s.closed = true
		close(s.events)
	}
	return nil
}
