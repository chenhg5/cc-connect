package telegram

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"
)

func newStreamingTestPlatform(bot *stubTelegramBot) *Platform {
	p := &Platform{token: "token"}
	p.publishBot(bot, &models.User{ID: 42, Username: "mybot"})
	return p
}

// TestStreamingCard_FirstUpdatePosts verifies lazy posting: no message until
// the first content arrives, then exactly one SendMessage.
func TestStreamingCard_FirstUpdatePosts(t *testing.T) {
	bot := newStubTelegramBot()
	p := newStreamingTestPlatform(bot)
	sc, err := p.CreateStreamingCard(context.Background(), replyContext{chatID: 1, threadID: 0, messageID: 7})
	if err != nil {
		t.Fatalf("CreateStreamingCard: %v", err)
	}
	card := sc.(*telegramStreamingCard)
	if err := card.Update(context.Background(), "hello"); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got := bot.sendMessageCalls; got != 1 {
		t.Fatalf("sendMessageCalls = %d, want 1", got)
	}
	if got := bot.editMessageTextCalls; got != 0 {
		t.Fatalf("editMessageTextCalls = %d, want 0", got)
	}
	if card.msgID == 0 {
		t.Fatal("card message ID not recorded after first post")
	}
}

// TestStreamingCard_ThrottlesRapidEdits verifies that back-to-back updates
// within the throttle window do not each hit the API.
func TestStreamingCard_ThrottlesRapidEdits(t *testing.T) {
	bot := newStubTelegramBot()
	p := newStreamingTestPlatform(bot)
	sc, err := p.CreateStreamingCard(context.Background(), replyContext{chatID: 1})
	if err != nil {
		t.Fatalf("CreateStreamingCard: %v", err)
	}
	card := sc.(*telegramStreamingCard)
	if err := card.Update(context.Background(), "v1"); err != nil {
		t.Fatalf("Update v1: %v", err)
	}
	if err := card.Update(context.Background(), "v2"); err != nil {
		t.Fatalf("Update v2: %v", err)
	}
	if got := bot.editMessageTextCalls; got != 0 {
		t.Fatalf("editMessageTextCalls = %d, want 0 (throttled)", got)
	}
	card.lastUpdate = time.Now().Add(-2 * telegramCardUpdateMinInterval)
	if err := card.Update(context.Background(), "v3"); err != nil {
		t.Fatalf("Update v3: %v", err)
	}
	if got := bot.editMessageTextCalls; got != 1 {
		t.Fatalf("editMessageTextCalls = %d, want 1", got)
	}
}

// TestStreamingCard_IdenticalContentSkipsEdit verifies no API call happens
// when the content did not change.
func TestStreamingCard_IdenticalContentSkipsEdit(t *testing.T) {
	bot := newStubTelegramBot()
	p := newStreamingTestPlatform(bot)
	sc, err := p.CreateStreamingCard(context.Background(), replyContext{chatID: 1})
	if err != nil {
		t.Fatalf("CreateStreamingCard: %v", err)
	}
	card := sc.(*telegramStreamingCard)
	if err := card.Update(context.Background(), "same"); err != nil {
		t.Fatalf("Update: %v", err)
	}
	card.lastUpdate = time.Now().Add(-2 * telegramCardUpdateMinInterval)
	if err := card.Update(context.Background(), "same"); err != nil {
		t.Fatalf("Update identical: %v", err)
	}
	if got := bot.editMessageTextCalls; got != 0 {
		t.Fatalf("editMessageTextCalls = %d, want 0", got)
	}
}

// TestStreamingCard_NotModifiedIsSuccess verifies the Telegram
// "message is not modified" error is swallowed.
func TestStreamingCard_NotModifiedIsSuccess(t *testing.T) {
	bot := newStubTelegramBot()
	p := newStreamingTestPlatform(bot)
	sc, err := p.CreateStreamingCard(context.Background(), replyContext{chatID: 1})
	if err != nil {
		t.Fatalf("CreateStreamingCard: %v", err)
	}
	card := sc.(*telegramStreamingCard)
	if err := card.Update(context.Background(), "v1"); err != nil {
		t.Fatalf("Update v1: %v", err)
	}
	bot.editErr = errors.New("Bad Request: message is not modified")
	card.lastUpdate = time.Now().Add(-2 * telegramCardUpdateMinInterval)
	if err := card.Update(context.Background(), "v2"); err != nil {
		t.Fatalf("Update with not-modified error: %v", err)
	}
	if card.Failed() {
		t.Fatal("card marked failed on not-modified")
	}
}

// TestStreamingCard_OverflowFinalizeDeletesStaleCard verifies the P1-5 fix:
// when the final payload exceeds the edit limit, the stale snapshot is
// deleted first and the full reply is posted fresh — the beginning is not
// delivered twice.
func TestStreamingCard_OverflowFinalizeDeletesStaleCard(t *testing.T) {
	bot := newStubTelegramBot()
	p := newStreamingTestPlatform(bot)
	sc, err := p.CreateStreamingCard(context.Background(), replyContext{chatID: 1})
	if err != nil {
		t.Fatalf("CreateStreamingCard: %v", err)
	}
	card := sc.(*telegramStreamingCard)
	if err := card.Update(context.Background(), "short"); err != nil {
		t.Fatalf("Update: %v", err)
	}
	big := strings.Repeat("x", telegramCardMaxText+100)
	if err := card.Finalize(context.Background(), big); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	if got := bot.deleteMessageCalls; got != 1 {
		t.Fatalf("deleteMessageCalls = %d, want 1 (stale snapshot removed)", got)
	}
	// The fresh post must carry the full content exactly once across the new
	// message(s); the deleted snapshot must not linger as a duplicate.
	fresh := strings.Join(bot.sentTexts[1:], "")
	if got := strings.Count(fresh, strings.Repeat("x", 100)); got != (telegramCardMaxText+100)/100 {
		t.Fatalf("fresh post repeats content %d times, want exactly once", got)
	}
	if card.Failed() {
		t.Fatal("card marked failed after successful overflow finalize")
	}
}

// TestStreamingCard_FinalizeNotModifiedIsSuccess verifies Finalize treats
// "message is not modified" like Update does instead of failing the card
// (which would make the engine send the answer a second time).
func TestStreamingCard_FinalizeNotModifiedIsSuccess(t *testing.T) {
	bot := newStubTelegramBot()
	p := newStreamingTestPlatform(bot)
	sc, err := p.CreateStreamingCard(context.Background(), replyContext{chatID: 1})
	if err != nil {
		t.Fatalf("CreateStreamingCard: %v", err)
	}
	card := sc.(*telegramStreamingCard)
	if err := card.Update(context.Background(), "v1"); err != nil {
		t.Fatalf("Update v1: %v", err)
	}
	bot.editErr = errors.New("Bad Request: message is not modified")
	if err := card.Finalize(context.Background(), "v2"); err != nil {
		t.Fatalf("Finalize with not-modified error: %v", err)
	}
	if card.Failed() {
		t.Fatal("card marked failed on not-modified finalize")
	}
}

// TestStreamingCard_PersistentFailureBacksOff verifies consecutive edit
// failures back off instead of hitting the API on every event.
func TestStreamingCard_PersistentFailureBacksOff(t *testing.T) {
	bot := newStubTelegramBot()
	p := newStreamingTestPlatform(bot)
	sc, err := p.CreateStreamingCard(context.Background(), replyContext{chatID: 1})
	if err != nil {
		t.Fatalf("CreateStreamingCard: %v", err)
	}
	card := sc.(*telegramStreamingCard)
	if err := card.Update(context.Background(), "v1"); err != nil {
		t.Fatalf("Update v1: %v", err)
	}
	bot.editErr = errors.New("Bad Request: bot was blocked by the user")
	for i := 0; i < 5; i++ {
		card.lastUpdate = time.Now().Add(-2 * telegramCardUpdateMinInterval)
		if err := card.Update(context.Background(), "v2"); err != nil {
			t.Fatalf("Update v2 attempt %d: %v", i, err)
		}
	}
	if got := bot.editMessageTextCalls; got != 1 {
		t.Fatalf("editMessageTextCalls = %d, want 1 (backed off after first failure)", got)
	}
	if card.Failed() {
		t.Fatal("card marked failed while still retrying with backoff")
	}
	// After the cooldown elapses the card tries again instead of staying dead.
	card.coolUntil = time.Now().Add(-time.Second)
	card.lastUpdate = time.Now().Add(-2 * telegramCardUpdateMinInterval)
	if err := card.Update(context.Background(), "v2"); err != nil {
		t.Fatalf("Update after cooldown: %v", err)
	}
	if got := bot.editMessageTextCalls; got != 2 {
		t.Fatalf("editMessageTextCalls = %d, want 2 (retry after cooldown)", got)
	}
}

// TestStreamingCard_FirstPostFailureBacksOff verifies the cooldown also
// covers the very first post: with SendMessage failing persistently,
// repeated updates do not hit the API on every event.
func TestStreamingCard_FirstPostFailureBacksOff(t *testing.T) {
	bot := newStubTelegramBot()
	bot.sendErr = errors.New("Bad Request: chat not found")
	p := newStreamingTestPlatform(bot)
	sc, err := p.CreateStreamingCard(context.Background(), replyContext{chatID: 1})
	if err != nil {
		t.Fatalf("CreateStreamingCard: %v", err)
	}
	card := sc.(*telegramStreamingCard)
	for i := 0; i < 5; i++ {
		if err := card.Update(context.Background(), "v1"); err != nil {
			t.Fatalf("Update attempt %d: %v", i, err)
		}
	}
	if got := bot.sendMessageCalls; got != 1 {
		t.Fatalf("sendMessageCalls = %d, want 1 (backed off after first failure)", got)
	}
	if card.Failed() {
		t.Fatal("card marked failed while still retrying with backoff")
	}
}

// TestStreamingCard_EmptyContentNoop verifies empty updates never post.
func TestStreamingCard_EmptyContentNoop(t *testing.T) {
	bot := newStubTelegramBot()
	p := newStreamingTestPlatform(bot)
	sc, err := p.CreateStreamingCard(context.Background(), replyContext{chatID: 1})
	if err != nil {
		t.Fatalf("CreateStreamingCard: %v", err)
	}
	card := sc.(*telegramStreamingCard)
	if err := card.Update(context.Background(), ""); err != nil {
		t.Fatalf("Update empty: %v", err)
	}
	if err := card.Finalize(context.Background(), ""); err != nil {
		t.Fatalf("Finalize empty: %v", err)
	}
	if got := bot.sendMessageCalls; got != 0 {
		t.Fatalf("sendMessageCalls = %d, want 0", got)
	}
}

// TestCreateStreamingCard_BadReplyCtx verifies invalid contexts are rejected.
func TestCreateStreamingCard_BadReplyCtx(t *testing.T) {
	bot := newStubTelegramBot()
	p := newStreamingTestPlatform(bot)
	if _, err := p.CreateStreamingCard(context.Background(), "nope"); err == nil {
		t.Fatal("expected error for invalid reply context, got nil")
	}
}
