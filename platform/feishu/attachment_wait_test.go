package feishu

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

// attachmentWaitHarness drives onMessage end to end for issue #2186 with a
// stub Feishu server that serves one image and one file resource.
type attachmentWaitHarness struct {
	t        *testing.T
	p        *Platform
	received chan *core.Message
	now      int64
}

const (
	awBotOpenID = "ou_bot"
	awChatID    = "oc_chat"
	awRootID    = "om_root"
	awImageKey  = "img_staged"
	awFileKey   = "file_staged"
	awThreadKey = "feishu:" + awChatID + ":root:" + awRootID
)

var (
	awImageBytes = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}
	awFileBytes  = []byte("quarterly report v2")
)

func newAttachmentWaitHarness(t *testing.T, mode string, ttl time.Duration) *attachmentWaitHarness {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal":
			writeJSON(t, w, map[string]any{"code": 0, "msg": "success", "expire": 7200, "tenant_access_token": "tenant-token"})
		case strings.HasSuffix(r.URL.Path, "/resources/"+awImageKey):
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(awImageBytes)
		case strings.HasSuffix(r.URL.Path, "/resources/"+awFileKey):
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write(awFileBytes)
		default:
			writeJSON(t, w, map[string]any{"code": 0, "msg": "success", "data": map[string]any{}})
		}
	}))
	t.Cleanup(srv.Close)

	h := &attachmentWaitHarness{t: t, received: make(chan *core.Message, 16), now: time.Now().UnixMilli()}
	h.p = &Platform{
		platformName:         "feishu",
		domain:               srv.URL,
		appID:                "cli_aw",
		appSecret:            "secret_aw",
		botOpenID:            awBotOpenID,
		threadIsolation:      true,
		attachmentWait:       mode,
		attachmentWaitTTL:    ttl,
		dedup:                &core.MessageDedup{},
		resourceDownloadHTTP: srv.Client(),
		resourceChunkSize:    1 << 20,
		resourceMaxBytes:     defaultResourceMaxBytes,
		fetchResourceToken:   func(context.Context) (string, error) { return "tenant-token", nil },
		client: lark.NewClient("cli_aw", "secret_aw",
			lark.WithOpenBaseUrl(srv.URL), lark.WithHttpClient(srv.Client())),
		handler: func(_ core.Platform, msg *core.Message) { h.received <- msg },
	}
	return h
}

func (h *attachmentWaitHarness) send(sender, msgID, msgType, content string, mentionBot bool, inThread bool) {
	h.t.Helper()
	chatType, senderType := "group", "user"
	h.now++
	ct := strconv.FormatInt(h.now, 10)
	m := &larkim.EventMessage{
		MessageId: stringPtr(msgID), ChatId: stringPtr(awChatID), ChatType: &chatType,
		MessageType: stringPtr(msgType), Content: stringPtr(content), CreateTime: &ct,
	}
	if inThread {
		m.RootId = stringPtr(awRootID)
	}
	if mentionBot {
		m.Mentions = []*larkim.MentionEvent{{
			Key: stringPtr("@_user_1"), Id: &larkim.UserId{OpenId: stringPtr(awBotOpenID)}, Name: stringPtr("bot"),
		}}
	}
	ev := &larkim.P2MessageReceiveV1{Event: &larkim.P2MessageReceiveV1Data{
		Sender:  &larkim.EventSender{SenderId: &larkim.UserId{OpenId: stringPtr(sender)}, SenderType: &senderType},
		Message: m,
	}}
	if err := h.p.onMessage(context.Background(), ev); err != nil {
		h.t.Fatalf("onMessage(%s): %v", msgID, err)
	}
}

func (h *attachmentWaitHarness) expect() *core.Message {
	h.t.Helper()
	select {
	case msg := <-h.received:
		return msg
	case <-time.After(3 * time.Second):
		h.t.Fatal("timed out waiting for dispatch")
		return nil
	}
}

func (h *attachmentWaitHarness) expectNone(wait time.Duration) {
	h.t.Helper()
	select {
	case msg := <-h.received:
		h.t.Fatalf("unexpected dispatch: id=%s content=%q images=%d files=%d",
			msg.MessageID, msg.Content, len(msg.Images), len(msg.Files))
	case <-time.After(wait):
	}
}

// engage opens the topic with an @bot message so it becomes active.
func (h *attachmentWaitHarness) engage(sender string) {
	h.t.Helper()
	h.send(sender, awRootID, "text", `{"text":"@_user_1 hi"}`, true, false)
	h.expect()
}

const (
	awImageContent = `{"image_key":"` + awImageKey + `"}`
	awFileContent  = `{"file_key":"` + awFileKey + `","file_name":"report.txt"}`
)

func TestAttachmentWaitOffKeepsImmediateDispatch(t *testing.T) {
	h := newAttachmentWaitHarness(t, attachmentWaitOff, 0)
	h.engage("ou_a")
	h.send("ou_a", "om_file", "file", awFileContent, false, true)
	msg := h.expect()
	if msg.MessageID != "om_file" || len(msg.Files) != 1 {
		t.Fatalf("off mode must dispatch the file alone as before, got id=%s files=%d", msg.MessageID, len(msg.Files))
	}
}

func TestAttachmentWaitMentionReleasesWithSameSenderMention(t *testing.T) {
	h := newAttachmentWaitHarness(t, attachmentWaitMention, time.Minute)
	h.engage("ou_a")

	h.send("ou_a", "om_img", "image", awImageContent, false, true)
	h.send("ou_a", "om_file", "file", awFileContent, false, true)
	h.expectNone(700 * time.Millisecond) // longer than the image batch window

	h.send("ou_a", "om_ask", "text", `{"text":"@_user_1 compare these with last week"}`, true, true)
	msg := h.expect()
	if msg.MessageID != "om_ask" || msg.Content != "compare these with last week" {
		t.Fatalf("release message = id %s content %q", msg.MessageID, msg.Content)
	}
	if len(msg.Images) != 1 || string(msg.Images[0].Data) != string(awImageBytes) {
		t.Fatalf("staged image not attached: %+v", msg.Images)
	}
	if len(msg.Files) != 1 || msg.Files[0].FileName != "report.txt" || string(msg.Files[0].Data) != string(awFileBytes) {
		t.Fatalf("staged file not attached: %+v", msg.Files)
	}
	h.expectNone(200 * time.Millisecond)
	if items := h.p.takeStagedAttachments(awThreadKey, "ou_a"); len(items) != 0 {
		t.Fatalf("stage must be consumed by release, still has %d", len(items))
	}
}

func TestAttachmentWaitMentionUnmentionedTextDoesNotRelease(t *testing.T) {
	h := newAttachmentWaitHarness(t, attachmentWaitMention, time.Minute)
	h.engage("ou_a")
	h.send("ou_a", "om_file", "file", awFileContent, false, true)
	h.send("ou_a", "om_chat", "text", `{"text":"ok got it"}`, false, true)
	h.expectNone(300 * time.Millisecond)

	h.send("ou_a", "om_ask", "text", `{"text":"@_user_1 summarize"}`, true, true)
	if msg := h.expect(); len(msg.Files) != 1 {
		t.Fatalf("file should still be staged for the @bot message, got %d files", len(msg.Files))
	}
}

func TestAttachmentWaitMentionIsolatesSenders(t *testing.T) {
	h := newAttachmentWaitHarness(t, attachmentWaitMention, time.Minute)
	h.engage("ou_a")
	h.send("ou_a", "om_file", "file", awFileContent, false, true)

	h.send("ou_b", "om_b", "text", `{"text":"@_user_1 what time is it"}`, true, true)
	if msg := h.expect(); msg.MessageID != "om_b" || len(msg.Files) != 0 || len(msg.Images) != 0 {
		t.Fatalf("another sender's @bot must not take A's attachments: id=%s files=%d", msg.MessageID, len(msg.Files))
	}
	h.send("ou_a", "om_a", "text", `{"text":"@_user_1 summarize"}`, true, true)
	if msg := h.expect(); msg.MessageID != "om_a" || len(msg.Files) != 1 {
		t.Fatalf("A's @bot should release A's file, got id=%s files=%d", msg.MessageID, len(msg.Files))
	}
}

func TestAttachmentWaitMentionBareMentionIsNotDroppedAsEmpty(t *testing.T) {
	h := newAttachmentWaitHarness(t, attachmentWaitMention, time.Minute)
	h.engage("ou_a")
	h.send("ou_a", "om_file", "file", awFileContent, false, true)
	h.send("ou_a", "om_ask", "text", `{"text":"@_user_1"}`, true, true)
	if msg := h.expect(); msg.MessageID != "om_ask" || len(msg.Files) != 1 || msg.Content != "" {
		t.Fatalf("bare @bot with staged file should dispatch the file, got id=%s content=%q files=%d", msg.MessageID, msg.Content, len(msg.Files))
	}
}

func TestAttachmentWaitMentionPostReleases(t *testing.T) {
	h := newAttachmentWaitHarness(t, attachmentWaitMention, time.Minute)
	h.engage("ou_a")
	h.send("ou_a", "om_img", "image", awImageContent, false, true)
	post := `{"zh_cn":{"title":"","content":[[{"tag":"at","user_id":"@_user_1"},{"tag":"text","text":" describe it"}]]}}`
	h.send("ou_a", "om_post", "post", post, true, true)
	if msg := h.expect(); msg.MessageID != "om_post" || len(msg.Images) != 1 {
		t.Fatalf("post @bot should release staged image, got id=%s images=%d", msg.MessageID, len(msg.Images))
	}
}

func TestAttachmentWaitMentionCommands(t *testing.T) {
	h := newAttachmentWaitHarness(t, attachmentWaitMention, time.Minute)
	h.engage("ou_a")
	h.send("ou_a", "om_file", "file", awFileContent, false, true)

	h.send("ou_a", "om_status", "text", `{"text":"@_user_1 /status"}`, true, true)
	if msg := h.expect(); msg.MessageID != "om_status" || len(msg.Files) != 0 {
		t.Fatalf("commands must not consume staged attachments: id=%s files=%d", msg.MessageID, len(msg.Files))
	}
	h.send("ou_a", "om_new", "text", `{"text":"@_user_1 /new"}`, true, true)
	if msg := h.expect(); msg.MessageID != "om_new" {
		t.Fatalf("expected /new dispatch, got %s", msg.MessageID)
	}
	h.send("ou_a", "om_ask", "text", `{"text":"@_user_1 summarize"}`, true, true)
	if msg := h.expect(); msg.MessageID != "om_ask" || len(msg.Files) != 0 {
		t.Fatalf("/new should clear staged attachments, got %d files", len(msg.Files))
	}
}

func TestAttachmentWaitMentionSkipsRecalledAttachment(t *testing.T) {
	h := newAttachmentWaitHarness(t, attachmentWaitMention, time.Minute)
	h.engage("ou_a")
	h.send("ou_a", "om_file", "file", awFileContent, false, true)
	h.p.markMessageRecalled("om_file")
	h.send("ou_a", "om_ask", "text", `{"text":"@_user_1 summarize"}`, true, true)
	if msg := h.expect(); len(msg.Files) != 0 {
		t.Fatal("recalled staged attachment must not be released")
	}
}

func TestAttachmentWaitMentionAudioStillDispatchesImmediately(t *testing.T) {
	for msgType, want := range map[string]bool{"image": true, "file": true, "media": true, "audio": false, "text": false} {
		if got := canStageAttachment(msgType); got != want {
			t.Errorf("canStageAttachment(%q) = %v, want %v", msgType, got, want)
		}
	}
}

func TestAttachmentWaitStageExpires(t *testing.T) {
	p := &Platform{platformName: "feishu", attachmentWait: attachmentWaitMention, attachmentWaitTTL: 50 * time.Millisecond}
	p.stageAttachment(awThreadKey, "ou_a", stagedAttachment{messageID: "om_1", msgType: "file"})
	time.Sleep(200 * time.Millisecond)
	if items := p.takeStagedAttachments(awThreadKey, "ou_a"); len(items) != 0 {
		t.Fatalf("expired stage should be dropped, got %d", len(items))
	}
}

func TestAttachmentWaitStaleTimerDoesNotDropNewStage(t *testing.T) {
	p := &Platform{platformName: "feishu", attachmentWait: attachmentWaitMention, attachmentWaitTTL: time.Minute}
	key := attachmentStageKey(awThreadKey, "ou_a")
	p.stageAttachment(awThreadKey, "ou_a", stagedAttachment{messageID: "om_1"})
	p.attachmentStageMu.Lock()
	old := p.attachmentStages[key]
	p.attachmentStageMu.Unlock()

	p.takeStagedAttachments(awThreadKey, "ou_a")
	p.stageAttachment(awThreadKey, "ou_a", stagedAttachment{messageID: "om_2"})
	p.expireAttachmentStage(key, old) // what a stale timer would do

	items := p.takeStagedAttachments(awThreadKey, "ou_a")
	if len(items) != 1 || items[0].messageID != "om_2" {
		t.Fatalf("stale timer must not drop the newer stage, got %+v", items)
	}
}

func TestAttachmentWaitStageLimitDropsOldest(t *testing.T) {
	p := &Platform{platformName: "feishu", attachmentWait: attachmentWaitMention, attachmentWaitTTL: time.Minute}
	for i := 0; i < maxStagedAttachments+3; i++ {
		p.stageAttachment(awThreadKey, "ou_a", stagedAttachment{messageID: "om_" + strconv.Itoa(i)})
	}
	items := p.takeStagedAttachments(awThreadKey, "ou_a")
	if len(items) != maxStagedAttachments || items[0].messageID != "om_3" {
		t.Fatalf("want newest %d items starting at om_3, got %d starting at %s",
			maxStagedAttachments, len(items), items[0].messageID)
	}
}

func TestAttachmentWaitStopDropsStages(t *testing.T) {
	p := &Platform{platformName: "feishu", attachmentWait: attachmentWaitMention, attachmentWaitTTL: time.Minute}
	p.stageAttachment(awThreadKey, "ou_a", stagedAttachment{messageID: "om_1"})
	if err := p.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if items := p.takeStagedAttachments(awThreadKey, "ou_a"); len(items) != 0 {
		t.Fatalf("Stop should drop staged attachments, got %d", len(items))
	}
}

func TestAttachmentWaitThreadActivityTTL(t *testing.T) {
	stale := time.Now().Add(-2 * time.Hour)

	off := &Platform{threadIsolation: true}
	off.activeThreadSessions.Store(awThreadKey, stale)
	if !off.isActiveThreadSession(awThreadKey) {
		t.Fatal("off mode keeps the existing lifetime-long activity")
	}

	on := &Platform{threadIsolation: true, attachmentWait: attachmentWaitMention, attachmentWaitTTL: time.Hour}
	on.activeThreadSessions.Store(awThreadKey, stale)
	if on.isActiveThreadSession(awThreadKey) {
		t.Fatal("mention mode should expire threads idle longer than attachment_wait_secs")
	}
	on.markThreadSessionActive(awThreadKey)
	if !on.isActiveThreadSession(awThreadKey) {
		t.Fatal("a new @bot message should make the thread active again")
	}
}

func TestAttachmentWaitExpiredThreadDropsUnmentionedAttachment(t *testing.T) {
	h := newAttachmentWaitHarness(t, attachmentWaitMention, time.Hour)
	h.engage("ou_a")
	h.p.activeThreadSessions.Store(awThreadKey, time.Now().Add(-2*time.Hour))
	h.send("ou_a", "om_file", "file", awFileContent, false, true)
	h.expectNone(200 * time.Millisecond)
	if items := h.p.takeStagedAttachments(awThreadKey, "ou_a"); len(items) != 0 {
		t.Fatal("attachments in an expired thread are ignored like unmentioned text, not staged")
	}
}

func TestParseAttachmentWaitOptions(t *testing.T) {
	cases := []struct {
		name     string
		opts     map[string]any
		wantMode string
		wantTTL  time.Duration
		wantErr  bool
	}{
		{"default", map[string]any{}, attachmentWaitOff, defaultAttachmentWaitSecs * time.Second, false},
		{"mention", map[string]any{"attachment_wait": "Mention"}, attachmentWaitMention, defaultAttachmentWaitSecs * time.Second, false},
		{"secs", map[string]any{"attachment_wait": "mention", "attachment_wait_secs": int64(600)}, attachmentWaitMention, 600 * time.Second, false},
		{"explicit off", map[string]any{"attachment_wait": "off"}, attachmentWaitOff, defaultAttachmentWaitSecs * time.Second, false},
		{"next reserved", map[string]any{"attachment_wait": "next"}, "", 0, true},
		{"unknown", map[string]any{"attachment_wait": "hold"}, "", 0, true},
		{"not string", map[string]any{"attachment_wait": true}, "", 0, true},
		{"zero secs", map[string]any{"attachment_wait_secs": int64(0)}, "", 0, true},
		{"too long", map[string]any{"attachment_wait_secs": int64(maxAttachmentWaitSecs + 1)}, "", 0, true},
		{"string secs", map[string]any{"attachment_wait_secs": "1800"}, "", 0, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			mode, ttl, err := parseAttachmentWaitOptions("feishu", c.opts)
			if (err != nil) != c.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, c.wantErr)
			}
			if !c.wantErr && (mode != c.wantMode || ttl != c.wantTTL) {
				t.Fatalf("got (%q, %v), want (%q, %v)", mode, ttl, c.wantMode, c.wantTTL)
			}
		})
	}
}
