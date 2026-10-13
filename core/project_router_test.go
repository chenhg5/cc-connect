package core

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func newProjectCUJ(t *testing.T, dir, bot string) *cujEnv {
	t.Helper()
	p := &stubPlatformEngine{n: "test"}
	root := NewEngine(bot, &stubAgent{}, []Platform{p}, filepath.Join(dir, bot, "control.json"), LangEnglish)
	projects := []BusinessProject{}
	for _, id := range []string{"a", "b"} {
		projects = append(projects, BusinessProject{ID: id, Name: id, BotIDs: []string{bot}, PrimaryDirectory: "src", Directories: []ProjectDirectory{{ID: "src", Path: dir}}})
	}
	r, err := NewProjectRouter(bot, false, projects, filepath.Join(dir, bot, "selection.json"), func(bp BusinessProject) (*Engine, error) {
		a := &projectCUJAgent{inner: &cujAgent{nextSessionEvents: []Event{{Type: EventResult, Content: "finished-" + bp.ID, Done: true}}}, nativeID: "native-" + bot + "-" + bp.ID}
		child := NewEngine(bot, a, []Platform{p}, filepath.Join(dir, bot, bp.ID, "sessions.json"), LangEnglish)
		child.SetBaseWorkDir(bp.WorkDir())
		return child, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	root.SetProjectRouter(r)
	return &cujEnv{t: t, engine: root, plat: p, tempDir: dir}
}

func TestProjectSelections_CorruptOrUnknownVersionDoesNotReset(t *testing.T) {
	path := filepath.Join(t.TempDir(), "selection.json")
	for _, data := range []string{"broken", `{"version":9,"selected":{}}`} {
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		_, err := NewProjectRouter("bot", false, nil, path, func(BusinessProject) (*Engine, error) { return nil, nil })
		if err == nil {
			t.Fatal("corrupt selection silently replaced")
		}
		got, _ := os.ReadFile(path)
		if string(got) != data {
			t.Fatal("corrupt file was overwritten")
		}
	}
}

func TestProjectContext_EmptyStoreNeverImportsForeignSessions(t *testing.T) {
	env := newProjectCUJ(t, t.TempDir(), "dev")
	defer env.engine.Stop()
	e := env.engine
	foreign := []AgentSessionInfo{{ID: "other-project", Summary: "private"}}
	if got := e.applySessionFilter(foreign, e.sessions); len(got) != 0 {
		t.Fatal("empty project exposed foreign CLI history")
	}
	s := e.sessions.GetOrCreateActive("user")
	s.SetAgentSessionID("owned", e.agent.Name())
	input := append(foreign, AgentSessionInfo{ID: "owned"})
	got := e.applySessionFilter(input, e.sessions)
	if len(got) != 1 || got[0].ID != "owned" {
		t.Fatal("project ownership filter failed")
	}
}

func TestProjectSelection_ConcurrentUsersAndPersistenceFailure(t *testing.T) {
	dir := t.TempDir()
	env := newProjectCUJ(t, dir, "dev")
	defer env.engine.Stop()
	var wg sync.WaitGroup
	for _, u := range []string{"alice", "bob"} {
		wg.Add(1)
		go func(u string) { defer wg.Done(); env.userSends(u, "/project use a") }(u)
	}
	wg.Wait()
	r := env.engine.projectRouter
	r.mu.Lock()
	if len(r.selected) != 2 {
		t.Fatal("users share selection")
	}
	before := len(r.selected)
	r.path = dir // rename onto a directory must fail and keep memory unchanged
	err := r.selectProject("new-user", "b")
	if err == nil || len(r.selected) != before {
		t.Fatal("failed persistence changed active project")
	}
	r.mu.Unlock()
}

func TestProjectContext_CallbackAndLegacyDirectoryCommandsCannotEscape(t *testing.T) {
	env := newProjectCUJ(t, t.TempDir(), "dev")
	defer env.engine.Stop()
	env.userSends("u", "/project use a")
	env.plat.clearSent()
	env.userSends("u", "/dir /tmp")
	env.waitFor("directory command rejected", time.Second, func() bool { return strings.Contains(strings.Join(env.plat.getSent(), "\n"), "disabled") })
	env.plat.clearSent()
	env.engine.ReceiveMessage(env.plat, &Message{SessionKey: "test:u", Platform: "test", UserID: "u", Content: "allow", IsPermissionResponse: true})
	if !strings.Contains(strings.Join(env.plat.getSent(), "\n"), "text") {
		t.Fatal("unscoped permission callback accepted")
	}
	if err := env.engine.SendToSessionInWorkDir("test:u", "work", nil, nil, "/tmp", nil, false); err == nil {
		t.Fatal("unscoped API dispatch accepted")
	}
}

type countedProjectPlatform struct {
	*stubPlatformEngine
	starts, stops int
}

func (p *countedProjectPlatform) Start(MessageHandler) error { p.starts++; return nil }
func (p *countedProjectPlatform) Stop() error                { p.stops++; return nil }

func TestProjectRuntime_DoesNotOwnSharedTransport(t *testing.T) {
	p := &countedProjectPlatform{stubPlatformEngine: &stubPlatformEngine{n: "test"}}
	e := NewEngine("child", &stubAgent{}, []Platform{p}, "", LangEnglish)
	e.SetSharedPlatforms()
	if err := e.Start(); err != nil {
		t.Fatal(err)
	}
	if err := e.Stop(); err != nil {
		t.Fatal(err)
	}
	if p.starts != 0 || p.stops != 0 {
		t.Fatal("project runtime restarted shared bot transport")
	}
}

func TestProjectContext_LegacyJobsAndRelayNeedProjectScope(t *testing.T) {
	env := newProjectCUJ(t, t.TempDir(), "dev")
	defer env.engine.Stop()
	for _, err := range []error{env.engine.ExecuteCronJob(nil), env.engine.ExecuteTimerJob(nil), env.engine.ExecuteHeartbeat("", "", false), env.engine.SendToSession("", "text")} {
		if err != ErrProjectContextRequired {
			t.Fatalf("unscoped dispatch result: %v", err)
		}
	}
	if _, err := env.engine.HandleRelay(env.engine.ctx, "another-bot", "test:u", "work"); err != ErrProjectContextRequired {
		t.Fatal("legacy relay bypasses context")
	}
}

func TestProjectContext_RejectCorruptHistoryBeforeOpening(t *testing.T) {
	file := filepath.Join(t.TempDir(), "sessions.json")
	if err := ValidateProjectSessionStore(file); err != nil {
		t.Fatal(err)
	}
	sm := NewSessionManager(file)
	sm.GetOrCreateActive("u")
	sm.Save()
	if err := ValidateProjectSessionStore(file); err != nil {
		t.Fatal(err)
	}
	for _, data := range []string{"{", `{"version":999}`, `{"version":1,"sessions":{"s1":null},"active_session":{}}`} {
		if err := os.WriteFile(file, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if err := ValidateProjectSessionStore(file); err == nil {
			t.Fatal("invalid project history accepted")
		}
		got, _ := os.ReadFile(file)
		if string(got) != data {
			t.Fatal("invalid history overwritten")
		}
	}
}

// This boundary stub exposes distinct native sessions so restart tests can
// detect accidentally resuming another project's conversation.
type projectCUJAgent struct {
	inner    *cujAgent
	nativeID string
	mu       sync.Mutex
	resumes  []string
}

func (a *projectCUJAgent) Name() string { return a.inner.Name() }
func (a *projectCUJAgent) ListSessions(context.Context) ([]AgentSessionInfo, error) {
	return []AgentSessionInfo{{ID: a.nativeID}, {ID: "foreign-project"}}, nil
}
func (a *projectCUJAgent) Stop() error { return a.inner.Stop() }
func (a *projectCUJAgent) StartSession(ctx context.Context, resume string) (AgentSession, error) {
	a.mu.Lock()
	a.resumes = append(a.resumes, resume)
	a.mu.Unlock()
	s, err := a.inner.StartSession(ctx, resume)
	if err != nil {
		return nil, err
	}
	return &projectCUJSession{AgentSession: s, id: a.nativeID}, nil
}

type projectCUJSession struct {
	AgentSession
	id string
}

func (s *projectCUJSession) CurrentSessionID() string { return s.id }
