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
	t          *testing.T
	srv        *httptest.Server
	mu         sync.Mutex
	listCalls  int
	downloads  []string // message IDs whose resource was fetched
	singleGets []string // message IDs fetched one by one (the quote path)
	listCode   int
	failMsgID  string // resource download for this message returns 500
	items      []map[string]any
	pages      [][]map[string]any        // when set, served page by page instead of items
	single     map[string]map[string]any // GET /im/v1/messages/:id responses
}

func (s *threadFilesServer) setItems(items []map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items = items
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
			code, items, pages := s.listCode, s.items, s.pages
			s.mu.Unlock()
			q := r.URL.Query()
			if q.Get("container_id_type") != "thread" || q.Get("container_id") != tfThread || q.Get("sort_type") != "ByCreateTimeDesc" || q.Get("user_id_type") != "open_id" {
				t.Errorf("unexpected list query %q", r.URL.RawQuery)
			}
			if code != 0 {
				writeJSON(t, w, map[string]any{"code": code, "msg": "Lack of necessary permissions"})
				return
			}
			if pages != nil {
				idx := 0
				if tok := q.Get("page_token"); tok != "" {
					idx, _ = strconv.Atoi(tok)
				}
				next := strconv.Itoa(idx + 1)
				writeJSON(t, w, map[string]any{"code": 0, "msg": "success", "data": map[string]any{
					"has_more": idx+1 < len(pages), "page_token": next, "items": pages[idx]}})
				return
			}
			writeJSON(t, w, map[string]any{"code": 0, "msg": "success", "data": map[string]any{"has_more": false, "items": items}})
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
		case strings.HasPrefix(r.URL.Path, "/open-apis/im/v1/messages/"):
			msgID := strings.TrimPrefix(r.URL.Path, "/open-apis/im/v1/messages/")
			s.mu.Lock()
			s.singleGets = append(s.singleGets, msgID)
			item, ok := s.single[msgID]
			s.mu.Unlock()
			if !ok {
				writeJSON(t, w, map[string]any{"code": 230011, "msg": "message not found"})
				return
			}
			writeJSON(t, w, map[string]any{"code": 0, "msg": "success", "data": map[string]any{"items": []map[string]any{item}}})
		default:
			writeJSON(t, w, map[string]any{"code": 0, "msg": "success"})
		}
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *threadFilesServer) platform(handler func(core.Platform, *core.Message)) *Platform {
	return &Platform{
		platformName:      "feishu",
		domain:            s.srv.URL,
		appID:             tfAppID,
		appSecret:         "secret",
		botOpenID:         tfBot,
		threadIsolation:   true,
		attachmentWait:    attachmentWaitMention,
		attachmentWaitTTL: time.Hour,
		dedup:             &core.MessageDedup{},
		handler:           handler,
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
	sendGroupReply(t, p, msgID, sender, text, ms, mentionBot, tfRoot)
}

// sendGroupReply is sendGroupText for a message that replies to parentID.
func sendGroupReply(t *testing.T, p *Platform, msgID, sender, text string, ms int64, mentionBot bool, parentID string) {
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
				MessageId: strPtr(msgID), RootId: strPtr(tfRoot), ParentId: strPtr(parentID), ThreadId: &threadID,
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
	if !strings.Contains(first.ExtraContent, "not permitted") || !strings.Contains(first.ExtraContent, "230027") {
		t.Fatalf("ExtraContent = %q, want a short notice with the denied code", first.ExtraContent)
	}
	// Scope setup is for the operator (logged), not for the chat.
	if strings.Contains(first.ExtraContent, "im:message.group_msg") || strings.Contains(first.ExtraContent, "Lack of necessary permissions") {
		t.Fatalf("ExtraContent = %q, want no operator setup details or raw API text", first.ExtraContent)
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

// Regression for the review of #2305: in a thread that is already engaged the
// quote path is not taken, so a reply to the sender's own file must get that
// file through the backfill rather than having it excluded as "the parent".
func TestThreadBackfillEngagedThreadReplyToOwnFile(t *testing.T) {
	// Timestamps move forward from now: core drops messages older than startup.
	now := time.Now().UnixMilli() + 3000
	s := newThreadFilesServer(t, []map[string]any{
		threadItem("om_first", "text", tfAlice, `{"text":"@_user_1 先看日志"}`, now-3000, true),
	})
	s.single = map[string]map[string]any{
		"om_file": threadItem("om_file", "file", tfAlice, `{"file_key":"file_k","file_name":"report.dbc"}`, now-1000, false),
	}
	got := make(chan *core.Message, 4)
	p := s.platform(func(_ core.Platform, msg *core.Message) { got <- msg })

	// Engage the thread: one accepted @bot message, no files yet.
	sendGroupText(t, p, "om_first", tfAlice, "@_user_1 先看日志", now-3000, true)
	if msg := waitMessage(t, got); msg.MessageID != "om_first" || len(msg.Files) != 0 {
		t.Fatalf("engaging message = %q with %d files", msg.MessageID, len(msg.Files))
	}
	if !p.isActiveThreadSession("feishu:" + tfChat + ":root:" + tfRoot) {
		t.Fatal("thread not engaged after the first @bot message")
	}

	// Alice uploads a file, then replies to that file message with @bot.
	s.setItems([]map[string]any{
		threadItem(tfTrigger, "text", tfAlice, `{"text":"@_user_1 analyse"}`, now, true),
		threadItem("om_file", "file", tfAlice, `{"file_key":"file_k","file_name":"report.dbc"}`, now-1000, false),
		threadItem("om_first", "text", tfAlice, `{"text":"@_user_1 先看日志"}`, now-3000, true),
	})
	sendGroupReply(t, p, tfTrigger, tfAlice, "@_user_1 analyse", now, true, "om_file")
	msg := waitMessage(t, got)
	if msg.MessageID != tfTrigger || msg.Content != "analyse" {
		t.Fatalf("dispatched %q with Content %q", msg.MessageID, msg.Content)
	}
	if len(msg.Files) != 1 || msg.Files[0].FileName != "report.dbc" {
		t.Fatalf("Files = %+v, want the replied-to report.dbc", msg.Files)
	}
	if string(msg.Files[0].Data) != "VERSION \"\"\nBODY om_file" {
		t.Fatalf("report.dbc bytes = %q", msg.Files[0].Data)
	}
	if len(msg.Images) != 0 {
		t.Fatalf("Images = %d, want 0", len(msg.Images))
	}
	s.mu.Lock()
	singleGets, downloads := append([]string(nil), s.singleGets...), append([]string(nil), s.downloads...)
	s.mu.Unlock()
	for _, id := range singleGets {
		if id == "om_file" {
			t.Fatalf("quote path fetched %s; in an engaged thread the file must come from the backfill", id)
		}
	}
	if strings.Join(downloads, ",") != "om_file" {
		t.Fatalf("downloads = %v, want om_file exactly once", downloads)
	}
}

// When the quote path does forward the parent (first message in the thread),
// the backfill must not attach the same file a second time.
func TestThreadBackfillBootstrapReplyToOwnFileNotDuplicated(t *testing.T) {
	now := time.Now().UnixMilli()
	fileItem := threadItem("om_file", "file", tfAlice, `{"file_key":"file_k","file_name":"report.dbc"}`, now-1000, false)
	s := newThreadFilesServer(t, []map[string]any{
		threadItem(tfTrigger, "text", tfAlice, `{"text":"@_user_1 analyse"}`, now, true),
		fileItem,
	})
	s.single = map[string]map[string]any{"om_file": fileItem}
	got := make(chan *core.Message, 2)
	p := s.platform(func(_ core.Platform, msg *core.Message) { got <- msg })

	sendGroupReply(t, p, tfTrigger, tfAlice, "@_user_1 analyse", now, true, "om_file")
	msg := waitMessage(t, got)
	if len(msg.Files) != 1 || msg.Files[0].FileName != "report.dbc" {
		t.Fatalf("Files = %+v, want report.dbc once", msg.Files)
	}
	if _, downloads := s.stats(); strings.Join(downloads, ",") != "om_file" {
		t.Fatalf("downloads = %v, want om_file exactly once", downloads)
	}
}

func TestThreadBackfillReportsTruncation(t *testing.T) {
	now := time.Now().UnixMilli()

	t.Run("attachment cap", func(t *testing.T) {
		items := []map[string]any{threadItem(tfTrigger, "text", tfAlice, `{"text":"@_user_1 analyse"}`, now, true)}
		for i := 1; i <= threadBackfillMaxAttachments+1; i++ {
			id := "om_f" + strconv.Itoa(i)
			items = append(items, threadItem(id, "file", tfAlice, `{"file_key":"k`+strconv.Itoa(i)+`","file_name":"f`+strconv.Itoa(i)+`.log"}`, now-int64(i)*1000, false))
		}
		s := newThreadFilesServer(t, items)
		got := make(chan *core.Message, 1)
		p := s.platform(func(_ core.Platform, msg *core.Message) { got <- msg })
		sendGroupText(t, p, tfTrigger, tfAlice, "@_user_1 analyse", now, true)
		msg := waitMessage(t, got)
		if len(msg.Files) != threadBackfillMaxAttachments || msg.Files[len(msg.Files)-1].FileName != "f1.log" {
			t.Fatalf("Files = %d (last %q), want the %d newest ending with f1.log", len(msg.Files), msg.Files[len(msg.Files)-1].FileName, threadBackfillMaxAttachments)
		}
		if !strings.Contains(msg.ExtraContent, "most recent") {
			t.Fatalf("ExtraContent = %q, want a truncation notice", msg.ExtraContent)
		}
	})

	t.Run("page cap", func(t *testing.T) {
		var pages [][]map[string]any
		for pg := 0; pg < threadBackfillMaxPages+1; pg++ {
			id := "om_p" + strconv.Itoa(pg)
			pages = append(pages, []map[string]any{threadItem(id, "text", tfBob, `{"text":"chatter"}`, now-int64(pg+1)*1000, false)})
		}
		pages[0] = append(pages[0], threadItem("om_f", "file", tfAlice, `{"file_key":"k","file_name":"a.log"}`, now-500, false))
		s := newThreadFilesServer(t, nil)
		s.pages = pages
		got := make(chan *core.Message, 1)
		p := s.platform(func(_ core.Platform, msg *core.Message) { got <- msg })
		sendGroupText(t, p, tfTrigger, tfAlice, "@_user_1 analyse", now, true)
		msg := waitMessage(t, got)
		if len(msg.Files) != 1 {
			t.Fatalf("Files = %d, want the one file in the listed pages", len(msg.Files))
		}
		if !strings.Contains(msg.ExtraContent, "latest "+strconv.Itoa(threadBackfillMaxPages*threadBackfillPageSize)+" messages") {
			t.Fatalf("ExtraContent = %q, want a page-cap notice", msg.ExtraContent)
		}
		if calls, _ := s.stats(); calls != threadBackfillMaxPages {
			t.Fatalf("list calls = %d, want %d", calls, threadBackfillMaxPages)
		}
	})

	t.Run("complete window", func(t *testing.T) {
		s := newThreadFilesServer(t, []map[string]any{
			threadItem("om_f", "file", tfAlice, `{"file_key":"k","file_name":"a.log"}`, now-500, false),
		})
		got := make(chan *core.Message, 1)
		p := s.platform(func(_ core.Platform, msg *core.Message) { got <- msg })
		sendGroupText(t, p, tfTrigger, tfAlice, "@_user_1 analyse", now, true)
		if msg := waitMessage(t, got); msg.ExtraContent != "" {
			t.Fatalf("ExtraContent = %q, want no notice", msg.ExtraContent)
		}
	})
}

func TestDeniedThreadsTTLAndBound(t *testing.T) {
	var d deniedThreads
	now := time.Now()
	if !d.firstNotice("a", now) || d.firstNotice("a", now.Add(time.Hour)) {
		t.Fatal("want one notice within the TTL")
	}
	if !d.firstNotice("a", now.Add(threadBackfillDeniedTTL)) {
		t.Fatal("want a new notice after the TTL")
	}
	for i := 0; i < threadBackfillDeniedMax+10; i++ {
		d.firstNotice("t"+strconv.Itoa(i), now.Add(time.Duration(i)*time.Second))
	}
	if n := len(d.at); n > threadBackfillDeniedMax {
		t.Fatalf("remembered %d threads, want at most %d", n, threadBackfillDeniedMax)
	}
}

func TestIsSlashText(t *testing.T) {
	for text, want := range map[string]bool{
		"@_user_1 /why 看下日志":        true,
		"/review":                   true,
		"/etc/nginx/nginx.conf 报错了": false,
		"@_user_1 /tmp/a.log 看下":    false,
		"看看 /why":                   false,
		"":                          false,
	} {
		if got := isSlashText(text); got != want {
			t.Errorf("isSlashText(%q) = %v, want %v", text, got, want)
		}
	}
}

func TestThreadBackfillDisabledWhenAttachmentWaitOff(t *testing.T) {
	now := time.Now().UnixMilli()
	s := newThreadFilesServer(t, []map[string]any{
		threadItem("om_f1", "file", tfAlice, `{"file_key":"file_k1","file_name":"a.dbc"}`, now-1000, false),
	})
	got := make(chan *core.Message, 2)
	p := s.platform(func(_ core.Platform, msg *core.Message) { got <- msg })
	p.attachmentWait = attachmentWaitOff

	sendGroupText(t, p, tfTrigger, tfAlice, "@_user_1 analyse", now, true)
	msg := waitMessage(t, got)
	if len(msg.Files) != 0 {
		t.Fatalf("Files = %+v, want none when attachment_wait = off", msg.Files)
	}
	if calls, _ := s.stats(); calls != 0 {
		t.Fatalf("list calls = %d, want 0 when attachment_wait = off", calls)
	}
}

func TestThreadBackfillNewCommandClosesWindow(t *testing.T) {
	now := time.Now().UnixMilli()
	s := newThreadFilesServer(t, []map[string]any{
		threadItem(tfTrigger, "text", tfAlice, `{"text":"@_user_1 analyse"}`, now, true),
		threadItem("om_f_after", "file", tfAlice, `{"file_key":"file_k2","file_name":"after.log"}`, now-1000, false),
		threadItem("om_new", "text", tfAlice, `{"text":"@_user_1 /new"}`, now-2000, true),
		threadItem("om_f_before", "file", tfAlice, `{"file_key":"file_k1","file_name":"before.log"}`, now-3000, false),
	})
	got := make(chan *core.Message, 2)
	p := s.platform(func(_ core.Platform, msg *core.Message) { got <- msg })
	p.activeThreadSessions.Store("feishu:"+tfChat+":root:"+tfRoot, time.Now())

	sendGroupText(t, p, tfTrigger, tfAlice, "@_user_1 analyse", now, true)
	msg := waitMessage(t, got)
	if len(msg.Files) != 1 || msg.Files[0].FileName != "after.log" {
		t.Fatalf("Files = %+v, want only after.log (/new must close the backfill window)", msg.Files)
	}
}
