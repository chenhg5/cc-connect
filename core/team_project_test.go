package core

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type teamTestAgent struct {
	stubModelModeAgent
	dir string
}

func (a *teamTestAgent) Name() string       { return "team-test-agent" }
func (a *teamTestAgent) GetWorkDir() string { return a.dir }
func (a *teamTestAgent) WorkspaceAgentOptions() map[string]any {
	return map[string]any{"model": a.model, "append_system_prompt": "role-prompt"}
}
func teamTestEngine(t *testing.T, dir, bot string) (*Engine, *stubPlatformEngine) {
	t.Helper()
	RegisterAgent("team-test-agent", func(opts map[string]any) (Agent, error) {
		model, _ := opts["model"].(string)
		wd, _ := opts["work_dir"].(string)
		return &teamTestAgent{stubModelModeAgent: stubModelModeAgent{model: model, mode: "default", reasoningEffort: "low", reasoningEfforts: []string{"low", "high"}}, dir: wd}, nil
	})
	p := &stubPlatformEngine{n: "test"}
	a := &teamTestAgent{stubModelModeAgent: stubModelModeAgent{model: "gpt-4.1-mini"}, dir: dir}
	e := NewEngine(bot, a, []Platform{p}, filepath.Join(dir, bot+".json"), LangEnglish)
	e.SetBaseWorkDir(dir)
	e.SetAdminFrom("owner")
	e.SetMultiWorkspace(dir, filepath.Join(dir, "bindings.json"))
	e.SetProjectStateStore(NewProjectStateStore(filepath.Join(dir, bot+".state.json")))
	store := NewTeamProjectStore(filepath.Join(dir, "team_projects.json"))
	for _, id := range []string{"a", "b"} {
		if err := store.Put(TeamProject{ID: id, Team: "default", Name: id, Platform: "test", ChatID: "group-" + id, Manager: "manager", Bots: []string{"manager", "dev"}, Directories: []string{dir}, State: "ready"}); err != nil {
			t.Fatal(err)
		}
	}
	e.SetTeamProjects(store, nil)
	return e, p
}
func teamSend(e *Engine, p *stubPlatformEngine, group, command string) {
	e.ReceiveMessage(p, &Message{Platform: "test", SessionKey: "test:" + group + ":owner", ChannelKey: group, UserID: "owner", Content: command, ReplyCtx: "ctx"})
}
func teamReply(t *testing.T, p *stubPlatformEngine, want string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(strings.Join(p.getSent(), "\n"), want) {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("want %q in %v", want, p.getSent())
}

func TestTeamWorkspace_SameDirectorySeparateBotsProjectsAndRestart(t *testing.T) {
	dir := t.TempDir()
	e, p := teamTestEngine(t, dir, "manager")
	teamSend(e, p, "group-a", "/model switch gpt-4.1")
	teamReply(t, p, "gpt-4.1")
	a, as, _, _, err := e.commandContextWithWorkspace(p, &Message{Platform: "test", SessionKey: "test:group-a:owner"})
	if err != nil {
		t.Fatal(err)
	}
	b, bs, _, _, err := e.commandContextWithWorkspace(p, &Message{Platform: "test", SessionKey: "test:group-b:owner"})
	if err != nil {
		t.Fatal(err)
	}
	if a == b || as == bs || b.(ModelSwitcher).GetModel() != "gpt-4.1-mini" {
		t.Fatal("project runtimes leaked")
	}
	session := as.GetOrCreateActive("test:group-a:owner")
	session.SetAgentSessionID("native-a", "team-test-agent")
	session.AddHistory("user", "A-only")
	as.Save()
	e.Stop()
	re, rp := teamTestEngine(t, dir, "manager")
	defer re.Stop()
	restored, rs, _, _, err := re.commandContextWithWorkspace(rp, &Message{Platform: "test", SessionKey: "test:group-a:owner"})
	if err != nil {
		t.Fatal(err)
	}
	if restored.(ModelSwitcher).GetModel() != "gpt-4.1" || rs.GetOrCreateActive("test:group-a:owner").GetAgentSessionID() != "native-a" {
		t.Fatal("restart lost model or native session")
	}
	other, op := teamTestEngine(t, dir, "dev")
	defer other.Stop()
	otherAgent, _, _, _, err := other.commandContextWithWorkspace(op, &Message{Platform: "test", SessionKey: "test:group-a:owner"})
	if err != nil {
		t.Fatal(err)
	}
	if otherAgent.(ModelSwitcher).GetModel() != "gpt-4.1-mini" {
		t.Fatal("model crossed bot boundary")
	}
}
func TestTeamWorkspace_ThreadsInheritGroupAndCardsUseScopedAgents(t *testing.T) {
	e, p := teamTestEngine(t, t.TempDir(), "manager")
	defer e.Stop()
	msg := &Message{Platform: "test", SessionKey: "test:group-a:root:thread1", ChannelKey: "group-a:topic:thread1", UserID: "owner", Content: "/model", ReplyCtx: "ctx"}
	e.ReceiveMessage(p, msg)
	teamReply(t, p, "gpt-4.1-mini")
	a, s, _, _, err := e.commandContextWithWorkspace(p, msg)
	if err != nil {
		t.Fatal(err)
	}
	if msg.ChannelKey != "group-a" {
		t.Fatal("thread did not inherit project group")
	}
	e.executeCardAction("/reasoning", "high", msg.SessionKey)
	e.executeCardAction("/mode", "acceptEdits", msg.SessionKey)
	if a.(ReasoningEffortSwitcher).GetReasoningEffort() != "high" {
		t.Fatal("card reasoning modified control runtime")
	}
	if a.(ModeSwitcher).GetMode() == "default" {
		t.Fatal("card mode modified control runtime")
	}
	control := e.agent.(ReasoningEffortSwitcher)
	if control.GetReasoningEffort() == "high" {
		t.Fatal("card contaminated manager")
	}
	_, other, _, _, err := e.commandContextWithWorkspace(p, &Message{Platform: "test", SessionKey: "test:group-a:root:thread2"})
	if err != nil {
		t.Fatal(err)
	}
	if s != other {
		t.Fatal("thread changed project workspace")
	}
}
func TestTeamProjects_FailClosedOnCorruptRegistryAndMissingManager(t *testing.T) {
	e, _ := teamTestEngine(t, t.TempDir(), "manager")
	defer e.Stop()
	e.SetTeamManagerCheck(func(context.Context, TeamProject) error { return errors.New("manager absent") })
	if _, err := e.HandleRelay(context.Background(), "dev", "test:group-a:owner", "work"); err == nil || !strings.Contains(err.Error(), "manager absent") {
		t.Fatal("dispatched without manager", err)
	}
	if err := e.ExecuteCronJob(&CronJob{SessionKey: "test:group-a:owner", Exec: "echo forbidden"}); err == nil || !strings.Contains(err.Error(), "manager absent") {
		t.Fatal("cron dispatched without manager", err)
	}
	if err := e.ExecuteTimerJob(&TimerJob{SessionKey: "test:group-a:owner", Exec: "echo forbidden"}); err == nil || !strings.Contains(err.Error(), "manager absent") {
		t.Fatal("timer dispatched without manager", err)
	}
	if err := os.WriteFile(e.teamProjects.path, []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := e.teamProject("test:group-a:owner"); err == nil {
		t.Fatal("corrupt group state ignored")
	}
	card := e.handleCardNav("act:/model switch gpt-4.1", "test:group-a:owner")
	if card == nil || e.agent.(ModelSwitcher).GetModel() != "gpt-4.1-mini" {
		t.Fatal("card fell back to control runtime on corrupt project state")
	}
}

func TestTeamSessions_CannotImportOtherProjectNativeHistory(t *testing.T) {
	e, p := teamTestEngine(t, t.TempDir(), "manager")
	defer e.Stop()
	_, a, _, err := e.commandContext(p, &Message{Platform: "test", SessionKey: "test:group-a:owner"})
	if err != nil {
		t.Fatal(err)
	}
	_, b, _, err := e.commandContext(p, &Message{Platform: "test", SessionKey: "test:group-b:owner"})
	if err != nil {
		t.Fatal(err)
	}
	all := []AgentSessionInfo{{ID: "native-a"}, {ID: "native-b"}, {ID: "external"}}
	if len(e.applySessionFilter(all, a)) != 0 {
		t.Fatal("empty project imported foreign history")
	}
	a.GetOrCreateActive("test:group-a:owner").SetAgentSessionID("native-a", "team-test-agent")
	b.GetOrCreateActive("test:group-b:owner").SetAgentSessionID("native-b", "team-test-agent")
	for sm, want := range map[*SessionManager]string{a: "native-a", b: "native-b"} {
		got := e.applySessionFilter(all, sm)
		if len(got) != 1 || got[0].ID != want {
			t.Fatalf("foreign sessions: %v", got)
		}
	}
}

func TestTeamRelay_ResolvesProjectWithoutManualBind(t *testing.T) {
	dir := t.TempDir()
	manager, _ := teamTestEngine(t, dir, "manager")
	dev, _ := teamTestEngine(t, dir, "dev")
	defer manager.Stop()
	defer dev.Stop()
	rm := NewRelayManager("")
	rm.SetVisibility(RelayVisibilityNone)
	rm.RegisterEngine("manager", manager)
	rm.RegisterEngine("dev", dev)
	checked := false
	dev.SetTeamManagerCheck(func(context.Context, TeamProject) error { checked = true; return errors.New("manager unavailable") })
	_, err := rm.Send(context.Background(), RelayRequest{From: "manager", To: "dev", SessionKey: "test:group-a:owner", Message: "work"})
	if !checked || err == nil || !strings.Contains(err.Error(), "manager unavailable") {
		t.Fatalf("project relay was not resolved: %v", err)
	}
	checked = false
	_, err = rm.Send(context.Background(), RelayRequest{From: "stranger", To: "dev", SessionKey: "test:group-a:owner", Message: "work"})
	if checked || err == nil || !strings.Contains(err.Error(), "sender is not a project member") {
		t.Fatalf("nonmember sender accepted: %v", err)
	}
}

func TestTeamCommand_RespectsExplicitDisableAndLeavesOrdinaryBotsUnchanged(t *testing.T) {
	e, p := teamTestEngine(t, t.TempDir(), "manager")
	defer e.Stop()
	e.SetDisabledCommands([]string{"team"})
	msg := &Message{UserID: "owner", Content: "/team", SessionKey: "test:group-a:owner"}
	if !e.handleTeamCommand(p, msg) || !strings.Contains(strings.Join(p.getSent(), "\n"), "disabled") {
		t.Fatal("explicit command configuration ignored")
	}
	e.SetTeamProjects(nil, nil)
	if e.handleTeamCommand(p, msg) {
		t.Fatal("Team intercepted an ordinary bot command")
	}
}

func TestTeamHelp_UsesOneNativeCardWithSeparateProjectTab(t *testing.T) {
	e, _ := teamTestEngine(t, t.TempDir(), "manager")
	defer e.Stop()
	handler := func(context.Context, Platform, *Message, []string) (string, error) { return "", nil }
	e.SetTeamProjects(e.teamProjects, handler)
	e.SetProjectCommand(handler)
	p := &stubCardPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}}
	e.cmdHelp(p, &Message{UserID: "owner", SessionKey: "test:private:owner", ReplyCtx: "ctx"})
	if len(p.getSent()) != 0 || len(p.repliedCards) != 1 {
		t.Fatalf("help must be one card without a separate Team message: messages=%v cards=%d", p.getSent(), len(p.repliedCards))
	}
	if got := countCardActionValues(p.repliedCards[0], "nav:/help "); got != 6 {
		t.Fatalf("help tabs = %d, want the original four plus Team and Project", got)
	}
	for _, tc := range []struct{ tab, want, absent string }{
		{"team", "/team members", "/project create"},
		{"project", "/project create", "/team bot create"},
		{"agent", "/model", "/project create"},
	} {
		card := e.handleCardNav("nav:/help "+tc.tab, "test:private:owner")
		if card == nil || !strings.Contains(card.RenderText(), tc.want) || strings.Contains(card.RenderText(), tc.absent) {
			t.Fatalf("wrong commands on %s help tab: %+v", tc.tab, card)
		}
		if strings.Contains(card.RenderText(), "/team member set") {
			t.Fatal("removed member set command is still visible in Help")
		}
		if tc.tab != "agent" {
			if _, ok := findCardAction(card, "cmd:"+tc.want); !ok {
				t.Fatalf("missing command button: %s", tc.want)
			}
		}
	}
}

func TestTeamProjectCommands_AppearInNativeMenusOnlyWhenEnabled(t *testing.T) {
	e, _ := teamTestEngine(t, t.TempDir(), "manager")
	defer e.Stop()
	handler := func(context.Context, Platform, *Message, []string) (string, error) { return "", nil }
	check := func(want bool) {
		t.Helper()
		menu, bridge := map[string]bool{}, map[string]bool{}
		for _, c := range e.GetAllCommands() {
			menu[c.Command] = true
		}
		for _, c := range e.GetBridgePublishedCommands() {
			bridge[c.Name] = true
		}
		for _, name := range []string{"team", "project"} {
			if menu[name] != want || bridge[name] != want {
				t.Fatalf("command %s: menu=%v bridge=%v, want %v", name, menu[name], bridge[name], want)
			}
		}
	}
	check(false)
	e.SetTeamProjects(e.teamProjects, handler)
	e.SetProjectCommand(handler)
	check(true)
	e.SetDisabledCommands([]string{"team", "project"})
	check(false)
}

func TestProjectCommand_LegacyAliasesCannotBypassPermissions(t *testing.T) {
	e, p := teamTestEngine(t, t.TempDir(), "manager")
	defer e.Stop()
	e.SetProjectCommand(func(context.Context, Platform, *Message, []string) (string, error) { return "unexpected dispatch", nil })
	for _, disabled := range [][]string{{"project"}, {"*"}} {
		e.SetDisabledCommands(disabled)
		for _, command := range []string{"/project list", "/team projects", "/team project create demo /tmp/demo"} {
			p.clearSent()
			teamSend(e, p, "private", command)
			if got := strings.Join(p.getSent(), "\n"); !strings.Contains(got, "disabled") {
				t.Fatalf("%s bypassed disable: %s", command, got)
			}
		}
	}
	e.SetDisabledCommands(nil)
	p.clearSent()
	e.ReceiveMessage(p, &Message{UserID: "stranger", SessionKey: "test:private:stranger", Content: "/project create demo /tmp/demo"})
	if got := strings.Join(p.getSent(), "\n"); !strings.Contains(got, e.i18n.Tf(MsgAdminRequired, "/project")) {
		t.Fatalf("non-admin project command accepted: %s", got)
	}
}
