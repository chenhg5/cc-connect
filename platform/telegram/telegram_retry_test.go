package telegram

import (
	"context"
	"errors"
	"testing"
	"time"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// rateLimitedBot answers the first failures calls of SendMessage and
// EditMessageText with 429 Too Many Requests.
type rateLimitedBot struct {
	*stubTelegramBot
	failures   int
	retryAfter int
	calls      int
}

func (b *rateLimitedBot) next() (*models.Message, error) {
	b.calls++
	if b.calls <= b.failures {
		return nil, &tgbot.TooManyRequestsError{Message: "Too Many Requests", RetryAfter: b.retryAfter}
	}
	return &models.Message{ID: 42}, nil
}

func (b *rateLimitedBot) SendMessage(context.Context, *tgbot.SendMessageParams) (*models.Message, error) {
	return b.next()
}

func (b *rateLimitedBot) EditMessageText(context.Context, *tgbot.EditMessageTextParams) (*models.Message, error) {
	return b.next()
}

func recordSleeps(t *testing.T) *[]time.Duration {
	t.Helper()
	var slept []time.Duration
	orig := sleepCtx
	sleepCtx = func(_ context.Context, d time.Duration) error {
		slept = append(slept, d)
		return nil
	}
	t.Cleanup(func() { sleepCtx = orig })
	return &slept
}

func TestRateLimitRetryBot_SendMessageRetriesAfterRetryAfter(t *testing.T) {
	slept := recordSleeps(t)
	inner := &rateLimitedBot{stubTelegramBot: newStubTelegramBot(), failures: 2, retryAfter: 7}

	msg, err := newRateLimitRetryBot(inner).SendMessage(context.Background(), &tgbot.SendMessageParams{Text: "final reply"})
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if msg.ID != 42 || inner.calls != 3 {
		t.Fatalf("got message %d after %d calls, want 42 after 3", msg.ID, inner.calls)
	}
	if len(*slept) != 2 || (*slept)[0] != 7*time.Second || (*slept)[1] != 7*time.Second {
		t.Fatalf("slept %v, want [7s 7s]", *slept)
	}
}

func TestRateLimitRetryBot_EditMessageTextRetries(t *testing.T) {
	recordSleeps(t)
	inner := &rateLimitedBot{stubTelegramBot: newStubTelegramBot(), failures: 1, retryAfter: 1}

	if _, err := newRateLimitRetryBot(inner).EditMessageText(context.Background(), &tgbot.EditMessageTextParams{Text: "x"}); err != nil {
		t.Fatalf("EditMessageText: %v", err)
	}
	if inner.calls != 2 {
		t.Fatalf("calls = %d, want 2", inner.calls)
	}
}

func TestRateLimitRetryBot_GivesUpAfterMaxAttempts(t *testing.T) {
	recordSleeps(t)
	inner := &rateLimitedBot{stubTelegramBot: newStubTelegramBot(), failures: 10, retryAfter: 1}

	_, err := newRateLimitRetryBot(inner).SendMessage(context.Background(), &tgbot.SendMessageParams{})
	var tooMany *tgbot.TooManyRequestsError
	if !errors.As(err, &tooMany) {
		t.Fatalf("err = %v, want TooManyRequestsError", err)
	}
	if inner.calls != rateLimitMaxAttempts {
		t.Fatalf("calls = %d, want %d", inner.calls, rateLimitMaxAttempts)
	}
}

func TestRateLimitRetryBot_DoesNotWaitBeyondLimit(t *testing.T) {
	slept := recordSleeps(t)
	inner := &rateLimitedBot{stubTelegramBot: newStubTelegramBot(), failures: 1, retryAfter: 3600}

	if _, err := newRateLimitRetryBot(inner).SendMessage(context.Background(), &tgbot.SendMessageParams{}); err == nil {
		t.Fatal("expected the 429 error to be returned")
	}
	if inner.calls != 1 || len(*slept) != 0 {
		t.Fatalf("calls = %d, slept %v; want 1 call and no sleep", inner.calls, *slept)
	}
}

func TestRateLimitRetryBot_OtherErrorsAreNotRetried(t *testing.T) {
	recordSleeps(t)
	inner := newStubTelegramBot()
	inner.sendErr = errors.New("bad request")

	if _, err := newRateLimitRetryBot(inner).SendMessage(context.Background(), &tgbot.SendMessageParams{}); err == nil {
		t.Fatal("expected error")
	}
	if inner.sendMessageCalls != 1 {
		t.Fatalf("calls = %d, want 1", inner.sendMessageCalls)
	}
}

func TestRateLimitRetryBot_StopsWhenContextIsDone(t *testing.T) {
	orig := sleepCtx
	t.Cleanup(func() { sleepCtx = orig })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	inner := &rateLimitedBot{stubTelegramBot: newStubTelegramBot(), failures: 5, retryAfter: 1}

	start := time.Now()
	if _, err := newRateLimitRetryBot(inner).SendMessage(ctx, &tgbot.SendMessageParams{}); err == nil {
		t.Fatal("expected error")
	}
	if inner.calls != 1 || time.Since(start) > time.Second {
		t.Fatalf("calls = %d after %v; want 1 call and an immediate return", inner.calls, time.Since(start))
	}
}
