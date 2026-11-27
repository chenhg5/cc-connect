package wecom

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestWSQuota_ReconnectWhileQueuedPreservesFreshAck(t *testing.T) {
	f := newWSQuotaFixture(t, quotaOptions(1, 100))
	p := f.p
	// Reserve the only slot without sending, then refund it after simulating
	// the previous connection's teardown. The fixture serves the fresh ACK.
	reservation := takeQuota(t, p.quota, quotaTarget{recipient: "chat"})
	defer reservation.finish(false)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- p.Send(ctx, wsQuotaContext("chat", "user"), "queued") }()
	deadline := time.Now().Add(time.Second)
	for unauthorizedWaiters(p) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("sender never queued for quota")
		}
		time.Sleep(time.Millisecond)
	}

	// Match runConnection's ACK drain: unsent queued requests must not belong
	// to the disconnected socket or inherit its failure.
	registered := 0
	p.pendingAcks.Range(func(key, value any) bool {
		registered++
		value.(chan wsAckResult) <- wsAckResult{err: errors.New("old connection closed")}
		p.pendingAcks.Delete(key)
		return true
	})
	if registered != 0 {
		t.Errorf("queued sender registered %d ACK listeners before sending", registered)
	}
	reservation.finish(false)
	if err := <-done; err != nil {
		t.Fatalf("fresh connection ACK lost after queued reconnect: %v", err)
	}
	if got := f.count("aibot_send_msg"); got != 1 {
		t.Fatalf("sent %d messages, want 1", got)
	}
}
