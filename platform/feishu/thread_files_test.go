package feishu

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

const (
	tfAppID   = "cli_thread_files"
	tfBot     = "ou_bot"
	tfAlice   = "ou_alice"
	tfBob     = "ou_bob"
	tfChat    = "oc_group"
	tfRoot    = "om_root"
	tfThread  = "omt_thread"
	tfTrigger = "om_trigger"
)

// threadItem builds one entry of GET /im/v1/messages.
func threadItem(id, msgType, sender, content string, ms int64, mentionBot bool) map[string]any {
	item := map[string]any{
		"message_id":  id,
		"msg_type":    msgType,
		"create_time": strconv.FormatInt(ms, 10),
		"sender":      map[string]any{"id": sender, "id_type": "open_id", "sender_type": "user"},
		"body":        map[string]any{"content": content},
	}
	if mentionBot {
		item["mentions"] = []map[string]any{{"key": "@_user_1", "id": tfBot, "id_type": "open_id", "name": "Bot"}}
	}
	return item
}

type threadFilesServer struct {
	t         *testing.T
	srv       *httptest.Server
	mu        sync.Mutex
	listCalls int
	downloads []string // message IDs whose resource was fetched
	listCode  int
	failMsgID string // resource download for this message returns 500
	items     []map[string]any
}

func newThreadFilesServer(t *testing.T, items []map[string]any) *threadFilesServer {
	s := &threadFilesServer{t: t, items: items}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal":
			writeJSON(t, w, map[string]any{"code": 0, "msg": "success", "expire": 7200, "tenant_access_token": "tenant-token"})
		case r.URL.Path == "/open-apis/im/v1/messages":
			s.mu.Lock()
			s.listCalls++
			code := s.listCode
			s.mu.Unlock()
			q := r.URL.Query()
			if q.Get("container_id_type") != "thread" || q.Get("container_id") != tfThread || q.Get("sort_type") != "ByCreateTimeDesc" {
				t.Errorf("unexpected list query %q", r.URL.RawQuery)
			}
			if code != 0 {
				writeJSON(t, w, map[string]any{"code": code, "msg": "Lack of necessary permissions"})
				return
			}
			writeJSON(t, w, map[string]any{"code": 0, "msg": "success", "data": map[string]any{"has_more": false, "items": s.items}})
		case strings.Contains(r.URL.Path, "/resources/"):
			msgID := strings.Split(strings.TrimPrefix(r.URL.Path, "/open-apis/im/v1/messages/"), "/")[0]
			s.mu.Lock()
			s.downloads = append(s.downloads, msgID)
			fail := msgID == s.failMsgID
			s.mu.Unlock()
			if fail {
				w.WriteHeader(http.StatusInternalServerError)
				writeJSON(t, w, map[string]any{"code": 234001, "msg": "boom"})
				return
			}
			w.Header().Set("Content-Type", "application/octet-stream")
			if r.URL.Query().Get("type") == "image" {
				w.Header().Set("Content-Type", "image/png")
				_, _ = w.Write([]byte("\x89PNG\r\n\x1a\n" + msgID))
				return
			}
			_, _ = w.Write([]byte("VERSION \"\"\nBODY " + msgID))
		default:
			writeJSON(t, w, map[string]any{"code": 0, "msg": "success"})
		}
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *threadFilesServer) platform(handler func(core.Platform, *core.Message)) *Platform {
	return &Platform{
		platformName:    "feishu",
		domain:          s.srv.URL,
		appID:           tfAppID,
		appSecret:       "secret",
		botOpenID:       tfBot,
		threadIsolation: true,
		dedup:           &core.MessageDedup{},
		handler:         handler,
		client: lark.NewClient(tfAppID, "secret",
			lark.WithOpenBaseUrl(s.srv.URL),
			lark.WithHttpClient(s.srv.Client()),
		),
	}
}

func (s *threadFilesServer) stats() (int, []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listCalls, append([]string(nil), s.downloads...)
}

// sendGroupText delivers a text event into the thread through onMessage.
func sendGroupText(t *testing.T, p *Platform, msgID, sender, text string, ms int64, mentionBot bool) {
	t.Helper()
	chatType, senderType, msgType, threadID := "group", "user", "text", tfThread
	content := `{"text":"` + text + `"}`
	createTime := strconv.FormatInt(ms, 10)
	var mentions []*larkim.MentionEvent
	if mentionBot {
		mentions = []*larkim.MentionEvent{{Key: strPtr("@_user_1"), Id: &larkim.UserId{OpenId: strPtr(tfBot)}, Name: strPtr("Bot")}}
	}
	err := p.onMessage(context.Background(), &larkim.P2MessageReceiveV1{
		Event: &larkim.P2MessageReceiveV1Data{
			Sender: &larkim.EventSender{SenderId: &larkim.UserId{OpenId: strPtr(sender)}, SenderType: &senderType},
			Message: &larkim.EventMessage{
				MessageId: strPtr(msgID), RootId: strPtr(tfRoot), ParentId: strPtr(tfRoot), ThreadId: &threadID,
				ChatId: strPtr(tfChat), ChatType: &chatType, MessageType: &msgType,
				Content: &content, CreateTime: &createTime, Mentions: mentions,
			},
		},
	})
	if err != nil {
		t.Fatalf("onMessage(%s) error = %v", msgID, err)
	}
}

func waitMessage(t *testing.T, got <-chan *core.Message) *core.Message {
	t.Helper()
	select {
	case msg := <-got:
		return msg
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for dispatched message")
		return nil
	}
}

// The incident: a user uploads a DBC into an engaged thread (the file event is
// dropped by the mention filter), then asks "@bot analyse it". The trigger must
// carry the file; files from before the previous @bot message, files of other
// members, and /commands must not shift or leak into the window.
func TestThreadBackfillForwardsSenderFilesSinceLastMention(t *testing.T) {
	now := time.Now().UnixMilli()
	items := []map[string]any{ // newest first, as ByCreateTimeDesc returns them
		threadItem(tfTrigger, "text", tfAlice, `{"text":"@_user_1 结合DBC分析"}`, now, true),
		threadItem("om_f2", "file", tfAlice, `{"file_key":"file_k2","file_name":"390.dbc"}`, now-1000, false),
		threadItem("om_bob_file", "file", tfBob, `{"file_key":"file_kb","file_name":"bob.dbc"}`, now-2000, false),
		threadItem("om_ps", "text", tfAlice, `{"text":"@_user_1 /ps 已经补充到当前话题"}`, now-3000, true),
		threadItem("om_img", "image", tfAlice, `{"image_key":"img_k1"}`, now-4000, false),
		threadItem("om_f1", "file", tfAlice, `{"file_key":"file_k1","file_name":"first.asc"}`, now-5000, false),
		threadItem("om_prev", "text", tfAlice, `{"text":"@_user_1 先看日志"}`, now-6000, true),
		threadItem("om_f0", "file", tfAlice, `{"file_key":"file_k0","file_name":"old.dbc"}`, now-7000, false),
	}
	s := newThreadFilesServer(t, items)
	got := make(chan *core.Message, 4)
	p := s.platform(func(_ core.Platform, msg *core.Message) { got <- msg })
	p.activeThreadSessions.Store("feishu:"+tfChat+":root:"+tfRoot, time.Now())

	// The file itself carries no mention: still filtered, nothing dispatched.
	fileType, chatType, senderType, threadID := "file", "group", "user", tfThread
	fileContent := `{"file_key":"file_k2","file_name":"390.dbc"}`
	fileTime := strconv.FormatInt(now-1000, 10)
	if err := p.onMessage(context.Background(), &larkim.P2MessageReceiveV1{Event: &larkim.P2MessageReceiveV1Data{
		Sender: &larkim.EventSender{SenderId: &larkim.UserId{OpenId: strPtr(tfAlice)}, SenderType: &senderType},
		Message: &larkim.EventMessage{MessageId: strPtr("om_f2"), RootId: strPtr(tfRoot), ThreadId: &threadID,
			ChatId: strPtr(tfChat), ChatType: &chatType, MessageType: &fileType, Content: &fileContent, CreateTime: &fileTime},
	}}); err != nil {
		t.Fatalf("onMessage(file) error = %v", err)
	}

	sendGroupText(t, p, tfTrigger, tfAlice, "@_user_1 结合DBC分析", now, true)
	msg := waitMessage(t, got)
	if msg.MessageID != tfTrigger {
		t.Fatalf("dispatched %q first, want the trigger (the bare file must stay filtered)", msg.MessageID)
	}
	if msg.Content != "结合DBC分析" {
		t.Fatalf("Content = %q", msg.Content)
	}
	var names []string
	for _, f := range msg.Files {
		names = append(names, f.FileName)
	}
	if strings.Join(names, ",") != "first.asc,390.dbc" {
		t.Fatalf("Files = %v, want [first.asc 390.dbc] in upload order", names)
	}
	if string(msg.Files[1].Data) != "VERSION \"\"\nBODY om_f2" {
		t.Fatalf("390.dbc bytes = %q", msg.Files[1].Data)
	}
	if len(msg.Images) != 1 {
		t.Fatalf("Images = %d, want the sender's image from the window", len(msg.Images))
	}
	listCalls, downloads := s.stats()
	if listCalls != 1 {
		t.Fatalf("list calls = %d, want 1", listCalls)
	}
	for _, id := range downloads {
		if id == "om_f0" || id == "om_bob_file" {
			t.Fatalf("downloaded %s; only the sender's files since the previous @bot message may be fetched", id)
		}
	}
}

func TestThreadBackfillSkipsCommandsUnmentionedAndNonThreadMessages(t *testing.T) {
	now := time.Now().UnixMilli()
	s := newThreadFilesServer(t, []map[string]any{
		threadItem("om_f1", "file", tfAlice, `{"file_key":"file_k1","file_name":"a.dbc"}`, now-1000, false),
	})
	got := make(chan *core.Message, 4)
	p := s.platform(func(_ core.Platform, msg *core.Message) { got <- msg })
	p.activeThreadSessions.Store("feishu:"+tfChat+":root:"+tfRoot, time.Now())

	// A slash command reaches the engine but never the agent: no backfill.
	sendGroupText(t, p, "om_cmd", tfAlice, "@_user_1 /ps", now, true)
	if msg := waitMessage(t, got); len(msg.Files) != 0 {
		t.Fatalf("command carried %d files", len(msg.Files))
	}
	// Without a mention the message is filtered before dispatch.
	sendGroupText(t, p, "om_plain", tfAlice, "看下文件", now+1, false)
	select {
	case msg := <-got:
		t.Fatalf("unmentioned message dispatched: %+v", msg)
	case <-time.After(200 * time.Millisecond):
	}
	if calls, _ := s.stats(); calls != 0 {
		t.Fatalf("list calls = %d, want 0 for commands and filtered messages", calls)
	}

	// Outside thread isolation there is no thread session: no backfill.
	p2 := s.platform(func(_ core.Platform, msg *core.Message) { got <- msg })
	p2.threadIsolation = false
	sendGroupText(t, p2, "om_flat", tfAlice, "@_user_1 看下文件", now+2, true)
	if msg := waitMessage(t, got); len(msg.Files) != 0 {
		t.Fatalf("non-thread session carried %d files", len(msg.Files))
	}
	if calls, _ := s.stats(); calls != 0 {
		t.Fatalf("list calls = %d, want 0 without thread isolation", calls)
	}
}

func TestThreadBackfillReportsDownloadFailure(t *testing.T) {
	now := time.Now().UnixMilli()
	s := newThreadFilesServer(t, []map[string]any{
		threadItem("om_bad", "file", tfAlice, `{"file_key":"file_bad","file_name":"broken.dbc"}`, now-2000, false),
		threadItem("om_ok", "file", tfAlice, `{"file_key":"file_ok","file_name":"ok.dbc"}`, now-1000, false),
	})
	s.failMsgID = "om_bad"
	got := make(chan *core.Message, 2)
	p := s.platform(func(_ core.Platform, msg *core.Message) { got <- msg })

	sendGroupText(t, p, tfTrigger, tfAlice, "@_user_1 analyse", now, true)
	msg := waitMessage(t, got)
	if len(msg.Files) != 1 || msg.Files[0].FileName != "ok.dbc" {
		t.Fatalf("Files = %+v, want only ok.dbc", msg.Files)
	}
	if !strings.Contains(msg.ExtraContent, "broken.dbc") || !strings.Contains(msg.ExtraContent, "om_bad") || !strings.Contains(msg.ExtraContent, "download failed") {
		t.Fatalf("ExtraContent = %q, want a notice naming the file, message and failed stage", msg.ExtraContent)
	}
	if msg.Content != "analyse" {
		t.Fatalf("Content = %q, want the user's text untouched", msg.Content)
	}
}

func TestThreadBackfillReportsMissingScopeOncePerThread(t *testing.T) {
	now := time.Now().UnixMilli()
	s := newThreadFilesServer(t, nil)
	s.listCode = 230027
	got := make(chan *core.Message, 2)
	p := s.platform(func(_ core.Platform, msg *core.Message) { got <- msg })

	sendGroupText(t, p, "om_t1", tfAlice, "@_user_1 analyse", now, true)
	first := waitMessage(t, got)
	if !strings.Contains(first.ExtraContent, "im:message.group_msg") || !strings.Contains(first.ExtraContent, "code=230027") {
		t.Fatalf("ExtraContent = %q, want the denied code and the required scopes", first.ExtraContent)
	}
	sendGroupText(t, p, "om_t2", tfAlice, "@_user_1 again", now+1, true)
	if second := waitMessage(t, got); strings.Contains(second.ExtraContent, "could not be read") {
		t.Fatalf("second ExtraContent = %q, want the scope notice only once per thread", second.ExtraContent)
	}
}

// A skill or custom command reaches the agent: it carries the files (images
// as files, so the engine still resolves the command) and closes the window.
func TestThreadBackfillSkillCommandCarriesFiles(t *testing.T) {
	now := time.Now().UnixMilli()
	s := newThreadFilesServer(t, []map[string]any{
		threadItem("om_img", "image", tfAlice, `{"image_key":"img_k1"}`, now-1000, false),
		threadItem("om_f1", "file", tfAlice, `{"file_key":"file_k1","file_name":"run.log"}`, now-2000, false),
		threadItem("om_why", "text", tfAlice, `{"text":"@_user_1 /why 之前那次"}`, now-3000, true),
		threadItem("om_f0", "file", tfAlice, `{"file_key":"file_k0","file_name":"old.log"}`, now-4000, false),
	})
	got := make(chan *core.Message, 2)
	p := s.platform(func(_ core.Platform, msg *core.Message) { got <- msg })
	p.activeThreadSessions.Store("feishu:"+tfChat+":root:"+tfRoot, time.Now())

	sendGroupText(t, p, tfTrigger, tfAlice, "@_user_1 /why 看下上面的日志", now, true)
	msg := waitMessage(t, got)
	var names []string
	for _, f := range msg.Files {
		names = append(names, f.FileName)
	}
	if strings.Join(names, ",") != "run.log,image_om_img.png" {
		t.Fatalf("Files = %v, want [run.log image_om_img.png]: the earlier /why closes the window", names)
	}
	if len(msg.Images) != 0 {
		t.Fatalf("Images = %d, want 0 so the engine still resolves /why", len(msg.Images))
	}
}

func TestIsEngineCommandText(t *testing.T) {
	for text, want := range map[string]bool{
		"@_user_1 /ps 已经补充":   true,
		"/new":                true,
		"  @_user_1   /stop ": true,
		"@_user_1 /why 看下日志":  false,
		"/no-such-command":    false,
		"@_user_1 看看 /tmp/x":  false,
		"":                    false,
	} {
		if got := isEngineCommandText(text); got != want {
			t.Errorf("isEngineCommandText(%q) = %v, want %v", text, got, want)
		}
	}
}
