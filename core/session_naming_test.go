package core

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

type namingTestSession struct {
	stubAgentSession
	names     chan string
	finish    chan struct{}
	supported bool
}

func (s *namingTestSession) SupportsSessionNaming() bool { return s.supported }
func (s *namingTestSession) SetSessionName(ctx context.Context, name string) error {
	s.names <- name
	select {
	case <-s.finish:
		return errors.New("native naming unavailable")
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestTeamNativeSessionNaming_CreateResumeAndReuse(t *testing.T) {
	for _, resume := range []bool{false, true} {
		t.Run(map[bool]string{false: "create", true: "resume"}[resume], func(t *testing.T) {
			dir := t.TempDir()
			store := NewTeamProjectStore(filepath.Join(dir, "projects.json"))
			if err := store.Put(TeamProject{ID: "project", Name: "容器动态挂载", Platform: "test", ChatID: "group", Bots: []string{"member"}, Directories: []string{dir}}); err != nil {
				t.Fatal(err)
			}
			store.SetMemberLabel("member", "Johnson · planner")
			store.SetMemberDisplayName("member", "Johnson")
			native := &namingTestSession{names: make(chan string, 2), finish: make(chan struct{}), supported: true}
			defer close(native.finish)
			a := &resultAgent{session: native}
			p := &stubPlatformEngine{n: "test"}
			e := NewEngine("member", a, []Platform{p}, filepath.Join(dir, "sessions.json"), LangEnglish)
			defer e.cancel()
			e.SetTeamProjects(store, nil)
			route := "test:group:root:task-one"
			s := e.sessions.GetOrCreateActive(route)
			if resume {
				s.SetAgentSessionID(native.CurrentSessionID(), a.Name())
			}
			done := make(chan *interactiveState, 1)
			go func() {
				done <- e.getOrCreateInteractiveStateWith("workspace:"+route, p, "reply", s, e.sessions, nil, route)
			}()
			var state *interactiveState
			select {
			case state = <-done:
			case <-time.After(time.Second):
				t.Fatal("naming blocked the first turn")
			}
			select {
			case name := <-native.names:
				if name != "容器动态挂载 · Johnson" {
					t.Fatal(name)
				}
			case <-time.After(time.Second):
				t.Fatal("native session was not named")
			}
			if state.agentSession != native || s.GetAgentSessionID() != "stub-session" {
				t.Fatal("naming replaced native session")
			}
			if got := NewSessionManager(filepath.Join(dir, "sessions.json")).GetSessionName("stub-session"); got != "容器动态挂载 · Johnson" {
				t.Fatal("name was not persisted:", got)
			}
			if e.getOrCreateInteractiveStateWith("workspace:"+route, p, "reply", s, e.sessions, nil, route) != state {
				t.Fatal("live session not reused")
			}
			select {
			case <-native.names:
				t.Fatal("renamed on every message")
			default:
			}
		})
	}
}

func TestTeamNativeSessionNaming_OrdinarySessionUntouched(t *testing.T) {
	native := &namingTestSession{names: make(chan string, 1), supported: true}
	e := NewEngine("member", &resultAgent{session: native}, nil, "", LangEnglish)
	defer e.cancel()
	s := e.sessions.GetOrCreateActive("test:private:user")
	s.SetName("My own title")
	e.nameTeamSession("test:private:user", s, e.sessions, native)
	if s.GetName() != "My own title" || e.sessions.GetSessionName("stub-session") != "" {
		t.Fatal("ordinary session was renamed")
	}
	select {
	case <-native.names:
		t.Fatal("ordinary native session was renamed")
	default:
	}
}
