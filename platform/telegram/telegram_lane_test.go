package telegram

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"

	"github.com/go-telegram/bot/models"
)

// ── Issue #2015: a stuck update must not take the whole bot down ───────────
//
// Telegram delivers updates through a single polling goroutine. When the
// update was handled inline on that goroutine, a built-in command that blocked
// (e.g. the /usage hang behind #2015) held the only update worker: every
// later update queued behind it and the bot stopped responding until
// cc-connect was restarted.
//
// The update dispatch path must therefore hand the update to its session lane
// and return to the polling loop immediately. The lane keeps the messages of
// a session in order, and a stuck handler must not hold the lane forever.

// lanePoll feeds updates to the platform the way the Telegram polling worker
// does: one update after the other, on a single goroutine.
//
// A dispatch path that blocks the poller stops the whole feed — which is
// exactly the #2015 failure mode.
func lanePoll(ctx context.Context, p *Platform, updates ...*models.Update) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		for _, update := range updates {
			p.processUpdate(ctx, update)
		}
	}()
	return done
}

// laneTestUpdate builds a private-chat message update for the given chat and
// user, so that tests can control which session an update belongs to.
func laneTestUpdate(updateID int64, text string, chatID int64, userID int64) *models.Update {
	return &models.Update{
		ID: updateID,
		Message: &models.Message{
			ID:   int(updateID),
			Text: text,
			Date: int(time.Now().Unix()),
			From: &models.User{ID: userID, Username: "alice"},
			Chat: models.Chat{ID: chatID, Type: models.ChatTypePrivate},
		},
	}
}

// laneTestCallbackUpdate builds a callback query update for the given chat
// and user.
func laneTestCallbackUpdate(updateID int64, chatID int64, userID int64) *models.Update {
	return &models.Update{
		ID: updateID,
		CallbackQuery: &models.CallbackQuery{
			ID:   "cb-1",
			From: models.User{ID: userID, Username: "alice"},
			Message: models.MaybeInaccessibleMessage{
				Message: &models.Message{
					ID:   int(updateID),
					Text: "menu",
					Date: int(time.Now().Unix()),
					From: &models.User{ID: userID, Username: "alice"},
					Chat: models.Chat{ID: chatID, Type: models.ChatTypePrivate},
				},
			},
			Data: "cmd:/status",
		},
	}
}

// TestProcessUpdate_StuckCommandDoesNotBlockLaterMessages is the regression
// test for issue #2015: a built-in command that hangs must not stop the bot
// from taking further updates.
//
// The test pins the *lane*, not the stall budget:
//
//   - the stall budget is left at its default (60s), far beyond this test, so
//     it cannot be what releases the session — only the lane can;
//   - the update dispatch must not block: the polling worker hands every
//     update to the lane and returns, so a stuck command cannot stop the bot
//     from receiving the next ones;
//   - the later message keeps its place in the session: it is not handled
//     while the command is stuck, and it is delivered once the command
//     returns.
//
// With the lane removed the dispatch blocks inside the stuck handler, the
// polling worker never returns, and this test fails.
func TestProcessUpdate_StuckCommandDoesNotBlockLaterMessages(t *testing.T) {
	handled := make(chan string, 4)
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseStuck := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseStuck)

	// The default stall budget (60s) is deliberately left in place: it is far
	// beyond this test, so it cannot release the session on its own.
	p := &Platform{}
	p.handler = func(_ core.Platform, msg *core.Message) {
		if msg.Content == "/usage" {
			<-release
			return
		}
		handled <- msg.Content
	}

	ctx := context.Background()
	// The poller is the Telegram polling worker: it hands every update to the
	// lane and returns, so one stuck command cannot block the next update.
	pollDone := lanePoll(ctx, p,
		laneTestUpdate(1, "/usage", 100, 7),
		laneTestUpdate(2, "later", 100, 7),
	)

	select {
	case <-pollDone:
	case <-time.After(5 * time.Second):
		t.Fatal("the stuck command blocked the update dispatch: the polling loop would stop receiving updates")
	}

	// The later message keeps its place in the session: it is queued behind
	// the stuck command, not handled alongside it.
	select {
	case got := <-handled:
		t.Fatalf("the later message was handled (%q) while the command was still stuck", got)
	case <-time.After(200 * time.Millisecond):
	}

	// Once the stuck command returns, the lane moves on and the later
	// message is delivered.
	releaseStuck()
	select {
	case got := <-handled:
		if got != "later" {
			t.Fatalf("handled content = %q, want %q", got, "later")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the later message was never delivered")
	}
}

// TestProcessUpdate_StuckHandlerIsAbandonedAfterStallBudget covers the other
// half of the contract: when the stall budget expires, the lane gives up on
// the stuck handler and moves on, so the rest of the session is still served.
func TestProcessUpdate_StuckHandlerIsAbandonedAfterStallBudget(t *testing.T) {
	handled := make(chan string, 4)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })

	p := &Platform{stallTimeout: 200 * time.Millisecond}
	p.handler = func(_ core.Platform, msg *core.Message) {
		if msg.Content == "/usage" {
			<-release
			return
		}
		handled <- msg.Content
	}

	ctx := context.Background()
	lanePoll(ctx, p,
		laneTestUpdate(1, "/usage", 100, 7),
		laneTestUpdate(2, "later", 100, 7),
	)

	select {
	case got := <-handled:
		if got != "later" {
			t.Fatalf("handled content = %q, want %q", got, "later")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the stall budget did not release the session")
	}
}

// TestProcessUpdate_StuckCommandDoesNotBlockOtherSessions covers the
// platform-wide symptom of #2015: a stuck command must not silence the whole
// bot, only its own session.
func TestProcessUpdate_StuckCommandDoesNotBlockOtherSessions(t *testing.T) {
	entered := make(chan struct{}, 4)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })

	p := &Platform{}
	p.handler = func(_ core.Platform, _ *core.Message) {
		entered <- struct{}{}
		<-release
	}

	ctx := context.Background()
	lanePoll(ctx, p,
		laneTestUpdate(1, "/usage", 100, 7),
		laneTestUpdate(2, "hello", 200, 8),
	)

	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("the stuck handler was never entered")
	}

	// A different session must not wait for the stuck one.
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("a stuck command in one session blocked another session")
	}
}

// TestProcessUpdate_PreservesMessageOrderWithinSession guards the ordering
// guarantee: the messages of one session must reach the core handler in the
// order the user sent them. A handler that blocks must not be overtaken by
// the next message of the same session.
func TestProcessUpdate_PreservesMessageOrderWithinSession(t *testing.T) {
	var mu sync.Mutex
	var order []string
	firstEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	secondHandled := make(chan struct{})

	p := &Platform{}
	p.handler = func(_ core.Platform, msg *core.Message) {
		mu.Lock()
		order = append(order, msg.Content)
		mu.Unlock()
		if msg.Content == "first" {
			close(firstEntered)
			<-releaseFirst
			return
		}
		close(secondHandled)
	}

	ctx := context.Background()
	lanePoll(ctx, p,
		laneTestUpdate(1, "first", 100, 7),
		laneTestUpdate(2, "second", 100, 7),
	)

	select {
	case <-firstEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("the first message was never handled")
	}

	// While the first handler is blocked, the second message must wait.
	select {
	case <-secondHandled:
		t.Fatal("the second message overtook the first one in the same session")
	case <-time.After(300 * time.Millisecond):
	}

	close(releaseFirst)

	select {
	case <-secondHandled:
	case <-time.After(2 * time.Second):
		t.Fatal("the second message was never handled")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(order) != 2 || order[0] != "first" || order[1] != "second" {
		t.Fatalf("handled order = %v, want [first second]", order)
	}
}

// TestProcessUpdate_CallbackIsNotBlockedByStuckMessage makes sure callback
// queries have a lane of their own: a permission button must not wait behind
// a message of the same session that is still being handled.
func TestProcessUpdate_CallbackIsNotBlockedByStuckMessage(t *testing.T) {
	entered := make(chan struct{}, 4)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })

	p := &Platform{
		bot:      newStubTelegramBot(),
		selfUser: &models.User{ID: 42, Username: "mybot"},
	}
	p.handler = func(_ core.Platform, _ *core.Message) {
		entered <- struct{}{}
		<-release
	}

	ctx := context.Background()
	lanePoll(ctx, p,
		laneTestUpdate(1, "/usage", 100, 7),
		laneTestCallbackUpdate(2, 100, 7),
	)

	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("the stuck handler was never entered")
	}

	// Same session, but a callback query: it must not queue behind the stuck
	// message.
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("a callback query was blocked behind a stuck message of the same session")
	}
}

// TestProcessUpdate_PanicInHandlerDoesNotStopLaterUpdates makes sure a
// panicking update is contained: the panic must not take the process (and
// with it every other platform) down, and later updates must still be served.
func TestProcessUpdate_PanicInHandlerDoesNotStopLaterUpdates(t *testing.T) {
	handled := make(chan string, 4)
	p := &Platform{}
	p.handler = func(_ core.Platform, msg *core.Message) {
		if msg.Content == "/boom" {
			panic("boom")
		}
		handled <- msg.Content
	}

	ctx := context.Background()
	lanePoll(ctx, p,
		laneTestUpdate(1, "/boom", 100, 7),
		laneTestUpdate(2, "hello", 100, 7),
	)

	select {
	case got := <-handled:
		if got != "hello" {
			t.Fatalf("handled content = %q, want %q", got, "hello")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("later update was not dispatched after a panicking handler")
	}
}

// TestStop_DropsQueuedUpdates makes sure the queued updates of a session are
// dropped when the platform stops: they would be handled by a bot that is no
// longer connected.
func TestStop_DropsQueuedUpdates(t *testing.T) {
	entered := make(chan struct{})
	handled := make(chan string, 4)
	release := make(chan struct{})

	p := &Platform{}
	p.handler = func(_ core.Platform, msg *core.Message) {
		if msg.Content == "first" {
			close(entered)
			<-release
			return
		}
		handled <- msg.Content
	}

	ctx := context.Background()
	lanePoll(ctx, p, laneTestUpdate(1, "first", 100, 7))
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("the first message was never handled")
	}

	// Wait for the update to be queued before stopping the platform.
	<-lanePoll(ctx, p, laneTestUpdate(2, "queued", 100, 7))
	if err := p.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	close(release)

	select {
	case got := <-handled:
		t.Fatalf("queued update %q was handled after Stop", got)
	case <-time.After(500 * time.Millisecond):
	}
}

// TestRunConnection_OlderConnectionStopsDispatchingAfterReconnect binds the
// update dispatch to the polling lifecycle: when the bot reconnects, the
// updates of the previous connection must not be dispatched any more.
func TestRunConnection_OlderConnectionStopsDispatchingAfterReconnect(t *testing.T) {
	handled := make(chan string, 8)

	p := &Platform{httpClient: &http.Client{}}
	p.handler = func(_ core.Platform, msg *core.Message) {
		handled <- msg.Content
	}

	var mu sync.Mutex
	var callbacks []func(context.Context, *models.Update)
	p.newBot = func(_ string, onUpdate func(context.Context, *models.Update), _ *http.Client) (telegramBot, *models.User, func(context.Context), error) {
		mu.Lock()
		callbacks = append(callbacks, onUpdate)
		mu.Unlock()
		return newStubTelegramBot(),
			&models.User{ID: 42, Username: "mybot"},
			func(ctx context.Context) { <-ctx.Done() },
			nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runConnection := func(want int) {
		go func() { _ = p.runConnection(ctx) }()
		for {
			mu.Lock()
			n := len(callbacks)
			mu.Unlock()
			if n >= want {
				return
			}
			time.Sleep(time.Millisecond)
		}
	}

	runConnection(1)
	mu.Lock()
	first := callbacks[0]
	mu.Unlock()

	runConnection(2)
	mu.Lock()
	second := callbacks[1]
	mu.Unlock()

	// The update of the replaced connection must be dropped.
	first(ctx, laneTestUpdate(1, "stale", 100, 7))
	select {
	case got := <-handled:
		t.Fatalf("update of a replaced connection was handled: %q", got)
	case <-time.After(500 * time.Millisecond):
	}

	// The current connection still dispatches.
	second(ctx, laneTestUpdate(2, "current", 100, 7))
	select {
	case got := <-handled:
		if got != "current" {
			t.Fatalf("handled content = %q, want %q", got, "current")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the current connection did not dispatch its update")
	}
}
