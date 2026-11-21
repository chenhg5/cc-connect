package core

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Queue follow-ups during typing cleanup, after the foreground event loop has
// checked its queue but before it releases the session's busy lock.
type lateQueueReaderPlatform struct {
	stubPlatformEngine
	starts        atomic.Int64
	queueOnce     sync.Once
	lateReplyOnce sync.Once
	lateReply     chan struct{}
	queue         func()
}

func (p *lateQueueReaderPlatform) Send(ctx context.Context, replyCtx any, content string) error {
	if err := p.stubPlatformEngine.Send(ctx, replyCtx, content); err != nil {
		return err
	}
	if strings.Contains(content, "late reply") {
		p.lateReplyOnce.Do(func() { close(p.lateReply) })
	}
	return nil
}

func (p *lateQueueReaderPlatform) StartTyping(context.Context, any) func() {
	if p.starts.Add(1) == 2 {
		// Delay the queued turn's foreground reader. If an idle reader was
		// incorrectly started, it can consume the queued EventResult here.
		select {
		case <-p.lateReply:
		case <-time.After(100 * time.Millisecond):
		}
	}
	return func() { p.queueOnce.Do(p.queue) }
}

func runCUJH2LateQueueKeepsForegroundReader(t *testing.T) {
	p := &lateQueueReaderPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}, lateReply: make(chan struct{})}
	a := &cujAgent{}
	e := NewEngine("test", a, []Platform{p}, "", LangEnglish)
	t.Cleanup(func() {
		if err := e.Stop(); err != nil {
			t.Errorf("stop engine: %v", err)
		}
	})
	p.queue = func() {
		a.mu.Lock()
		s := a.sessions[0]
		a.mu.Unlock()
		s.mu.Lock()
		s.reply = "late reply"
		s.mu.Unlock()
		// User actions 2 and 3 arrive as the first turn finishes.
		e.ReceiveMessage(p, &Message{SessionKey: "test:user", Platform: "test", MessageID: "late", Content: "late follow-up", ReplyCtx: "reply"})
		e.ReceiveMessage(p, &Message{SessionKey: "test:user", Platform: "test", MessageID: "third", Content: "third task", ReplyCtx: "reply"})
	}
	// User action 1 starts the foreground turn.
	e.ReceiveMessage(p, &Message{SessionKey: "test:user", Platform: "test", MessageID: "first", Content: "first task", ReplyCtx: "reply"})

	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		session := e.sessions.GetOrCreateActive("test:user")
		lateReplies := 0
		for _, text := range p.getSent() {
			if strings.Contains(text, "late reply") {
				lateReplies++
			}
		}
		// Both follow-ups must produce visible replies and release the
		// foreground turn; a reply relayed by the idle reader is insufficient.
		if lateReplies == 2 && !session.Busy() {
			return
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatalf("queued follow-ups did not both finish: busy=%v history=%+v replies=%q", session.Busy(), session.GetHistory(0), p.getSent())
		}
	}
}
