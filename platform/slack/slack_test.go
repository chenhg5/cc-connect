package slack

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/chenhg5/cc-connect/core"
	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"
)

func TestStripAppMentionText(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "strips bot mention prefix",
			in:   "<@U0BOT123> run tests",
			want: "run tests",
		},
		{
			name: "empty mention becomes empty text",
			in:   "<@U0BOT123> ",
			want: "",
		},
		{
			name: "plain text remains unchanged",
			in:   "run tests",
			want: "run tests",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := stripAppMentionText(tt.in); got != tt.want {
				t.Fatalf("stripAppMentionText(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestConvertBangPrefix(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		want     string
		wantConv bool
	}{
		{name: "simple command", input: "!commit", want: "/commit", wantConv: true},
		{name: "cc gateway", input: "!cc help", want: "/cc help", wantConv: true},
		{name: "with args", input: "!compact --summary", want: "/compact --summary", wantConv: true},
		{name: "space after bang unchanged", input: "! something", want: "! something", wantConv: false},
		{name: "bare bang unchanged", input: "!", want: "!", wantConv: false},
		{name: "plain text unchanged", input: "hello", want: "hello", wantConv: false},
		{name: "empty unchanged", input: "", want: "", wantConv: false},
		{name: "already slash unchanged", input: "/already-slash", want: "/already-slash", wantConv: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, conv := convertBangPrefix(tt.input)
			if got != tt.want || conv != tt.wantConv {
				t.Fatalf("convertBangPrefix(%q) = (%q, %v), want (%q, %v)", tt.input, got, conv, tt.want, tt.wantConv)
			}
		})
	}
}

func TestReplyUsesResponseURLWhenPresent(t *testing.T) {
	var gotMethod string
	var gotPayload map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		if err := json.NewDecoder(r.Body).Decode(&gotPayload); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer ts.Close()

	p := &Platform{}
	err := p.Reply(context.Background(), replyContext{
		channel:  "C123",
		response: ts.URL,
	}, "hello from slash command")
	if err != nil {
		t.Fatalf("Reply: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Fatalf("method = %q, want POST", gotMethod)
	}
	if gotPayload["text"] != "hello from slash command" {
		t.Fatalf("payload text = %#v", gotPayload["text"])
	}
	if gotPayload["response_type"] != "in_channel" {
		t.Fatalf("payload response_type = %#v", gotPayload["response_type"])
	}
}

func TestHandleSlashCommandStoresResponseURL(t *testing.T) {
	p := &Platform{}
	var gotCtx replyContext
	var gotContent string
	p.handler = func(_ core.Platform, msg *core.Message) {
		rc, ok := msg.ReplyCtx.(replyContext)
		if !ok {
			t.Fatalf("ReplyCtx type = %T", msg.ReplyCtx)
		}
		gotCtx = rc
		gotContent = msg.Content
	}

	p.handleEvent(socketmode.Event{
		Type: socketmode.EventTypeSlashCommand,
		Data: slack.SlashCommand{
			Command:     "/cc",
			Text:        "help",
			UserID:      "U123",
			UserName:    "leigh",
			ChannelID:   "C123",
			ResponseURL: "https://example.test/response",
		},
	})

	if gotContent != "/cc help" {
		t.Fatalf("content = %q, want %q", gotContent, "/cc help")
	}
	if gotCtx.channel != "C123" {
		t.Fatalf("channel = %q, want C123", gotCtx.channel)
	}
	if gotCtx.response != "https://example.test/response" {
		t.Fatalf("response URL = %q", gotCtx.response)
	}
}

func TestDownloadSlackFile_HTMLDetection(t *testing.T) {
	// Test that we detect HTML responses (Slack login page) and return an error
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Simulate Slack returning HTML login page when auth is missing
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("<!DOCTYPE html><html><body>Please login</body></html>"))
	}))
	defer ts.Close()

	p := &Platform{botToken: "xoxb-test-token"}
	_, err := p.downloadSlackFile(ts.URL)
	if err == nil {
		t.Fatal("expected error for HTML response, got nil")
	}
	// Should detect HTML prefix
	if err != nil && err.Error() == "" {
		t.Fatal("expected non-empty error message")
	}
}

func TestDownloadSlackFile_MissingAuth(t *testing.T) {
	// Test that we return an error for non-200 status codes
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte("unauthorized"))
	}))
	defer ts.Close()

	p := &Platform{botToken: "xoxb-test-token"}
	_, err := p.downloadSlackFile(ts.URL)
	if err == nil {
		t.Fatal("expected error for 401 response, got nil")
	}
}

func TestDownloadSlackFile_Success(t *testing.T) {
	// Test successful binary download
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify Authorization header is set
		auth := r.Header.Get("Authorization")
		if auth != "Bearer xoxb-test-token" {
			t.Errorf("expected Authorization header 'Bearer xoxb-test-token', got %q", auth)
		}
		w.Header().Set("Content-Type", "image/png")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("\x89PNG\r\n\x1a\n")) // PNG magic bytes
	}))
	defer ts.Close()

	p := &Platform{botToken: "xoxb-test-token"}
	data, err := p.downloadSlackFile(ts.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(data) != 8 {
		t.Errorf("expected 8 bytes, got %d", len(data))
	}
}

func TestDownloadSlackFile_EmptyURL(t *testing.T) {
	p := &Platform{botToken: "xoxb-test-token"}
	_, err := p.downloadSlackFile("")
	if err == nil {
		t.Fatal("expected error for empty URL, got nil")
	}
}

func TestParseSlackInnerEventFiles(t *testing.T) {
	raw := json.RawMessage(`{"type":"app_mention","user":"U1","text":"<@B> hi","files":[{"id":"F1","name":"a.pdf","mimetype":"application/pdf","url_private_download":"http://example/f"}]}`)
	files := parseSlackInnerEventFiles(&raw)
	if len(files) != 1 {
		t.Fatalf("len(files) = %d, want 1", len(files))
	}
	if files[0].Name != "a.pdf" || files[0].Mimetype != "application/pdf" {
		t.Fatalf("unexpected file: %+v", files[0])
	}
}

func TestProcessSlackFileShares_GenericFile(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("%PDF-1.4 minimal"))
	}))
	defer ts.Close()

	p := &Platform{botToken: "xoxb-test"}
	images, audio, docs := p.processSlackFileShares([]slackevents.File{
		{
			ID:                 "Fpdf",
			Name:               "doc.pdf",
			Mimetype:           "application/pdf",
			URLPrivateDownload: ts.URL,
		},
	})
	if len(images) != 0 || audio != nil {
		t.Fatalf("expected only doc file, got images=%d audio=%v", len(images), audio)
	}
	if len(docs) != 1 {
		t.Fatalf("len(docs) = %d, want 1", len(docs))
	}
	if docs[0].FileName != "doc.pdf" || docs[0].MimeType != "application/pdf" {
		t.Fatalf("unexpected doc: %+v", docs[0])
	}
	if string(docs[0].Data) != "%PDF-1.4 minimal" {
		t.Fatalf("unexpected data %q", docs[0].Data)
	}
}

func TestProcessSlackFileShares_ImageVsDoc(t *testing.T) {
	imgSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("fakepng"))
	}))
	defer imgSrv.Close()
	txtSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("hello"))
	}))
	defer txtSrv.Close()

	p := &Platform{botToken: "xoxb-test"}
	images, audio, docs := p.processSlackFileShares([]slackevents.File{
		{ID: "1", Name: "x.png", Mimetype: "image/png", URLPrivateDownload: imgSrv.URL},
		{ID: "2", Name: "n.txt", Mimetype: "text/plain", URLPrivateDownload: txtSrv.URL},
	})
	if audio != nil {
		t.Fatal("unexpected audio")
	}
	if len(images) != 1 || len(docs) != 1 {
		t.Fatalf("want 1 image 1 doc, got images=%d docs=%d", len(images), len(docs))
	}
	if images[0].MimeType != "image/png" {
		t.Errorf("image mime: %q", images[0].MimeType)
	}
	if docs[0].MimeType != "text/plain" || string(docs[0].Data) != "hello" {
		t.Errorf("unexpected text file: %+v", docs[0])
	}
}

func TestProcessSlackFileShares_EmptyMimeBecomesOctetStream(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte{0, 1, 2})
	}))
	defer ts.Close()

	p := &Platform{botToken: "xoxb-test"}
	_, _, docs := p.processSlackFileShares([]slackevents.File{
		{ID: "z", Name: "blob.bin", Mimetype: "", URLPrivateDownload: ts.URL},
	})
	if len(docs) != 1 || docs[0].MimeType != "application/octet-stream" {
		t.Fatalf("got %+v", docs)
	}
}
