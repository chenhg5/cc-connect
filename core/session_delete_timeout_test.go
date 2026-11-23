package core

import (
	"errors"
	"sync"
	"testing"
	"time"
)

func TestDeletionWaitTimeoutDoesNotReleaseReservation(t *testing.T) {
	sm := NewSessionManager("")
	sm.deletionWaitTimeout = 20 * time.Millisecond
	victim := sm.NewSideSession("other", "victim")
	victim.SetAgentSessionID("target", "stub")
	sm.GetOrCreateActive("other")
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	finish := func() { once.Do(func() { close(release) }) }
	t.Cleanup(finish)
	done := make(chan error, 1)
	go func() {
		done <- sm.DeleteAgentSessionIfInactive("target", "caller", func() error {
			close(entered)
			<-release
			return errors.New("confirmed failure")
		})
	}()
	<-entered
	for i := 0; i < 2; i++ {
		if sm.SwitchToAgentSession("switcher", "target", "stub", "") != nil {
			t.Fatal("timed-out wait activated a still-deleting ID")
		}
		if !errors.Is(sm.AgentSessionDeletionError("target"), ErrAgentSessionDeletionPending) {
			t.Fatal("wait timeout cleared the deletion reservation")
		}
	}
	if _, err := sm.SwitchSession("other", victim.ID); !errors.Is(err, ErrAgentSessionDeletionPending) {
		t.Fatalf("local switch error = %v", err)
	}
	if err := sm.DeleteAgentSessionIfInactive("target", "caller", func() error { t.Error("second deletion ran"); return nil }); !errors.Is(err, ErrAgentSessionDeletionPending) {
		t.Fatalf("second delete error = %v", err)
	}
	started := time.Now()
	sm.PruneDuplicateSessions(false)
	if time.Since(started) > time.Second {
		t.Fatal("pruning waited without a bound")
	}
	finish()
	if err := <-done; err == nil {
		t.Fatal("expected confirmed adapter failure")
	}
	if sm.SwitchToAgentSession("switcher", "target", "stub", "") == nil {
		t.Fatal("confirmed failed deletion did not release activation")
	}
}

func TestUncertainDeletionQuarantinesActivation(t *testing.T) {
	sm := NewSessionManager("")
	victim := sm.NewSideSession("other", "victim")
	victim.SetAgentSessionID("target", "stub")
	sm.GetOrCreateActive("other")
	err := sm.DeleteAgentSessionIfInactive("target", "caller", func() error { return ErrSessionDeletionUncertain })
	if !errors.Is(err, ErrSessionDeletionUncertain) {
		t.Fatal(err)
	}
	if sm.SwitchToAgentSession("switcher", "target", "stub", "") != nil {
		t.Fatal("uncertain deletion allowed activation")
	}
	if _, err := sm.SwitchSession("other", victim.ID); !errors.Is(err, ErrSessionDeletionUncertain) {
		t.Fatalf("local activation error = %v", err)
	}
	if err := sm.DeleteAgentSessionIfInactive("target", "caller", func() error { t.Error("uncertain deletion retried"); return nil }); !errors.Is(err, ErrSessionDeletionUncertain) {
		t.Fatalf("retry error = %v", err)
	}
	current := sm.GetOrCreateActive("other")
	victim.AddHistory("user", "keep this history")
	sm.PruneDuplicateSessions(true)
	if sm.GetOrCreateActive("other") != current {
		t.Fatal("pruning activated quarantined target")
	}
	if sm.FindByID(victim.ID) == nil {
		t.Fatal("uncertain outcome falsely removed local history")
	}
	if sm.SwitchToAgentSession("unrelated", "safe", "stub", "") == nil {
		t.Fatal("unrelated session was blocked")
	}
}
