package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

func pollingTestClient(handle func(*http.Request, url.Values) (*http.Response, error)) *http.Client {
	return &http.Client{Transport: downtimeRoundTrip(func(req *http.Request) (*http.Response, error) {
		params := req.URL.Query()
		if req.Method == http.MethodPost {
			defer req.Body.Close()
			if err := req.ParseMultipartForm(1 << 20); err != nil && !errors.Is(err, io.EOF) {
				return nil, err
			}
			if req.MultipartForm != nil {
				defer req.MultipartForm.RemoveAll()
			}
			params = req.Form
		}
		return handle(req, params)
	})}
}

func pollingTestResponse(result any) (*http.Response, error) {
	body, err := json.Marshal(map[string]any{"ok": true, "result": result})
	if err != nil {
		return nil, err
	}
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(string(body))), Header: make(http.Header)}, nil
}

func TestDefaultNewBot_PollFailureFinishesFetchedLiveBatch(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	failedRequest := make(chan context.Context, 1)
	polls := 0
	client := pollingTestClient(func(req *http.Request, _ url.Values) (*http.Response, error) {
		switch {
		case strings.HasSuffix(req.URL.Path, "/getMe"):
			return pollingTestResponse(&models.User{ID: 42, IsBot: true})
		case strings.HasSuffix(req.URL.Path, "/getUpdates"):
			polls++
			if polls == 1 {
				return pollingTestResponse([]*models.Update{{ID: 1}, {ID: 2}, {ID: 3}})
			}
			if polls == 2 {
				failedRequest <- req.Context()
				return nil, errors.New("network unavailable")
			}
			<-req.Context().Done()
			return nil, req.Context().Err()
		case strings.HasSuffix(req.URL.Path, "/sendMessage"):
			if err := req.Context().Err(); err != nil {
				t.Errorf("live handler's reply cancelled by polling failure: %v", err)
			}
			return pollingTestResponse(&models.Message{ID: 99})
		default:
			return nil, errors.New("unexpected API method")
		}
	})
	var bot telegramBot
	var received []int64
	var err error
	bot, _, poll, err := defaultNewBot("test-token", func(handlerCtx context.Context, update *models.Update) {
		received = append(received, update.ID)
		if update.ID != 3 {
			return
		}
		select {
		case requestCtx := <-failedRequest:
			// Wait until the wrapper cancels polling while this live handler is
			// still running. Its independent context must remain usable.
			<-requestCtx.Done()
		case <-ctx.Done():
			t.Error("poller did not reach the network failure")
		}
		if handlerCtx.Err() != nil {
			t.Errorf("live handler cancelled before it could finish: %v", handlerCtx.Err())
		}
		if _, err := bot.SendMessage(handlerCtx, &tgbot.SendMessageParams{ChatID: 10, Text: "reply"}); err != nil {
			t.Errorf("live handler reply: %v", err)
		}
	}, client)
	if err != nil {
		t.Fatal(err)
	}
	poll(ctx)
	if ctx.Err() != nil {
		t.Fatal("poll failure did not return control for downtime recovery")
	}
	if !reflect.DeepEqual(received, []int64{1, 2, 3}) || polls != 2 {
		t.Fatalf("received=%v polls=%d; want the complete live batch and no internal retry", received, polls)
	}
}

type pollingCloseErrorBody struct{ io.ReadCloser }

func (b pollingCloseErrorBody) Close() error {
	_ = b.ReadCloser.Close()
	return errors.New("send response close failed")
}

func TestDefaultNewBot_SendFailureDoesNotRestartPolling(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	sendFinished := make(chan struct{})
	client := pollingTestClient(func(req *http.Request, _ url.Values) (*http.Response, error) {
		switch {
		case strings.HasSuffix(req.URL.Path, "/getMe"):
			return pollingTestResponse(&models.User{ID: 42, IsBot: true})
		case strings.HasSuffix(req.URL.Path, "/getUpdates"):
			<-req.Context().Done()
			return nil, req.Context().Err()
		case strings.HasSuffix(req.URL.Path, "/sendMessage"):
			response, err := pollingTestResponse(&models.Message{ID: 99})
			response.Body = pollingCloseErrorBody{response.Body}
			return response, err
		default:
			return nil, errors.New("unexpected API method")
		}
	})
	bot, _, poll, err := defaultNewBot("test-token", func(context.Context, *models.Update) {}, client)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { defer close(done); poll(ctx) }()
	go func() {
		defer close(sendFinished)
		_, _ = bot.SendMessage(ctx, &tgbot.SendMessageParams{ChatID: 10, Text: "reply"})
	}()
	<-sendFinished
	select {
	case <-done:
		t.Fatal("an outbound API error restarted polling")
	case <-time.After(50 * time.Millisecond):
	}
	cancel()
	<-done
}

func TestPlatform_PollingReconnectDoesNotSummarizeProcessedUnacknowledgedUpdates(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	now := time.Now().UTC().Truncate(time.Second)
	processed := []*models.Update{
		downtimeMessage(8, int(now.Unix())-1, 10, 21),
		downtimeMessage(9, int(now.Unix())-1, 10, 21),
	}
	processed[0].Message.Text, processed[1].Message.Text = "before outage one", "before outage two"
	old := downtimeMessage(10, int(now.Unix())-1, 10, 21)
	live := downtimeMessage(11, int(now.Unix())+10, 10, 21)
	old.Message.Text, live.Message.Text = "during outage", "after recovery"
	var mu sync.Mutex
	failed := false
	delivered := false
	confirmed := int64(0)
	var notices []url.Values
	readyCount := 0
	client := pollingTestClient(func(req *http.Request, params url.Values) (*http.Response, error) {
		mu.Lock()
		switch {
		case strings.HasSuffix(req.URL.Path, "/getMe"):
			mu.Unlock()
			return pollingTestResponse(&models.User{ID: 42, IsBot: true, Username: "testbot"})
		case strings.HasSuffix(req.URL.Path, "/sendMessage"):
			notices = append(notices, params)
			mu.Unlock()
			return pollingTestResponse(&models.Message{ID: 99})
		case strings.HasSuffix(req.URL.Path, "/getUpdates"):
			if req.Method == http.MethodPost && !failed {
				if !delivered {
					delivered = true
					mu.Unlock()
					return pollingTestResponse(processed)
				}
				// Fail before Telegram sees the acknowledgement offset. The
				// processed batch remains queued alongside the outage message.
				failed = true
				mu.Unlock()
				return nil, errors.New("network unavailable")
			}
			offset, _ := strconv.ParseInt(params.Get("offset"), 10, 64)
			if offset > confirmed {
				confirmed = offset
			}
			var updates []*models.Update
			if failed {
				for _, update := range []*models.Update{processed[0], processed[1], old, live} {
					if update.ID >= confirmed {
						updates = append(updates, update)
					}
				}
			}
			mu.Unlock()
			if req.Method == http.MethodPost && len(updates) == 0 {
				<-req.Context().Done()
				return nil, req.Context().Err()
			}
			return pollingTestResponse(updates)
		default:
			mu.Unlock()
			return nil, errors.New("unexpected API method")
		}
	})
	var received []string
	p := &Platform{
		token: "test-token", allowFrom: "7", httpClient: client,
		newBackoffTimer: immediateTimer, newBot: defaultNewBot,
		// Represent a connection established before the simulated outage.
		downtime: &downtimeDrain{cutoff: now.Add(-time.Minute), groups: make(map[downtimeDestination]*downtimeSummary)},
		handler: func(_ core.Platform, msg *core.Message) {
			received = append(received, msg.Content)
			if msg.Content == live.Message.Text {
				cancel()
			}
		},
		lifecycleHandler: testLifecycleHandler{onReady: func(core.Platform) { readyCount++ }},
	}
	p.connectLoop(ctx)
	if !reflect.DeepEqual(received, []string{"before outage one", "before outage two", "after recovery"}) || readyCount != 2 {
		t.Fatalf("received=%v ready=%d; want processed inputs once and only live input after recovery", received, readyCount)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(notices) != 1 || notices[0].Get("chat_id") != "10" || notices[0].Get("message_thread_id") != "21" || !strings.Contains(notices[0].Get("text"), "skipped 1 messages") {
		t.Fatalf("outage notice missing or misrouted: %+v", notices)
	}
}
