package telegram

import (
	"context"
	"io"
	"net/http"
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
