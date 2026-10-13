package core

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func projectSettingsEngines(t *testing.T) (*Engine, *Engine, *stubPlatformEngine) {
	t.Helper()
	dir := t.TempDir()
	manager, p := teamTestEngine(t, dir, "manager")
	dev, _ := teamTestEngine(t, dir, "dev")
	handler := func(context.Context, Platform, *Message, []string) (string, error) {
		return "project list/create help", nil
	}
	manager.SetProjectCommand(handler)
	dev.SetTeamProjects(manager.teamProjects, handler)
	dev.SetProjectCommand(handler)
	t.Cleanup(func() { manager.Stop(); dev.Stop() })
	return manager, dev, p
}
func TestProjectSettings_DirectoryAliasesUpdateAllBotsAndPreserveHistory(t *testing.T) {
	e, dev, p := projectSettingsEngines(t)
	session := "test:group-a:owner"
	_, old, _, _, err := e.commandContextWithWorkspace(p, &Message{Platform: "test", SessionKey: session, ChannelKey: "group-a"})
	if err != nil {
		t.Fatal(err)
	}
	old.GetOrCreateActive(session).SetAgentSessionID("old-native-context", "team-test-agent")
	old.GetOrCreateActive(session).AddHistory("user", "original project history")
	old.Save()
	oldPath := old.StorePath()
	next := filepath.Join(t.TempDir(), "repo with spaces")
	if err := os.Mkdir(next, 0700); err != nil {
		t.Fatal(err)
	}
	teamSend(e, p, "group-a", `/workspace route "`+next+`"`)
	teamReply(t, p, e.i18n.T(MsgProjectSettingsSaved))
	if got := strings.Join(p.getSent(), "\n"); !strings.Contains(got, "Scope: current project group") || !strings.Contains(got, next) {
		t.Fatal(got)
	}
	for _, bot := range []*Engine{e, dev} {
		a, sm, _, wd, err := bot.commandContextWithWorkspace(p, &Message{Platform: "test", SessionKey: session, ChannelKey: "group-a"})
		if err != nil || wd != next || a.(interface{ GetWorkDir() string }).GetWorkDir() != next {
			t.Fatalf("wrong directory: %s %v", wd, err)
		}
		if sm == old || sm.GetOrCreateActive(session).GetAgentSessionID() != "" || len(sm.GetOrCreateActive(session).GetHistory(5)) != 0 {
			t.Fatal("old context reused")
		}
		other, _, _, _, err := bot.commandContextWithWorkspace(p, &Message{Platform: "test", SessionKey: "test:group-b:owner", ChannelKey: "group-b"})
		if err != nil || other.(interface{ GetWorkDir() string }).GetWorkDir() == next {
			t.Fatal("other project changed")
		}
	}
	saved, err := os.ReadFile(oldPath)
	if err != nil || !strings.Contains(string(saved), "original project history") {
		t.Fatal("history erased", err)
	}
	// Returning to an old path starts a fresh context, rather than reviving a stale native session.
	p.clearSent()
	teamSend(e, p, "group-a", "/dir "+e.baseWorkDir)
	teamReply(t, p, e.i18n.T(MsgProjectSettingsSaved))
	_, current, _, _, err := e.commandContextWithWorkspace(p, &Message{Platform: "test", SessionKey: session, ChannelKey: "group-a"})
	if err != nil || current.StorePath() == oldPath {
		t.Fatal("old path revived old context", err)
	}
	// Restart uses the persisted project revision and selected directory.
	restored := NewTeamProjectStore(e.teamProjects.path)
	records, err := restored.All()
	if err != nil || !records["a"].DirectoryUnified || records["a"].ContextRevision != 2 {
		t.Fatalf("settings not persisted: %+v %v", records, err)
	}
}
func TestProjectSettings_BusyOtherBotBlocksDirectoryMutation(t *testing.T) {
	e, dev, p := projectSettingsEngines(t)
	_, sm, _, _, err := dev.commandContextWithWorkspace(p, &Message{Platform: "test", SessionKey: "test:group-a:thread:user", ChannelKey: "group-a"})
	if err != nil {
		t.Fatal(err)
	}
	session := sm.GetOrCreateActive("test:group-a:thread:user")
	gen, ok := session.TryLock()
	if !ok {
		t.Fatal("lock")
	}
	next := t.TempDir()
	teamSend(e, p, "group-a", "/project directory "+next)
	teamReply(t, p, e.i18n.T(MsgProjectBusy))
	record, _ := e.teamProject("test:group-a:owner")
	if record.Directories[0] == next {
		t.Fatal("changed during active task")
	}
	session.Unlock(gen)
	p.clearSent()
	teamSend(e, p, "group-a", "/project directory "+next)
	teamReply(t, p, e.i18n.T(MsgProjectSettingsSaved))
}
func TestProjectSettings_DispatchGuardClosesDirectoryChangeRace(t *testing.T) {
	e, _, _ := projectSettingsEngines(t)
	unlock := e.lockProjectDispatch("test:group-a:owner")
	next := t.TempDir()
	if err := e.updateProjectSettings("a", "directory", next); err == nil {
		t.Fatal("changed while dispatch resolves old directory")
	}
	// An unrelated project can still be configured.
	if err := e.updateProjectSettings("b", "directory", next); err != nil {
		t.Fatal(err)
	}
	unlock()
	if err := e.updateProjectSettings("a", "directory", next); err != nil {
		t.Fatal(err)
	}
}
func TestProjectSettings_LegacyConflictRequiresExplicitChoice(t *testing.T) {
	e, dev, p := projectSettingsEngines(t)
	legacy := t.TempDir()
	dev.workspaceBindings.Bind("project:dev", "test:group-a", "old", legacy)
	dev.projectState.SetWorkspaceDirOverride(e.baseWorkDir+":test:group-a:thread:user", legacy)
	teamSend(e, p, "group-a", "/workspace")
	teamReply(t, p, "Directory settings conflict")
	if got := strings.Join(p.getSent(), "\n"); !strings.Contains(got, legacy) || !strings.Contains(got, e.baseWorkDir) {
		t.Fatal("differences not displayed", got)
	}
	p.clearSent()
	teamSend(e, p, "group-a", "/model")
	teamReply(t, p, "Directory settings conflict")
	p.clearSent()
	teamSend(e, p, "group-a", "/project directory "+legacy)
	teamReply(t, p, e.i18n.T(MsgProjectSettingsSaved))
	p.clearSent()
	teamSend(e, p, "group-a", "/model")
	teamReply(t, p, "gpt-4.1-mini")
	// A stale native /dir card cannot write another override.
	card := e.handleCardNav("act:/dir reset", "test:group-a:owner")
	if card == nil || !strings.Contains(card.RenderText(), legacy) {
		t.Fatal("old card bypassed settings")
	}
	record, _ := e.teamProject("test:group-a:owner")
	if !record.DirectoryUnified || record.Directories[0] != legacy {
		t.Fatal("selection lost")
	}
}
func TestProjectSettings_HelpAndCardButtonsUseUnifiedEntry(t *testing.T) {
	e, _, _ := projectSettingsEngines(t)
	for _, tab := range []string{"system", "tools", "agent"} {
		card := e.renderHelpForSession(tab, "test:group-a:owner")
		if strings.Contains(card.RenderText(), "**/workspace**") || strings.Contains(card.RenderText(), "**/dir**") {
			t.Fatal("duplicate directory entry", card.RenderText())
		}
	}
	project, _ := e.teamProject("test:group-a:owner")
	card := e.renderProjectSettings(*project)
	for _, cmd := range []string{"cmd:/project edit", "cmd:/project directory", "cmd:/project bot"} {
		if _, ok := findCardAction(card, cmd); !ok {
			t.Fatal("missing settings button", cmd)
		}
	}
	ordinary := e.renderHelpForSession("system", "test:private:owner")
	// Native help puts workspace under system; check all groups to avoid assuming a tab label.
	found := strings.Contains(ordinary.RenderText(), "**/workspace**")
	for _, g := range helpCardGroups() {
		found = found || strings.Contains(e.renderHelpForSession(g.key, "test:private:owner").RenderText(), "**/workspace**")
	}
	if !found {
		t.Fatal("native workspace hidden outside project")
	}
}
func TestProjectSettings_ProjectModelResetDoesNotChangeOtherContexts(t *testing.T) {
	e, _, p := projectSettingsEngines(t)
	teamSend(e, p, "group-a", "/model switch gpt-4.1")
	teamReply(t, p, "gpt-4.1")
	p.clearSent()
	teamSend(e, p, "group-a", "/project bot")
	teamReply(t, p, "Current project override")
	e.SetDisabledCommands([]string{"model"})
	p.clearSent()
	teamSend(e, p, "group-a", "/project model reset")
	teamReply(t, p, "disabled")
	e.SetDisabledCommands(nil)
	p.clearSent()
	teamSend(e, p, "group-a", "/project model reset")
	teamReply(t, p, "Inherited from bot default")
	if got := strings.Join(p.getSent(), "\n"); !strings.Contains(got, "gpt-4.1-mini") || !strings.Contains(got, "all projects") {
		t.Fatal("model source or shared scope not shown", got)
	}
	p.clearSent()
	teamSend(e, p, "group-b", "/model")
	teamReply(t, p, "gpt-4.1-mini")
	if e.agent.(ModelSwitcher).GetModel() != "gpt-4.1-mini" {
		t.Fatal("bot default changed")
	}
}
func TestProjectSettings_AliasesRespectAdminAndDisabledCommands(t *testing.T) {
	e, _, p := projectSettingsEngines(t)
	next := t.TempDir()
	for _, cmd := range []string{"/project directory " + next, "/workspace route " + next, "/dir " + next} {
		p.clearSent()
		e.ReceiveMessage(p, &Message{UserID: "stranger", Platform: "test", SessionKey: "test:group-a:stranger", Content: cmd})
		teamReply(t, p, "admin privilege")
	}
	e.SetDisabledCommands([]string{"project"})
	for _, cmd := range []string{"/workspace route " + next, "/dir " + next} {
		p.clearSent()
		teamSend(e, p, "group-a", cmd)
		teamReply(t, p, "disabled")
	}
	record, _ := e.teamProject("test:group-a:owner")
	if record.Directories[0] == next {
		t.Fatal("unauthorized settings change")
	}
}

func TestProjectSettings_RestartRestoresUnifiedDirectoryAndModel(t *testing.T) {
	e, _, p := projectSettingsEngines(t)
	next := t.TempDir()
	teamSend(e, p, "group-a", "/project directory "+next)
	teamReply(t, p, e.i18n.T(MsgProjectSettingsSaved))
	p.clearSent()
	teamSend(e, p, "group-a", "/model switch gpt-4.1")
	teamReply(t, p, "gpt-4.1")
	fresh := NewEngine("manager", &teamTestAgent{stubModelModeAgent: stubModelModeAgent{model: "gpt-4.1-mini"}, dir: e.baseWorkDir}, []Platform{p}, e.sessions.StorePath(), LangEnglish)
	fresh.SetMultiWorkspace(e.baseWorkDir, filepath.Join(e.baseWorkDir, "bindings.json"))
	fresh.SetProjectStateStore(NewProjectStateStore(e.projectState.storePath))
	fresh.SetTeamProjects(NewTeamProjectStore(e.teamProjects.path), nil)
	fresh.SetProjectCommand(func(context.Context, Platform, *Message, []string) (string, error) { return "", nil })
	fresh.SetAdminFrom("owner")
	defer fresh.Stop()
	p.clearSent()
	teamSend(fresh, p, "group-a", "/workspace")
	teamReply(t, p, next)
	p.clearSent()
	teamSend(fresh, p, "group-a", "/project bot")
	teamReply(t, p, "Current project override")
	if got := strings.Join(p.getSent(), "\n"); !strings.Contains(got, "gpt-4.1") {
		t.Fatal("model lost on restart", got)
	}
}
