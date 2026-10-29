package core

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Use the index as the baseline: git diff needs a tracked file but no commit.
func commandWorkDirRepo(t *testing.T, marker string) string {
	t.Helper()
	dir := commandWorkDirTempDir(t)
	if err := os.WriteFile(filepath.Join(dir, "changes.txt"), []byte("baseline\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "--quiet"}, {"add", "--", "changes.txt"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "changes.txt"), []byte("baseline\n"+marker+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func commandWorkDirTempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestCommandWorkDir_UsesOnlyTheSelectedLiveConversation(t *testing.T) {
	defaultDir, savedDir, liveDir := commandWorkDirTempDir(t), commandWorkDirTempDir(t), commandWorkDirTempDir(t)
	for _, tc := range []struct {
		name, selectedID, liveID, want string
		stopped, dead                  bool
	}{
		{name: "matching live session", selectedID: "chosen", liveID: "chosen", want: liveDir},
		{name: "different live session after switch", selectedID: "chosen", liveID: "previous", want: savedDir},
		{name: "old live session after new", liveID: "previous", want: defaultDir},
		{name: "stopped matching process", selectedID: "chosen", liveID: "chosen", stopped: true, want: savedDir},
		{name: "dead matching process", selectedID: "chosen", liveID: "chosen", dead: true, want: savedDir},
		{name: "new process before ID arrives", want: liveDir},
	} {
		t.Run(tc.name, func(t *testing.T) {
			agent := &cujDirectoryAgent{defaultWorkDir: defaultDir, workDirs: map[string]string{"chosen": savedDir}}
			e := NewEngine("test", agent, nil, "", LangEnglish)
			t.Cleanup(func() { _ = e.Stop() })
			const key = "test:cwd-user"
			e.sessions.GetOrCreateActive(key).SetAgentSessionID(tc.selectedID, agent.Name())
			live := &cujDirectorySession{cujAgentSession: newCUJAgentSession(), id: tc.liveID, workDir: liveDir}
			live.closed.Set(tc.dead)
			e.interactiveStates[key] = &interactiveState{agent: agent, agentSession: live, stopped: tc.stopped}
			got, err := e.commandWorkDir(agent, e.sessions, key, &Message{SessionKey: key})
			if err != nil || got != tc.want {
				t.Fatalf("command directory = %q, %v; want %q", got, err, tc.want)
			}
			if agent.GetWorkDir() != defaultDir {
				t.Fatal("reading a conversation directory changed the project default")
			}
		})
	}
}

func TestCommandWorkDir_NormalizesRelativeLiveDirectoryButRejectsRelativeSavedDirectory(t *testing.T) {
	root := commandWorkDirTempDir(t)
	t.Chdir(root)
	if err := os.Mkdir("project", 0o700); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{".", "project"} {
		t.Run(dir, func(t *testing.T) {
			agent := &cujDirectoryAgent{defaultWorkDir: root, workDirs: map[string]string{"chosen": dir}}
			e := NewEngine("test", agent, nil, "", LangEnglish)
			t.Cleanup(func() { _ = e.Stop() })
			const key = "test:relative-cwd"
			e.sessions.GetOrCreateActive(key).SetAgentSessionID("chosen", agent.Name())
			msg := &Message{SessionKey: key}
			if got, err := e.commandWorkDir(agent, e.sessions, key, msg); err == nil || got != "" {
				t.Fatalf("relative saved directory accepted: %q, %v", got, err)
			}
			live := &cujDirectorySession{cujAgentSession: newCUJAgentSession(), id: "chosen", workDir: dir}
			e.interactiveStates[key] = &interactiveState{agent: agent, agentSession: live}
			got, err := e.commandWorkDir(agent, e.sessions, key, msg)
			if want := filepath.Join(root, dir); err != nil || got != want {
				t.Fatalf("live directory = %q, %v; want %q", got, err, want)
			}
		})
	}
}

type commandWorkDirLookupAgent struct {
	stubWorkDirAgent
	savedDir string
	err      error
	lookup   func(context.Context, string, string) (string, error)
}

func (a *commandWorkDirLookupAgent) GetSessionWorkDir(ctx context.Context, id, providerName string) (string, error) {
	if a.lookup != nil {
		return a.lookup(ctx, id, providerName)
	}
	return a.savedDir, a.err
}

func TestCommandWorkDir_RestoresPersistedProviderBeforeFirstTurn(t *testing.T) {
	defaultDir, savedDir, liveDir := commandWorkDirTempDir(t), commandWorkDirTempDir(t), commandWorkDirTempDir(t)
	store := filepath.Join(t.TempDir(), "sessions.json")
	lookupCalls := 0
	var requestedID, requestedProvider string
	agent := &commandWorkDirLookupAgent{stubWorkDirAgent: stubWorkDirAgent{workDir: defaultDir}}
	agent.lookup = func(_ context.Context, id, providerName string) (string, error) {
		lookupCalls++
		requestedID, requestedProvider = id, providerName
		if providerName == "saved-provider" {
			return savedDir, nil
		}
		return defaultDir, nil
	}
	const key = "test:cwd-user"
	previous := NewEngine("test", agent, nil, store, LangEnglish)
	selected := previous.sessions.GetOrCreateActive(key)
	selected.SetAgentSessionID("chosen", agent.Name())
	selected.SetActiveProvider("saved-provider")
	previous.sessions.Save()
	if err := previous.Stop(); err != nil {
		t.Fatal(err)
	}

	// No process has resumed after reload, so its provider has not been applied
	// to the agent. Directory lookup must use the conversation's saved choice.
	e := NewEngine("test", agent, nil, store, LangEnglish)
	t.Cleanup(func() { _ = e.Stop() })
	msg := &Message{SessionKey: key}
	got, err := e.commandWorkDir(agent, e.sessions, key, msg)
	if err != nil || got != savedDir || requestedID != "chosen" || requestedProvider != "saved-provider" {
		t.Fatalf("reloaded directory=%q, err=%v, lookup=(%q,%q); want saved conversation/provider in %q", got, err, requestedID, requestedProvider, savedDir)
	}

	// A matching process may know a newer cwd; it must win without consulting
	// the persisted lookup or changing the shared agent default.
	live := &cujDirectorySession{cujAgentSession: newCUJAgentSession(), id: "chosen", workDir: liveDir}
	e.interactiveStates[key] = &interactiveState{agent: agent, agentSession: live}
	lookupCalls = 0
	got, err = e.commandWorkDir(agent, e.sessions, key, msg)
	if err != nil || got != liveDir || lookupCalls != 0 {
		t.Fatalf("live directory=%q, err=%v, saved lookups=%d; want %q with no saved lookup", got, err, lookupCalls, liveDir)
	}
	if agent.GetWorkDir() != defaultDir {
		t.Fatal("conversation lookup changed the shared default directory")
	}
}

func TestCommandWorkDir_LookupFailureNeverRunsInDefaultDirectory(t *testing.T) {
	for _, failure := range []string{"lookup error", "missing directory"} {
		t.Run(failure, func(t *testing.T) {
			defaultDir := commandWorkDirRepo(t, "DEFAULT_REPOSITORY_CHANGE")
			agent := &commandWorkDirLookupAgent{stubWorkDirAgent: stubWorkDirAgent{workDir: defaultDir}}
			wantError := "lookup unavailable"
			if failure == "lookup error" {
				agent.err = errors.New(wantError)
			} else {
				agent.savedDir = filepath.Join(t.TempDir(), "deleted")
				wantError = agent.savedDir
			}
			p := &stubPlatformEngine{n: "test"}
			e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
			e.SetAdminFrom("*")
			e.SetBaseWorkDir(defaultDir)
			t.Cleanup(func() { _ = e.Stop() })
			env := &cujEnv{t: t, engine: e, plat: p}
			e.sessions.GetOrCreateActive("test:cwd-user").SetAgentSessionID("chosen", agent.Name())
			for _, command := range []string{"/shell echo unexpected > unexpectedly-executed.txt", "/diff", "/show changes.txt", "/dir"} {
				p.clearSent()
				env.userSends("cwd-user", command)
				env.waitFor(command+" lookup error", 3*time.Second, func() bool { return env.sentContains(wantError) })
				if env.sentContains("DEFAULT_REPOSITORY_CHANGE") {
					t.Fatalf("%s read from the default repository after lookup failed: %v", command, p.getSent())
				}
			}
			if _, err := os.Stat(filepath.Join(defaultDir, "unexpectedly-executed.txt")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("shell ran in the default repository after lookup failed: %v", err)
			}

			// Help and reset must remain available to recover a broken binding.
			p.clearSent()
			env.userSends("cwd-user", "/dir help")
			env.waitFor("directory help", time.Second, func() bool { return env.sentContains(e.i18n.T(MsgDirUsage)) })
			p.clearSent()
			env.userSends("cwd-user", "/dir reset")
			env.waitFor("directory reset", time.Second, func() bool { return env.sentContains(e.i18n.Tf(MsgDirReset, defaultDir)) })
			p.clearSent()
			env.userSends("cwd-user", "/shell pwd")
			env.waitFor("shell after recovery", 3*time.Second, func() bool { return env.sentContains(defaultDir) })
		})
	}
}

func TestCommandWorkDir_CardsShowSelectedConversationDirectory(t *testing.T) {
	defaultDir, savedDir := commandWorkDirTempDir(t), commandWorkDirTempDir(t)
	agent := &cujDirectoryAgent{defaultWorkDir: defaultDir, workDirs: map[string]string{"chosen": savedDir}}
	e := NewEngine("test", agent, nil, "", LangEnglish)
	t.Cleanup(func() { _ = e.Stop() })
	const key = "test:cwd-user"
	e.sessions.GetOrCreateActive(key).SetAgentSessionID("chosen", agent.Name())
	dirCard, err := e.renderDirCard(key, 1)
	if err != nil {
		t.Fatal(err)
	}
	for name, card := range map[string]*Card{"dir": dirCard, "status": e.renderStatusCard(key, "cwd-user")} {
		visible := card.RenderText()
		if !strings.Contains(visible, savedDir) || strings.Contains(visible, defaultDir) {
			t.Fatalf("%s card does not describe the selected directory: %s", name, visible)
		}
	}
}

func TestCmdDir_RelativePathUsesSelectedConversationDirectory(t *testing.T) {
	defaultDir, savedDir := commandWorkDirTempDir(t), commandWorkDirTempDir(t)
	for _, dir := range []string{defaultDir, savedDir} {
		if err := os.Mkdir(filepath.Join(dir, "next"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	agent := &cujDirectoryAgent{defaultWorkDir: defaultDir, workDirs: map[string]string{"chosen": savedDir}}
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
	e.SetAdminFrom("*")
	t.Cleanup(func() { _ = e.Stop() })
	env := &cujEnv{t: t, engine: e, plat: p}
	e.sessions.GetOrCreateActive("test:cwd-user").SetAgentSessionID("chosen", agent.Name())
	env.userSends("cwd-user", "/dir next")
	want := filepath.Join(savedDir, "next")
	env.waitFor("relative directory change", time.Second, func() bool { return env.sentContains(want) })
	if env.sentContains(filepath.Join(defaultDir, "next")) {
		t.Fatalf("relative /dir used the default repository: %v", p.getSent())
	}
}
