package codex

import (
	"context"
	"github.com/chenhg5/cc-connect/core"
	"testing"
)

func TestProgress_PublicMessagesAreNotClassifiedAsPrivateReasoning(t *testing.T) {
	s := &appServerSession{events: make(chan core.Event, 10), ctx: context.Background()}
	s.pendingMsgs = []string{"Read configuration; implementing validation next"}
	s.flushPendingProgress()
	ev := <-s.events
	if ev.Type != core.EventProgress || ev.Content == "" {
		t.Fatal(ev)
	}
	cs := &codexSession{events: make(chan core.Event, 10), ctx: context.Background(), pendingMsgs: []string{"Running regression tests"}}
	cs.flushPendingProgress()
	ev = <-cs.events
	if ev.Type != core.EventProgress || ev.Content == "" {
		t.Fatal(ev)
	}
}
