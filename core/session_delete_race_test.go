package core

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// The deletion callback is entered only after the active-session check. Keep it
// blocked while another chat tries to activate the same agent session.
type blockingSessionDeleter struct {
	entered chan struct{}
	release chan struct{}
	err     error
}

func (d *blockingSessionDeleter) DeleteSession(context.Context, string) error {
	close(d.entered)
	<-d.release
	return d.err
}

// A stale list intentionally retains the target after deletion, exercising the
// command's race handling rather than relying on ListSessions to hide it.
type blockingDeleteListAgent struct {
	stubListAgent
	*blockingSessionDeleter
}

// Card navigation reaches the same activation reservation as text /switch.
// It must not revive a deleted ID even when ListSessions returns a stale row.
func TestDeleteSingleSessionReply_BlocksConcurrentCardSwitch(t *testing.T) {
	for _, fails := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[fails], func(t *testing.T) {
			d := &blockingSessionDeleter{entered: make(chan struct{}), release: make(chan struct{})}
			if fails {
				d.err = errors.New("delete failed")
			}
			a := &blockingDeleteListAgent{stubListAgent: stubListAgent{sessions: []AgentSessionInfo{{ID: "target", Summary: "Target"}}}, blockingSessionDeleter: d}
			e := NewEngine("test", a, nil, "", LangEnglish)
			e.sessions.GetOrCreateActive("test:other")
			var once sync.Once
			release := func() { once.Do(func() { close(d.release) }) }
			t.Cleanup(release)
			deleted := make(chan string, 1)
			go func() {
				deleted <- e.deleteSingleSessionReply(&Message{SessionKey: "test:caller"}, d, &AgentSessionInfo{ID: "target"})
			}()
			select {
			case <-d.entered:
			case <-time.After(2 * time.Second):
				t.Fatal("deletion did not start")
			}
			switched := make(chan struct{})
			go func() { e.executeCardAction("/switch", "target", "test:other"); close(switched) }()
			select {
			case <-switched:
				t.Fatal("card activation did not wait for deletion")
			case <-time.After(50 * time.Millisecond):
			}
			release()
			select {
			case <-deleted:
			case <-time.After(2 * time.Second):
				t.Fatal("deletion did not finish")
			}
			select {
			case <-switched:
			case <-time.After(2 * time.Second):
				t.Fatal("card activation remained blocked")
			}
			got := e.sessions.GetOrCreateActive("test:other").GetAgentSessionID()
			if (got == "target") != fails {
				t.Fatalf("card target = %q after deletion failure=%v", got, fails)
			}
		})
	}
}

func TestDeleteSingleSessionReply_BlocksConcurrentSwitch(t *testing.T) {
	const targetID = "session-to-delete"
	activationPaths := []struct {
		name     string
		activate func(*SessionManager, *Session, *Session) string
	}{
		{"SwitchSession", func(sm *SessionManager, victim, current *Session) string {
			s, err := sm.SwitchSession("test:other", victim.ID)
			if err != nil {
				return ""
			}
			return s.GetAgentSessionID()
		}},
		{"SwitchToAgentSessionExisting", func(sm *SessionManager, victim, current *Session) string {
			s := sm.SwitchToAgentSession("test:other", targetID, "stub", "target")
			if s == nil {
				return ""
			}
			return s.GetAgentSessionID()
		}},
		{"SwitchToAgentSessionNew", func(sm *SessionManager, victim, current *Session) string {
			s := sm.SwitchToAgentSession("test:new-chat", targetID, "stub", "target")
			if s == nil {
				return ""
			}
			return s.GetAgentSessionID()
		}},
		{"SetAgentSessionID", func(sm *SessionManager, victim, current *Session) string {
			current.SetAgentSessionID(targetID, "stub")
			return current.GetAgentSessionID()
		}},
		{"SetAgentInfo", func(sm *SessionManager, victim, current *Session) string {
			current.SetAgentInfo(targetID, "stub", "target")
			return current.GetAgentSessionID()
		}},
		{"CompareAndSetAgentSessionID", func(sm *SessionManager, victim, current *Session) string {
			current.CompareAndSetAgentSessionID(targetID, "stub")
			return current.GetAgentSessionID()
		}},
		{"NewSession", func(sm *SessionManager, victim, current *Session) string {
			s := sm.NewSession("test:other", "target")
			s.SetAgentSessionID(targetID, "stub")
			return s.GetAgentSessionID()
		}},
		{"GetOrCreateActive", func(sm *SessionManager, victim, current *Session) string {
			s := sm.GetOrCreateActive("test:new-chat")
			s.SetAgentSessionID(targetID, "stub")
			return s.GetAgentSessionID()
		}},
		{"NewSideSession", func(sm *SessionManager, victim, current *Session) string {
			s := sm.NewSideSession("test:other", "target")
			s.SetAgentSessionID(targetID, "stub")
			return s.GetAgentSessionID()
		}},
		{"PruneDuplicateSessions", func(sm *SessionManager, victim, current *Session) string {
			sm.PruneDuplicateSessions(true)
			return sm.GetOrCreateActive("test:other").GetAgentSessionID()
		}},
	}
	for _, deletionFails := range []bool{false, true} {
		outcome := "success"
		if deletionFails {
			outcome = "failure"
		}
		for _, path := range activationPaths {
			t.Run(outcome+"/"+path.name, func(t *testing.T) {
				e := NewEngine("test", &stubListAgent{}, nil, "", LangEnglish)
				victim := e.sessions.NewSideSession("test:other", "target")
				victim.SetAgentSessionID(targetID, "stub")
				victim.AddHistory("user", "preserve this conversation")
				current := e.sessions.GetOrCreateActive("test:other")
				e.sessions.SetSessionName(targetID, "Target")
				deleter := &blockingSessionDeleter{entered: make(chan struct{}), release: make(chan struct{})}
				if deletionFails {
					deleter.err = errors.New("deletion failed")
				}
				var releaseOnce sync.Once
				release := func() { releaseOnce.Do(func() { close(deleter.release) }) }
				t.Cleanup(release)
				deleted := make(chan string, 1)
				go func() {
					deleted <- e.deleteSingleSessionReply(&Message{SessionKey: "test:caller"}, deleter,
						&AgentSessionInfo{ID: targetID, Summary: "target"})
				}()
				select {
				case <-deleter.entered:
				case <-time.After(2 * time.Second):
					t.Fatal("DeleteSession was not entered")
				}

				started := make(chan struct{})
				activated := make(chan string, 1)
				go func() {
					close(started)
					activated <- path.activate(e.sessions, victim, current)
				}()
				<-started
				select {
				case id := <-activated:
					t.Fatalf("activation completed with %q while DeleteSession was blocked", id)
				case <-time.After(50 * time.Millisecond):
				}
				release()

				select {
				case reply := <-deleted:
					want := "Session deleted: Target"
					if deletionFails {
						want = "deletion failed"
					}
					if !strings.Contains(reply, want) {
						t.Fatalf("delete reply = %q, want %q", reply, want)
					}
				case <-time.After(2 * time.Second):
					t.Fatal("deletion did not finish")
				}
				select {
				case id := <-activated:
					if deletionFails && id != targetID {
						t.Fatalf("failed deletion prevented activation: got %q", id)
					}
					if !deletionFails && id == targetID {
						t.Fatal("activation resurrected the deleted agent session")
					}
				case <-time.After(2 * time.Second):
					t.Fatal("activation stayed blocked after deletion returned")
				}
				if !deletionFails {
					if e.sessions.FindByID(victim.ID) != nil {
						t.Fatal("deleted local session remains")
					}
					if e.sessions.GetSessionName(targetID) != "" {
						t.Fatal("deleted session name remains")
					}
				}
			})
		}
	}
}
