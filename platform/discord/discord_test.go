package discord

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/chenhg5/cc-connect/core"
)

// ── Thread tests (upstream) ──────────────────────────────────

type fakeThreadOps struct {
	resolveChannel func(channelID string) (*discordgo.Channel, error)
	startThread    func(channelID, messageID, name string, archiveDuration int) (*discordgo.Channel, error)
	joinThread     func(threadID string) error
}

func (f fakeThreadOps) ResolveChannel(channelID string) (*discordgo.Channel, error) {
	if f.resolveChannel == nil {
		return nil, nil
	}
	return f.resolveChannel(channelID)
}

func (f fakeThreadOps) StartThread(channelID, messageID, name string, archiveDuration int) (*discordgo.Channel, error) {
	if f.startThread == nil {
		return nil, nil
	}
	return f.startThread(channelID, messageID, name, archiveDuration)
}

func (f fakeThreadOps) JoinThread(threadID string) error {
	if f.joinThread == nil {
		return nil
	}
	return f.joinThread(threadID)
}

func TestResolveThreadReplyContext_UsesExistingThreadChannel(t *testing.T) {
	ops := fakeThreadOps{
		resolveChannel: func(channelID string) (*discordgo.Channel, error) {
			return &discordgo.Channel{ID: channelID, Type: discordgo.ChannelTypeGuildPublicThread}, nil
		},
	}

	joinedThread := ""
	ops.joinThread = func(threadID string) error {
		joinedThread = threadID
		return nil
	}

	msg := &discordgo.MessageCreate{
		Message: &discordgo.Message{
			ID:        "m1",
			ChannelID: "thread-1",
			GuildID:   "guild-1",
			Author:    &discordgo.User{ID: "u1", Username: "jun"},
		},
	}

	sessionKey, rc, err := resolveThreadReplyContext(msg, "bot-1", ops)
	if err != nil {
		t.Fatalf("resolveThreadReplyContext() error = %v", err)
	}
	if sessionKey != "discord:thread-1" {
		t.Fatalf("sessionKey = %q, want discord:thread-1", sessionKey)
	}
	if rc.channelID != "thread-1" || rc.threadID != "thread-1" {
		t.Fatalf("replyContext = %#v, want thread channel routing", rc)
	}
	if joinedThread != "thread-1" {
		t.Fatalf("joinedThread = %q, want thread-1", joinedThread)
	}
}

func TestResolveThreadReplyContext_CreatesThreadForGuildMessage(t *testing.T) {
	ops := fakeThreadOps{
		resolveChannel: func(channelID string) (*discordgo.Channel, error) {
			return &discordgo.Channel{ID: channelID, Type: discordgo.ChannelTypeGuildText}, nil
		},
	}

	var (
		startChannelID string
		startMessageID string
		startName      string
		joinedThread   string
	)
	ops.startThread = func(channelID, messageID, name string, archiveDuration int) (*discordgo.Channel, error) {
		startChannelID = channelID
		startMessageID = messageID
		startName = name
		if archiveDuration != 1440 {
			t.Fatalf("archiveDuration = %d, want 1440", archiveDuration)
		}
		return &discordgo.Channel{ID: "thread-99", Type: discordgo.ChannelTypeGuildPublicThread}, nil
	}
	ops.joinThread = func(threadID string) error {
		joinedThread = threadID
		return nil
	}

	msg := &discordgo.MessageCreate{
		Message: &discordgo.Message{
			ID:        "msg-42",
			ChannelID: "channel-1",
			GuildID:   "guild-1",
			Content:   "<@bot-1> investigate build failure",
			Author:    &discordgo.User{ID: "u1", Username: "jun"},
		},
	}

	sessionKey, rc, err := resolveThreadReplyContext(msg, "bot-1", ops)
	if err != nil {
		t.Fatalf("resolveThreadReplyContext() error = %v", err)
	}
	if sessionKey != "discord:thread-99" {
		t.Fatalf("sessionKey = %q, want discord:thread-99", sessionKey)
	}
	if rc.channelID != "thread-99" || rc.threadID != "thread-99" {
		t.Fatalf("replyContext = %#v, want thread channel routing", rc)
	}
	if startChannelID != "channel-1" || startMessageID != "msg-42" {
		t.Fatalf("thread start args = (%q, %q), want (channel-1, msg-42)", startChannelID, startMessageID)
	}
	if startName != "investigate build failure" {
		t.Fatalf("thread name = %q, want sanitized content", startName)
	}
	if joinedThread != "thread-99" {
		t.Fatalf("joinedThread = %q, want thread-99", joinedThread)
	}
}

func TestSessionKeyForChannel_UsesThreadKeyWhenChannelIsThread(t *testing.T) {
	ops := fakeThreadOps{
		resolveChannel: func(channelID string) (*discordgo.Channel, error) {
			return &discordgo.Channel{ID: channelID, Type: discordgo.ChannelTypeGuildPrivateThread}, nil
		},
	}

	if got := resolveSessionKeyForChannel("thread-7", "user-1", false, true, ops); got != "discord:thread-7" {
		t.Fatalf("resolveSessionKeyForChannel() = %q, want discord:thread-7", got)
	}
}

func TestReconstructReplyCtx_ThreadSessionKey(t *testing.T) {
	p := &Platform{}

	rctx, err := p.ReconstructReplyCtx("discord:thread-7")
	if err != nil {
		t.Fatalf("ReconstructReplyCtx() error = %v", err)
	}
	rc := rctx.(replyContext)
	if rc.channelID != "thread-7" || rc.threadID != "thread-7" {
		t.Fatalf("replyContext = %#v, want thread reply context", rc)
	}
}

// ── Dedup tests ──────────────────────────────────────────────

// simulateHandlerCall mimics the dedup + dispatch logic in the MessageCreate
// handler registered by Platform.Start.  It returns true when the message
// was dispatched (not a duplicate).
func (p *Platform) simulateHandlerCall(msgID, userID, userName, channelID, content string) bool {
	// --- dedup (same logic as Start handler) ---
	if _, loaded := p.seenMsgs.LoadOrStore(msgID, struct{}{}); loaded {
		return false
	}
	time.AfterFunc(2*time.Minute, func() { p.seenMsgs.Delete(msgID) })

	msg := &core.Message{
		SessionKey: p.makeSessionKey(channelID, userID),
		Platform:   "discord",
		MessageID:  msgID,
		UserID:     userID,
		UserName:   userName,
		Content:    content,
	}
	p.handler(p, msg)
	return true
}

// newTestPlatform creates a Platform suitable for unit tests (no real Discord
// connection).  The provided handler records every dispatched message.
func newTestPlatform(handler core.MessageHandler) *Platform {
	return &Platform{
		token:     "test-token",
		allowFrom: "*",
		handler:   handler,
		botID:     "BOT_ID",
		readyCh:   make(chan struct{}),
	}
}

// TestDuplicateMessage_SameIDDeduped reproduces GitHub issue #122:
// Discord gateway delivers the same MessageCreate event twice within ~1 ms.
// The second delivery must be silently dropped.
func TestDuplicateMessage_SameIDDeduped(t *testing.T) {
	var calls int32
	p := newTestPlatform(func(_ core.Platform, _ *core.Message) {
		atomic.AddInt32(&calls, 1)
	})

	const msgID = "1482313396505411717"

	// First delivery — must be processed.
	if !p.simulateHandlerCall(msgID, "user1", "quabug", "ch1", "hello") {
		t.Fatal("first delivery was incorrectly treated as duplicate")
	}

	// Second delivery (same msg_id, ~1 ms later) — must be dropped.
	if p.simulateHandlerCall(msgID, "user1", "quabug", "ch1", "hello") {
		t.Fatal("second delivery was not caught as duplicate")
	}

	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Fatalf("handler called %d times, want 1", n)
	}
}

// TestDuplicateMessage_DifferentIDsProcessed ensures distinct messages are
// not incorrectly suppressed by dedup.
func TestDuplicateMessage_DifferentIDsProcessed(t *testing.T) {
	var calls int32
	p := newTestPlatform(func(_ core.Platform, _ *core.Message) {
		atomic.AddInt32(&calls, 1)
	})

	if !p.simulateHandlerCall("msg-1", "user1", "quabug", "ch1", "first") {
		t.Fatal("msg-1 should be processed")
	}
	if !p.simulateHandlerCall("msg-2", "user1", "quabug", "ch1", "second") {
		t.Fatal("msg-2 should be processed")
	}
	if !p.simulateHandlerCall("msg-3", "user1", "quabug", "ch1", "third") {
		t.Fatal("msg-3 should be processed")
	}

	if n := atomic.LoadInt32(&calls); n != 3 {
		t.Fatalf("handler called %d times, want 3", n)
	}
}

// TestDuplicateMessage_ConcurrentRace fires N goroutines that all try to
// deliver the same message simultaneously — exactly one must win.
func TestDuplicateMessage_ConcurrentRace(t *testing.T) {
	var calls int32
	p := newTestPlatform(func(_ core.Platform, _ *core.Message) {
		atomic.AddInt32(&calls, 1)
	})

	const (
		msgID      = "race-msg-1"
		goroutines = 50
	)

	var wg sync.WaitGroup
	wg.Add(goroutines)
	start := make(chan struct{}) // barrier so all goroutines race together

	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			<-start
			p.simulateHandlerCall(msgID, "user1", "quabug", "ch1", "race")
		}()
	}

	close(start) // release all goroutines at once
	wg.Wait()

	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Fatalf("handler called %d times under race, want exactly 1", n)
	}
}

// TestDuplicateMessage_MultipleDuplicateBursts sends multiple distinct
// messages, each duplicated, and verifies that each unique message is
// processed exactly once.
func TestDuplicateMessage_MultipleDuplicateBursts(t *testing.T) {
	received := make(map[string]int)
	var mu sync.Mutex
	p := newTestPlatform(func(_ core.Platform, msg *core.Message) {
		mu.Lock()
		received[msg.MessageID]++
		mu.Unlock()
	})

	// Simulate 10 messages, each delivered twice (as observed in logs).
	for i := 0; i < 10; i++ {
		id := "burst-" + string(rune('A'+i))
		p.simulateHandlerCall(id, "user1", "quabug", "ch1", "msg")
		p.simulateHandlerCall(id, "user1", "quabug", "ch1", "msg") // duplicate
	}

	for id, count := range received {
		if count != 1 {
			t.Errorf("message %q processed %d times, want 1", id, count)
		}
	}
	if len(received) != 10 {
		t.Errorf("got %d unique messages, want 10", len(received))
	}
}

// ── Mention tests ────────────────────────────────────────────

// TestStripDiscordMention verifies mention stripping helper.
func TestStripDiscordMention(t *testing.T) {
	tests := []struct {
		name    string
		content string
		botID   string
		want    string
	}{
		{"strips bot mention at start", "<@123456> hello", "123456", "hello"},
		{"strips bot mention with ! prefix", "<@!123456> hello", "123456", "hello"},
		{"strips bot mention in middle", "hey <@123456> do this", "123456", "hey  do this"},
		{"no mention", "hello world", "123456", "hello world"},
		{"only mention", "<@123456>", "123456", ""},
		{"different bot ID unchanged", "<@999999> hello", "123456", "<@999999> hello"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stripDiscordMention(tt.content, tt.botID)
			if got != tt.want {
				t.Errorf("stripDiscordMention(%q, %q) = %q, want %q",
					tt.content, tt.botID, got, tt.want)
			}
		})
	}
}
