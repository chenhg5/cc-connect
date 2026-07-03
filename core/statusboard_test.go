package core

import (
	"context"
	"strings"
	"testing"
	"time"

	tgbot "github.com/go-telegram/bot"
)

func TestStatusBoard_DisabledIsSafeNoOp(t *testing.T) {
	sb := NewStatusBoard(false, "", "", "")
	if sb.enabled {
		t.Fatal("expected disabled board")
	}
	// None of these should panic or attempt any network call (sb.bot is nil).
	sb.OnTurnStart("dev-pro", "some task")
	sb.OnTurnEnd("dev-pro")
	sb.OnRelayDispatched("chef-flash-seat", "dev-pro", "fix the bug")
	sb.OnHungDetected("dev-pro", "112s")
	sb.recoverIfWasHung("dev-pro", "idle")

	if len(sb.seats) != 0 {
		t.Fatalf("disabled board should never record seat state, got %d entries", len(sb.seats))
	}
}

func TestStatusBoard_InvalidChatIDDisables(t *testing.T) {
	sb := NewStatusBoard(true, "fake-token", "not-a-number", "")
	if sb.enabled {
		t.Fatal("expected board to self-disable on unparseable chat_id")
	}
}

// enabledBoardNoBot exercises the real transition()/render() code paths
// with enabled=true but no bot client — render() no-ops on sb.bot==nil,
// so seat-state bookkeeping is tested without a network dependency.
func enabledBoardNoBot() *StatusBoard {
	return &StatusBoard{enabled: true, seats: make(map[string]*seatStatus)}
}

func TestStatusBoard_TransitionDedup(t *testing.T) {
	sb := enabledBoardNoBot()
	sb.OnTurnStart("dev-pro", "task A")
	if len(sb.seats) != 1 || sb.seats["dev-pro"].State != "working" {
		t.Fatalf("expected 1 working seat entry, got %+v", sb.seats)
	}

	// Same state+detail again must not create a second entry or otherwise
	// change recorded state.
	sb.OnTurnStart("dev-pro", "task A")
	if len(sb.seats) != 1 {
		t.Fatalf("expected dedup to keep 1 seat entry, got %d", len(sb.seats))
	}

	sb.OnTurnEnd("dev-pro")
	if sb.seats["dev-pro"].State != "idle" {
		t.Fatalf("expected state to update to idle, got %q", sb.seats["dev-pro"].State)
	}
}

func TestStatusBoard_RenderEnabledDoesNotSelfDeadlock(t *testing.T) {
	botClient, err := tgbot.New("123456:fake", tgbot.WithSkipGetMe())
	if err != nil {
		t.Fatalf("create bot client: %v", err)
	}
	sb := &StatusBoard{
		enabled:     true,
		bot:         botClient,
		chatID:      1,
		minInterval: 0,
		seats:       make(map[string]*seatStatus),
	}
	sb.seats["dev-pro"] = &seatStatus{State: "working", Detail: "task A"}
	sb.messageID = 1

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan struct{})
	go func() {
		sb.render(ctx)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("render blocked; likely self-deadlocked on status board mutex")
	}
}

func TestStatusBoard_RecoverIfWasHung(t *testing.T) {
	sb := enabledBoardNoBot()
	sb.OnHungDetected("dev-pro", "200s")
	if sb.seats["dev-pro"].State != "hung" {
		t.Fatal("expected seat to be marked hung")
	}

	sb.recoverIfWasHung("dev-pro", "working")
	if sb.seats["dev-pro"].State != "working" {
		t.Fatalf("expected recovery to clear hung state, got %q", sb.seats["dev-pro"].State)
	}

	// Calling recoverIfWasHung on a seat that was never hung must be a no-op.
	sb.recoverIfWasHung("dev-swift", "idle")
	if _, ok := sb.seats["dev-swift"]; ok {
		t.Fatal("recoverIfWasHung must not create an entry for a seat that was never hung")
	}
}

func TestStatusBoard_RelayDispatchIncludesSourceAndTask(t *testing.T) {
	sb := enabledBoardNoBot()
	sb.OnRelayDispatched("chef-flash-seat", "dev-pro", "fix the whitescreen bug")

	st := sb.seats["dev-pro"]
	if st == nil || st.State != "assigned" {
		t.Fatalf("expected dev-pro marked assigned, got %+v", st)
	}
	if !strings.Contains(st.Detail, "chef-flash-seat") || !strings.Contains(st.Detail, "fix the whitescreen bug") {
		t.Fatalf("expected detail to include source seat and task, got %q", st.Detail)
	}
}

func TestStatusBoard_RenderTextSortedAndFormatted(t *testing.T) {
	sb := &StatusBoard{
		enabled: true,
		seats: map[string]*seatStatus{
			"dev-swift": {State: "idle"},
			"dev-pro":   {State: "hung", Detail: "last output 90s ago"},
		},
	}
	text := sb.renderText()

	devProIdx := strings.Index(text, "dev-pro")
	devSwiftIdx := strings.Index(text, "dev-swift")
	if devProIdx == -1 || devSwiftIdx == -1 {
		t.Fatalf("expected both seats in rendered text, got: %s", text)
	}
	if devProIdx > devSwiftIdx {
		t.Fatalf("expected seats sorted alphabetically (dev-pro before dev-swift), got: %s", text)
	}
	if !strings.Contains(text, "last output 90s ago") {
		t.Fatalf("expected hung detail in rendered text, got: %s", text)
	}
}
