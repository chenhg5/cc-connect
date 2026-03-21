package telegram

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

func TestExtractEntityText(t *testing.T) {
	tests := []struct {
		name   string
		text   string
		offset int
		length int
		want   string
	}{
		{
			name:   "ASCII only",
			text:   "hello @bot world",
			offset: 6,
			length: 4,
			want:   "@bot",
		},
		{
			name:   "Chinese before mention",
			text:   "你好 @mybot 你好",
			offset: 3,
			length: 6,
			want:   "@mybot",
		},
		{
			// 👍 is U+1F44D = surrogate pair (2 UTF-16 code units)
			// "Hi " = 3, "👍" = 2, " " = 1 → @mybot starts at UTF-16 offset 6
			name:   "emoji before mention (surrogate pair)",
			text:   "Hi 👍 @mybot test",
			offset: 6,
			length: 6,
			want:   "@mybot",
		},
		{
			name:   "multiple emoji before mention",
			text:   "🎉🎊 @testbot",
			offset: 5,
			length: 8,
			want:   "@testbot",
		},
		{
			name:   "out of range returns empty",
			text:   "short",
			offset: 10,
			length: 5,
			want:   "",
		},
		{
			name:   "negative offset returns empty",
			text:   "hello",
			offset: -1,
			length: 3,
			want:   "",
		},
		{
			name:   "negative length returns empty",
			text:   "hello",
			offset: 0,
			length: -1,
			want:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractEntityText(tt.text, tt.offset, tt.length)
			if got != tt.want {
				t.Errorf("extractEntityText(%q, %d, %d) = %q, want %q",
					tt.text, tt.offset, tt.length, got, tt.want)
			}
		})
	}
}

func TestSendAudioMP3PrefersVoice(t *testing.T) {
	var paths []string
	p := newTelegramTestPlatform(t, func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		fmt.Fprint(w, `{"ok":true,"result":{"message_id":1}}`)
	})

	if err := p.SendAudio(context.Background(), replyContext{chatID: 123}, []byte("mp3-data"), "mp3"); err != nil {
		t.Fatalf("SendAudio returned error: %v", err)
	}

	if len(paths) != 1 {
		t.Fatalf("request count = %d, want 1", len(paths))
	}
	if !strings.HasSuffix(paths[0], "/sendVoice") {
		t.Fatalf("path = %q, want sendVoice", paths[0])
	}
}

func TestSendAudioWAVConvertsToVoice(t *testing.T) {
	orig := telegramConvertAudioToOpus
	t.Cleanup(func() { telegramConvertAudioToOpus = orig })

	var (
		paths      []string
		converted  bool
		gotFormat  string
		gotPayload []byte
	)
	telegramConvertAudioToOpus = func(_ context.Context, audio []byte, format string) ([]byte, error) {
		converted = true
		gotFormat = format
		gotPayload = append([]byte(nil), audio...)
		return []byte("converted-opus"), nil
	}

	p := newTelegramTestPlatform(t, func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		fmt.Fprint(w, `{"ok":true,"result":{"message_id":1}}`)
	})

	if err := p.SendAudio(context.Background(), replyContext{chatID: 123}, []byte("wav-data"), "wav"); err != nil {
		t.Fatalf("SendAudio returned error: %v", err)
	}

	if !converted {
		t.Fatal("expected wav input to be converted before sendVoice")
	}
	if gotFormat != "wav" {
		t.Fatalf("converter format = %q, want wav", gotFormat)
	}
	if string(gotPayload) != "wav-data" {
		t.Fatalf("converter payload = %q, want wav-data", gotPayload)
	}
	if len(paths) != 1 || !strings.HasSuffix(paths[0], "/sendVoice") {
		t.Fatalf("paths = %v, want only sendVoice", paths)
	}
}

func TestSendAudioFallsBackToSendAudioForMP3(t *testing.T) {
	var paths []string
	p := newTelegramTestPlatform(t, func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if strings.HasSuffix(r.URL.Path, "/sendVoice") {
			fmt.Fprint(w, `{"ok":false,"error_code":400,"description":"voice rejected"}`)
			return
		}
		fmt.Fprint(w, `{"ok":true,"result":{"message_id":1}}`)
	})

	if err := p.SendAudio(context.Background(), replyContext{chatID: 123}, []byte("mp3-data"), "mp3"); err != nil {
		t.Fatalf("SendAudio returned error: %v", err)
	}

	if len(paths) != 2 {
		t.Fatalf("request count = %d, want 2", len(paths))
	}
	if !strings.HasSuffix(paths[0], "/sendVoice") || !strings.HasSuffix(paths[1], "/sendAudio") {
		t.Fatalf("paths = %v, want sendVoice then sendAudio", paths)
	}
}

func TestSendAudioRejectsInvalidReplyContext(t *testing.T) {
	p := &Platform{}

	err := p.SendAudio(context.Background(), "bad-context", []byte("data"), "mp3")
	if err == nil {
		t.Fatal("expected error for invalid reply context")
	}
	if !strings.Contains(err.Error(), "telegram: SendAudio: invalid reply context type") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSendAudioReturnsConversionErrorForWAV(t *testing.T) {
	orig := telegramConvertAudioToOpus
	t.Cleanup(func() { telegramConvertAudioToOpus = orig })

	telegramConvertAudioToOpus = func(_ context.Context, _ []byte, _ string) ([]byte, error) {
		return nil, errors.New("mock conversion failure")
	}

	p := &Platform{bot: &tgbotapi.BotAPI{}}
	err := p.SendAudio(context.Background(), replyContext{chatID: 123}, []byte("wav-data"), "wav")
	if err == nil {
		t.Fatal("expected conversion error")
	}
	if !strings.Contains(err.Error(), "telegram: SendAudio: convert wav to opus") {
		t.Fatalf("unexpected error prefix: %v", err)
	}
	if !strings.Contains(err.Error(), "mock conversion failure") {
		t.Fatalf("expected wrapped conversion error, got: %v", err)
	}
}

func newTelegramTestPlatform(t *testing.T, handler func(http.ResponseWriter, *http.Request)) *Platform {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/getMe") {
			fmt.Fprint(w, `{"ok":true,"result":{"id":1,"is_bot":true,"first_name":"Test","username":"testbot"}}`)
			return
		}
		handler(w, r)
	}))
	t.Cleanup(server.Close)

	bot, err := tgbotapi.NewBotAPIWithClient("TEST_TOKEN", server.URL+"/bot%s/%s", server.Client())
	if err != nil {
		t.Fatalf("NewBotAPIWithClient returned error: %v", err)
	}

	return &Platform{bot: bot}
}
