package core

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

// syncBuffer is a race-safe log sink for assertions on slog output.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// TestUnsolicitedReader_DropOnCancellationIsLogged: when a stalled platform
// send keeps the flush queue full and the reader is cancelled, the dropped
// chunk must be logged — the operator-facing log must not claim a flush that
// never reached the platform.
func TestUnsolicitedReader_DropOnCancellationIsLogged(t *testing.T) {
	var buf syncBuffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(prev)

	gate := make(chan struct{})
	defer close(gate)
	p := &slowSendPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}, gate: gate}
	sess := newControllableSession("unsol-drop")
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	defer func() { _ = e.Stop() }()

	session := e.sessions.GetOrCreateActive("test:drop:u1")
	state := &interactiveState{agentSession: sess, platform: p, replyCtx: "ctx"}
	e.startUnsolicitedReader(state, session, e.sessions, "test:drop:u1", "")

	// Each round emits a flush and an error notice; the first item is taken
	// by the sender (stuck behind the gate) and the next four fill the
	// queue, so later enqueues block until cancellation drops them.
	for i := 0; i < 6; i++ {
		sess.events <- Event{Type: EventText, Content: fmt.Sprintf("seg-%d ", i)}
		sess.events <- Event{Type: EventError, Error: errors.New("turn failed")}
	}
	deadline := time.Now().Add(2 * time.Second)
	for len(sess.events) > 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond) // let the blocked enqueues park

	e.stopUnsolicitedReader(state) // cancellation with a full queue

	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(buf.String(), "dropping queued chunks on cancellation") {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("expected a drop-on-cancellation log line, got %q", buf.String())
}
