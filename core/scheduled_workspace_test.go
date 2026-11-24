package core

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// Mirror the platform capability for Feishu's persisted root/thread keys.
// The generic core parser must not be able to infer these topic bindings.
type scheduledTopicPlatform struct{ stubPlatformEngine }

func (p *scheduledTopicPlatform) ChannelKeyFromSessionKey(key string) (string, bool) {
	for _, topic := range []string{"a", "b"} {
		for _, scope := range []string{"root", "thread"} {
			if key == "feishu:room:"+scope+":"+topic {
				return "room:topic:" + topic, true
			}
		}
	}
	return "", false
}
func (*scheduledTopicPlatform) ReconstructReplyCtx(key string) (any, error) { return key, nil }

type scheduledWorkspaceAgent struct {
	stubAgent
	name, workDir string
	mu            sync.Mutex
	providers     []ProviderConfig
	active        string
	nextID        int
}

func newScheduledWorkspaceAgent(name, dir string) *scheduledWorkspaceAgent {
	return &scheduledWorkspaceAgent{name: name, workDir: dir, active: "provider-b", providers: []ProviderConfig{{Name: "provider-a"}, {Name: "provider-b"}}}
}
func (a *scheduledWorkspaceAgent) Name() string       { return a.name }
func (a *scheduledWorkspaceAgent) GetWorkDir() string { return a.workDir }
func (a *scheduledWorkspaceAgent) StartSession(_ context.Context, id string) (AgentSession, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if id == "" {
		a.nextID++
		id = fmt.Sprintf("native-%s-%d", a.active, a.nextID)
	}
	s := newCUJAgentSession()
	s.reply = "provider=" + a.active + " session=" + id + " workspace=" + a.workDir
	return &scheduledWorkspaceSession{cujAgentSession: s, id: id}, nil
}
func (a *scheduledWorkspaceAgent) SetProviders(providers []ProviderConfig) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.providers = append([]ProviderConfig(nil), providers...)
}
func (a *scheduledWorkspaceAgent) ListProviders() []ProviderConfig {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]ProviderConfig(nil), a.providers...)
}
func (a *scheduledWorkspaceAgent) SetActiveProvider(name string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, p := range a.providers {
		if p.Name == name {
			a.active = name
			return true
		}
	}
	return false
}
func (a *scheduledWorkspaceAgent) GetActiveProvider() *ProviderConfig {
	a.mu.Lock()
	defer a.mu.Unlock()
	return &ProviderConfig{Name: a.active}
}

type scheduledWorkspaceSession struct {
	*cujAgentSession
	id string
}

func (s *scheduledWorkspaceSession) CurrentSessionID() string { return s.id }

func newScheduledWorkspaceEngine(t *testing.T, base string) (*Engine, *scheduledTopicPlatform, string, string) {
	t.Helper()
	name := "scheduled-workspace-" + t.Name()
	a, b := normalizeWorkspacePath(filepath.Join(base, "a")), normalizeWorkspacePath(filepath.Join(base, "b"))
	for _, dir := range []string{a, b} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
	}
	RegisterAgent(name, func(opts map[string]any) (Agent, error) {
		return newScheduledWorkspaceAgent(name, opts["work_dir"].(string)), nil
	})
	p := &scheduledTopicPlatform{stubPlatformEngine: stubPlatformEngine{n: "feishu"}}
	e := NewEngine("test", newScheduledWorkspaceAgent(name, base), []Platform{p}, filepath.Join(base, "sessions.json"), LangEnglish)
	e.SetMultiWorkspace(base, filepath.Join(base, "bindings.json"))
	t.Cleanup(func() { _ = e.Stop() })
	return e, p, a, b
}

func bindScheduledTopics(e *Engine, a, b string) {
	e.workspaceBindings.Bind("project:test", "feishu:room:topic:a", "topic A", a)
	e.workspaceBindings.Bind("project:test", "feishu:room:topic:b", "topic B", b)
	// A different chat-level default must never shadow the topic override.
	e.workspaceBindings.Bind("project:test", "feishu:room", "room", b)
}

func executeScheduledCheck(e *Engine, kind, key, workDir string) error {
	if kind == "timer" {
		return e.ExecuteTimerJob(&TimerJob{ID: "check", Project: "test", SessionKey: key, Prompt: "check progress", WorkDir: workDir})
	}
	return e.ExecuteCronJob(&CronJob{ID: "check", Project: "test", SessionKey: key, Prompt: "check progress", WorkDir: workDir})
}

func TestScheduledChecks_FeishuTopicWorkspaceAndProviderSurviveRestart(t *testing.T) {
	for _, kind := range []string{"timer", "cron"} {
		t.Run(kind, func(t *testing.T) {
			base := t.TempDir()
			e, _, a, b := newScheduledWorkspaceEngine(t, base)
			bindScheduledTopics(e, a, b)
			_, sessions, err := e.getOrCreateWorkspaceAgent(a)
			if err != nil {
				t.Fatal(err)
			}
			s := sessions.GetOrCreateActive("feishu:room:root:a")
			s.SetActiveProvider("provider-a")
			s.SetAgentSessionID("original-topic-a-session", e.agent.Name())
			s.AddHistory("user", "original topic A task")
			sessions.Save()
			_ = e.Stop()
			restarted, p, _, _ := newScheduledWorkspaceEngine(t, base)
			if err := executeScheduledCheck(restarted, kind, "feishu:room:root:a", ""); err != nil {
				t.Fatal(err)
			}
			text := strings.Join(p.getSent(), "\n")
			for _, want := range []string{"workspace=" + a, "provider=provider-a", "session=original-topic-a-session"} {
				if !strings.Contains(text, want) {
					t.Fatalf("scheduled check lost %q: %s", want, text)
				}
			}
			if strings.Contains(text, "workspace="+b) {
				t.Fatal("check used the other topic's workspace")
			}
		})
	}
}

func TestScheduledChecks_WorkspaceFailureDoesNotRunGlobalAgent(t *testing.T) {
	for _, kind := range []string{"timer", "cron"} {
		for _, mode := range []string{"missing", "not-directory", "factory", "explicit"} {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				e, p, a, b := newScheduledWorkspaceEngine(t, t.TempDir())
				bindScheduledTopics(e, a, b)
				workDir := ""
				if mode == "missing" || mode == "not-directory" {
					if err := os.Remove(a); err != nil {
						t.Fatal(err)
					}
					if mode == "not-directory" {
						if err := os.WriteFile(a, []byte("not a directory"), 0600); err != nil {
							t.Fatal(err)
						}
					}
				} else {
					RegisterAgent(e.agent.Name(), func(map[string]any) (Agent, error) { return nil, fmt.Errorf("workspace unavailable") })
					if mode == "explicit" {
						workDir = a
					}
				}
				if err := executeScheduledCheck(e, kind, "feishu:room:root:a", workDir); err == nil {
					t.Fatal("workspace failure silently ran a different project")
				}
				if strings.Contains(strings.Join(p.getSent(), "\n"), "provider=") {
					t.Fatal("global agent executed after workspace resolution failed")
				}
				if binding, _ := e.workspaceBindings.LookupEffective("project:test", "feishu:room:topic:a"); binding == nil {
					t.Fatal("scheduled lookup deleted the workspace binding")
				}
			})
		}
	}
}

func TestScheduledChecks_DirOverrideAndExplicitWorkDir(t *testing.T) {
	for _, kind := range []string{"timer", "cron"} {
		t.Run(kind, func(t *testing.T) {
			e, p, a, b := newScheduledWorkspaceEngine(t, t.TempDir())
			bindScheduledTopics(e, a, b)
			e.SetProjectStateStore(NewProjectStateStore(filepath.Join(t.TempDir(), "project.json")))
			key := "feishu:room:root:a"
			override := normalizeWorkspacePath(t.TempDir())
			e.projectState.SetWorkspaceDirOverride(a+":"+key, override)
			e.projectState.Save()
			for _, tc := range []struct{ explicit, want string }{{"", override}, {a, a}} {
				p.clearSent()
				if err := executeScheduledCheck(e, kind, key, tc.explicit); err != nil {
					t.Fatal(err)
				}
				if text := strings.Join(p.getSent(), "\n"); !strings.Contains(text, "workspace="+tc.want) {
					t.Fatalf("scheduled working directory priority was lost: %s", text)
				}
			}
		})
	}
}

// Called as a sub-case of the existing topic-workspace CUJ. User actions go
// through ReceiveMessage; firing a saved timer uses the real scheduler.
func scheduledTopicTimerCUJ(t *testing.T) {
	base := t.TempDir()
	e, p, a, b := newScheduledWorkspaceEngine(t, base)
	bindScheduledTopics(e, a, b)
	store, err := NewTimerStore(base)
	if err != nil {
		t.Fatal(err)
	}
	sched := NewTimerScheduler(store)
	sched.RegisterEngine("test", e)
	e.SetTimerScheduler(sched)
	defer sched.Stop()
	key := "feishu:room:root:a"
	send := func(content string) {
		e.ReceiveMessage(p, &Message{SessionKey: key, Platform: p.Name(), ChannelKey: "room:topic:a", UserID: "user", Content: content, ReplyCtx: key})
	}
	send("/provider switch provider-a")
	send("original topic A task")
	env := &cujEnv{t: t, engine: e, plat: &p.stubPlatformEngine}
	env.waitFor("initial topic task completes", 3*time.Second, func() bool {
		_, sessions := e.sessionContextForKey(key)
		return strings.Contains(strings.Join(p.getSent(), "\n"), "provider=provider-a") && !sessions.GetOrCreateActive(key).Busy()
	})
	send("/timer add 2h check progress")
	jobs := store.ListPending()
	if len(jobs) != 1 {
		t.Fatalf("created %d timers", len(jobs))
	}
	sched.Stop()
	_ = e.Stop()
	restarted, replies, _, _ := newScheduledWorkspaceEngine(t, base)
	reloaded, err := NewTimerStore(base)
	if err != nil {
		t.Fatal(err)
	}
	runner := NewTimerScheduler(reloaded)
	runner.RegisterEngine("test", restarted)
	restarted.SetTimerScheduler(runner)
	defer runner.Stop()
	runner.executeJob(jobs[0].ID)
	text := strings.Join(replies.getSent(), "\n")
	for _, want := range []string{"workspace=" + a, "provider=provider-a", "session=native-provider-a-1"} {
		if !strings.Contains(text, want) {
			t.Fatalf("after timer/restart user did not see %q: %s", want, text)
		}
	}
	restarted.ReceiveMessage(replies, &Message{SessionKey: key, Platform: replies.Name(), ChannelKey: "room:topic:a", UserID: "user", Content: "/history", ReplyCtx: key})
	text = strings.Join(replies.getSent(), "\n")
	if !strings.Contains(text, "original topic A task") || !strings.Contains(text, "check progress") {
		t.Fatalf("timer did not continue the original visible history: %s", text)
	}
}
