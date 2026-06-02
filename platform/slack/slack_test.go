package slack

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/slack-go/slack/slackevents"
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

func TestStripSlackBotMentionText(t *testing.T) {
	tests := []struct {
		name      string
		text      string
		botUserID string
		want      string
	}{
		{
			name:      "strips bot mention",
			text:      "hey <@U0BOT123> run tests",
			botUserID: "U0BOT123",
			want:      "hey  run tests",
		},
		{
			name:      "strips bot nick mention",
			text:      "<@!U0BOT123> run tests",
			botUserID: "U0BOT123",
			want:      "run tests",
		},
		{
			name:      "keeps other user mention",
			text:      "ask <@U0OTHER> about it",
			botUserID: "U0BOT123",
			want:      "ask <@U0OTHER> about it",
		},
		{
			name:      "empty bot id keeps text",
			text:      "<@U0BOT123> run tests",
			botUserID: "",
			want:      "<@U0BOT123> run tests",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := stripSlackBotMentionText(tt.text, tt.botUserID); got != tt.want {
				t.Fatalf("stripSlackBotMentionText() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRememberSlackEventDedupsChannelTimestamp(t *testing.T) {
	p := &Platform{}
	if !p.rememberSlackEvent("C123", "1710000000.000001") {
		t.Fatal("first event should be remembered")
	}
	if p.rememberSlackEvent("C123", "1710000000.000001") {
		t.Fatal("duplicate event should be rejected")
	}
	if !p.rememberSlackEvent("C123", "1710000000.000002") {
		t.Fatal("different timestamp should be accepted")
	}
	if !p.rememberSlackEvent("G123", "1710000000.000001") {
		t.Fatal("same timestamp in different channel should be accepted")
	}
}

func TestShouldHandleSlackMessageEventMentionPolicy(t *testing.T) {
	tests := []struct {
		name   string
		policy slackMentionPolicy
		ev     *slackevents.MessageEvent
		want   bool
	}{
		{
			name:   "dm message is handled with default policy",
			policy: defaultSlackMentionPolicy(),
			ev:     &slackevents.MessageEvent{ChannelType: "im", Channel: "D123", Text: "hello"},
			want:   true,
		},
		{
			name:   "assistant dm thread is handled with default policy",
			policy: defaultSlackMentionPolicy(),
			ev:     &slackevents.MessageEvent{ChannelType: "im", Channel: "D123", ThreadTimeStamp: "1710000000.000001", Text: "hello"},
			want:   true,
		},
		{
			name:   "channel message is ignored by default",
			policy: defaultSlackMentionPolicy(),
			ev:     &slackevents.MessageEvent{ChannelType: "channel", Channel: "C123", Text: "hello"},
			want:   false,
		},
		{
			name:   "channel thread reply is ignored by default",
			policy: defaultSlackMentionPolicy(),
			ev:     &slackevents.MessageEvent{ChannelType: "channel", Channel: "C123", ThreadTimeStamp: "1710000000.000001", Text: "hello"},
			want:   false,
		},
		{
			name:   "private channel message is ignored by default",
			policy: defaultSlackMentionPolicy(),
			ev:     &slackevents.MessageEvent{ChannelType: "group", Channel: "G123", Text: "hello"},
			want:   false,
		},
		{
			name:   "mpim message is ignored by default",
			policy: defaultSlackMentionPolicy(),
			ev:     &slackevents.MessageEvent{ChannelType: "mpim", Channel: "G123", Text: "hello"},
			want:   false,
		},
		{
			name:   "empty channel type falls back to dm channel id",
			policy: defaultSlackMentionPolicy(),
			ev:     &slackevents.MessageEvent{Channel: "D123", Text: "hello"},
			want:   true,
		},
		{
			name:   "empty channel type non-dm channel is ignored by default",
			policy: defaultSlackMentionPolicy(),
			ev:     &slackevents.MessageEvent{Channel: "C123", Text: "hello"},
			want:   false,
		},
		{
			name:   "global no-mention policy handles channel messages",
			policy: slackMentionPolicy{requireMention: false},
			ev:     &slackevents.MessageEvent{ChannelType: "channel", Channel: "C123", Text: "hello"},
			want:   true,
		},
		{
			name: "channel override can allow no-mention messages",
			policy: slackMentionPolicy{
				requireMention:         true,
				requireMentionChannels: map[string]bool{"C123": false},
			},
			ev:   &slackevents.MessageEvent{ChannelType: "channel", Channel: "C123", Text: "hello"},
			want: true,
		},
		{
			name: "channel override can require mention when global allows",
			policy: slackMentionPolicy{
				requireMention:         false,
				requireMentionChannels: map[string]bool{"C123": true},
			},
			ev:   &slackevents.MessageEvent{ChannelType: "channel", Channel: "C123", Text: "hello"},
			want: false,
		},
		{
			name: "thread override can allow no-mention replies",
			policy: slackMentionPolicy{
				requireMention:        true,
				requireMentionThreads: map[string]bool{"C123:1710000000.000001": false},
			},
			ev: &slackevents.MessageEvent{
				ChannelType:     "channel",
				Channel:         "C123",
				ThreadTimeStamp: "1710000000.000001",
				Text:            "hello",
			},
			want: true,
		},
		{
			name: "thread override wins over channel override",
			policy: slackMentionPolicy{
				requireMention:         true,
				requireMentionChannels: map[string]bool{"C123": false},
				requireMentionThreads:  map[string]bool{"C123:1710000000.000001": true},
			},
			ev: &slackevents.MessageEvent{
				ChannelType:     "channel",
				Channel:         "C123",
				ThreadTimeStamp: "1710000000.000001",
				Text:            "hello",
			},
			want: false,
		},
		{
			name: "thread timestamp shorthand is accepted",
			policy: slackMentionPolicy{
				requireMention:        true,
				requireMentionThreads: map[string]bool{"1710000000.000001": false},
			},
			ev: &slackevents.MessageEvent{
				ChannelType:     "group",
				Channel:         "G123",
				ThreadTimeStamp: "1710000000.000001",
				Text:            "hello",
			},
			want: true,
		},
		{
			name:   "nil event is ignored",
			policy: defaultSlackMentionPolicy(),
			ev:     nil,
			want:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldHandleSlackMessageEvent(tt.ev, tt.policy); got != tt.want {
				t.Fatalf("shouldHandleSlackMessageEvent() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestNewSlackMentionPolicyFromOptions(t *testing.T) {
	policy, err := newSlackMentionPolicy(map[string]any{
		"require_mention": false,
		"require_mention_channels": map[string]any{
			"C123": true,
			"G123": false,
		},
		"require_mention_threads": map[string]bool{
			"C123:1710000000.000001": false,
		},
	})
	if err != nil {
		t.Fatalf("newSlackMentionPolicy() error = %v", err)
	}
	if policy.requireMention {
		t.Fatal("requireMention = true, want false")
	}
	if got := policy.requireMentionChannels["C123"]; !got {
		t.Fatalf("channel override C123 = %v, want true", got)
	}
	if got := policy.requireMentionChannels["G123"]; got {
		t.Fatalf("channel override G123 = %v, want false", got)
	}
	if got := policy.requireMentionThreads["C123:1710000000.000001"]; got {
		t.Fatalf("thread override = %v, want false", got)
	}
}

func TestAppMentionReplyTSUsesExistingThread(t *testing.T) {
	tests := []struct {
		name string
		ev   *slackevents.AppMentionEvent
		want string
	}{
		{
			name: "top-level mention replies under mention message",
			ev:   &slackevents.AppMentionEvent{TimeStamp: "1710000000.000001"},
			want: "1710000000.000001",
		},
		{
			name: "thread mention replies in existing thread",
			ev: &slackevents.AppMentionEvent{
				TimeStamp:       "1710000001.000002",
				ThreadTimeStamp: "1710000000.000001",
			},
			want: "1710000000.000001",
		},
		{
			name: "nil mention has no reply timestamp",
			ev:   nil,
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := appMentionReplyTS(tt.ev); got != tt.want {
				t.Fatalf("appMentionReplyTS() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestAssistantOrThreadTSForDirectMessages(t *testing.T) {
	tests := []struct {
		name string
		ev   *slackevents.MessageEvent
		want string
	}{
		{
			name: "dm top-level reply stays top-level",
			ev:   &slackevents.MessageEvent{ChannelType: "im", Channel: "D123", TimeStamp: "1710000000.000001"},
			want: "",
		},
		{
			name: "dm fallback by channel id stays top-level",
			ev:   &slackevents.MessageEvent{Channel: "D123", TimeStamp: "1710000000.000001"},
			want: "",
		},
		{
			name: "assistant thread reply stays in thread",
			ev: &slackevents.MessageEvent{
				ChannelType:     "im",
				Channel:         "D123",
				TimeStamp:       "1710000001.000002",
				ThreadTimeStamp: "1710000000.000001",
			},
			want: "1710000000.000001",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := assistantOrThreadTS(tt.ev); got != tt.want {
				t.Fatalf("assistantOrThreadTS() = %q, want %q", got, tt.want)
			}
		})
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
