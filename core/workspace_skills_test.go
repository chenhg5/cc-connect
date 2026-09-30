package core

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type workspaceSkillAgent struct {
	stubAgent
	name, workDir, global string
}

func (a *workspaceSkillAgent) Name() string { return a.name }
func (a *workspaceSkillAgent) SkillDirs() []string {
	return []string{filepath.Join(a.workDir, ".agents", "skills"), a.global}
}
func (a *workspaceSkillAgent) StartSession(context.Context, string) (AgentSession, error) {
	return &workspaceSkillSession{cujAgentSession: newCUJAgentSession(), workDir: a.workDir}, nil
}

type workspaceSkillSession struct {
	*cujAgentSession
	workDir string
}

type catalogSkillAgent struct {
	workspaceSkillAgent
	catalog []*Skill
	err     error
}

func (a *catalogSkillAgent) ListSkills(context.Context) ([]*Skill, error) {
	return a.catalog, a.err
}

func newCatalogSkillsEngine(t *testing.T, p Platform) (*Engine, *catalogSkillAgent) {
	t.Helper()
	base := t.TempDir()
	root := filepath.Join(base, ".agents", "skills")
	// The directory provider deliberately exposes files outside the native
	// allowlist, including a disabled sibling in the same skill root.
	for _, name := range []string{"enabled", "disabled-sibling", "claude-only", "cached-only"} {
		writeWorkspaceSkill(t, root, name, "Unfiltered "+name)
	}
	a := &catalogSkillAgent{
		workspaceSkillAgent: workspaceSkillAgent{name: "catalog-agent", workDir: base},
		catalog:             []*Skill{{Name: "plugin:enabled", Description: "Enabled native skill", Prompt: "Native selected instructions", Source: filepath.Join(root, "enabled")}},
	}
	e := NewEngine("test", a, []Platform{p}, filepath.Join(base, "sessions.json"), LangEnglish)
	t.Cleanup(func() { _ = e.Stop() })
	return e, a
}

func TestSkills_CatalogExcludesDisabledSiblingsAndOtherAgents(t *testing.T) {
	p := &stubCardPlatform{stubPlatformEngine: stubPlatformEngine{n: "feishu"}}
	e, a := newCatalogSkillsEngine(t, p)
	msg := skillMessage(p.Name(), "a", "/skills")
	e.ReceiveMessage(p, msg)
	p.mu.Lock()
	cards := append([]*Card(nil), p.repliedCards...)
	p.mu.Unlock()
	if len(cards) == 0 {
		t.Fatal("missing skills card")
	}
	cards = append(cards, e.handleCardNav("nav:/skills", msg.SessionKey))
	for _, card := range cards {
		text := card.RenderText()
		if !strings.Contains(text, "/plugin:enabled") {
			t.Fatalf("native skill missing: %s", text)
		}
		for _, forbidden := range []string{"disabled-sibling", "claude-only", "cached-only"} {
			if strings.Contains(text, forbidden) {
				t.Fatalf("unselected skill %s leaked: %s", forbidden, text)
			}
		}
	}
	a.catalog = nil
	if got := e.ListSkills(); len(got) != 0 {
		t.Fatalf("empty native catalog fell back to directories: %v", got)
	}
	a.err = fmt.Errorf("catalog unavailable")
	registry, err := e.skillsForMessage(p, msg)
	if err == nil || registry != nil {
		t.Fatal("catalog failure must propagate without a directory fallback")
	}
}

func (s *workspaceSkillSession) Send(prompt, sessionID string, images []ImageAttachment, files []FileAttachment) error {
	s.mu.Lock()
	s.reply = "Executed in " + s.workDir + "\n" + prompt
	s.mu.Unlock()
	return s.cujAgentSession.Send(prompt, sessionID, images, files)
}
func writeWorkspaceSkill(t *testing.T, root, name, body string) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
}
func newWorkspaceSkillsEngine(t *testing.T, p Platform) (*Engine, string, string) {
	t.Helper()
	base := t.TempDir()
	a, b, global := filepath.Join(base, "a"), filepath.Join(base, "b"), filepath.Join(base, "global")
	for _, ws := range []string{a, b} {
		writeWorkspaceSkill(t, filepath.Join(ws, ".agents", "skills"), filepath.Base(ws)+"-only", "Unique "+filepath.Base(ws))
		writeWorkspaceSkill(t, filepath.Join(ws, ".agents", "skills"), "shared-skill", "Instructions "+filepath.Base(ws))
	}
	writeWorkspaceSkill(t, global, "global-skill", "Global instructions")
	writeWorkspaceSkill(t, global, "shared-skill", "Wrong global instructions")
	name := "workspace-skills-" + t.Name()
	RegisterAgent(name, func(opts map[string]any) (Agent, error) {
		return &workspaceSkillAgent{name: name, workDir: opts["work_dir"].(string), global: global}, nil
	})
	e := NewEngine("test", &workspaceSkillAgent{name: name, workDir: base, global: global}, []Platform{p}, filepath.Join(base, "sessions.json"), LangEnglish)
	e.SetMultiWorkspace(base, filepath.Join(base, "bindings.json"))
	for channel, ws := range map[string]string{"a": a, "b": b} {
		e.workspaceBindings.Bind("project:test", workspaceChannelKey(p.Name(), "oc_"+channel), channel, ws)
	}
	t.Cleanup(func() { _ = e.Stop() })
	return e, a, b
}
func skillMessage(platform, channel, content string) *Message {
	return &Message{Platform: platform, SessionKey: platform + ":oc_" + channel + ":user", ChannelKey: "oc_" + channel, UserID: "user", Content: content, ReplyCtx: channel}
}
func assertWorkspaceSkills(t *testing.T, text, own, other string) {
	t.Helper()
	for _, want := range []string{"/" + own + "-only", "/shared-skill", "/global-skill"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %s in %q", want, text)
		}
	}
	if strings.Contains(text, "/"+other+"-only") {
		t.Errorf("other workspace leaked: %q", text)
	}
}
func TestSkills_WorkspaceCardCommandAndNavigation(t *testing.T) {
	p := &stubCardPlatform{stubPlatformEngine: stubPlatformEngine{n: "feishu"}}
	e, _, _ := newWorkspaceSkillsEngine(t, p)
	for _, channel := range []string{"a", "b"} {
		e.ReceiveMessage(p, skillMessage(p.Name(), channel, "/skills"))
		p.mu.Lock()
		if len(p.repliedCards) == 0 {
			p.mu.Unlock()
			t.Fatal("no skill card")
		}
		card := p.repliedCards[len(p.repliedCards)-1]
		p.mu.Unlock()
		other := "a"
		if channel == "a" {
			other = "b"
		}
		assertWorkspaceSkills(t, card.RenderText(), channel, other)
		card = e.handleCardNav("nav:/skills", skillMessage(p.Name(), channel, "").SessionKey)
		assertWorkspaceSkills(t, card.RenderText(), channel, other)
	}
}

func TestSkills_WorkspaceResolutionIsolationAndDirectoryChanges(t *testing.T) {
	p := &stubPlatformEngine{n: "feishu"}
	e, a, b := newWorkspaceSkillsEngine(t, p)
	for _, channel := range []string{"a", "b"} {
		r, err := e.skillsForMessage(p, skillMessage(p.Name(), channel, ""))
		if err != nil {
			t.Fatal(err)
		}
		other := "a"
		if channel == "a" {
			other = "b"
		}
		if r.Resolve(other+"-only") != nil {
			t.Fatal("resolved other workspace skill")
		}
		if got := r.Resolve("SHARED_SKILL"); got == nil || got.Prompt != "Instructions "+channel {
			t.Fatalf("wrong shared skill: %#v", got)
		}
		for _, skill := range r.ListAll() {
			if r.Resolve(skill.Name) != skill {
				t.Fatalf("listed skill cannot be resolved: %s", skill.Name)
			}
		}
	}
	// Directory overrides must affect card navigation as well as slash commands.
	store := NewProjectStateStore(filepath.Join(t.TempDir(), "project.json"))
	e.SetProjectStateStore(store)
	store.SetWorkspaceDirOverride(a+":"+skillMessage(p.Name(), "a", "").SessionKey, b)
	card := e.handleCardNav("nav:/skills", skillMessage(p.Name(), "a", "").SessionKey)
	assertWorkspaceSkills(t, card.RenderText(), "b", "a")
	store.ClearWorkspaceDirOverride(a + ":" + skillMessage(p.Name(), "a", "").SessionKey)
	// The provider's current work_dir is consulted even in single-workspace mode.
	e.multiWorkspace = false
	agent := e.agent.(*workspaceSkillAgent)
	agent.workDir = a
	e.cmdSkills(p, skillMessage(p.Name(), "a", "/skills"))
	sent := p.getSent()
	assertWorkspaceSkills(t, sent[len(sent)-1], "a", "b")
	agent.workDir = b
	e.cmdSkills(p, skillMessage(p.Name(), "a", "/skills"))
	sent = p.getSent()
	assertWorkspaceSkills(t, sent[len(sent)-1], "b", "a")
}

func TestSkills_ConcurrentWorkspaceRequests(t *testing.T) {
	p := &stubPlatformEngine{n: "feishu"}
	e, _, _ := newWorkspaceSkillsEngine(t, p)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		for _, channel := range []string{"a", "b"} {
			wg.Add(1)
			go func(channel string) {
				defer wg.Done()
				r, err := e.skillsForMessage(p, skillMessage(p.Name(), channel, ""))
				if err != nil {
					t.Error(err)
					return
				}
				skill := r.Resolve("shared-skill")
				if skill == nil || skill.Prompt != "Instructions "+channel {
					t.Errorf("cross-workspace result: %#v", skill)
				}
			}(channel)
		}
	}
	wg.Wait()
}

func TestSkills_UnboundMissingAndFailedWorkspace(t *testing.T) {
	p := &stubPlatformEngine{n: "feishu"}
	e, a, _ := newWorkspaceSkillsEngine(t, p)
	// Discovery of unknown names must not start an agent session.
	r, err := e.skillsForMessage(p, skillMessage(p.Name(), "unbound", ""))
	if err != nil {
		t.Fatal(err)
	}
	if r.Resolve("a-only") != nil || r.Resolve("unknown") != nil {
		t.Fatal("unbound channel inherited project skills")
	}
	if len(e.interactiveStates) != 0 {
		t.Fatal("discovery started a model session")
	}
	if err := os.RemoveAll(a); err != nil {
		t.Fatal(err)
	}
	r, err = e.skillsForMessage(p, skillMessage(p.Name(), "a", ""))
	if err != nil {
		t.Fatal(err)
	}
	if r.Resolve("a-only") != nil {
		t.Fatal("removed workspace remained cached")
	}
	// A failed workspace factory cannot silently resolve the global same-name skill.
	RegisterAgent(e.agent.Name(), func(map[string]any) (Agent, error) { return nil, fmt.Errorf("fixture factory failure") })
	e.cmdSkills(p, skillMessage(p.Name(), "b", "/skills"))
	sent := p.getSent()
	if !strings.Contains(sent[len(sent)-1], "fixture factory failure") {
		t.Fatalf("missing workspace error: %v", sent)
	}
	if len(e.interactiveStates) != 0 {
		t.Fatal("error started a model session")
	}
}

func TestSkills_MultiWorkspaceMenuDoesNotExposeProjectSkills(t *testing.T) {
	p := &stubPlatformEngine{n: "feishu"}
	e, _, _ := newWorkspaceSkillsEngine(t, p)
	commands, _ := e.menuCommandsForPlatform(p.Name())
	for _, command := range commands {
		if command.IsSkill {
			t.Fatalf("unscoped skill in global menu: %s", command.Command)
		}
	}
}

func TestSkills_ScheduledJobsUseExecutionWorkspace(t *testing.T) {
	for _, kind := range []string{"cron", "timer"} {
		for _, override := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/override=%v", kind, override), func(t *testing.T) {
				plain := &stubPlatformEngine{n: "feishu"}
				p := &cujReplyCtxPlatform{stubPlatformEngine: plain}
				e, a, b := newWorkspaceSkillsEngine(t, p)
				workDir, wantDir, wantBody := "", a, "Instructions a"
				if override {
					workDir, wantDir, wantBody = b, b, "Instructions b"
				}
				key := skillMessage(p.Name(), "a", "").SessionKey
				var err error
				if kind == "cron" {
					err = e.ExecuteCronJob(&CronJob{ID: "skill", SessionKey: key, Prompt: "/shared-skill scheduled", WorkDir: workDir})
				} else {
					err = e.ExecuteTimerJob(&TimerJob{ID: "skill", SessionKey: key, Prompt: "/shared-skill scheduled", WorkDir: workDir})
				}
				if err != nil {
					t.Fatal(err)
				}
				text := strings.Join(plain.getSent(), "\n")
				for _, want := range []string{"Executed in " + wantDir, wantBody, "scheduled"} {
					if !strings.Contains(text, want) {
						t.Errorf("missing %q: %s", want, text)
					}
				}
				if strings.Contains(text, "Wrong global instructions") {
					t.Fatal("expanded global skill")
				}
			})
		}
	}
}
