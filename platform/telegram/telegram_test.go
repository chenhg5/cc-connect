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

	"github.com/chenhg5/cc-connect/core"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
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
	userName string
	userID   int64
	token    string

	mu           sync.Mutex
	updates      chan tgbotapi.Update
	sendErr      error
	requestErr   error
	fileErr      error
	file         tgbotapi.File
	stopCalls    int
	sendCalls    int
	requestCalls int
	getFileCalls int
}

func newStubTelegramBot(userName string) *stubTelegramBot {
	return &stubTelegramBot{
		userName: userName,
		userID:   42,
		token:    "token",
		updates:  make(chan tgbotapi.Update),
		file:     tgbotapi.File{FilePath: "files/test.dat"},
	}
}

func (b *stubTelegramBot) SelfUser() tgbotapi.User {
	return tgbotapi.User{ID: b.userID, UserName: b.userName}
}

func (b *stubTelegramBot) Token() string {
	return b.token
}

func (b *stubTelegramBot) Send(tgbotapi.Chattable) (tgbotapi.Message, error) {
	b.mu.Lock()
	b.sendCalls++
	b.mu.Unlock()
	if b.sendErr != nil {
		return tgbotapi.Message{}, b.sendErr
	}
	return tgbotapi.Message{MessageID: 99}, nil
}

func (b *stubTelegramBot) Request(tgbotapi.Chattable) (*tgbotapi.APIResponse, error) {
	b.mu.Lock()
	b.requestCalls++
	b.mu.Unlock()
	if b.requestErr != nil {
		return nil, b.requestErr
	}
	return &tgbotapi.APIResponse{Ok: true}, nil
}

func (b *stubTelegramBot) GetUpdates(tgbotapi.UpdateConfig) ([]tgbotapi.Update, error) {
	return nil, nil
}

func (b *stubTelegramBot) GetUpdatesChan(tgbotapi.UpdateConfig) tgbotapi.UpdatesChannel {
	return b.updates
}

func (b *stubTelegramBot) StopReceivingUpdates() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.stopCalls++
	select {
	case <-b.updates:
	default:
	}
	select {
	case <-b.updates:
	default:
	}
	defer func() {
		_ = recover()
	}()
	close(b.updates)
}

func (b *stubTelegramBot) GetFile(tgbotapi.FileConfig) (tgbotapi.File, error) {
	b.mu.Lock()
	b.getFileCalls++
	b.mu.Unlock()
	if b.fileErr != nil {
		return tgbotapi.File{}, b.fileErr
	}
	return b.file, nil
}

func (b *stubTelegramBot) StopCalls() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.stopCalls
}

func (b *stubTelegramBot) SendCalls() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.sendCalls
}

func (b *stubTelegramBot) GetFileCalls() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.getFileCalls
}

func (b *stubTelegramBot) RequestCalls() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.requestCalls
}

func TestPlatformStart_RetriesInBackgroundUntilConnected(t *testing.T) {
	var attempts atomic.Int32
	readyCh := make(chan struct{}, 1)
	connectedBot := newStubTelegramBot("mybot")

	p := &Platform{
		token:      "token",
		httpClient: &http.Client{},
		newBot: func(string, *http.Client) (telegramBot, error) { //nolint:nonamedreturns
			if attempts.Add(1) == 1 {
				return nil, errors.New("dial failed")
			}
			return connectedBot, nil
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

	p := &Platform{
		token:      "token",
		httpClient: &http.Client{},
		newBot: func(string, *http.Client) (telegramBot, error) {
			if attempts.Add(1) <= 2 {
				return nil, errors.New("dial failed")
			}
			return newStubTelegramBot("mybot"), nil
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
	rctx := replyContext{chatID: 1, messageID: 2}

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
	lateBot := newStubTelegramBot("latebot")

	p := &Platform{
		token:      "token",
		httpClient: &http.Client{},
		newBot: func(string, *http.Client) (telegramBot, error) {
			close(connectStarted)
			defer close(connectDone)
			<-releaseConnect
			return lateBot, nil
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

	if got := lateBot.StopCalls(); got != 1 {
		t.Fatalf("StopReceivingUpdates calls = %d, want 1", got)
	}
}

func TestPlatformStaleGenerationCleanupDoesNotClobberNewerBot(t *testing.T) {
	p := &Platform{token: "token", httpClient: &http.Client{}}
	oldBot := newStubTelegramBot("old")
	newBot := newStubTelegramBot("new")

	oldGen, ok := p.publishConnectedBot(oldBot)
	if !ok {
		t.Fatal("failed to publish old bot")
	}
	newGen, ok := p.publishConnectedBot(newBot)
	if !ok {
		t.Fatal("failed to publish new bot")
	}

	p.finishConnection(oldGen, oldBot, errors.New("old lost"), false)

	gotBot, gotGen, err := p.currentBot()
	if err != nil {
		t.Fatalf("currentBot: %v", err)
	}
	if gotBot != newBot {
		t.Fatal("stale cleanup replaced current bot")
	}
	if gotGen != newGen {
		t.Fatalf("generation = %d, want %d", gotGen, newGen)
	}
}

func TestPlatformUnavailableEmittedExactlyOnceForCurrentLiveGeneration(t *testing.T) {
	var unavailableCount atomic.Int32
	p := &Platform{token: "token", httpClient: &http.Client{}}
	bot := newStubTelegramBot("live")

	p.SetLifecycleHandler(testLifecycleHandler{
		onUnavailable: func(core.Platform, error) {
			unavailableCount.Add(1)
		},
	})

	gen, ok := p.publishConnectedBot(bot)
	if !ok {
		t.Fatal("failed to publish bot")
	}

	p.finishConnection(gen, bot, errors.New("lost"), false)
	p.finishConnection(gen, bot, errors.New("lost again"), false)

	if got := unavailableCount.Load(); got != 1 {
		t.Fatalf("unavailable callbacks = %d, want 1", got)
	}
}

func TestPlatformStartTypingSwitchesToCurrentBotAfterReconnect(t *testing.T) {
	oldBot := newStubTelegramBot("old")
	newBot := newStubTelegramBot("new")
	ticker := newStubTypingTicker()

	p := &Platform{
		token:      "token",
		httpClient: &http.Client{},
		newTypingTicker: func(time.Duration) typingTicker {
			return ticker
		},
	}

	if _, ok := p.publishConnectedBot(oldBot); !ok {
		t.Fatal("failed to publish old bot")
	}

	ctx, cancel := context.WithCancel(context.Background())
	stop := p.StartTyping(ctx, replyContext{chatID: 1, messageID: 2})
	defer func() {
		stop()
		cancel()
	}()

	if got := oldBot.RequestCalls(); got != 1 {
		t.Fatalf("old bot request calls after initial typing = %d, want 1", got)
	}

	if _, ok := p.publishConnectedBot(newBot); !ok {
		t.Fatal("failed to publish new bot")
	}

	ticker.ch <- time.Now()
	time.Sleep(20 * time.Millisecond)

	if got := oldBot.RequestCalls(); got != 1 {
		t.Fatalf("old bot request calls after reconnect tick = %d, want 1", got)
	}
	if got := newBot.RequestCalls(); got != 1 {
		t.Fatalf("new bot request calls after reconnect tick = %d, want 1", got)
	}
}

func TestPlatformDownloadFileRejectsStaleBot(t *testing.T) {
	p := &Platform{token: "token", httpClient: &http.Client{}}
	oldBot := newStubTelegramBot("old")
	newBot := newStubTelegramBot("new")

	if _, ok := p.publishConnectedBot(oldBot); !ok {
		t.Fatal("failed to publish old bot")
	}
	if _, ok := p.publishConnectedBot(newBot); !ok {
		t.Fatal("failed to publish new bot")
	}

	_, err := p.downloadFile(oldBot, "file-1")
	if err == nil {
		t.Fatal("expected stale bot error, got nil")
	}
	if !strings.Contains(err.Error(), "not connected") {
		t.Fatalf("error = %q, want to contain %q", err.Error(), "not connected")
	}
	if got := oldBot.GetFileCalls(); got != 0 {
		t.Fatalf("old bot getFile calls = %d, want 0", got)
	}
}

func TestPlatformHandleCallbackQueryRejectsStaleBot(t *testing.T) {
	p := &Platform{token: "token", httpClient: &http.Client{}}
	oldBot := newStubTelegramBot("old")
	newBot := newStubTelegramBot("new")
	handled := make(chan *core.Message, 1)

	p.handler = func(_ core.Platform, msg *core.Message) {
		handled <- msg
	}

	if _, ok := p.publishConnectedBot(oldBot); !ok {
		t.Fatal("failed to publish old bot")
	}
	if _, ok := p.publishConnectedBot(newBot); !ok {
		t.Fatal("failed to publish new bot")
	}

	callbackData := "perm:allow"
	p.handleCallbackQuery(oldBot, &tgbotapi.CallbackQuery{
		ID:   "cb1",
		Data: callbackData,
		From: &tgbotapi.User{ID: 7, UserName: "alice"},
		Message: &tgbotapi.Message{
			MessageID:   10,
			Text:        "perm?",
			Chat:        &tgbotapi.Chat{ID: 1, Type: "private"},
			ReplyMarkup: &tgbotapi.InlineKeyboardMarkup{},
		},
	})

	if got := oldBot.RequestCalls(); got != 0 {
		t.Fatalf("old bot request calls = %d, want 0", got)
	}
	if got := oldBot.SendCalls(); got != 0 {
		t.Fatalf("old bot send calls = %d, want 0", got)
	}

	select {
	case msg := <-handled:
		t.Fatalf("unexpected handled callback message: %+v", msg)
	default:
	}
}

func TestPlatformIsDirectedAtBotRejectsStaleBot(t *testing.T) {
	p := &Platform{token: "token", httpClient: &http.Client{}}
	oldBot := newStubTelegramBot("oldbot")
	newBot := newStubTelegramBot("newbot")

	if _, ok := p.publishConnectedBot(oldBot); !ok {
		t.Fatal("failed to publish old bot")
	}
	if _, ok := p.publishConnectedBot(newBot); !ok {
		t.Fatal("failed to publish new bot")
	}

	msg := &tgbotapi.Message{
		Text: "/help@oldbot",
		Chat: &tgbotapi.Chat{ID: 1, Type: "group"},
		Entities: []tgbotapi.MessageEntity{
			{Type: "bot_command", Offset: 0, Length: len("/help@oldbot")},
		},
	}

	if got := p.isDirectedAtBot(oldBot, msg); got {
		t.Fatal("stale bot should not be treated as current bot target")
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

	p := &Platform{bot: &botAPIWrapper{BotAPI: &tgbotapi.BotAPI{}}}
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

	return &Platform{bot: &botAPIWrapper{BotAPI: bot}}
}
