package core

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// receiptAgentSession makes the external session fixture safe for engine
// teardown racing with the processing goroutine's liveness checks.
type receiptAgentSession struct {
	*queuingAgentSession
	closeOnce sync.Once
}

func (s *receiptAgentSession) Alive() bool {
	select {
	case <-s.closed:
		return false
	default:
		return true
	}
}

func (s *receiptAgentSession) Close() error {
	s.closeOnce.Do(func() { close(s.closed); close(s.events) })
	return nil
}

// receiptStartAgent holds the external agent startup boundary until released.
// This lets tests observe acknowledgements without relying on startup timing.
type receiptStartAgent struct {
	stubAgent
	session *receiptAgentSession
	entered chan struct{}
	release chan struct{}
	once    sync.Once
	err     error
}

func (a *receiptStartAgent) StartSession(ctx context.Context, _ string) (AgentSession, error) {
	a.once.Do(func() { close(a.entered) })
	select {
	case <-a.release:
		return a.session, a.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

type receiptAckEnv struct {
	*cujEnv
	startup *receiptStartAgent
}

func newReceiptAckEnv(t *testing.T, startErr error) *receiptAckEnv {
	t.Helper()
	p := &stubPlatformEngine{n: "test"}
	a := &receiptStartAgent{
		session: &receiptAgentSession{queuingAgentSession: newQueuingSession("receipt-session")},
		entered: make(chan struct{}), release: make(chan struct{}), err: startErr,
	}
	e := NewEngine("receipt", a, []Platform{p}, t.TempDir()+"/sessions.json", LangEnglish)
	t.Cleanup(func() { _ = e.Stop() })
	return &receiptAckEnv{
		cujEnv: &cujEnv{t: t, engine: e, plat: p}, startup: a,
	}
}

func (env *receiptAckEnv) send(id, content string, timestamp int64) {
	// A platform adapter uses OnAccepted to record its visible reaction on the
	// original message. Keep the boundary visible through the platform recorder.
	env.engine.ReceiveMessage(env.plat, &Message{
		SessionKey: "test:receipt-user", Platform: "test", UserID: "receipt-user",
		MessageID: id, Content: content, ReplyCtx: id, UserMessageTimeMs: timestamp,
		OnAccepted: func() {
			_ = env.plat.Reply(context.Background(), id, "receipt:"+id)
		},
	})
}

func (env *receiptAckEnv) assertReceipts(ids ...string) {
	env.t.Helper()
	var got []string
	for _, message := range env.plat.getSent() {
		if strings.HasPrefix(message, "receipt:") {
			got = append(got, strings.TrimPrefix(message, "receipt:"))
		}
	}
	if fmt.Sprint(got) != fmt.Sprint(ids) {
		env.t.Fatalf("visible receipts = %v, want %v", got, ids)
	}
}

func (env *receiptAckEnv) awaitStartup() {
	env.t.Helper()
	select {
	case <-env.startup.entered:
	case <-time.After(3 * time.Second):
		env.t.Fatal("agent startup was not reached")
	}
}

func (env *receiptAckEnv) awaitSendCount(count int) {
	env.t.Helper()
	env.waitFor("agent send", 3*time.Second, func() bool {
		env.startup.session.sendMu.Lock()
		defer env.startup.session.sendMu.Unlock()
		return len(env.startup.session.sendCalls) >= count
	})
}

func (env *receiptAckEnv) awaitVisible(content string) {
	env.t.Helper()
	env.waitFor(content, 3*time.Second, func() bool {
		for _, message := range env.plat.getSent() {
			if strings.Contains(message, content) {
				return true
			}
		}
		return false
	})
}

func TestReceiveMessage_ReceiptNotSentForRejectedMessages(t *testing.T) {
	env := newReceiptAckEnv(t, nil)
	env.engine.maxQueuedMessages = 1
	env.send("first", "first task", 2000)
	env.awaitStartup()
	close(env.startup.release)
	env.awaitSendCount(1)
	env.send("queued", "next task", 3000)
	env.assertReceipts("first", "queued")

	// These all go through the real public entrypoint while the first turn is busy.
	env.send("overflow", "queue is full", 4000)
	env.awaitVisible(fmt.Sprintf(env.engine.i18n.T(MsgQueueFull), 1))
	env.send("stale", "redelivered older task", 1000)
	env.send("command", "/status", 5000)
	env.engine.SetDisabledCommands([]string{"shell"})
	env.send("disabled-command", "/shell echo no", 5500)
	env.awaitVisible(fmt.Sprintf(env.engine.i18n.T(MsgCommandDisabled), "/shell"))
	env.send("empty", "  ", 6000)
	env.assertReceipts("first", "queued")
}

func TestReceiveMessage_ReceiptSurvivesFailureWithoutRepeating(t *testing.T) {
	for _, startupFailure := range []bool{true, false} {
		t.Run(fmt.Sprintf("startup_failure=%v", startupFailure), func(t *testing.T) {
			var startErr error
			if startupFailure {
				startErr = errors.New("receipt startup failure")
			}
			env := newReceiptAckEnv(t, startErr)
			env.send("request", "do the task", 1000)
			env.awaitStartup()
			env.assertReceipts("request")
			close(env.startup.release)
			if startupFailure {
				env.awaitVisible("failed to start agent session")
			} else {
				env.awaitSendCount(1)
				env.startup.session.events <- Event{Type: EventError, Error: errors.New("receipt turn failure")}
				env.awaitVisible("receipt turn failure")
			}
			// The user sees an error as well as the original receipt, never a
			// second receipt claiming the failed operation was newly accepted.
			env.assertReceipts("request")
		})
	}
}
