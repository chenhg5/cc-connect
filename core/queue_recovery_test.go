package core

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type dyingQueueSession struct {
	*queuingAgentSession
	dead atomic.Bool
}

func (s *dyingQueueSession) Alive() bool { return !s.dead.Load() }

func TestDeadSessionQueuedMessageRestartsOnce(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	dead := newQueuingSession("dead")
	dead.alive = false
	fresh := newQueuingSession("fresh")
	agent := &controllableAgent{nextSession: fresh}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
	key := "test:dead-restart"
	session := e.sessions.GetOrCreateActive(key)
	gen, ok := session.TryLock()
	if !ok {
		t.Fatal("failed to lock session")
	}
	state := &interactiveState{agentSession: dead, platform: p, busySession: session}
	e.interactiveMu.Lock()
	e.interactiveStates[key] = state
	e.interactiveMu.Unlock()

	if !e.queueMessageForBusySession(p, &Message{SessionKey: key, Platform: "test", Content: "second", MessageID: "second", ReplyCtx: "reply"}, key) {
		t.Fatal("dead session rejected the queued message")
	}
	go func() {
		for {
			fresh.sendMu.Lock()
			n := len(fresh.sendCalls)
			fresh.sendMu.Unlock()
			if n > 0 {
				fresh.events <- Event{Type: EventResult, Content: "answer", Done: true}
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()
	done := make(chan struct{})
	go func() {
		e.drainOrphanedQueue(session, e.sessions, key, agent, "", gen)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("dead session queue never drained")
	}
	fresh.sendMu.Lock()
	sends := append([]string(nil), fresh.sendCalls...)
	fresh.sendMu.Unlock()
	if len(sends) != 1 || !strings.Contains(sends[0], "second") {
		t.Fatalf("fresh session sends = %v, want second exactly once", sends)
	}
	e.interactiveMu.Lock()
	current := e.interactiveStates[key]
	e.interactiveMu.Unlock()
	if current != nil {
		e.stopUnsolicitedReader(current)
	}
	if sent := p.getSent(); len(sent) != 2 || sent[0] != e.i18n.T(MsgMessageQueued) || sent[1] != "answer" {
		t.Fatalf("platform sends = %v, want one queue notice then answer", sent)
	}
}

func TestDeadForegroundDrainsQueuedMessageAfterError(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	old := &dyingQueueSession{queuingAgentSession: newQueuingSession("old")}
	fresh := newQueuingSession("fresh")
	var starts atomic.Int32
	agent := &controllableAgent{startSessionFn: func(_ context.Context, _ string) (AgentSession, error) {
		if starts.Add(1) == 1 {
			return old, nil
		}
		return fresh, nil
	}}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
	key := "test:foreground-dead"
	session := e.sessions.GetOrCreateActive(key)
	gen, ok := session.TryLock()
	if !ok {
		t.Fatal("failed to lock session")
	}
	done := make(chan struct{})
	go func() {
		e.processInteractiveMessageWith(p, &Message{SessionKey: key, Platform: "test", Content: "first", MessageID: "first"},
			session, agent, e.sessions, key, "", key, gen)
		close(done)
	}()
	deadline := time.After(5 * time.Second)
	for {
		old.sendMu.Lock()
		n := len(old.sendCalls)
		old.sendMu.Unlock()
		if n == 1 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("first prompt was not sent")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	old.dead.Store(true)
	if !e.queueMessageForBusySession(p, &Message{SessionKey: key, Platform: "test", Content: "second", MessageID: "second", ReplyCtx: "reply"}, key) {
		t.Fatal("dead foreground session rejected queued message")
	}
	old.events <- Event{Type: EventError, Error: errors.New("agent exited")}
	go func() {
		for {
			fresh.sendMu.Lock()
			n := len(fresh.sendCalls)
			fresh.sendMu.Unlock()
			if n == 1 {
				fresh.events <- Event{Type: EventResult, Content: "second answer", Done: true}
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("queued message remained stranded after foreground error")
	}
	fresh.sendMu.Lock()
	sends := append([]string(nil), fresh.sendCalls...)
	fresh.sendMu.Unlock()
	if len(sends) != 1 || !strings.Contains(sends[0], "second") {
		t.Fatalf("restarted sends = %v, want second exactly once", sends)
	}
	e.interactiveMu.Lock()
	current := e.interactiveStates[key]
	e.interactiveMu.Unlock()
	if current != nil {
		e.stopUnsolicitedReader(current)
	}
	var notices, answers int
	for _, sent := range p.getSent() {
		if sent == e.i18n.T(MsgMessageQueued) {
			notices++
		}
		if sent == "second answer" {
			answers++
		}
	}
	if notices != 1 || answers != 1 {
		t.Fatalf("notices=%d answers=%d, want one each; sent=%v", notices, answers, p.getSent())
	}
}

func TestQueuedTurnOwnsEventsUntilDrainCompletes(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	backend := newQueuingSession("queued-reader")
	agent := &controllableAgent{nextSession: backend}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
	key := "test:queued-reader"
	session := e.sessions.GetOrCreateActive(key)
	gen, ok := session.TryLock()
	if !ok {
		t.Fatal("failed to lock session")
	}
	done := make(chan struct{})
	go func() {
		e.processInteractiveMessageWith(p, &Message{SessionKey: key, Platform: "test", Content: "first", MessageID: "first"},
			session, agent, e.sessions, key, "", key, gen)
		close(done)
	}()
	waitSends := func(want int) {
		t.Helper()
		deadline := time.After(5 * time.Second)
		for {
			backend.sendMu.Lock()
			n := len(backend.sendCalls)
			backend.sendMu.Unlock()
			if n >= want {
				return
			}
			select {
			case <-deadline:
				t.Fatalf("send calls = %d, want %d", n, want)
			default:
				time.Sleep(time.Millisecond)
			}
		}
	}
	waitSends(1)
	if !e.queueMessageForBusySession(p, &Message{SessionKey: key, Platform: "test", Content: "second", MessageID: "second"}, key) {
		t.Fatal("second message was not queued")
	}
	backend.events <- Event{Type: EventResult, Content: "first answer", Done: true}
	waitSends(2)
	e.interactiveMu.Lock()
	state := e.interactiveStates[key]
	e.interactiveMu.Unlock()
	state.mu.Lock()
	readerStarted := state.unsolicitedCancel != nil
	state.mu.Unlock()
	if readerStarted {
		t.Fatal("unsolicited reader started while queued foreground turn owns Events")
	}
	backend.events <- Event{Type: EventResult, Content: "second answer", Done: true}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("queued turn did not finish")
	}
	e.stopUnsolicitedReader(state)
}

// TestCUJ_QueueStealsResult_Recovery exercises #2366 without a skip on the
// fixing branch; the original reproducer in #2362 remains skipped until merge.
func TestCUJ_QueueStealsResult_Recovery(t *testing.T) {
	const messages = 5
	pA := &stubPlatformEngine{n: "platA"}
	pB := &stubPlatformEngine{n: "platB"}
	e := NewEngine("test", &cujAgent{}, []Platform{pA, pB}, t.TempDir()+"/sessions.json", LangEnglish)
	t.Cleanup(func() { _ = e.Stop() })
	var wg sync.WaitGroup
	for i := 0; i < messages; i++ {
		for _, tc := range []struct {
			platform *stubPlatformEngine
			key      string
		}{
			{pA, "platA:queued"}, {pB, "platB:queued"},
		} {
			wg.Add(1)
			go func(i int, p *stubPlatformEngine, key string) {
				defer wg.Done()
				e.ReceiveMessage(p, &Message{SessionKey: key, Platform: p.Name(),
					MessageID: fmt.Sprintf("%s-%d", key, i), UserID: "queued", UserName: "queued",
					Content: fmt.Sprintf("prompt %d", i), ReplyCtx: "ctx"})
			}(i, tc.platform, tc.key)
		}
	}
	wg.Wait()
	deadline := time.Now().Add(3 * time.Second)
	for {
		counts := [2]int{}
		busy := false
		for j, key := range []string{"platA:queued", "platB:queued"} {
			session := e.sessions.GetOrCreateActive(key)
			busy = busy || session.Busy()
			for _, h := range session.GetHistory(0) {
				if h.Role == "user" {
					counts[j]++
				}
			}
		}
		if counts == [2]int{messages, messages} && !busy {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("queued results stranded: user histories=%v, busy=%v", counts, busy)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestExplicitCleanupDropsQueuedMessages(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", &controllableAgent{}, []Platform{p}, "", LangEnglish)
	key := "test:explicit-reset"
	state := &interactiveState{agentSession: newQueuingSession("old"), platform: p,
		restartQueuedOnExit: true,
		pendingMessages:     []queuedMessage{{platform: p, content: "queued", replyCtx: "queued-ctx"}}}
	e.interactiveMu.Lock()
	e.interactiveStates[key] = state
	e.interactiveMu.Unlock()
	e.cleanupInteractiveState(key, state)
	e.interactiveMu.Lock()
	_, exists := e.interactiveStates[key]
	e.interactiveMu.Unlock()
	if exists {
		t.Fatal("explicit cleanup kept a restart placeholder")
	}
	if sent := p.getSent(); len(sent) != 1 || !strings.Contains(sent[0], "Error") {
		t.Fatalf("queued message was not notified of reset: %v", sent)
	}
}

// TestCmdNewNilSnapshotCleansConcurrentState forces the /new interleaving
// where it observes no state, then a concurrent message starts a session
// before /new performs cleanup. The new process must not survive the reset.
func TestCmdNewNilSnapshotCleansConcurrentState(t *testing.T) {
	e := newTestEngine()
	key := "test:new-start-race"
	e.interactiveMu.Lock()
	observed := e.interactiveStates[key]
	e.interactiveMu.Unlock()
	if observed != nil {
		t.Fatal("expected an empty initial state")
	}
	backend := newControllableSession("concurrent-start")
	started := make(chan struct{})
	go func() {
		e.interactiveMu.Lock()
		e.interactiveStates[key] = &interactiveState{agentSession: backend}
		e.interactiveMu.Unlock()
		close(started)
	}()
	<-started
	e.cleanupInteractiveStateForNew(key, observed)
	select {
	case <-backend.closed:
	default:
		t.Fatal("/new left the concurrently started agent process alive")
	}
	e.interactiveMu.Lock()
	_, exists := e.interactiveStates[key]
	e.interactiveMu.Unlock()
	if exists {
		t.Fatal("/new left the concurrently created interactive state behind")
	}
}
