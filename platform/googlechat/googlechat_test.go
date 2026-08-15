package googlechat

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

// testServiceAccountJSON is a syntactically valid service-account JSON whose
// private_key is intentionally invalid. JWTConfigFromJSON accepts it (key
// parsing is lazy), so New() succeeds — useful for testing config paths beyond
// credential loading.
const testServiceAccountJSON = `{"type":"service_account","project_id":"p","private_key_id":"k","private_key":"not-a-key","client_email":"x@p.iam.gserviceaccount.com","token_uri":"https://oauth2.googleapis.com/token"}`

// newTestPlatform builds a Platform directly so tests can exercise parsing and
// routing without a Pub/Sub client or service-account client.
func newTestPlatform(allowFrom, scope string) *Platform {
	return &Platform{allowFrom: allowFrom, sessionScope: scope}
}

// wrapEvent renders a Chat-app event as the Pub/Sub message payload the Chat app
// publishes: {"type":<evType>,"message":<msg>}.
func wrapEvent(t *testing.T, evType string, msg map[string]any) []byte {
	t.Helper()
	data, err := json.Marshal(map[string]any{"type": evType, "message": msg})
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	return data
}

// messageEvent wraps msg as a MESSAGE event line.
func messageEvent(t *testing.T, msg map[string]any) []byte {
	return wrapEvent(t, "MESSAGE", msg)
}

func humanMessage(text, argumentText string) map[string]any {
	return map[string]any{
		"name":         "spaces/AAA/messages/MMM",
		"text":         text,
		"argumentText": argumentText,
		"sender":       map[string]any{"name": "users/123", "displayName": "Alice", "type": "HUMAN"},
		"space":        map[string]any{"name": "spaces/AAA"},
		"thread":       map[string]any{"name": "spaces/AAA/threads/TTT"},
	}
}

func TestParseEvent_MentionMode(t *testing.T) {
	p := newTestPlatform("*", "space")
	line := messageEvent(t, humanMessage("@Claude summarize this", "summarize this"))

	msg, ok := p.parseEvent(line)
	if !ok {
		t.Fatal("expected message to be handled")
	}
	if msg.Content != "summarize this" {
		t.Errorf("Content = %q, want stripped argumentText", msg.Content)
	}
	if msg.UserID != "users/123" || msg.UserName != "Alice" {
		t.Errorf("sender = %q/%q", msg.UserID, msg.UserName)
	}
	if msg.SessionKey != "googlechat:spaces/AAA" {
		t.Errorf("SessionKey = %q", msg.SessionKey)
	}
	rc, ok := msg.ReplyCtx.(replyContext)
	if !ok || rc.space != "spaces/AAA" || rc.thread != "spaces/AAA/threads/TTT" {
		t.Errorf("ReplyCtx = %+v", msg.ReplyCtx)
	}
}

func TestParseEvent_MentionModeFallsBackToText(t *testing.T) {
	p := newTestPlatform("*", "space")
	msg, ok := p.parseEvent(messageEvent(t, humanMessage("hello there", "")))
	if !ok {
		t.Fatal("expected message to be handled")
	}
	if msg.Content != "hello there" {
		t.Errorf("Content = %q, want full text fallback", msg.Content)
	}
}

func TestParseEvent_SkipsNonHuman(t *testing.T) {
	p := newTestPlatform("*", "space")
	data := humanMessage("hi", "hi")
	data["sender"] = map[string]any{"name": "users/bot", "type": "BOT"}

	if _, ok := p.parseEvent(messageEvent(t, data)); ok {
		t.Error("expected non-human sender to be ignored")
	}
}

func TestParseEvent_IgnoresNonMessageType(t *testing.T) {
	p := newTestPlatform("*", "space")
	if _, ok := p.parseEvent(wrapEvent(t, "ADDED_TO_SPACE", humanMessage("hi", "hi"))); ok {
		t.Error("expected non-MESSAGE event to be ignored")
	}
}

func TestParseEvent_AllowFromEnforced(t *testing.T) {
	p := newTestPlatform("users/999", "space")
	if _, ok := p.parseEvent(messageEvent(t, humanMessage("hi", "hi"))); ok {
		t.Error("expected unauthorized sender to be ignored")
	}

	p2 := newTestPlatform("users/123", "space")
	if _, ok := p2.parseEvent(messageEvent(t, humanMessage("hi", "hi"))); !ok {
		t.Error("expected authorized sender to be handled")
	}
}

func TestParseEvent_DropsOldMessage(t *testing.T) {
	p := newTestPlatform("*", "space")
	data := humanMessage("hi", "hi")
	data["createTime"] = "2000-01-01T00:00:00Z"
	if _, ok := p.parseEvent(messageEvent(t, data)); ok {
		t.Error("expected message predating startup to be ignored")
	}
}

// fakeAckMsg is a test double for *pubsub.Message that records Ack/Nack calls
// and supports optional callbacks so tests can track call ordering.
type fakeAckMsg struct {
	acked  bool
	nacked bool
	ackFn  func()
	nackFn func()
}

func (f *fakeAckMsg) Ack() {
	f.acked = true
	if f.ackFn != nil {
		f.ackFn()
	}
}

func (f *fakeAckMsg) Nack() {
	f.nacked = true
	if f.nackFn != nil {
		f.nackFn()
	}
}

func TestDispatchMessage_AcksAfterHandler(t *testing.T) {
	p := newTestPlatform("*", "space")
	data := messageEvent(t, humanMessage("hello", "hello"))

	var events []string
	m := &fakeAckMsg{ackFn: func() { events = append(events, "ack") }}
	p.handler = func(_ core.Platform, _ *core.Message) {
		events = append(events, "handler")
	}
	p.dispatchMessage(m, data)

	want := []string{"handler", "ack"}
	if len(events) != len(want) || events[0] != want[0] || events[1] != want[1] {
		t.Errorf("event order: got %v, want %v", events, want)
	}
	if m.nacked {
		t.Error("Nack should not be called on success")
	}
}

func TestDispatchMessage_NacksOnPanic(t *testing.T) {
	p := newTestPlatform("*", "space")
	data := messageEvent(t, humanMessage("hello", "hello"))

	m := &fakeAckMsg{}
	p.handler = func(_ core.Platform, _ *core.Message) {
		panic("simulated handler panic")
	}
	p.dispatchMessage(m, data)

	if m.acked {
		t.Error("Ack should not be called when handler panics")
	}
	if !m.nacked {
		t.Error("Nack should be called when handler panics")
	}
}

func TestDispatchMessage_AcksOnParseFailure(t *testing.T) {
	p := newTestPlatform("*", "space")

	handlerCalled := false
	m := &fakeAckMsg{}
	p.handler = func(_ core.Platform, _ *core.Message) { handlerCalled = true }
	p.dispatchMessage(m, []byte("not-json"))

	if !m.acked {
		t.Error("Ack should be called for unparseable messages")
	}
	if m.nacked {
		t.Error("Nack should not be called for unparseable messages")
	}
	if handlerCalled {
		t.Error("handler should not be called for unparseable messages")
	}
}

func TestBuildSessionKey(t *testing.T) {
	cases := []struct {
		scope, space, user, thread, want string
	}{
		{"space", "spaces/A", "users/1", "spaces/A/threads/T", "googlechat:spaces/A"},
		{"thread", "spaces/A", "users/1", "spaces/A/threads/T", "googlechat:spaces/A:t:spaces/A/threads/T"},
		{"thread", "spaces/A", "users/1", "", "googlechat:spaces/A"},
		{"user", "spaces/A", "users/1", "spaces/A/threads/T", "googlechat:spaces/A:users/1"},
	}
	for _, c := range cases {
		p := newTestPlatform("*", c.scope)
		if got := p.buildSessionKey(c.space, c.user, c.thread); got != c.want {
			t.Errorf("scope=%s buildSessionKey = %q, want %q", c.scope, got, c.want)
		}
	}
}

func TestReconstructReplyCtx(t *testing.T) {
	p := newTestPlatform("*", "space")
	cases := []struct {
		key           string
		space, thread string
	}{
		{"googlechat:spaces/A", "spaces/A", ""},
		{"googlechat:spaces/A:t:spaces/A/threads/T", "spaces/A", "spaces/A/threads/T"},
		{"googlechat:spaces/A:users/1", "spaces/A", ""},
	}
	for _, c := range cases {
		got, err := p.ReconstructReplyCtx(c.key)
		if err != nil {
			t.Errorf("key=%s: %v", c.key, err)
			continue
		}
		rc := got.(replyContext)
		if rc.space != c.space || rc.thread != c.thread {
			t.Errorf("key=%s reconstructed = %+v, want space=%q thread=%q", c.key, rc, c.space, c.thread)
		}
	}

	if _, err := p.ReconstructReplyCtx("slack:foo"); err == nil {
		t.Error("expected error for non-googlechat key")
	}
}

func TestBuildAttachmentRequest(t *testing.T) {
	// Threaded reply: URL carries reply option, body carries thread + attachment.
	u, body, err := buildAttachmentRequest(replyContext{space: "spaces/A", thread: "spaces/A/threads/T"}, "ref/123")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(u, chatAPIBase+"spaces/A/messages") {
		t.Errorf("url = %q, want prefix %q", u, chatAPIBase+"spaces/A/messages")
	}
	parsed, _ := url.Parse(u)
	if parsed.Query().Get("messageReplyOption") != "REPLY_MESSAGE_FALLBACK_TO_NEW_THREAD" {
		t.Errorf("url = %q, want messageReplyOption query", u)
	}
	var b map[string]any
	if err := json.Unmarshal(body, &b); err != nil {
		t.Fatal(err)
	}
	attachments, _ := b["attachment"].([]any)
	if len(attachments) != 1 {
		t.Fatalf("body attachment count = %d, want 1", len(attachments))
	}
	att, _ := attachments[0].(map[string]any)
	ref, _ := att["attachmentDataRef"].(map[string]any)
	if ref["resourceName"] != "ref/123" {
		t.Errorf("attachmentDataRef.resourceName = %v, want ref/123", ref["resourceName"])
	}
	thread, _ := b["thread"].(map[string]any)
	if thread["name"] != "spaces/A/threads/T" {
		t.Errorf("body thread = %+v", b["thread"])
	}

	// No thread: no reply option, no thread in body.
	u2, body2, err := buildAttachmentRequest(replyContext{space: "spaces/A"}, "ref/456")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(u2, "messageReplyOption") {
		t.Errorf("unexpected messageReplyOption for top-level: %q", u2)
	}
	var b2 map[string]any
	if err := json.Unmarshal(body2, &b2); err != nil {
		t.Fatal(err)
	}
	if _, ok := b2["thread"]; ok {
		t.Errorf("unexpected thread for top-level reply: %+v", b2)
	}
	attachments2, _ := b2["attachment"].([]any)
	if len(attachments2) != 1 {
		t.Fatalf("body attachment count = %d, want 1", len(attachments2))
	}
	att2, _ := attachments2[0].(map[string]any)
	ref2, _ := att2["attachmentDataRef"].(map[string]any)
	if ref2["resourceName"] != "ref/456" {
		t.Errorf("attachmentDataRef.resourceName = %v, want ref/456", ref2["resourceName"])
	}
}

func TestFormattingInstructions(t *testing.T) {
	p := newTestPlatform("*", "space")
	s := p.FormattingInstructions()
	if s == "" {
		t.Fatal("FormattingInstructions() returned empty string")
	}
	for _, want := range []string{"*bold*", "_italic_", "##", "[text](url)", ">text", "|display text"} {
		if !strings.Contains(s, want) {
			t.Errorf("FormattingInstructions() missing expected substring %q", want)
		}
	}
}

func TestBuildSendRequest(t *testing.T) {
	// Threaded reply: URL carries the reply option, body carries the thread.
	u, body, err := buildSendRequest(replyContext{space: "spaces/A", thread: "spaces/A/threads/T"}, "hi")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(u, chatAPIBase+"spaces/A/messages") {
		t.Errorf("url = %q, want prefix %q", u, chatAPIBase+"spaces/A/messages")
	}
	parsed, _ := url.Parse(u)
	if parsed.Query().Get("messageReplyOption") != "REPLY_MESSAGE_FALLBACK_TO_NEW_THREAD" {
		t.Errorf("url = %q, want messageReplyOption query", u)
	}
	var b map[string]any
	if err := json.Unmarshal(body, &b); err != nil {
		t.Fatal(err)
	}
	if b["text"] != "hi" {
		t.Errorf("body text = %v", b["text"])
	}
	thread, _ := b["thread"].(map[string]any)
	if thread["name"] != "spaces/A/threads/T" {
		t.Errorf("body thread = %+v", b["thread"])
	}

	// No thread: no reply option, no thread in body.
	u2, body2, err := buildSendRequest(replyContext{space: "spaces/A"}, "hi")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(u2, "messageReplyOption") {
		t.Errorf("unexpected messageReplyOption for top-level reply: %q", u2)
	}
	var b2 map[string]any
	if err := json.Unmarshal(body2, &b2); err != nil {
		t.Fatal(err)
	}
	if _, ok := b2["thread"]; ok {
		t.Errorf("unexpected thread for top-level reply: %+v", b2)
	}
}

func TestBuildUpdateRequest(t *testing.T) {
	msgName := "spaces/AAA/messages/MMM"
	u, body, err := buildUpdateRequest(msgName, "updated text")
	if err != nil {
		t.Fatal(err)
	}

	// URL must point at the message resource and carry updateMask=text.
	wantPrefix := chatAPIBase + msgName
	if !strings.HasPrefix(u, wantPrefix) {
		t.Errorf("url = %q, want prefix %q", u, wantPrefix)
	}
	parsed, _ := url.Parse(u)
	if parsed.Query().Get("updateMask") != "text" {
		t.Errorf("url = %q, want updateMask=text query param", u)
	}

	// Body must carry the updated text.
	var b map[string]any
	if err := json.Unmarshal(body, &b); err != nil {
		t.Fatal(err)
	}
	if b["text"] != "updated text" {
		t.Errorf("body text = %v, want %q", b["text"], "updated text")
	}
	// No thread or reply-option fields.
	if _, ok := b["thread"]; ok {
		t.Errorf("unexpected thread field in update body: %+v", b)
	}
}

// testAttachmentServer creates an httptest.Server that handles both the upload
// endpoint and the create-message endpoint. uploadFn and msgFn are called for
// the respective requests; pass nil to use a default no-op that returns 200.
func testAttachmentServer(t *testing.T, uploadFn, msgFn http.HandlerFunc) (p *Platform, restore func()) {
	t.Helper()
	if uploadFn == nil {
		uploadFn = func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			if _, err := fmt.Fprintln(w, `{"attachmentDataRef":{"resourceName":"ref/default"}}`); err != nil {
				t.Fatalf("write default upload response: %v", err)
			}
		}
	}
	if msgFn == nil {
		msgFn = func(w http.ResponseWriter, r *http.Request) {
			if _, err := fmt.Fprintln(w, "{}"); err != nil {
				t.Fatalf("write default message response: %v", err)
			}
		}
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "attachments") {
			uploadFn(w, r)
		} else {
			msgFn(w, r)
		}
	}))
	origUpload, origAPI := chatUploadBase, chatAPIBase
	chatUploadBase = ts.URL + "/upload/"
	chatAPIBase = ts.URL + "/api/"
	return &Platform{botClient: &http.Client{}}, func() {
		ts.Close()
		chatUploadBase, chatAPIBase = origUpload, origAPI
	}
}

func TestUploadAttachment_Success(t *testing.T) {
	p, restore := testAttachmentServer(t, nil, nil)
	defer restore()
	name, err := p.uploadAttachment(context.Background(), "spaces/X", "f.txt", "text/plain", []byte("hello"))
	if err != nil {
		t.Fatal(err)
	}
	if name != "ref/default" {
		t.Errorf("resourceName = %q, want ref/default", name)
	}
}

func TestUploadAttachment_HTTPError(t *testing.T) {
	p, restore := testAttachmentServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		if _, err := fmt.Fprintln(w, "access denied"); err != nil {
			t.Fatalf("write forbidden response: %v", err)
		}
	}, nil)
	defer restore()
	_, err := p.uploadAttachment(context.Background(), "spaces/X", "f.txt", "text/plain", []byte("x"))
	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Errorf("expected HTTP 403 error, got %v", err)
	}
}

func TestUploadAttachment_EmptyResourceName(t *testing.T) {
	p, restore := testAttachmentServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if _, err := fmt.Fprintln(w, `{"attachmentDataRef":{"resourceName":""}}`); err != nil {
			t.Fatalf("write empty resourceName response: %v", err)
		}
	}, nil)
	defer restore()
	_, err := p.uploadAttachment(context.Background(), "spaces/X", "f.txt", "text/plain", []byte("x"))
	if err == nil || !strings.Contains(err.Error(), "empty resourceName") {
		t.Errorf("expected empty resourceName error, got %v", err)
	}
}

func TestPostAttachment_MissingSpace(t *testing.T) {
	p := &Platform{botClient: &http.Client{}}
	err := p.postAttachment(context.Background(), replyContext{}, "f.txt", "text/plain", []byte("x"))
	if err == nil || !strings.Contains(err.Error(), "missing space") {
		t.Errorf("expected missing space error, got %v", err)
	}
}

func TestPostAttachment_TwoStep(t *testing.T) {
	var gotCreateBody []byte
	p, restore := testAttachmentServer(t,
		func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			if _, err := fmt.Fprintln(w, `{"attachmentDataRef":{"resourceName":"ref/xyz"}}`); err != nil {
				t.Fatalf("write upload response: %v", err)
			}
		},
		func(w http.ResponseWriter, r *http.Request) {
			gotCreateBody, _ = io.ReadAll(r.Body)
			if _, err := fmt.Fprintln(w, "{}"); err != nil {
				t.Fatalf("write create response: %v", err)
			}
		},
	)
	defer restore()

	rc := replyContext{space: "spaces/A", thread: "spaces/A/threads/T"}
	if err := p.postAttachment(context.Background(), rc, "doc.pdf", "application/pdf", []byte("pdf")); err != nil {
		t.Fatal(err)
	}

	var b map[string]any
	if err := json.Unmarshal(gotCreateBody, &b); err != nil {
		t.Fatal(err)
	}
	thread, _ := b["thread"].(map[string]any)
	if thread["name"] != "spaces/A/threads/T" {
		t.Errorf("thread.name = %v, want spaces/A/threads/T", thread["name"])
	}
	attachments, _ := b["attachment"].([]any)
	if len(attachments) != 1 {
		t.Fatalf("attachment count = %d, want 1", len(attachments))
	}
	att, _ := attachments[0].(map[string]any)
	ref, _ := att["attachmentDataRef"].(map[string]any)
	if ref["resourceName"] != "ref/xyz" {
		t.Errorf("attachmentDataRef.resourceName = %v, want ref/xyz", ref["resourceName"])
	}
}

// parseUploadParts parses the multipart/related upload body and returns the
// filename from the metadata part and the MIME type from the media part.
func parseUploadParts(t *testing.T, r *http.Request) (filename, mimeType string) {
	t.Helper()
	ct := r.Header.Get("Content-Type")
	_, params, err := mime.ParseMediaType(ct)
	if err != nil {
		t.Fatalf("parse Content-Type %q: %v", ct, err)
	}
	mr := multipart.NewReader(r.Body, params["boundary"])
	for {
		part, err := mr.NextPart()
		if err != nil {
			break
		}
		partCT := part.Header.Get("Content-Type")
		if strings.HasPrefix(partCT, "application/json") {
			var meta map[string]string
			json.NewDecoder(part).Decode(&meta) //nolint:errcheck
			filename = meta["filename"]
		} else {
			mimeType = partCT
		}
	}
	return
}

func TestSendImage_Defaults(t *testing.T) {
	var gotFilename, gotMIME string
	p, restore := testAttachmentServer(t,
		func(w http.ResponseWriter, r *http.Request) {
			gotFilename, gotMIME = parseUploadParts(t, r)
			w.Header().Set("Content-Type", "application/json")
			if _, err := fmt.Fprintln(w, `{"attachmentDataRef":{"resourceName":"ref/1"}}`); err != nil {
				t.Fatalf("write upload response: %v", err)
			}
		},
		nil,
	)
	defer restore()

	err := p.SendImage(context.Background(), replyContext{space: "spaces/A"}, core.ImageAttachment{Data: []byte("img")})
	if err != nil {
		t.Fatal(err)
	}
	if gotFilename != "image.png" {
		t.Errorf("filename = %q, want image.png", gotFilename)
	}
	if gotMIME != "image/png" {
		t.Errorf("MIME = %q, want image/png", gotMIME)
	}
}

func TestSendFile_Defaults(t *testing.T) {
	var gotFilename, gotMIME string
	p, restore := testAttachmentServer(t,
		func(w http.ResponseWriter, r *http.Request) {
			gotFilename, gotMIME = parseUploadParts(t, r)
			w.Header().Set("Content-Type", "application/json")
			if _, err := fmt.Fprintln(w, `{"attachmentDataRef":{"resourceName":"ref/2"}}`); err != nil {
				t.Fatalf("write upload response: %v", err)
			}
		},
		nil,
	)
	defer restore()

	err := p.SendFile(context.Background(), replyContext{space: "spaces/A"}, core.FileAttachment{Data: []byte("bin")})
	if err != nil {
		t.Fatal(err)
	}
	if gotFilename != "attachment" {
		t.Errorf("filename = %q, want attachment", gotFilename)
	}
	if gotMIME != "application/octet-stream" {
		t.Errorf("MIME = %q, want application/octet-stream", gotMIME)
	}
}

func TestNew_MissingSubscription(t *testing.T) {
	for _, opts := range []map[string]any{
		{},
		{"subscription": ""},
		{"subscription": "   "},
	} {
		_, err := New(opts)
		if err == nil || !strings.Contains(err.Error(), "subscription is required") {
			t.Errorf("opts %v: want subscription required error, got %v", opts, err)
		}
	}
}

func TestNew_MalformedSubscription(t *testing.T) {
	_, err := New(map[string]any{"subscription": "bad/format"})
	if err == nil {
		t.Error("expected error for malformed subscription")
	}
}

func TestNew_MissingCredentialsFile(t *testing.T) {
	for _, opts := range []map[string]any{
		{"subscription": "projects/p/subscriptions/s"},
		{"subscription": "projects/p/subscriptions/s", "credentials_file": ""},
		{"subscription": "projects/p/subscriptions/s", "credentials_file": "   "},
	} {
		_, err := New(opts)
		if err == nil || !strings.Contains(err.Error(), "credentials_file is required") {
			t.Errorf("opts %v: want credentials_file required error, got %v", opts, err)
		}
	}
}

func TestNew_UnreadableCredentialsFile(t *testing.T) {
	_, err := New(map[string]any{
		"subscription":     "projects/p/subscriptions/s",
		"credentials_file": filepath.Join(t.TempDir(), "nonexistent.json"),
	})
	if err == nil {
		t.Error("expected error for non-existent credentials file")
	}
}

func TestNew_InvalidCredentialsFile(t *testing.T) {
	f := filepath.Join(t.TempDir(), "key.json")
	if err := os.WriteFile(f, []byte("not valid json"), 0o600); err != nil {
		t.Fatalf("write test file: %v", err)
	}
	_, err := New(map[string]any{
		"subscription":     "projects/p/subscriptions/s",
		"credentials_file": f,
	})
	if err == nil || !strings.Contains(err.Error(), "parse service account credentials") {
		t.Errorf("want parse credentials error, got %v", err)
	}
}

func TestNew_UnknownSessionScope(t *testing.T) {
	f := filepath.Join(t.TempDir(), "key.json")
	if err := os.WriteFile(f, []byte(testServiceAccountJSON), 0o600); err != nil {
		t.Fatalf("write test file: %v", err)
	}
	p, err := New(map[string]any{
		"subscription":     "projects/p/subscriptions/s",
		"credentials_file": f,
		"session_scope":    "invalid_scope",
	})
	if err != nil {
		t.Fatalf("New() with unknown session_scope should succeed, got: %v", err)
	}
	if got := p.(*Platform).sessionScope; got != "space" {
		t.Errorf("sessionScope = %q, want space", got)
	}
}
