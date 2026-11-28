package core

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unicode/utf8"
)

type autoOnlyTestAgent struct {
	stubAgent
	dir string
}

func (a *autoOnlyTestAgent) AutoMemoryDir() string { return a.dir }

type projectAndAutoTestAgent struct {
	stubMemoryAgentFull
	dir string
}

func (a *projectAndAutoTestAgent) AutoMemoryDir() string { return a.dir }

func writeMemoryFixture(t *testing.T, path, data string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
}

func memoryCommandReply(t *testing.T, agent Agent, command string) string {
	t.Helper()
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
	defer e.cancel()
	msg := &Message{SessionKey: "test:memory:user", Content: command, ReplyCtx: "ctx"}
	e.handleCommand(p, msg, command)
	sent := p.getSent()
	if len(sent) != 1 {
		t.Fatalf("%s produced %d replies, want one", command, len(sent))
	}
	return sent[0]
}

func requireMemoryFragments(t *testing.T, reply string, ordered ...string) {
	t.Helper()
	rest := reply
	for _, fragment := range ordered {
		_, after, ok := strings.Cut(rest, fragment)
		if !ok {
			t.Fatalf("missing or out-of-order fragment %q in %q", fragment, reply)
		}
		rest = after
	}
}

// Regression for #859: project memory and auto-discovered topics share one reply.
func TestCmdMemory_ShowsAutoMemorySummaryAndFiles(t *testing.T) {
	dir := t.TempDir()
	writeMemoryFixture(t, filepath.Join(dir, "Zulu.md"), "one\ntwo\nthree\nfour")
	writeMemoryFixture(t, filepath.Join(dir, "alpha.md"), "one\ntwo\nthree")
	writeMemoryFixture(t, filepath.Join(dir, "MEMORY.md"), "one\ntwo\n")
	i18n := NewI18n(LangEnglish)
	for _, tc := range []struct {
		name   string
		create bool
		data   string
	}{
		{name: "missing"},
		{name: "empty", create: true},
		{name: "populated", create: true, data: "Keep public APIs stable."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			project := filepath.Join(t.TempDir(), "CLAUDE.md")
			agent := &projectAndAutoTestAgent{stubMemoryAgentFull: stubMemoryAgentFull{projectFile: project}, dir: dir}
			if tc.create {
				writeMemoryFixture(t, project, tc.data)
			}
			projectSection := i18n.Tf(MsgMemoryEmpty, project)
			if tc.data != "" {
				projectSection = i18n.Tf(MsgMemoryShowProject, project, tc.data)
			}
			for _, command := range []string{"/memory", "/memory show"} {
				reply := memoryCommandReply(t, agent, command)
				requireMemoryFragments(t, reply, projectSection, "Auto Memory", "MEMORY.md (2 lines)", "alpha.md (3 lines)", "Zulu.md (4 lines)", "/memory auto <file>")
			}
		})
	}
}

func TestCmdMemory_AutoLegacyCompatibility(t *testing.T) {
	project := filepath.Join(t.TempDir(), "CLAUDE.md")
	writeMemoryFixture(t, project, "Rule from project file.")
	legacy := stubMemoryAgentFull{projectFile: project}
	i18n := NewI18n(LangEnglish)
	noMarkdown := t.TempDir()
	writeMemoryFixture(t, filepath.Join(noMarkdown, "notes.txt"), "ignore")
	for _, tc := range []struct {
		name      string
		agent     Agent
		autoReply MsgKey
	}{
		{"legacy provider", &legacy, MsgMemoryAutoNotSupported},
		{"unconfigured", &projectAndAutoTestAgent{legacy, ""}, MsgMemoryAutoNotSupported},
		{"missing directory", &projectAndAutoTestAgent{legacy, filepath.Join(t.TempDir(), "missing")}, MsgMemoryAutoEmpty},
		{"empty directory", &projectAndAutoTestAgent{legacy, t.TempDir()}, MsgMemoryAutoEmpty},
		{"no markdown", &projectAndAutoTestAgent{legacy, noMarkdown}, MsgMemoryAutoEmpty},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want := i18n.Tf(MsgMemoryShowProject, project, "Rule from project file.")
			if got := memoryCommandReply(t, tc.agent, "/memory"); got != want {
				t.Errorf("/memory = %q, want legacy reply %q", got, want)
			}
			if got, want := memoryCommandReply(t, tc.agent, "/memory auto"), i18n.T(tc.autoReply); got != want {
				t.Errorf("/memory auto = %q, want %q", got, want)
			}
		})
	}
	t.Run("neither provider", func(t *testing.T) {
		if got, want := memoryCommandReply(t, &stubAgent{}, "/memory auto"), i18n.T(MsgMemoryAutoNotSupported); got != want {
			t.Errorf("/memory auto = %q, want %q", got, want)
		}
	})
}

func TestCmdMemory_AutoWithoutProjectFile(t *testing.T) {
	dir := t.TempDir()
	writeMemoryFixture(t, filepath.Join(dir, "MEMORY.md"), "Index preview.")
	i18n := NewI18n(LangEnglish)
	autoOnly := &autoOnlyTestAgent{dir: dir}
	for _, agent := range []Agent{autoOnly, &projectAndAutoTestAgent{dir: dir}} {
		for _, command := range []string{"/memory", "/memory auto"} {
			got := memoryCommandReply(t, agent, command)
			requireMemoryFragments(t, got, "Auto Memory", "MEMORY.md", "Index preview.", "/memory auto <file>")
			if strings.Contains(got, i18n.T(MsgMemoryNotSupported)) {
				t.Errorf("%T %s includes conflicting unsupported notice: %q", agent, command, got)
			}
		}
	}
	for _, command := range []string{"/memory add test", "/memory global"} {
		if got, want := memoryCommandReply(t, autoOnly, command), i18n.T(MsgMemoryNotSupported); got != want {
			t.Errorf("%s = %q, want %q", command, got, want)
		}
	}
}

func TestAutoMemory_OverviewLimitOrderAndPreview(t *testing.T) {
	dir := t.TempDir()
	indexPrefix := strings.Repeat("猫🌍", 1000)
	writeMemoryFixture(t, filepath.Join(dir, "MEMORY.md"), indexPrefix+"beyond-preview")
	writeMemoryFixture(t, filepath.Join(dir, "Zulu.md"), "overflow")
	writeMemoryFixture(t, filepath.Join(dir, "Beta.md"), "second topic")
	writeMemoryFixture(t, filepath.Join(dir, "alpha.md"), "first topic")
	for i := 1; i <= 19; i++ {
		writeMemoryFixture(t, filepath.Join(dir, fmt.Sprintf("topic%02d.md", i)), "topic")
	}

	reply := memoryCommandReply(t, &autoOnlyTestAgent{dir: dir}, "/memory auto")
	want := []string{"Auto Memory", "MEMORY.md", "alpha.md", "Beta.md"}
	for i := 1; i <= 17; i++ {
		want = append(want, fmt.Sprintf("topic%02d.md", i))
	}
	want = append(want, NewI18n(LangEnglish).Tf(MsgMemoryAutoMoreFiles, 3), indexPrefix+"\n\n... (truncated)", "/memory auto <file>")
	requireMemoryFragments(t, reply, want...)
	for _, hidden := range []string{"topic18.md", "topic19.md", "Zulu.md", "beyond-preview"} {
		if strings.Contains(reply, hidden) {
			t.Errorf("overview includes omitted item %q", hidden)
		}
	}
}

func TestAutoMemory_FileLookup(t *testing.T) {
	dir := t.TempDir()
	writeMemoryFixture(t, filepath.Join(dir, "MEMORY.md"), "Index contents.")
	writeMemoryFixture(t, filepath.Join(dir, "feedback_testing.md"), "Run targeted tests.")
	writeMemoryFixture(t, filepath.Join(dir, "project_v2.1.md"), "Versioned topic.")
	writeMemoryFixture(t, filepath.Join(dir, "v1..v2.md"), "Version range.")
	writeMemoryFixture(t, filepath.Join(dir, "blank.md"), " \n\t")
	i18n := NewI18n(LangEnglish)
	for _, tc := range []struct{ name, want string }{
		{"feedback_testing.md", i18n.Tf(MsgMemoryAutoShowFile, "feedback_testing.md", "Run targeted tests.")},
		{"feedback_testing", i18n.Tf(MsgMemoryAutoShowFile, "feedback_testing.md", "Run targeted tests.")},
		{"memory", i18n.Tf(MsgMemoryAutoShowFile, "MEMORY.md", "Index contents.")},
		{"project_v2.1", i18n.Tf(MsgMemoryAutoShowFile, "project_v2.1.md", "Versioned topic.")},
		{"v1..v2.md", i18n.Tf(MsgMemoryAutoShowFile, "v1..v2.md", "Version range.")},
		{"v1..v2", i18n.Tf(MsgMemoryAutoShowFile, "v1..v2.md", "Version range.")},
		{"foo.json", i18n.T(MsgMemoryAutoInvalidFile)},
		{"missing", i18n.Tf(MsgMemoryAutoFileNotFound, "missing")},
		{"blank.md", i18n.Tf(MsgMemoryEmpty, "blank.md")},
		{"missing.md", i18n.Tf(MsgMemoryAutoFileNotFound, "missing.md")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := memoryCommandReply(t, &autoOnlyTestAgent{dir: dir}, "/memory auto "+tc.name); got != tc.want {
				t.Errorf("reply = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestAutoMemory_RejectsInvalidEntries(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "auto")
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "trap.md"), 0700); err != nil {
		t.Fatal(err)
	}
	const marker = "synthetic outside-file marker"
	outside := filepath.Join(root, "secret.md")
	writeMemoryFixture(t, outside, marker)
	writeMemoryFixture(t, filepath.Join(dir, "visible.md"), "safe")
	for _, name := range []string{".hidden.md", "notes.txt", "sub/file.md"} {
		writeMemoryFixture(t, filepath.Join(dir, name), marker)
	}
	agent := &autoOnlyTestAgent{dir: dir}
	checkOverview := func(t *testing.T, hidden ...string) {
		t.Helper()
		got := memoryCommandReply(t, agent, "/memory auto")
		requireMemoryFragments(t, got, "visible.md")
		for _, name := range append(hidden, marker) {
			if strings.Contains(got, name) {
				t.Errorf("overview exposed %q", name)
			}
		}
	}
	checkInvalid := func(t *testing.T, name string) {
		t.Helper()
		got := memoryCommandReply(t, agent, "/memory auto "+name)
		if strings.Contains(got, marker) {
			t.Fatalf("reading %q exposed fixture contents", name)
		}
		if want := NewI18n(LangEnglish).T(MsgMemoryAutoInvalidFile); got != want {
			t.Errorf("reading %q: got %q, want %q", name, got, want)
		}
	}
	checkOverview(t, ".hidden.md", "notes.txt", "sub/", "file.md", "trap.md")
	for _, name := range []string{"../secret.md", "../../etc/passwd", "/etc/passwd", "sub/file.md", ".hidden.md", "notes.txt", "trap.md"} {
		checkInvalid(t, name)
	}
	for _, tc := range []struct{ link, target string }{
		{"external.md", outside},
		{"hidden_link.md", ".hidden.md"},
		{"txt_link.md", "notes.txt"},
		{"nested_link.md", "txt_link.md"},
		{"loop.md", "loop.md"},
		{"missing_link.md", "missing.md"},
		{"dangling.md", filepath.Join(root, "missing.md")},
	} {
		t.Run(tc.link, func(t *testing.T) {
			if err := os.Symlink(tc.target, filepath.Join(dir, tc.link)); err != nil {
				if runtime.GOOS == "windows" {
					t.Skipf("could not create symlink: %v", err)
				}
				t.Fatal(err)
			}
			checkOverview(t, tc.link)
			checkInvalid(t, tc.link)
		})
	}
	t.Run("visible alias", func(t *testing.T) {
		if err := os.Symlink("visible.md", filepath.Join(dir, "alias.MD")); err != nil {
			if runtime.GOOS == "windows" {
				t.Skipf("could not create symlink: %v", err)
			}
			t.Fatal(err)
		}
		requireMemoryFragments(t, memoryCommandReply(t, agent, "/memory auto"), "alias.MD (1 lines)", "visible.md (1 lines)")
		requireMemoryFragments(t, memoryCommandReply(t, agent, "/memory auto ALIAS"), "alias.MD", "safe")
	})
}

func TestAutoMemory_OversizedUnicodeFile(t *testing.T) {
	dir := t.TempDir()
	// 256 KiB is not divisible by three, so the byte limit lands inside a rune.
	data := strings.Repeat("界", 256*1024/3+2)
	writeMemoryFixture(t, filepath.Join(dir, "large.md"), data)
	got := memoryCommandReply(t, &autoOnlyTestAgent{dir: dir}, "/memory auto large")
	want := NewI18n(LangEnglish).Tf(MsgMemoryAutoShowFile, "large.md", strings.Repeat("界", 2000)+"\n\n... (truncated)")
	if !utf8.ValidString(got) || got != want {
		t.Errorf("large file reply must contain exactly 2000 intact runes and a truncation notice (got %d bytes)", len(got))
	}
}

func TestI18n_AutoMemoryTranslations(t *testing.T) {
	for _, lang := range []Language{LangEnglish, LangChinese, LangTraditionalChinese, LangJapanese, LangSpanish} {
		if !strings.Contains(messages[MsgHelp][lang], "/memory [add|global|global add|auto]") {
			t.Errorf("%s help omits Auto Memory", lang)
		}
	}
	for _, key := range []MsgKey{
		MsgMemoryAutoNotSupported, MsgMemoryAutoEmpty, MsgMemoryAutoHeader, MsgMemoryAutoShowFile,
		MsgMemoryAutoFileNotFound, MsgMemoryAutoInvalidFile, MsgMemoryAutoReadFailed, MsgMemoryAutoHint,
		MsgMemoryAutoMoreFiles, MsgMemoryAutoLineCount, MsgMemoryAddUsage, MsgMemoryAutoUsage,
	} {
		for _, lang := range []Language{LangEnglish, LangChinese, LangTraditionalChinese, LangJapanese, LangSpanish} {
			// Look up the entry directly so English fallback cannot mask a missing translation.
			if strings.TrimSpace(messages[key][lang]) == "" {
				t.Errorf("missing %s translation for %s", lang, key)
			}
		}
	}
}

func TestCmdMemory_AddPrefix(t *testing.T) {
	project := filepath.Join(t.TempDir(), "CLAUDE.md")
	agent := &projectAndAutoTestAgent{stubMemoryAgentFull: stubMemoryAgentFull{projectFile: project}}
	requireMemoryFragments(t, memoryCommandReply(t, agent, "/memory a always format code"), NewI18n(LangEnglish).Tf(MsgMemoryAdded, project))
	requireMemoryFragments(t, memoryCommandReply(t, agent, "/memory"), "always format code")
}

func TestCmdMemory_NoConfiguredFiles(t *testing.T) {
	if got, want := memoryCommandReply(t, &projectAndAutoTestAgent{}, "/memory"), NewI18n(LangEnglish).T(MsgMemoryNotSupported); got != want {
		t.Fatalf("/memory = %q, want %q", got, want)
	}
}

func TestCmdMemory_CapabilityScopedUsage(t *testing.T) {
	for _, tc := range []struct {
		name          string
		agent         Agent
		project, auto bool
	}{
		{"project", &stubMemoryAgentFull{}, true, false},
		{"auto", &autoOnlyTestAgent{}, false, true},
		{"both", &projectAndAutoTestAgent{}, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			commands := []string{"help", "--help", "-h", "unknown"}
			if tc.project {
				commands = append(commands, "add", "global invalid", "global add")
			}
			for _, sub := range commands {
				got := memoryCommandReply(t, tc.agent, "/memory "+sub)
				if !strings.HasPrefix(got, "Usage:") || strings.Contains(got, "/memory auto") != tc.auto || strings.Contains(got, "/memory add") != tc.project || strings.Contains(got, "/memory global") != tc.project {
					t.Errorf("%s: unexpected usage %q", sub, got)
				}
			}
		})
	}
}

func TestAutoMemory_UnreadableFileReportsSanitizedError(t *testing.T) {
	if os.Geteuid() == 0 || runtime.GOOS == "windows" {
		t.Skip("requires Unix read permissions enforced for the current user")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "topic.md")
	writeMemoryFixture(t, path, "topic")
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"/memory auto", "/memory auto topic"} {
		got := memoryCommandReply(t, &autoOnlyTestAgent{dir: dir}, command)
		if want := NewI18n(LangEnglish).Tf(MsgMemoryAutoReadFailed, "permission denied"); got != want || strings.Contains(got, dir) {
			t.Errorf("%s = %q, want %q", command, got, want)
		}
	}
}

func TestAutoMemory_OversizedListingLineCount(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"MEMORY.md", "topic.md"} {
		writeMemoryFixture(t, filepath.Join(dir, name), strings.Repeat("a\n", autoMemoryMaxReadBytes/2)+"end")
	}
	got := memoryCommandReply(t, &autoOnlyTestAgent{dir: dir}, "/memory auto")
	requireMemoryFragments(t, got, "MEMORY.md (131072+ lines)", "topic.md (131072+ lines)")
}

// Create the same topic in both stores so a lookup must use the bound workspace.
func memoryWorkspaceFixture(t *testing.T, marker string) *projectAndAutoTestAgent {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "auto")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	project := filepath.Join(root, "CLAUDE.md")
	writeMemoryFixture(t, project, marker+" project")
	writeMemoryFixture(t, filepath.Join(dir, "MEMORY.md"), marker+" index")
	writeMemoryFixture(t, filepath.Join(dir, "topic.md"), marker+" topic")
	writeMemoryFixture(t, filepath.Join(dir, marker+".md"), marker)
	return &projectAndAutoTestAgent{stubMemoryAgentFull: stubMemoryAgentFull{projectFile: project}, dir: dir}
}

func bindMemoryWorkspace(t *testing.T, e *Engine, key string, agent *projectAndAutoTestAgent) {
	t.Helper()
	dir := filepath.Dir(agent.ProjectMemoryFile())
	if e.workspacePool == nil {
		e.workspacePool = newWorkspacePool(DefaultWorkspaceIdleTimeout)
	}
	ws := e.workspacePool.GetOrCreate(dir)
	ws.agent, ws.sessions = agent, NewSessionManager("")
	e.bindSendWorkDir(key, dir)
}

func TestCmdMemory_SendWorkDirUsesBoundStore(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", memoryWorkspaceFixture(t, "secret_A"), []Platform{p}, "", LangEnglish)
	defer e.cancel()
	const key = "test:bound:user"
	bindMemoryWorkspace(t, e, key, memoryWorkspaceFixture(t, "secret_B"))
	for _, tc := range []struct{ command, want string }{
		{"/memory", "secret_B project"},
		{"/memory auto", "secret_B index"},
		{"/memory auto topic", "secret_B topic"},
	} {
		p.clearSent()
		e.ReceiveMessage(p, &Message{SessionKey: key, UserID: "user", Platform: "test", Content: tc.command, ReplyCtx: "ctx"})
		got := p.getSent()
		if len(got) != 1 || !strings.Contains(got[0], tc.want) || strings.Contains(got[0], "secret_A") {
			t.Errorf("%s: expected only the bound store, got %q", tc.command, got)
		}
	}
}
