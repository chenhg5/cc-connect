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
	// A retry is the re-check: a failure that is not a confirmed deletion,
	// uncertain or not, keeps the quarantine in place.
	for _, retryErr := range []error{ErrSessionDeletionUncertain, errors.New("still unknown")} {
		if err := sm.DeleteAgentSessionIfInactive("target", "caller", func() error { return retryErr }); !errors.Is(err, retryErr) {
			t.Fatalf("retry error = %v, want %v", err, retryErr)
		}
		if !errors.Is(sm.AgentSessionDeletionError("target"), ErrSessionDeletionUncertain) {
			t.Fatalf("failed retry (%v) lifted the quarantine", retryErr)
		}
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

// An uncertain deletion must not disable pruning, and a confirmed retry is
// the reconciliation that lifts the quarantine.
func TestUncertainDeletionKeepsPruningAndIsLiftedByConfirmedRetry(t *testing.T) {
	sm := NewSessionManager("")
	const user1, user2, user3 = "feishu:oc_chat:ou_1", "feishu:oc_chat:ou_2", "feishu:oc_chat:ou_3"
	victim := sm.NewSideSession(user1, "victim")
	victim.SetAgentSessionID("target", "stub")
	victim.AddHistory("user", "keep this history")
	kept := sm.GetOrCreateActive(user1)
	kept.AddHistory("user", "newest")
	kept.mu.Lock()
	kept.UpdatedAt = time.Now().Add(time.Hour)
	kept.mu.Unlock()
	dup := sm.GetOrCreateActive(user2)
	dup.AddHistory("user", "older duplicate")
	other := sm.GetOrCreateActive(user3)
	other.AddHistory("user", "another older duplicate")
	if err := sm.DeleteAgentSessionIfInactive("target", "caller", func() error { return ErrSessionDeletionUncertain }); !errors.Is(err, ErrSessionDeletionUncertain) {
		t.Fatal(err)
	}

	result := sm.PruneDuplicateSessions(true)
	if sm.FindByID(dup.ID) != nil || sm.FindByID(other.ID) != nil || len(result.RemovedSessions) != 2 {
		t.Fatalf("pruning stopped after an uncertain deletion: %+v", result)
	}
	if s := sm.FindByID(victim.ID); s == nil || len(s.GetHistory(0)) != 1 {
		t.Fatal("pruning merged or removed a quarantined session")
	}
	if got := sm.ActiveSessionID(user1); got != kept.ID {
		t.Fatalf("active session = %q, want kept %q (never the quarantined one)", got, kept.ID)
	}
	if !errors.Is(sm.AgentSessionDeletionError("target"), ErrSessionDeletionUncertain) {
		t.Fatal("pruning lifted the quarantine")
	}

	retried := false
	if err := sm.DeleteAgentSessionIfInactive("target", "caller", func() error { retried = true; return nil }); err != nil || !retried {
		t.Fatalf("confirmed retry: err=%v ran=%v", err, retried)
	}
	if err := sm.AgentSessionDeletionError("target"); err != nil {
		t.Fatalf("confirmed retry kept the quarantine: %v", err)
	}
	if sm.FindByID(victim.ID) != nil {
		t.Fatal("confirmed retry kept the deleted local session")
	}
	if sm.SwitchToAgentSession("switcher", "target", "stub", "") != nil {
		t.Fatal("confirmed deletion allowed activation of the deleted ID")
	}
}
