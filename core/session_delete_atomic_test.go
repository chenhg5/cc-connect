package core

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestDeleteAgentSessionIfInactive_ActivationWins(t *testing.T) {
	for _, key := range []string{"caller", "other"} {
		t.Run(key, func(t *testing.T) {
			sm := NewSessionManager("")
			sm.SwitchToAgentSession(key, "target", "stub", "target")
			called := false
			err := sm.DeleteAgentSessionIfInactive("target", "caller", func() error {
				called = true
				return nil
			})
			want := ErrAgentSessionActiveElsewhere
			if key == "caller" {
				want = ErrAgentSessionActive
			}
			if !errors.Is(err, want) || called {
				t.Fatalf("delete = %v, callback called = %v; want %v without deletion", err, called, want)
			}
			if got := sm.GetOrCreateActive(key).GetAgentSessionID(); got != "target" {
				t.Fatalf("active session = %q, want target", got)
			}
		})
	}
}

func TestDeleteAgentSessionIfInactive_LoadedSessionsRejectLateIDs(t *testing.T) {
	store := filepath.Join(t.TempDir(), "sessions.json")
	sm := NewSessionManager(store)
	victim := sm.NewSideSession("other", "target")
	victim.SetAgentSessionID("target", "stub")
	sm.GetOrCreateActive("other")
	sm.Save()

	loaded := NewSessionManager(store)
	current := loaded.GetOrCreateActive("other")
	if err := loaded.DeleteAgentSessionIfInactive("target", "caller", func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	current.SetAgentSessionID("target", "stub")
	current.SetAgentInfo("target", "stub", "late")
	loaded.SetSessionName("target", "late name")
	if loaded.GetSessionName("target") != "" {
		t.Fatal("late event restored a deleted session name")
	}
	if current.CompareAndSetAgentSessionID("target", "stub") || current.GetAgentSessionID() != "" {
		t.Fatal("loaded session accepted an ID deleted by its manager")
	}
	if NewSessionManager(store).FindByID(victim.ID) != nil {
		t.Fatal("deleted session was not removed from the persisted snapshot")
	}
}

func TestDeleteAgentSessionIfInactive_UnrelatedChatsRemainResponsive(t *testing.T) {
	sm := NewSessionManager(filepath.Join(t.TempDir(), "sessions.json"))
	entered := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	finish := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(finish)
	deleted := make(chan error, 1)
	go func() {
		deleted <- sm.DeleteAgentSessionIfInactive("target", "caller", func() error {
			close(entered)
			<-release
			return nil
		})
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("deletion did not start")
	}

	responsive := make(chan struct{})
	go func() {
		s := sm.GetOrCreateActive("unrelated")
		s.SetAgentSessionID("different-target", "stub")
		sm.Save()
		sm.ListSessions("unrelated")
		sm.SwitchToAgentSession("another-chat", "another-target", "stub", "other")
		close(responsive)
	}()
	select {
	case <-responsive:
	case <-time.After(2 * time.Second):
		t.Fatal("agent deletion blocked unrelated session operations")
	}
	finish()
	select {
	case err := <-deleted:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("deletion did not finish")
	}
}

func TestDeleteAgentSessionIfInactive_ConcurrentDeletes(t *testing.T) {
	for _, firstFails := range []bool{false, true} {
		name := "success"
		if firstFails {
			name = "failure"
		}
		t.Run(name, func(t *testing.T) {
			sm := NewSessionManager("")
			entered := make(chan struct{})
			release := make(chan struct{})
			var releaseOnce sync.Once
			finish := func() { releaseOnce.Do(func() { close(release) }) }
			t.Cleanup(finish)
			first := make(chan error, 1)
			firstError := errors.New("first deletion failed")
			go func() {
				first <- sm.DeleteAgentSessionIfInactive("target", "caller", func() error {
					close(entered)
					<-release
					if firstFails {
						return firstError
					}
					return nil
				})
			}()
			select {
			case <-entered:
			case <-time.After(2 * time.Second):
				t.Fatal("first deletion did not start")
			}
			second := make(chan error, 1)
			secondCalled := make(chan struct{}, 1)
			go func() {
				second <- sm.DeleteAgentSessionIfInactive("target", "other", func() error {
					secondCalled <- struct{}{}
					return nil
				})
			}()
			select {
			case <-second:
				t.Fatal("concurrent deletion did not wait for the first attempt")
			case <-time.After(50 * time.Millisecond):
			}
			finish()
			select {
			case err := <-first:
				if firstFails != errors.Is(err, firstError) {
					t.Fatalf("first deletion = %v, failure expected = %v", err, firstFails)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("first deletion did not finish")
			}
			select {
			case err := <-second:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("first deletion did not wake the concurrent attempt")
			}
			if called := len(secondCalled) != 0; called != firstFails {
				t.Fatalf("second callback called = %v, want %v", called, firstFails)
			}
		})
	}
}
