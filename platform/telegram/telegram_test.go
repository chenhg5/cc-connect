package telegram

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/chenhg5/cc-connect/core"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

type testLifecycleHandler struct {
	onReady       func(core.Platform)
	onUnavailable func(core.Platform, error)
}

func (h testLifecycleHandler) OnPlatformReady(p core.Platform) {
	if h.onReady != nil {
		h.onReady(p)
	}
}

func (h testLifecycleHandler) OnPlatformUnavailable(p core.Platform, err error) {
	if h.onUnavailable != nil {
		h.onUnavailable(p, err)
	}
}

type stubBackoffTimer struct {
	ch chan time.Time
}

func immediateTimer(time.Duration) backoffTimer {
	ch := make(chan time.Time)
	close(ch)
	return &stubBackoffTimer{ch: ch}
}

func (t *stubBackoffTimer) C() <-chan time.Time {
	return t.ch
}

func (t *stubBackoffTimer) Stop() bool {
	return true
}

type stubTypingTicker struct {
	ch chan time.Time
}

func newStubTypingTicker() *stubTypingTicker {
	return &stubTypingTicker{ch: make(chan time.Time, 8)}
}

func (t *stubTypingTicker) C() <-chan time.Time {
	return t.ch
}

func (t *stubTypingTicker) Stop() {}

type stubTelegramBot struct {
	mu                   sync.Mutex
	sendMessageCalls     int
	sendPhotoCalls       int
	sendDocumentCalls    int
	sendVoiceCalls       int
	sendAudioCalls       int
	sendChatActionCalls  int
	editMessageTextCalls int
	deleteMessageCalls   int
	answerCallbackCalls  int
	setMyCommandsCalls   int
	getFileCalls         int
	setReactionCalls     int

	sendErr    error
	getFileErr error
	file       *models.File
}

func newStubTelegramBot() *stubTelegramBot {
	return &stubTelegramBot{
		file: &models.File{FilePath: "files/test.dat"},
	}
}

func (b *stubTelegramBot) SendMessage(_ context.Context, _ *tgbot.SendMessageParams) (*models.Message, error) {
	b.mu.Lock()
	b.sendMessageCalls++
	b.mu.Unlock()
	if b.sendErr != nil {
		return nil, b.sendErr
	}
	return &models.Message{ID: 99}, nil
}

func (b *stubTelegramBot) SendPhoto(_ context.Context, _ *tgbot.SendPhotoParams) (*models.Message, error) {
	b.mu.Lock()
	b.sendPhotoCalls++
	b.mu.Unlock()
	if b.sendErr != nil {
		return nil, b.sendErr
	}
	return &models.Message{ID: 99}, nil
}

func (b *stubTelegramBot) SendDocument(_ context.Context, _ *tgbot.SendDocumentParams) (*models.Message, error) {
	b.mu.Lock()
	b.sendDocumentCalls++
	b.mu.Unlock()
	if b.sendErr != nil {
		return nil, b.sendErr
	}
	return &models.Message{ID: 99}, nil
}

func (b *stubTelegramBot) SendVoice(_ context.Context, _ *tgbot.SendVoiceParams) (*models.Message, error) {
	b.mu.Lock()
	b.sendVoiceCalls++
	b.mu.Unlock()
	if b.sendErr != nil {
		return nil, b.sendErr
	}
	return &models.Message{ID: 99}, nil
}

func (b *stubTelegramBot) SendAudio(_ context.Context, _ *tgbot.SendAudioParams) (*models.Message, error) {
	b.mu.Lock()
	b.sendAudioCalls++
	b.mu.Unlock()
	if b.sendErr != nil {
		return nil, b.sendErr
	}
	return &models.Message{ID: 99}, nil
}

func (b *stubTelegramBot) SendChatAction(_ context.Context, _ *tgbot.SendChatActionParams) (bool, error) {
	b.mu.Lock()
	b.sendChatActionCalls++
	b.mu.Unlock()
	if b.sendErr != nil {
		return false, b.sendErr
	}
	return true, nil
}

func (b *stubTelegramBot) EditMessageText(_ context.Context, _ *tgbot.EditMessageTextParams) (*models.Message, error) {
	b.mu.Lock()
	b.editMessageTextCalls++
	b.mu.Unlock()
	if b.sendErr != nil {
		return nil, b.sendErr
	}
	return &models.Message{ID: 99}, nil
}

func (b *stubTelegramBot) DeleteMessage(_ context.Context, _ *tgbot.DeleteMessageParams) (bool, error) {
	b.mu.Lock()
	b.deleteMessageCalls++
	b.mu.Unlock()
	if b.sendErr != nil {
		return false, b.sendErr
	}
	return true, nil
}

func (b *stubTelegramBot) AnswerCallbackQuery(_ context.Context, _ *tgbot.AnswerCallbackQueryParams) (bool, error) {
	b.mu.Lock()
	b.answerCallbackCalls++
	b.mu.Unlock()
	return true, nil
}

func (b *stubTelegramBot) SetMyCommands(_ context.Context, _ *tgbot.SetMyCommandsParams) (bool, error) {
	b.mu.Lock()
	b.setMyCommandsCalls++
	b.mu.Unlock()
	if b.sendErr != nil {
		return false, b.sendErr
	}
	return true, nil
}

func (b *stubTelegramBot) GetFile(_ context.Context, _ *tgbot.GetFileParams) (*models.File, error) {
	b.mu.Lock()
	b.getFileCalls++
	b.mu.Unlock()
	if b.getFileErr != nil {
		return nil, b.getFileErr
	}
	return b.file, nil
}

func (b *stubTelegramBot) FileDownloadLink(f *models.File) string {
	return "https://test.example.com/file/" + f.FilePath
}

func (b *stubTelegramBot) SetMessageReaction(_ context.Context, _ *tgbot.SetMessageReactionParams) (bool, error) {
	b.mu.Lock()
	b.setReactionCalls++
	b.mu.Unlock()
	return true, nil
}

func (b *stubTelegramBot) SendMessageCallCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.sendMessageCalls
}

func (b *stubTelegramBot) SendChatActionCallCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.sendChatActionCalls
}

func (b *stubTelegramBot) GetFileCallCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.getFileCalls
}

func TestPlatformStart_RetriesInBackgroundUntilConnected(t *testing.T) {
	var attempts atomic.Int32
	readyCh := make(chan struct{}, 1)
	stubBot := newStubTelegramBot()
	me := &models.User{ID: 42, Username: "mybot"}

	p := &Platform{
		token:      "token",
		httpClient: &http.Client{},
		newBot: func(_ string, _ func(context.Context, *models.Update), _ *http.Client) (telegramBot, *models.User, func(context.Context), error) {
			if attempts.Add(1) == 1 {
				return nil, nil, nil, errors.New("dial failed")
			}
			return stubBot, me, func(ctx context.Context) { <-ctx.Done() }, nil
		},
		newBackoffTimer: immediateTimer,
	}
	p.SetLifecycleHandler(testLifecycleHandler{
		onReady: func(core.Platform) {
			readyCh <- struct{}{}
		},
	})

	if err := p.Start(func(core.Platform, *core.Message) {}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() {
		if err := p.Stop(); err != nil {
			t.Fatalf("Stop: %v", err)
		}
	}()

	select {
	case <-readyCh:
	case <-time.After(time.Second):
		t.Fatal("ready callback not observed")
	}

	if got := attempts.Load(); got < 2 {
		t.Fatalf("attempts = %d, want >= 2", got)
	}
}

func TestPlatformStart_InitialConnectFailureEmitsUnavailableOnceBeforeReady(t *testing.T) {
	var attempts atomic.Int32
	var unavailableCount atomic.Int32
	readyCh := make(chan struct{}, 1)
	stubBot := newStubTelegramBot()
	me := &models.User{ID: 42, Username: "mybot"}

	p := &Platform{
		token:      "token",
		httpClient: &http.Client{},
		newBot: func(_ string, _ func(context.Context, *models.Update), _ *http.Client) (telegramBot, *models.User, func(context.Context), error) {
			if attempts.Add(1) <= 2 {
				return nil, nil, nil, errors.New("dial failed")
			}
			return stubBot, me, func(ctx context.Context) { <-ctx.Done() }, nil
		},
		newBackoffTimer: immediateTimer,
	}
	p.SetLifecycleHandler(testLifecycleHandler{
		onReady: func(core.Platform) {
			readyCh <- struct{}{}
		},
		onUnavailable: func(core.Platform, error) {
			unavailableCount.Add(1)
		},
	})

	if err := p.Start(func(core.Platform, *core.Message) {}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() {
		if err := p.Stop(); err != nil {
			t.Fatalf("Stop: %v", err)
		}
	}()

	select {
	case <-readyCh:
	case <-time.After(time.Second):
		t.Fatal("ready callback not observed")
	}

	if got := unavailableCount.Load(); got != 1 {
		t.Fatalf("unavailable callbacks = %d, want 1", got)
	}
}

func TestPlatformDisconnectedSendPathsReturnNotConnected(t *testing.T) {
	p := &Platform{token: "token", httpClient: &http.Client{}}
	ctx := context.Background()
	rctx := replyContext{chatID: 1, threadID: 0, messageID: 2}

	tests := []struct {
		name string
		run  func() error
	}{
		{name: "Reply", run: func() error { return p.Reply(ctx, rctx, "hello") }},
		{name: "Send", run: func() error { return p.Send(ctx, rctx, "hello") }},
		{name: "SendImage", run: func() error { return p.SendImage(ctx, rctx, core.ImageAttachment{Data: []byte("img")}) }},
		{name: "SendFile", run: func() error { return p.SendFile(ctx, rctx, core.FileAttachment{Data: []byte("file")}) }},
		{name: "SendWithButtons", run: func() error {
			return p.SendWithButtons(ctx, rctx, "hello", [][]core.ButtonOption{{{Text: "A", Data: "a"}}})
		}},
		{name: "SendPreviewStart", run: func() error {
			_, err := p.SendPreviewStart(ctx, rctx, "preview")
			return err
		}},
		{name: "UpdateMessage", run: func() error {
			return p.UpdateMessage(ctx, &telegramPreviewHandle{chatID: 1, messageID: 2}, "preview")
		}},
		{name: "DeletePreviewMessage", run: func() error {
			return p.DeletePreviewMessage(ctx, &telegramPreviewHandle{chatID: 1, messageID: 2})
		}},
		{name: "RegisterCommands", run: func() error {
			return p.RegisterCommands([]core.BotCommandInfo{{Command: "help", Description: "help"}})
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.run()
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), "not connected") {
				t.Fatalf("error = %q, want to contain %q", err.Error(), "not connected")
			}
		})
	}

	stop := p.StartTyping(ctx, rctx)
	stop()
}

func TestPlatformLateReadyIgnoredAfterStop(t *testing.T) {
	connectStarted := make(chan struct{})
	releaseConnect := make(chan struct{})
	connectDone := make(chan struct{})
	readyCh := make(chan struct{}, 1)
	unavailableCh := make(chan error, 1)
	stubBot := newStubTelegramBot()
	me := &models.User{ID: 42, Username: "latebot"}

	p := &Platform{
		token:      "token",
		httpClient: &http.Client{},
		newBot: func(_ string, _ func(context.Context, *models.Update), _ *http.Client) (telegramBot, *models.User, func(context.Context), error) {
			close(connectStarted)
			defer close(connectDone)
			<-releaseConnect
			return stubBot, me, func(ctx context.Context) { <-ctx.Done() }, nil
		},
		newBackoffTimer: immediateTimer,
	}
	p.SetLifecycleHandler(testLifecycleHandler{
		onReady: func(core.Platform) {
			readyCh <- struct{}{}
		},
		onUnavailable: func(_ core.Platform, err error) {
			unavailableCh <- err
		},
	})

	if err := p.Start(func(core.Platform, *core.Message) {}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	<-connectStarted
	if err := p.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	close(releaseConnect)
	<-connectDone

	select {
	case <-readyCh:
		t.Fatal("unexpected ready callback after Stop")
	case err := <-unavailableCh:
		t.Fatalf("unexpected unavailable callback after Stop: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestPlatformStartTypingSwitchesToCurrentBotAfterReconnect(t *testing.T) {
	oldBot := newStubTelegramBot()
	newBot := newStubTelegramBot()
	ticker := newStubTypingTicker()

	p := &Platform{
		token:      "token",
		httpClient: &http.Client{},
		newTypingTicker: func(time.Duration) typingTicker {
			return ticker
		},
	}

	me := &models.User{ID: 42, Username: "old"}
	p.publishBot(oldBot, me)

	ctx, cancel := context.WithCancel(context.Background())
	stop := p.StartTyping(ctx, replyContext{chatID: 1, threadID: 0, messageID: 2})
	defer func() {
		stop()
		cancel()
	}()

	if got := oldBot.SendChatActionCallCount(); got != 1 {
		t.Fatalf("old bot action calls after initial typing = %d, want 1", got)
	}

	me2 := &models.User{ID: 42, Username: "new"}
	p.publishBot(newBot, me2)

	ticker.ch <- time.Now()
	time.Sleep(20 * time.Millisecond)

	if got := oldBot.SendChatActionCallCount(); got != 1 {
		t.Fatalf("old bot action calls after reconnect tick = %d, want 1", got)
	}
	if got := newBot.SendChatActionCallCount(); got != 1 {
		t.Fatalf("new bot action calls after reconnect tick = %d, want 1", got)
	}
}

func TestRetryLogMessage_DistinguishesFailureModes(t *testing.T) {
	tests := []struct {
		name  string
		cause retryCause
		want  string
	}{
		{name: "initial connect failure", cause: retryCauseInitialConnectFailure, want: "telegram: initial connection failed, retrying"},
		{name: "reconnect failure", cause: retryCauseReconnectFailure, want: "telegram: reconnect failed, retrying"},
		{name: "connection lost", cause: retryCauseConnectionLost, want: "telegram: connection lost, retrying"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := retryLogMessage(tt.cause); got != tt.want {
				t.Fatalf("retryLogMessage(%v) = %q, want %q", tt.cause, got, tt.want)
			}
		})
	}
}

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

	stubBot := newStubTelegramBot()
	p := &Platform{}
	p.bot = stubBot
	p.selfUser = &models.User{ID: 1, Username: "testbot"}

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

func TestTruncateTelegramBotDescription_UTF8Safe(t *testing.T) {
	t.Parallel()
	cjk := strings.Repeat("你", 200)
	out := truncateTelegramBotDescription(cjk)
	if !utf8.ValidString(out) {
		t.Fatal("invalid UTF-8 from CJK description")
	}
	if got, max := utf8.RuneCountInString(out), telegramBotCommandDescriptionLimit; got > max {
		t.Fatalf("rune count %d > %d", got, max)
	}

	long := strings.Repeat("b", 60)
	out2 := truncateTelegramBotDescription(long)
	if want := telegramBotCommandDescriptionLimit; utf8.RuneCountInString(out2) != want {
		t.Fatalf("ascii truncation: got %d runes want %d", utf8.RuneCountInString(out2), want)
	}
	if !utf8.ValidString(out2) {
		t.Fatal("invalid UTF-8 after ascii truncation")
	}
}

func TestTruncateForLog_UTF8Safe(t *testing.T) {
	t.Parallel()
	s := strings.Repeat("世", 50) // 50 runes
	out := truncateForLog(s, 10)
	if !utf8.ValidString(out) {
		t.Fatal("invalid UTF-8")
	}
	if utf8.RuneCountInString(out) != 13 { // 10 + "..."
		t.Fatalf("got %d runes", utf8.RuneCountInString(out))
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

func TestBuildSessionKey(t *testing.T) {
	tests := []struct {
		name   string
		shared bool
		chatID int64
		thread int
		userID int64
		want   string
	}{
		{name: "private no topic", shared: false, chatID: 100, thread: 0, userID: 7, want: "telegram:100:7"},
		{name: "private with topic", shared: false, chatID: 100, thread: 42, userID: 7, want: "telegram:100:42:7"},
		{name: "shared no topic", shared: true, chatID: 100, thread: 0, userID: 7, want: "telegram:100"},
		{name: "shared with topic", shared: true, chatID: 100, thread: 42, userID: 7, want: "telegram:100:42"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &Platform{shareSessionInChannel: tt.shared}
			got := p.buildSessionKey(tt.chatID, tt.thread, tt.userID)
			if got != tt.want {
				t.Fatalf("buildSessionKey(%d, %d, %d) = %q, want %q", tt.chatID, tt.thread, tt.userID, got, tt.want)
			}
		})
	}
}

func TestReconstructReplyCtx(t *testing.T) {
	tests := []struct {
		name     string
		shared   bool
		key      string
		wantChat int64
		wantThr  int
		wantErr  bool
	}{
		{name: "shared no topic", shared: true, key: "telegram:100", wantChat: 100, wantThr: 0},
		{name: "shared with topic", shared: true, key: "telegram:100:42", wantChat: 100, wantThr: 42},
		{name: "per-user no topic", shared: false, key: "telegram:100:7", wantChat: 100, wantThr: 0},
		{name: "per-user with topic", shared: false, key: "telegram:100:42:7", wantChat: 100, wantThr: 42},
		{name: "invalid prefix", shared: false, key: "slack:100:7", wantErr: true},
		{name: "too short", shared: false, key: "telegram", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &Platform{shareSessionInChannel: tt.shared}
			rctx, err := p.ReconstructReplyCtx(tt.key)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			rc := rctx.(replyContext)
			if rc.chatID != tt.wantChat {
				t.Fatalf("chatID = %d, want %d", rc.chatID, tt.wantChat)
			}
			if rc.threadID != tt.wantThr {
				t.Fatalf("threadID = %d, want %d", rc.threadID, tt.wantThr)
			}
		})
	}
}

func TestIsDirectedAtBot(t *testing.T) {
	p := &Platform{token: "token", httpClient: &http.Client{}}
	p.selfUser = &models.User{ID: 42, Username: "mybot"}

	tests := []struct {
		name string
		msg  *models.Message
		want bool
	}{
		{
			name: "command without @suffix",
			msg: &models.Message{
				Text: "/help",
				Chat: models.Chat{ID: 1, Type: models.ChatTypeGroup},
				Entities: []models.MessageEntity{
					{Type: models.MessageEntityTypeBotCommand, Offset: 0, Length: 5},
				},
			},
			want: true,
		},
		{
			name: "command @mybot",
			msg: &models.Message{
				Text: "/help@mybot",
				Chat: models.Chat{ID: 1, Type: models.ChatTypeGroup},
				Entities: []models.MessageEntity{
					{Type: models.MessageEntityTypeBotCommand, Offset: 0, Length: 11},
				},
			},
			want: true,
		},
		{
			name: "command @otherbot",
			msg: &models.Message{
				Text: "/help@otherbot",
				Chat: models.Chat{ID: 1, Type: models.ChatTypeGroup},
				Entities: []models.MessageEntity{
					{Type: models.MessageEntityTypeBotCommand, Offset: 0, Length: 14},
				},
			},
			want: false,
		},
		{
			name: "@mention in text",
			msg: &models.Message{
				Text: "hey @mybot do something",
				Chat: models.Chat{ID: 1, Type: models.ChatTypeGroup},
				Entities: []models.MessageEntity{
					{Type: models.MessageEntityTypeMention, Offset: 4, Length: 6},
				},
			},
			want: true,
		},
		{
			name: "reply to bot message",
			msg: &models.Message{
				Text: "yes do it",
				Chat: models.Chat{ID: 1, Type: models.ChatTypeGroup},
				ReplyToMessage: &models.Message{
					From: &models.User{ID: 42},
				},
			},
			want: true,
		},
		{
			name: "plain text not directed",
			msg: &models.Message{
				Text: "hello everyone",
				Chat: models.Chat{ID: 1, Type: models.ChatTypeGroup},
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := p.isDirectedAtBot(tt.msg)
			if got != tt.want {
				t.Fatalf("isDirectedAtBot() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestHandleMessageWithForumTopic(t *testing.T) {
	handled := make(chan *core.Message, 1)
	p := &Platform{
		token:         "token",
		httpClient:    &http.Client{},
		groupReplyAll: true,
	}
	p.handler = func(_ core.Platform, msg *core.Message) {
		handled <- msg
	}
	stubBot := newStubTelegramBot()
	p.bot = stubBot
	p.selfUser = &models.User{ID: 42, Username: "mybot"}

	msg := &models.Message{
		ID:              10,
		MessageThreadID: 55,
		Text:            "hello from topic",
		Date:            int(time.Now().Unix()),
		From:            &models.User{ID: 7, Username: "alice"},
		Chat: models.Chat{
			ID:      100,
			Type:    models.ChatTypeSupergroup,
			Title:   "Test Group",
			IsForum: true,
		},
	}

	p.handleMessage(context.Background(), msg)

	select {
	case got := <-handled:
		if got.SessionKey != "telegram:100:55:7" {
			t.Fatalf("SessionKey = %q, want %q", got.SessionKey, "telegram:100:55:7")
		}
		rc := got.ReplyCtx.(replyContext)
		if rc.threadID != 55 {
			t.Fatalf("threadID = %d, want 55", rc.threadID)
		}
	case <-time.After(time.Second):
		t.Fatal("message not handled")
	}
}

func TestHandleMessageNonForumIgnoresThreadID(t *testing.T) {
	handled := make(chan *core.Message, 1)
	p := &Platform{
		token:         "token",
		httpClient:    &http.Client{},
		groupReplyAll: true,
	}
	p.handler = func(_ core.Platform, msg *core.Message) {
		handled <- msg
	}
	stubBot := newStubTelegramBot()
	p.bot = stubBot
	p.selfUser = &models.User{ID: 42, Username: "mybot"}

	msg := &models.Message{
		ID:              10,
		MessageThreadID: 55, // set but not a forum
		Text:            "hello",
		Date:            int(time.Now().Unix()),
		From:            &models.User{ID: 7, Username: "alice"},
		Chat: models.Chat{
			ID:      100,
			Type:    models.ChatTypeGroup,
			Title:   "Test Group",
			IsForum: false,
		},
	}

	p.handleMessage(context.Background(), msg)

	select {
	case got := <-handled:
		if got.SessionKey != "telegram:100:7" {
			t.Fatalf("SessionKey = %q, want %q (no thread)", got.SessionKey, "telegram:100:7")
		}
		rc := got.ReplyCtx.(replyContext)
		if rc.threadID != 0 {
			t.Fatalf("threadID = %d, want 0", rc.threadID)
		}
	case <-time.After(time.Second):
		t.Fatal("message not handled")
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

	b, err := tgbot.New("TEST_TOKEN",
		tgbot.WithServerURL(server.URL),
		tgbot.WithHTTPClient(5*time.Second, server.Client()),
	)
	if err != nil {
		t.Fatalf("tgbot.New returned error: %v", err)
	}

	return &Platform{
		bot:        b,
		selfUser:   &models.User{ID: 1, Username: "testbot"},
		httpClient: server.Client(),
	}
}
