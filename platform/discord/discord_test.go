package discord

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/bwmarrin/discordgo"
	"github.com/chenhg5/cc-connect/core"
)

// ── Thread tests (upstream) ──────────────────────────────────

type fakeThreadOps struct {
	resolveChannel        func(channelID string) (*discordgo.Channel, error)
	startThread           func(channelID, messageID, name string, archiveDuration int) (*discordgo.Channel, error)
	startStandaloneThread func(channelID, name string, typ discordgo.ChannelType, archiveDuration int) (*discordgo.Channel, error)
	joinThread            func(threadID string) error
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

func (f fakeThreadOps) StartStandaloneThread(channelID, name string, typ discordgo.ChannelType, archiveDuration int) (*discordgo.Channel, error) {
	if f.startStandaloneThread == nil {
		return nil, nil
	}
	return f.startStandaloneThread(channelID, name, typ, archiveDuration)
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

func TestResolveCronReplyTarget_CreatesStandaloneThread(t *testing.T) {
	ops := fakeThreadOps{
		resolveChannel: func(channelID string) (*discordgo.Channel, error) {
			return &discordgo.Channel{ID: channelID, Type: discordgo.ChannelTypeGuildText}, nil
		},
	}

	var (
		startChannelID string
		startName      string
		startType      discordgo.ChannelType
		joinedThread   string
	)
	ops.startStandaloneThread = func(channelID, name string, typ discordgo.ChannelType, archiveDuration int) (*discordgo.Channel, error) {
		startChannelID = channelID
		startName = name
		startType = typ
		if archiveDuration != 1440 {
			t.Fatalf("archiveDuration = %d, want 1440", archiveDuration)
		}
		return &discordgo.Channel{ID: "thread-fresh", Type: discordgo.ChannelTypeGuildPublicThread}, nil
	}
	ops.joinThread = func(threadID string) error {
		joinedThread = threadID
		return nil
	}

	sessionKey, rc, err := resolveCronReplyTarget("discord:channel-1:user-1", "Daily sync", ops)
	if err != nil {
		t.Fatalf("resolveCronReplyTarget() error = %v", err)
	}
	if sessionKey != "discord:thread-fresh" {
		t.Fatalf("sessionKey = %q, want discord:thread-fresh", sessionKey)
	}
	if rc.channelID != "thread-fresh" || rc.threadID != "thread-fresh" {
		t.Fatalf("replyContext = %#v, want fresh thread routing", rc)
	}
	if startChannelID != "channel-1" {
		t.Fatalf("startChannelID = %q, want channel-1", startChannelID)
	}
	if startName != "Daily sync" {
		t.Fatalf("thread name = %q, want Daily sync", startName)
	}
	if startType != discordgo.ChannelTypeGuildPublicThread {
		t.Fatalf("thread type = %v, want public thread", startType)
	}
	if joinedThread != "thread-fresh" {
		t.Fatalf("joinedThread = %q, want thread-fresh", joinedThread)
	}
}

func TestResolveCronReplyTarget_ReusesExistingThreadKey(t *testing.T) {
	ops := fakeThreadOps{
		resolveChannel: func(channelID string) (*discordgo.Channel, error) {
			switch channelID {
			case "thread-1":
				return &discordgo.Channel{ID: "thread-1", Type: discordgo.ChannelTypeGuildPublicThread, ParentID: "channel-1"}, nil
			case "channel-1":
				return &discordgo.Channel{ID: "channel-1", Type: discordgo.ChannelTypeGuildText}, nil
			default:
				t.Fatalf("unexpected channel lookup %q", channelID)
				return nil, nil
			}
		},
	}

	startChannelID := ""
	ops.startStandaloneThread = func(channelID, name string, typ discordgo.ChannelType, archiveDuration int) (*discordgo.Channel, error) {
		startChannelID = channelID
		return &discordgo.Channel{ID: "thread-fresh-2", Type: discordgo.ChannelTypeGuildPublicThread}, nil
	}

	sessionKey, rc, err := resolveCronReplyTarget("discord:thread-1", "cron", ops)
	if err != nil {
		t.Fatalf("resolveCronReplyTarget() error = %v", err)
	}
	if sessionKey != "discord:thread-fresh-2" {
		t.Fatalf("sessionKey = %q, want discord:thread-fresh-2", sessionKey)
	}
	if rc.threadID != "thread-fresh-2" {
		t.Fatalf("replyContext = %#v, want thread-fresh-2", rc)
	}
	if startChannelID != "channel-1" {
		t.Fatalf("startChannelID = %q, want channel-1", startChannelID)
	}
}

func TestSendWithButtons_UsesFollowupComponents(t *testing.T) {
	requests := make([]string, 0, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.URL.Path)
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		switch {
		case strings.Contains(r.URL.Path, "/messages/@original"):
			if payload["content"] != "choose mode" {
				t.Fatalf("original content = %#v, want choose mode", payload["content"])
			}
		case strings.Contains(r.URL.Path, "/webhooks/app-1/token-1"):
			if payload["content"] != "choose mode" {
				t.Fatalf("followup content = %#v, want choose mode", payload["content"])
			}
			components, ok := payload["components"].([]any)
			if !ok || len(components) != 1 {
				t.Fatalf("components = %#v, want one row", payload["components"])
			}
			row := components[0].(map[string]any)
			rowComponents := row["components"].([]any)
			if rowComponents[0].(map[string]any)["custom_id"] != "cmd:/mode default" {
				t.Fatalf("button0 = %#v, want cmd:/mode default", rowComponents[0])
			}
			if rowComponents[1].(map[string]any)["custom_id"] != "cmd:/mode yolo" {
				t.Fatalf("button1 = %#v, want cmd:/mode yolo", rowComponents[1])
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"msg-1","channel_id":"ch-1"}`)
	}))
	defer server.Close()

	oldEndpointDiscord := discordgo.EndpointDiscord
	oldEndpointAPI := discordgo.EndpointAPI
	oldEndpointChannels := discordgo.EndpointChannels
	oldEndpointWebhooks := discordgo.EndpointWebhooks
	discordgo.EndpointDiscord = server.URL + "/"
	discordgo.EndpointAPI = discordgo.EndpointDiscord + "api/v" + discordgo.APIVersion + "/"
	discordgo.EndpointChannels = discordgo.EndpointAPI + "channels/"
	discordgo.EndpointWebhooks = discordgo.EndpointAPI + "webhooks/"
	defer func() {
		discordgo.EndpointDiscord = oldEndpointDiscord
		discordgo.EndpointAPI = oldEndpointAPI
		discordgo.EndpointChannels = oldEndpointChannels
		discordgo.EndpointWebhooks = oldEndpointWebhooks
	}()

	s, err := discordgo.New("Bot test-token")
	if err != nil {
		t.Fatalf("discordgo.New() error = %v", err)
	}
	s.Client = server.Client()

	p := &Platform{session: s}
	rc := &interactionReplyCtx{interaction: &discordgo.Interaction{AppID: "app-1", Token: "token-1"}}
	err = p.SendWithButtons(context.Background(), rc, "choose mode", [][]core.ButtonOption{{
		{Text: "Default", Data: "cmd:/mode default"},
		{Text: "YOLO", Data: "cmd:/mode yolo"},
	}})
	if err != nil {
		t.Fatalf("SendWithButtons() error = %v", err)
	}
	if len(requests) != 2 {
		t.Fatalf("requests = %v, want 2", requests)
	}
}

func TestSendWithButtons_PreservesMultipleRows(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if strings.Contains(r.URL.Path, "/messages/@original") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"id":"msg-1","channel_id":"ch-1"}`)
			return
		}
		components, ok := payload["components"].([]any)
		if !ok || len(components) != 2 {
			t.Fatalf("components = %#v, want two rows", payload["components"])
		}
		first := components[0].(map[string]any)["components"].([]any)
		second := components[1].(map[string]any)["components"].([]any)
		if first[0].(map[string]any)["custom_id"] != "cmd:/reasoning 1" || first[1].(map[string]any)["custom_id"] != "cmd:/reasoning 2" {
			t.Fatalf("first row = %#v, want cmd:/reasoning 1 and 2", first)
		}
		if second[0].(map[string]any)["custom_id"] != "cmd:/reasoning 3" {
			t.Fatalf("second row = %#v, want cmd:/reasoning 3", second)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"msg-2","channel_id":"ch-1"}`)
	}))
	defer server.Close()

	oldEndpointDiscord := discordgo.EndpointDiscord
	oldEndpointAPI := discordgo.EndpointAPI
	oldEndpointChannels := discordgo.EndpointChannels
	oldEndpointWebhooks := discordgo.EndpointWebhooks
	discordgo.EndpointDiscord = server.URL + "/"
	discordgo.EndpointAPI = discordgo.EndpointDiscord + "api/v" + discordgo.APIVersion + "/"
	discordgo.EndpointChannels = discordgo.EndpointAPI + "channels/"
	discordgo.EndpointWebhooks = discordgo.EndpointAPI + "webhooks/"
	defer func() {
		discordgo.EndpointDiscord = oldEndpointDiscord
		discordgo.EndpointAPI = oldEndpointAPI
		discordgo.EndpointChannels = oldEndpointChannels
		discordgo.EndpointWebhooks = oldEndpointWebhooks
	}()

	s, err := discordgo.New("Bot test-token")
	if err != nil {
		t.Fatalf("discordgo.New() error = %v", err)
	}
	s.Client = server.Client()

	p := &Platform{session: s}
	rc := &interactionReplyCtx{interaction: &discordgo.Interaction{AppID: "app-1", Token: "token-1"}}
	err = p.SendWithButtons(context.Background(), rc, "choose reasoning", [][]core.ButtonOption{
		{{Text: "low", Data: "cmd:/reasoning 1"}, {Text: "medium", Data: "cmd:/reasoning 2"}},
		{{Text: "high", Data: "cmd:/reasoning 3"}},
	})
	if err != nil {
		t.Fatalf("SendWithButtons() error = %v", err)
	}
}

func TestSendFile_SendsChannelAttachment(t *testing.T) {
	var contentType string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		contentType = r.Header.Get("Content-Type")
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"msg-file","channel_id":"ch-1"}`)
	}))
	defer server.Close()

	oldEndpointDiscord := discordgo.EndpointDiscord
	oldEndpointAPI := discordgo.EndpointAPI
	oldEndpointChannels := discordgo.EndpointChannels
	discordgo.EndpointDiscord = server.URL + "/"
	discordgo.EndpointAPI = discordgo.EndpointDiscord + "api/v" + discordgo.APIVersion + "/"
	discordgo.EndpointChannels = discordgo.EndpointAPI + "channels/"
	defer func() {
		discordgo.EndpointDiscord = oldEndpointDiscord
		discordgo.EndpointAPI = oldEndpointAPI
		discordgo.EndpointChannels = oldEndpointChannels
	}()

	s, err := discordgo.New("Bot test-token")
	if err != nil {
		t.Fatalf("discordgo.New() error = %v", err)
	}
	s.Client = server.Client()

	p := &Platform{session: s}
	err = p.SendFile(context.Background(), replyContext{channelID: "ch-1"}, core.FileAttachment{
		FileName: "report.pdf",
		MimeType: "application/pdf",
		Data:     []byte("pdf-data"),
	})
	if err != nil {
		t.Fatalf("SendFile() error = %v", err)
	}
	if !strings.Contains(contentType, "multipart/form-data") {
		t.Fatalf("content type = %q, want multipart/form-data", contentType)
	}
}

func TestSendFile_UsesInteractionEndpoints(t *testing.T) {
	requests := make([]string, 0, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"msg-file","channel_id":"ch-1"}`)
	}))
	defer server.Close()

	oldEndpointDiscord := discordgo.EndpointDiscord
	oldEndpointAPI := discordgo.EndpointAPI
	oldEndpointChannels := discordgo.EndpointChannels
	oldEndpointWebhooks := discordgo.EndpointWebhooks
	discordgo.EndpointDiscord = server.URL + "/"
	discordgo.EndpointAPI = discordgo.EndpointDiscord + "api/v" + discordgo.APIVersion + "/"
	discordgo.EndpointChannels = discordgo.EndpointAPI + "channels/"
	discordgo.EndpointWebhooks = discordgo.EndpointAPI + "webhooks/"
	defer func() {
		discordgo.EndpointDiscord = oldEndpointDiscord
		discordgo.EndpointAPI = oldEndpointAPI
		discordgo.EndpointChannels = oldEndpointChannels
		discordgo.EndpointWebhooks = oldEndpointWebhooks
	}()

	s, err := discordgo.New("Bot test-token")
	if err != nil {
		t.Fatalf("discordgo.New() error = %v", err)
	}
	s.Client = server.Client()

	p := &Platform{session: s}
	rc := &interactionReplyCtx{interaction: &discordgo.Interaction{AppID: "app-1", Token: "token-1"}}
	err = p.SendFile(context.Background(), rc, core.FileAttachment{
		FileName: "report.pdf",
		MimeType: "application/pdf",
		Data:     []byte("pdf-data"),
	})
	if err != nil {
		t.Fatalf("SendFile() error = %v", err)
	}
	if len(requests) != 1 || !strings.Contains(requests[0], "/messages/@original") {
		t.Fatalf("requests = %v, want one original interaction edit", requests)
	}
}

func TestSendChannelReply_WithoutMessageIDFallsBackToChannelSend(t *testing.T) {
	var payload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"msg-reply","channel_id":"ch-1"}`)
	}))
	defer server.Close()

	oldEndpointDiscord := discordgo.EndpointDiscord
	oldEndpointAPI := discordgo.EndpointAPI
	oldEndpointChannels := discordgo.EndpointChannels
	discordgo.EndpointDiscord = server.URL + "/"
	discordgo.EndpointAPI = discordgo.EndpointDiscord + "api/v" + discordgo.APIVersion + "/"
	discordgo.EndpointChannels = discordgo.EndpointAPI + "channels/"
	defer func() {
		discordgo.EndpointDiscord = oldEndpointDiscord
		discordgo.EndpointAPI = oldEndpointAPI
		discordgo.EndpointChannels = oldEndpointChannels
	}()

	s, err := discordgo.New("Bot test-token")
	if err != nil {
		t.Fatalf("discordgo.New() error = %v", err)
	}
	s.Client = server.Client()

	p := &Platform{session: s}
	err = p.sendChannelReply(replyContext{channelID: "ch-1"}, "language set to English")
	if err != nil {
		t.Fatalf("sendChannelReply() error = %v", err)
	}
	if payload["content"] != "language set to English" {
		t.Fatalf("content = %#v, want language set to English", payload["content"])
	}
	if _, ok := payload["message_reference"]; ok {
		t.Fatalf("message_reference = %#v, want omitted when messageID is empty", payload["message_reference"])
	}
}

// ── Dedup tests ──────────────────────────────────────────────

// simulateHandlerCall mimics the dedup + dispatch logic in the MessageCreate
// handler registered by Platform.Start.  It returns true when the message
// was dispatched (not a duplicate).
func (p *Platform) simulateHandlerCall(msgID, userID, userName, channelID, content string) bool {
	// --- dedup (same logic as Start handler) ---
	if !rememberDedupID(&p.seenMsgs, msgID) {
		return false
	}

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

// simulateInteractionHandlerCall mimics the dedup + dispatch logic shared by
// slash commands and button interactions. It returns true when the interaction
// was dispatched (not a duplicate).
func (p *Platform) simulateInteractionHandlerCall(interactionID, userID, userName, channelID, content string) bool {
	if !rememberDedupID(&p.seenInteractions, interactionID) {
		return false
	}

	msg := &core.Message{
		SessionKey: p.makeSessionKey(channelID, userID),
		Platform:   "discord",
		MessageID:  interactionID,
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

// TestDuplicateInteraction_SameIDDeduped verifies the shared interaction dedup
// path used by slash commands and button interactions.
func TestDuplicateInteraction_SameIDDeduped(t *testing.T) {
	var calls int32
	p := newTestPlatform(func(_ core.Platform, _ *core.Message) {
		atomic.AddInt32(&calls, 1)
	})

	const interactionID = "1499999999999999999"

	if !p.simulateInteractionHandlerCall(interactionID, "user1", "quabug", "ch1", "/config thinking_max_len 200") {
		t.Fatal("first interaction delivery was incorrectly treated as duplicate")
	}
	if p.simulateInteractionHandlerCall(interactionID, "user1", "quabug", "ch1", "/config thinking_max_len 200") {
		t.Fatal("second interaction delivery was not caught as duplicate")
	}

	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Fatalf("handler called %d times, want 1", n)
	}
}

func TestDuplicateInteraction_ConcurrentRace(t *testing.T) {
	var calls int32
	p := newTestPlatform(func(_ core.Platform, _ *core.Message) {
		atomic.AddInt32(&calls, 1)
	})

	const (
		interactionID = "race-interaction-1"
		goroutines    = 50
	)

	var wg sync.WaitGroup
	wg.Add(goroutines)
	start := make(chan struct{})

	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			<-start
			p.simulateInteractionHandlerCall(interactionID, "user1", "quabug", "ch1", "cmd:new")
		}()
	}

	close(start)
	wg.Wait()

	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Fatalf("handler called %d times under race, want exactly 1", n)
	}
}

// ── @everyone mention tests ──────────────────────────────────

func TestIsDiscordBotMention_Everyone(t *testing.T) {
	tests := []struct {
		name                       string
		respondToAtEveryoneAndHere bool
		mentionEveryone            bool
		want                       bool
	}{
		{"enabled + @everyone", true, true, true},
		{"disabled + @everyone", false, true, false},
		{"enabled + no @everyone", true, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &discordgo.MessageCreate{
				Message: &discordgo.Message{
					MentionEveryone: tt.mentionEveryone,
					Content:         "hello",
					Author:          &discordgo.User{ID: "user1"},
				},
			}
			got := isDiscordBotMention(m, "bot1", "", tt.respondToAtEveryoneAndHere)
			if got != tt.want {
				t.Errorf("isDiscordBotMention(respondToAtEveryoneAndHere=%v, MentionEveryone=%v) = %v, want %v",
					tt.respondToAtEveryoneAndHere, tt.mentionEveryone, got, tt.want)
			}
		})
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

func TestReplyContextForDeferredInteractionFallback(t *testing.T) {
	cid := "chan-1"
	tests := []struct {
		name string
		ch   *discordgo.Channel
		want replyContext
	}{
		{"nil channel", nil, replyContext{channelID: cid}},
		{"guild text", &discordgo.Channel{ID: cid, Type: discordgo.ChannelTypeGuildText}, replyContext{channelID: cid}},
		{"public thread", &discordgo.Channel{ID: cid, Type: discordgo.ChannelTypeGuildPublicThread}, replyContext{channelID: cid, threadID: cid}},
		{"private thread", &discordgo.Channel{ID: cid, Type: discordgo.ChannelTypeGuildPrivateThread}, replyContext{channelID: cid, threadID: cid}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := replyContextForDeferredInteractionFallback(tt.ch, cid)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %+v want %+v", got, tt.want)
			}
		})
	}
}
