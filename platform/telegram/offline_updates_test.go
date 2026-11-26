package telegram

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
	"github.com/go-telegram/bot/models"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestNewBotPreservesPendingUpdates(t *testing.T) {
	var requests []string
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requests = append(requests, r.URL.Path+"?"+r.URL.RawQuery)
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"ok":true,"result":{"id":1,"is_bot":true,"first_name":"Test","username":"testbot"}}`)),
			Header:     make(http.Header),
		}, nil
	})}
	_, _, _, err := defaultNewBot("test-token", func(context.Context, *models.Update) {}, client)
	if err != nil {
		t.Fatalf("defaultNewBot: %v", err)
	}
	for _, request := range requests {
		if !strings.HasSuffix(request, "/getMe?") {
			t.Fatalf("startup request %q would acknowledge offline messages", request)
		}
	}
}

func TestOfflineMessageDeliveredAfterRestart(t *testing.T) {
	handled := make(chan *core.Message, 1)
	p := &Platform{token: "token", httpClient: &http.Client{}}
	p.handler = func(_ core.Platform, msg *core.Message) { handled <- msg }
	p.bot = newStubTelegramBot()
	p.selfUser = &models.User{ID: 42, Username: "mybot"}

	p.handleMessage(context.Background(), &models.Message{
		ID: 7, Date: int(core.StartTime.Add(-time.Minute).Unix()), Text: "offline request",
		From: &models.User{ID: 7, Username: "alice"},
		Chat: models.Chat{ID: 100, Type: models.ChatTypePrivate},
	})

	select {
	case got := <-handled:
		if got.Content != "offline request" || got.MessageID != "7" {
			t.Fatalf("offline message = %+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("offline message was discarded")
	}
}

func TestOfflineMessageMaxAge(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts map[string]any
		age  time.Duration
		want bool
	}{
		{name: "default recent", opts: nil, age: 5 * time.Minute, want: true},
		{name: "default stale", opts: nil, age: 11 * time.Minute, want: false},
		{name: "unlimited stale", opts: map[string]any{"offline_message_max_age_mins": 0}, age: 23 * time.Hour, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts := map[string]any{"token": "test-token", "allow_from": "*"}
			for key, value := range tc.opts {
				opts[key] = value
			}
			platform, err := New(opts)
			if err != nil {
				t.Fatal(err)
			}
			p := platform.(*Platform)
			p.bot = newStubTelegramBot()
			p.selfUser = &models.User{ID: 42, Username: "mybot"}
			var handled int
			p.handler = func(_ core.Platform, _ *core.Message) { handled++ }
			p.handleMessage(context.Background(), &models.Message{
				ID: 7, Date: int(time.Now().Add(-tc.age).Unix()), Text: "offline request",
				From: &models.User{ID: 7, Username: "alice"},
				Chat: models.Chat{ID: 100, Type: models.ChatTypePrivate},
			})
			if got := handled == 1; got != tc.want {
				t.Errorf("handled = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestOfflineMessageMaxAgeValidation(t *testing.T) {
	for _, value := range []any{-1, 1.5, "10", int64(1 << 62)} {
		if _, err := New(map[string]any{"token": "test-token", "allow_from": "*", "offline_message_max_age_mins": value}); err == nil {
			t.Errorf("accepted invalid max age %v", value)
		}
	}
}

func TestStaleOfflineMessagesLoggedAsCount(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	p := &Platform{offlineMessageMaxAge: defaultOfflineMessageMaxAge}
	for i := 0; i < 2; i++ {
		p.handleMessage(context.Background(), &models.Message{
			ID: i + 1, Date: int(time.Now().Add(-time.Hour).Unix()),
			Text: "sentinel-private-content",
		})
	}
	p.logSkippedOfflineMessages()
	p.logSkippedOfflineMessages()
	log := output.String()
	if strings.Count(log, "skipped stale offline messages") != 1 || !strings.Contains(log, "count=2") {
		t.Errorf("wanted one aggregate skip log, got %q", log)
	}
	if strings.Contains(log, "sentinel-private-content") {
		t.Fatal("stale message content appeared in log")
	}
}

func TestPollingRetriesConflictWithoutAcknowledgingBacklog(t *testing.T) {
	var offsets []int64
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body := `{"ok":true,"result":{"id":1,"is_bot":true,"first_name":"Test","username":"testbot"}}`
		status := http.StatusOK
		if strings.HasSuffix(r.URL.Path, "/getUpdates") {
			if err := r.ParseMultipartForm(1024); err != nil {
				return nil, err
			}
			offset, err := strconv.ParseInt(r.FormValue("offset"), 10, 64)
			if err != nil {
				return nil, err
			}
			offsets = append(offsets, offset)
			if len(offsets) == 1 {
				status = http.StatusConflict
				body = `{"ok":false,"error_code":409,"description":"Conflict"}`
			} else {
				body = `{"ok":true,"result":[{"update_id":10}]}`
			}
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
	_, _, startPoll, err := defaultNewBot("test-token", func(context.Context, *models.Update) { cancel() }, client)
	if err != nil {
		t.Fatal(err)
	}
	startPoll(ctx)
	if len(offsets) < 2 || offsets[0] != offsets[1] || offsets[0] < 0 {
		t.Errorf("getUpdates offsets after conflict = %v; want retry without acknowledging backlog", offsets)
	}
}
