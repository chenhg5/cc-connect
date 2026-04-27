package max

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func TestSplitMessage(t *testing.T) {
	cases := []struct {
		name     string
		in       string
		limit    int
		chunks   int
		firstLen int // expected len of first chunk (0 = skip)
	}{
		{"short stays whole", "hello", 10, 1, 5},
		{"exact limit stays whole", "abcdefghij", 10, 1, 10},
		{"over limit splits", strings.Repeat("a", 25), 10, 3, 10},
		{"newline aware over threshold", strings.Repeat("a", 150) + "\n" + strings.Repeat("b", 150), 200, 2, 150},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := splitMessage(c.in, c.limit)
			if len(got) != c.chunks {
				t.Fatalf("chunks: got %d, want %d (%q)", len(got), c.chunks, got)
			}
			if c.firstLen > 0 && len(got[0]) != c.firstLen {
				t.Errorf("first chunk len: got %d, want %d (%q)", len(got[0]), c.firstLen, got[0])
			}
			joined := strings.ReplaceAll(strings.Join(got, ""), "", "")
			if strings.ReplaceAll(joined, "\n", "") != strings.ReplaceAll(c.in, "\n", "") {
				t.Errorf("joined chunks lost data: %q vs %q", joined, c.in)
			}
		})
	}
}

func TestSniffImageMime(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
		want string
	}{
		{"png", []byte{0x89, 'P', 'N', 'G', 0, 0, 0, 0}, "image/png"},
		{"jpeg", []byte{0xFF, 0xD8, 0, 0}, "image/jpeg"},
		{"gif", []byte("GIF89a"), "image/gif"},
		{"webp", []byte("RIFF\x00\x00\x00\x00WEBP....."), "image/webp"},
		{"unknown", []byte{0, 1, 2, 3, 4, 5, 6, 7}, "application/octet-stream"},
		{"empty", []byte{}, "application/octet-stream"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := sniffImageMime(c.in); got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestIsAttachmentNotReady(t *testing.T) {
	cases := map[string]bool{
		`{"code":"attachment.not.ready","message":"retry"}`: true,
		`{"error":"not.ready"}`:                             true,
		`{"code":"rate.limit"}`:                             false,
		`{"ok":true}`:                                       false,
		``:                                                  false,
	}
	for body, want := range cases {
		if got := isAttachmentNotReady([]byte(body)); got != want {
			t.Errorf("isAttachmentNotReady(%q) = %v, want %v", body, got, want)
		}
	}
}

func TestDefaultFilename(t *testing.T) {
	cases := map[string]string{
		"image": "image.png",
		"video": "video.mp4",
		"audio": "audio.mp3",
		"file":  "file.bin",
		"xyz":   "file.bin",
	}
	for in, want := range cases {
		if got := defaultFilename(in); got != want {
			t.Errorf("defaultFilename(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestReconstructReplyCtx(t *testing.T) {
	p := &Platform{}
	cases := []struct {
		key     string
		chatID  string
		wantErr bool
	}{
		{"max:12345", "12345", false},
		{"max:12345:99", "12345", false},
		{"telegram:12345", "", true},
		{"max:", "", true},
		{"", "", true},
	}
	for _, c := range cases {
		t.Run(c.key, func(t *testing.T) {
			got, err := p.ReconstructReplyCtx(c.key)
			if (err != nil) != c.wantErr {
				t.Fatalf("err=%v, wantErr=%v", err, c.wantErr)
			}
			if err != nil {
				return
			}
			rc, ok := got.(replyContext)
			if !ok {
				t.Fatalf("wrong type %T", got)
			}
			if rc.chatID != c.chatID {
				t.Errorf("chatID=%q, want %q", rc.chatID, c.chatID)
			}
		})
	}
}

// --- Integration tests against a mock MAX API ---

type mockAPI struct {
	server       *httptest.Server
	cdnServer    *httptest.Server
	messageCalls int32
	uploadCalls  int32
	cdnCalls     int32
	editCalls    int32

	// capture last POST /messages body for inspection
	mu           sync.Mutex
	lastMsgBody  maxSendBody
	lastMsgQuery string
	lastEditBody maxSendBody
	lastEditMID  string

	// attachmentReadyAfter: return attachment.not.ready this many times before 200
	attachmentReadyAfter int32
}

func newMockAPI(t *testing.T) *mockAPI {
	t.Helper()
	m := &mockAPI{}
	m.cdnServer = httptest.NewServer(http.HandlerFunc(m.handleCDN))
	mux := http.NewServeMux()
	mux.HandleFunc("/me", m.handleMe)
	mux.HandleFunc("/updates", m.handleUpdates)
	mux.HandleFunc("/messages", m.handleMessages)
	mux.HandleFunc("/uploads", m.handleUploads)
	mux.HandleFunc("/audios/", m.handleMediaResolve)
	mux.HandleFunc("/videos/", m.handleMediaResolve)
	mux.HandleFunc("/blob/", m.handleBlob)
	m.server = httptest.NewServer(mux)
	return m
}

// handleMediaResolve replies with a JSON pointing to our own /blob/<token>
// endpoint, letting tests simulate the MAX /audios/{token} → URL → download
// round-trip without depending on a real CDN.
func (m *mockAPI) handleMediaResolve(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimPrefix(r.URL.Path, "/audios/")
	token = strings.TrimPrefix(token, "/videos/")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"url":      m.server.URL + "/blob/" + token,
		"filename": "voice.ogg",
	})
}

func (m *mockAPI) handleBlob(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "audio/ogg")
	_, _ = w.Write([]byte("OggS\x00\x00fake-audio-bytes"))
}

func (m *mockAPI) close() {
	m.server.Close()
	m.cdnServer.Close()
}

func (m *mockAPI) handleMe(w http.ResponseWriter, _ *http.Request) {
	_ = json.NewEncoder(w).Encode(map[string]any{"name": "test-bot", "user_id": 42})
}

func (m *mockAPI) handleUpdates(w http.ResponseWriter, r *http.Request) {
	<-r.Context().Done() // block until caller cancels
	w.WriteHeader(http.StatusNoContent)
}

func (m *mockAPI) handleMessages(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		atomic.AddInt32(&m.messageCalls, 1)
		var body maxSendBody
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		// attachment.not.ready simulation
		remaining := atomic.LoadInt32(&m.attachmentReadyAfter)
		if remaining > 0 {
			atomic.AddInt32(&m.attachmentReadyAfter, -1)
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"code":"attachment.not.ready"}`))
			return
		}
		m.mu.Lock()
		m.lastMsgBody = body
		m.lastMsgQuery = r.URL.RawQuery
		m.mu.Unlock()
		_, _ = w.Write([]byte(`{"message_id":"test-mid"}`))
	case http.MethodPut:
		atomic.AddInt32(&m.editCalls, 1)
		var body maxSendBody
		_ = json.NewDecoder(r.Body).Decode(&body)
		m.mu.Lock()
		m.lastEditBody = body
		m.lastEditMID = r.URL.Query().Get("message_id")
		m.mu.Unlock()
		_, _ = w.Write([]byte(`{"ok":true}`))
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (m *mockAPI) handleUploads(w http.ResponseWriter, r *http.Request) {
	atomic.AddInt32(&m.uploadCalls, 1)
	kind := r.URL.Query().Get("type")
	if kind == "" {
		http.Error(w, "missing type", http.StatusBadRequest)
		return
	}
	resp := map[string]any{"url": m.cdnServer.URL + "/upload?kind=" + kind}
	// video/audio carry the real token in the /uploads response itself
	if kind == "video" || kind == "audio" {
		resp["token"] = "urltok-" + kind
	}
	_ = json.NewEncoder(w).Encode(resp)
}

// handleCDN mimics per-kind MAX CDN response shapes:
//   image: {"photos": {"<id>": {"token": "..."}}}
//   file:  {"token": "..."}
//   video/audio: XML "<retval>1</retval>" (token comes from /uploads instead)
func (m *mockAPI) handleCDN(w http.ResponseWriter, r *http.Request) {
	atomic.AddInt32(&m.cdnCalls, 1)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	f, _, err := r.FormFile("data")
	if err != nil {
		http.Error(w, "missing data field: "+err.Error(), http.StatusBadRequest)
		return
	}
	defer f.Close()
	if _, err := io.Copy(io.Discard, f); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	kind := r.URL.Query().Get("kind")
	switch kind {
	case "image":
		_ = json.NewEncoder(w).Encode(map[string]any{
			"photos": map[string]any{
				"photo-id-1": map[string]any{"token": "cdntok-image"},
			},
		})
	case "video", "audio":
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte("<retval>1</retval>"))
	default:
		_ = json.NewEncoder(w).Encode(map[string]any{"token": "cdntok-" + kind})
	}
}

func newTestPlatform(t *testing.T, apiBase string) *Platform {
	t.Helper()
	p, err := New(map[string]any{
		"token":    "test-token",
		"api_base": apiBase,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return p.(*Platform)
}

func TestSendText(t *testing.T) {
	m := newMockAPI(t)
	defer m.close()
	p := newTestPlatform(t, m.server.URL)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := p.Send(ctx, replyContext{chatID: "111"}, "hello world"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if atomic.LoadInt32(&m.messageCalls) != 1 {
		t.Fatalf("want 1 message call, got %d", m.messageCalls)
	}
	m.mu.Lock()
	body := m.lastMsgBody
	query := m.lastMsgQuery
	m.mu.Unlock()
	if body.Text != "hello world" {
		t.Errorf("text: got %q", body.Text)
	}
	if body.Format != "markdown" {
		t.Errorf("format: got %q", body.Format)
	}
	if !strings.Contains(query, "chat_id=111") {
		t.Errorf("chat_id missing from query: %q", query)
	}
}

func TestSendTextSplitsLong(t *testing.T) {
	m := newMockAPI(t)
	defer m.close()
	p := newTestPlatform(t, m.server.URL)

	long := strings.Repeat("a", 8500)
	ctx := context.Background()
	if err := p.Send(ctx, replyContext{chatID: "111"}, long); err != nil {
		t.Fatalf("Send: %v", err)
	}
	// 8500 / 4000 = 3 chunks
	if got := atomic.LoadInt32(&m.messageCalls); got != 3 {
		t.Errorf("want 3 chunk messages, got %d", got)
	}
}

func TestSendWithButtons(t *testing.T) {
	m := newMockAPI(t)
	defer m.close()
	p := newTestPlatform(t, m.server.URL)

	ctx := context.Background()
	buttons := [][]core.ButtonOption{{{Text: "OK", Data: "ok"}, {Text: "Cancel", Data: "cancel"}}}
	if err := p.SendWithButtons(ctx, replyContext{chatID: "111"}, "pick", buttons); err != nil {
		t.Fatalf("SendWithButtons: %v", err)
	}
	m.mu.Lock()
	body := m.lastMsgBody
	m.mu.Unlock()
	if len(body.Attachments) != 1 {
		t.Fatalf("want 1 attachment, got %d", len(body.Attachments))
	}
	if body.Attachments[0].Type != "inline_keyboard" {
		t.Errorf("attachment type: got %q", body.Attachments[0].Type)
	}
}

func TestSendImage(t *testing.T) {
	m := newMockAPI(t)
	defer m.close()
	p := newTestPlatform(t, m.server.URL)

	ctx := context.Background()
	img := core.ImageAttachment{MimeType: "image/png", Data: []byte{0x89, 'P', 'N', 'G', 0, 0, 0, 0}, FileName: "chart.png"}
	if err := p.SendImage(ctx, replyContext{chatID: "111"}, img); err != nil {
		t.Fatalf("SendImage: %v", err)
	}
	if got := atomic.LoadInt32(&m.uploadCalls); got != 1 {
		t.Errorf("uploads: got %d", got)
	}
	if got := atomic.LoadInt32(&m.cdnCalls); got != 1 {
		t.Errorf("cdn: got %d", got)
	}
	m.mu.Lock()
	body := m.lastMsgBody
	m.mu.Unlock()
	if len(body.Attachments) != 1 || body.Attachments[0].Type != "image" {
		t.Fatalf("want image attachment, got %+v", body.Attachments)
	}
}

func TestSendFileRoutesImageByMime(t *testing.T) {
	m := newMockAPI(t)
	defer m.close()
	p := newTestPlatform(t, m.server.URL)

	ctx := context.Background()
	file := core.FileAttachment{MimeType: "image/jpeg", Data: []byte{0xFF, 0xD8, 0, 0}, FileName: "photo.jpg"}
	if err := p.SendFile(ctx, replyContext{chatID: "111"}, file); err != nil {
		t.Fatalf("SendFile: %v", err)
	}
	m.mu.Lock()
	body := m.lastMsgBody
	m.mu.Unlock()
	if len(body.Attachments) != 1 || body.Attachments[0].Type != "image" {
		t.Fatalf("image/* mime should route to type=image, got %+v", body.Attachments)
	}
}

func TestSendFileGeneric(t *testing.T) {
	m := newMockAPI(t)
	defer m.close()
	p := newTestPlatform(t, m.server.URL)

	ctx := context.Background()
	file := core.FileAttachment{MimeType: "application/pdf", Data: []byte("%PDF-1.4"), FileName: "report.pdf"}
	if err := p.SendFile(ctx, replyContext{chatID: "111"}, file); err != nil {
		t.Fatalf("SendFile: %v", err)
	}
	m.mu.Lock()
	body := m.lastMsgBody
	m.mu.Unlock()
	if len(body.Attachments) != 1 || body.Attachments[0].Type != "file" {
		t.Fatalf("pdf should be type=file, got %+v", body.Attachments)
	}
}

func TestAttachmentNotReadyRetry(t *testing.T) {
	m := newMockAPI(t)
	defer m.close()
	// First two POST /messages return attachment.not.ready, third succeeds
	atomic.StoreInt32(&m.attachmentReadyAfter, 2)

	p := newTestPlatform(t, m.server.URL)

	ctx := context.Background()
	img := core.ImageAttachment{MimeType: "image/png", Data: []byte{0x89, 'P', 'N', 'G', 0, 0, 0, 0}}
	if err := p.SendImage(ctx, replyContext{chatID: "111"}, img); err != nil {
		t.Fatalf("SendImage: %v", err)
	}
	// 1 upload + 1 cdn + 3 message attempts
	if got := atomic.LoadInt32(&m.messageCalls); got != 3 {
		t.Errorf("message attempts: got %d, want 3", got)
	}
}

func TestUpdateMessage(t *testing.T) {
	m := newMockAPI(t)
	defer m.close()
	p := newTestPlatform(t, m.server.URL)

	ctx := context.Background()
	if err := p.UpdateMessage(ctx, replyContext{chatID: "111", messageID: "mid-42"}, "edited"); err != nil {
		t.Fatalf("UpdateMessage: %v", err)
	}
	if got := atomic.LoadInt32(&m.editCalls); got != 1 {
		t.Errorf("edit calls: got %d, want 1", got)
	}
	m.mu.Lock()
	body := m.lastEditBody
	mid := m.lastEditMID
	m.mu.Unlock()
	if body.Text != "edited" {
		t.Errorf("edit text: got %q", body.Text)
	}
	if mid != "mid-42" {
		t.Errorf("edit mid: got %q", mid)
	}
}

func TestUpdateMessageWithoutMID(t *testing.T) {
	m := newMockAPI(t)
	defer m.close()
	p := newTestPlatform(t, m.server.URL)

	err := p.UpdateMessage(context.Background(), replyContext{chatID: "111"}, "noop")
	if err == nil {
		t.Fatal("expected error when messageID is empty")
	}
	if !strings.Contains(err.Error(), "message id") {
		t.Errorf("error should mention missing message id: %v", err)
	}
}

func TestNewRequiresToken(t *testing.T) {
	_, err := New(map[string]any{})
	if err == nil {
		t.Fatal("expected error when token missing")
	}
}

func TestPollLoopStopsOnCtxCancel(t *testing.T) {
	m := newMockAPI(t)
	defer m.close()
	p := newTestPlatform(t, m.server.URL)

	handlerCalled := false
	err := p.Start(func(_ core.Platform, _ *core.Message) { handlerCalled = true })
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	// give the loop a moment to hit /updates
	time.Sleep(100 * time.Millisecond)
	if err := p.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if handlerCalled {
		t.Error("handler should not be called for empty /updates")
	}
}

// sanity: make sure the /uploads handler sees the expected type query param
func TestUploadKindPropagation(t *testing.T) {
	var seenKinds []string
	var mu sync.Mutex

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seenKinds = append(seenKinds, r.URL.Query().Get("type"))
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"url": r.URL.Scheme + "://" + r.Host + "/invalid"})
	}))
	defer server.Close()

	p := newTestPlatform(t, server.URL)
	// The CDN request will fail, but we only care about the /uploads kind param.
	_, _ = p.uploadAttachment(context.Background(), "audio", []byte("x"), "a.mp3")
	mu.Lock()
	got := append([]string(nil), seenKinds...)
	mu.Unlock()
	if len(got) != 1 || got[0] != "audio" {
		t.Errorf("uploads kinds: got %v", got)
	}
}

func TestAudioFormatFromMime(t *testing.T) {
	cases := []struct{ mime, filename, want string }{
		{"audio/ogg", "", "ogg"},
		{"audio/mpeg", "", "mp3"},
		{"audio/mp4", "", "m4a"},
		{"audio/x-m4a", "", "m4a"},
		{"audio/wav", "", "wav"},
		{"audio/webm", "voice.webm", "webm"},
		{"", "clip.m4a", "m4a"},
		{"", "weird_no_ext", "ogg"},
		{"application/octet-stream", "", "octet-stream"},
	}
	for _, c := range cases {
		if got := audioFormatFromMime(c.mime, c.filename); got != c.want {
			t.Errorf("audioFormatFromMime(%q,%q) = %q, want %q", c.mime, c.filename, got, c.want)
		}
	}
}

func TestFetchAttachmentsRoutesAudio(t *testing.T) {
	m := newMockAPI(t)
	defer m.close()
	p := newTestPlatform(t, m.server.URL)

	atts := []maxAttachmentRaw{
		{Type: "audio", Payload: maxAttachmentPayld{Token: "vm-abc"}},
	}
	images, files, audio := p.fetchAttachments(context.Background(), atts)
	if len(images) != 0 || len(files) != 0 {
		t.Errorf("audio must not leak into images/files (img=%d, f=%d)", len(images), len(files))
	}
	if audio == nil {
		t.Fatal("audio attachment missing")
	}
	if audio.Format != "ogg" {
		t.Errorf("format: got %q, want ogg", audio.Format)
	}
	if len(audio.Data) == 0 {
		t.Error("audio data is empty")
	}
}

func TestFetchAttachmentsFileWithAudioMimeRoutesToAudio(t *testing.T) {
	// MAX delivers audio files attached via the paperclip menu as type="file"
	// with audio/* mime. Ensure those also route to Audio so transcription kicks in.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "audio/mpeg")
		_, _ = w.Write([]byte("fake-mp3-data"))
	}))
	defer server.Close()

	p := newTestPlatform(t, "http://unused")
	atts := []maxAttachmentRaw{
		{Type: "file", Filename: "song.mp3", Payload: maxAttachmentPayld{URL: server.URL + "/song.mp3"}},
	}
	_, files, audio := p.fetchAttachments(context.Background(), atts)
	if len(files) != 0 {
		t.Errorf("audio/* mime should not be in files, got %+v", files)
	}
	if audio == nil {
		t.Fatal("audio attachment missing")
	}
	if audio.Format != "mp3" {
		t.Errorf("format: got %q, want mp3", audio.Format)
	}
}

func TestHandleMessageDedupsByID(t *testing.T) {
	p := newTestPlatform(t, "http://unused")

	delivered := 0
	_ = p.Start(func(_ core.Platform, _ *core.Message) { delivered++ })
	defer func() { _ = p.Stop() }()

	msg := &maxMessage{
		Sender:    maxUser{UserID: 1, Name: "u"},
		Recipient: maxRecipient{ChatID: 42},
		Timestamp: time.Now().UnixMilli(),
		Body:      maxBody{Mid: "mid-1", Text: "hi"},
	}
	ctx := context.Background()
	p.handleMessage(ctx, msg)
	p.handleMessage(ctx, msg) // duplicate mid → should be dropped
	if delivered != 1 {
		t.Errorf("handler fired %d times, want 1 (dedup failed)", delivered)
	}
}

func TestSendAudio(t *testing.T) {
	m := newMockAPI(t)
	defer m.close()
	p := newTestPlatform(t, m.server.URL)

	ctx := context.Background()
	if err := p.SendAudio(ctx, replyContext{chatID: "111"}, []byte("fake-audio"), "mp3"); err != nil {
		t.Fatalf("SendAudio: %v", err)
	}
	if got := atomic.LoadInt32(&m.uploadCalls); got != 1 {
		t.Errorf("uploads: got %d, want 1", got)
	}
	if got := atomic.LoadInt32(&m.cdnCalls); got != 1 {
		t.Errorf("cdn: got %d, want 1", got)
	}
	m.mu.Lock()
	body := m.lastMsgBody
	m.mu.Unlock()
	if len(body.Attachments) != 1 || body.Attachments[0].Type != "audio" {
		t.Fatalf("want audio attachment, got %+v", body.Attachments)
	}
}

