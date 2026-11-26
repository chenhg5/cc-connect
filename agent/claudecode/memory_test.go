package claudecode

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func TestAutoMemoryProcessConfigDir(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", configHome)
	t.Setenv("CLAUDE_CODE_DISABLE_AUTO_MEMORY", "")
	workDir := filepath.Join(t.TempDir(), "old_project")
	// Existing projects can use the legacy key that preserves underscores.
	legacyKey := strings.NewReplacer("/", "-", "\\", "-", ":", "-").Replace(workDir)
	want := filepath.Join(configHome, "projects", legacyKey, "memory")
	if err := os.MkdirAll(want, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(want, "MEMORY.md"), []byte("# Project memory\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := &Agent{workDir: workDir, configEnv: []string{"CLAUDE_CONFIG_DIR= "}}
	if got := a.AutoMemoryDir(); got != want {
		t.Fatalf("AutoMemoryDir() = %q, want existing memory directory %q", got, want)
	}
}

func TestAutoMemoryProjectConfigDir(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	t.Setenv("CLAUDE_CODE_DISABLE_AUTO_MEMORY", "")
	configHome, providerHome, sessionHome := t.TempDir(), t.TempDir(), t.TempDir()
	a := &Agent{workDir: t.TempDir(), configEnv: []string{
		"CLAUDE_CONFIG_DIR=" + t.TempDir(),
		"CLAUDE_CONFIG_DIR= " + configHome + " ",
		"CLAUDE_CONFIG_DIR= ",
	}}
	check := func(home string) {
		t.Helper()
		checkAutoMemoryDir(t, a, filepath.Join(home, "projects", encodeClaudeProjectKey(a.workDir), "memory"))
	}
	check(configHome)
	a.providers = []core.ProviderConfig{{}, {Env: map[string]string{"CLAUDE_CONFIG_DIR": providerHome}}}
	a.activeIdx = 1
	check(providerHome)
	a.sessionEnv = []string{"CLAUDE_CONFIG_DIR=" + configHome, "CLAUDE_CONFIG_DIR=" + sessionHome, "CLAUDE_CONFIG_DIR="}
	check(sessionHome)
}

func TestAutoMemorySymlinkedWorkDir(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", configHome)
	t.Setenv("CLAUDE_CODE_DISABLE_AUTO_MEMORY", "")
	realDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "linked")
	if err := os.Symlink(realDir, link); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("cannot create directory symlink: %v", err)
		}
		t.Fatal(err)
	}
	a := &Agent{workDir: link}
	realMemory := filepath.Join(configHome, "projects", encodeClaudeProjectKey(realDir), "memory")
	linkMemory := filepath.Join(configHome, "projects", encodeClaudeProjectKey(link), "memory")
	if err := os.MkdirAll(filepath.Dir(realMemory), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := a.AutoMemoryDir(); got != realMemory {
		t.Fatalf("AutoMemoryDir() = %q, want resolved project path %q", got, realMemory)
	}
	if err := os.MkdirAll(filepath.Dir(linkMemory), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := a.AutoMemoryDir(); got != linkMemory {
		t.Fatalf("AutoMemoryDir() = %q, want direct project path %q", got, linkMemory)
	}
	if err := os.Mkdir(realMemory, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := a.AutoMemoryDir(); got != realMemory {
		t.Fatalf("AutoMemoryDir() = %q, want existing resolved memory %q", got, realMemory)
	}
	if err := os.Mkdir(linkMemory, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := a.AutoMemoryDir(); got != linkMemory {
		t.Fatalf("AutoMemoryDir() = %q, want existing direct memory %q", got, linkMemory)
	}
}

func TestAutoMemoryMissingProjectDir(t *testing.T) {
	t.Chdir(t.TempDir()) // Exercise relative paths outside a Git repository.
	configHome := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", configHome)
	t.Setenv("CLAUDE_CODE_DISABLE_AUTO_MEMORY", "")
	for _, workDir := range []string{"project", "项目 space.v1_part"} {
		t.Run(workDir, func(t *testing.T) {
			absWorkDir, err := filepath.Abs(workDir)
			if err != nil {
				t.Fatal(err)
			}
			want := filepath.Join(configHome, "projects", encodeClaudeProjectKey(absWorkDir), "memory")
			if got := (&Agent{workDir: workDir}).AutoMemoryDir(); got != want {
				t.Fatalf("AutoMemoryDir() = %q, want canonical path %q", got, want)
			}
		})
	}
}

func checkAutoMemoryDir(t *testing.T, a *Agent, want string) {
	t.Helper()
	if got := a.AutoMemoryDir(); got != want {
		t.Fatalf("AutoMemoryDir() = %q, want %q", got, want)
	}
}

func writeAutoMemoryFixture(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestAutoMemoryRunAsUser(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("run_as_user is unsupported on Windows")
	}
	configHome, workDir := t.TempDir(), t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", configHome)
	t.Setenv("CLAUDE_CODE_DISABLE_AUTO_MEMORY", "")
	memory := filepath.Join(configHome, "projects", encodeClaudeProjectKey(workDir), "memory")
	writeAutoMemoryFixture(t, filepath.Join(memory, "MEMORY.md"), "supervisor memory")
	a := &Agent{workDir: workDir}
	checkAutoMemoryDir(t, a, memory)
	a.spawnOpts.RunAsUser = "alice"
	checkAutoMemoryDir(t, a, "")
}

func TestAutoMemoryEmptyWorkDir(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	t.Setenv("CLAUDE_CODE_DISABLE_AUTO_MEMORY", "")
	for _, dir := range []string{"", " \t "} {
		checkAutoMemoryDir(t, &Agent{workDir: dir}, "")
	}
}

func TestAutoMemoryDisableEnv(t *testing.T) {
	const key = "CLAUDE_CODE_DISABLE_AUTO_MEMORY"
	configHome := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", configHome)
	t.Setenv(key, "1")
	a := &Agent{workDir: t.TempDir()}
	defaultDir := filepath.Join(configHome, "projects", encodeClaudeProjectKey(a.workDir), "memory")
	checkAutoMemoryDir(t, a, "")
	a.configEnv = []string{key + "=false"}
	checkAutoMemoryDir(t, a, defaultDir)
	a.configEnv = append(a.configEnv, key+"=1", key+"=  ")
	checkAutoMemoryDir(t, a, "")
	a.providers = []core.ProviderConfig{{Env: map[string]string{key: "0"}}}
	checkAutoMemoryDir(t, a, defaultDir)
	a.providers[0].Env[key] = "1"
	checkAutoMemoryDir(t, a, "")
	a.sessionEnv = []string{key + "=0"}
	checkAutoMemoryDir(t, a, defaultDir)
	for _, value := range []string{"1", " TrUe "} {
		a.sessionEnv = []string{key + "=" + value}
		checkAutoMemoryDir(t, a, "")
	}
}

func TestAutoMemorySettings(t *testing.T) {
	configHome, repo, home := t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", configHome)
	t.Setenv("CLAUDE_CODE_DISABLE_AUTO_MEMORY", "")
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	workDir := filepath.Join(repo, "services", "api")
	writeAutoMemoryFixture(t, filepath.Join(repo, ".git", "HEAD"), "ref: refs/heads/main")
	writeAutoMemoryFixture(t, filepath.Join(workDir, ".claude", "settings.json"), `{"autoMemoryDirectory":"untrusted"}`)
	a := &Agent{workDir: workDir}
	writeAutoMemoryFixture(t, filepath.Join(configHome, "settings.json"), "{ // user settings\n\"autoMemoryEnabled\":false, \"autoMemoryDirectory\":\"user-memory\"}")
	checkAutoMemoryDir(t, a, "")
	writeAutoMemoryFixture(t, filepath.Join(configHome, "settings.local.json"), `{"autoMemoryEnabled":true}`)
	checkAutoMemoryDir(t, a, filepath.Join(workDir, "user-memory"))
	writeAutoMemoryFixture(t, filepath.Join(repo, ".claude", "settings.json"), `{/* shared */ "autoMemoryEnabled":false}`)
	checkAutoMemoryDir(t, a, "")
	writeAutoMemoryFixture(t, filepath.Join(repo, ".claude", "settings.local.json"), `{"autoMemoryEnabled":true,"autoMemoryDirectory":"root-memory"}`)
	checkAutoMemoryDir(t, a, filepath.Join(workDir, "root-memory"))
	for _, tc := range []struct{ setting, want string }{
		{"../custom", filepath.Join(workDir, "..", "custom")},
		{"~/memories", filepath.Join(home, "memories")},
		{"~", home},
	} {
		writeAutoMemoryFixture(t, filepath.Join(workDir, ".claude", "settings.local.json"), `{"autoMemoryDirectory":"`+tc.setting+`"}`)
		checkAutoMemoryDir(t, a, tc.want)
	}
}

func TestAutoMemoryGitRoots(t *testing.T) {
	configHome, mainRepo, worktree := t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", configHome)
	t.Setenv("CLAUDE_CODE_DISABLE_AUTO_MEMORY", "")
	gitDir := filepath.Join(mainRepo, ".git", "worktrees", "wt1")
	relative, err := filepath.Rel(worktree, gitDir)
	if err != nil {
		t.Fatal(err)
	}
	writeAutoMemoryFixture(t, filepath.Join(worktree, ".git"), "gitdir: "+relative+"\n")
	common := filepath.Join(gitDir, "commondir")
	writeAutoMemoryFixture(t, common, "../..\n")
	workDirs := []string{filepath.Join(mainRepo, "services", "api"), filepath.Join(worktree, "service")}
	for _, dir := range workDirs {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	want := filepath.Join(configHome, "projects", encodeClaudeProjectKey(mainRepo), "memory")
	for _, dir := range workDirs {
		checkAutoMemoryDir(t, &Agent{workDir: dir}, want)
	}
	writeAutoMemoryFixture(t, filepath.Join(want, "MEMORY.md"), "shared")
	for _, dir := range workDirs {
		checkAutoMemoryDir(t, &Agent{workDir: dir}, want)
	}
	if err := os.Remove(common); err != nil {
		t.Fatal(err)
	}
	checkAutoMemoryDir(t, &Agent{workDir: worktree}, want)
	// An explicit commondir can describe a nonstandard admin path.
	admin := filepath.Join(worktree, "admin")
	writeAutoMemoryFixture(t, filepath.Join(worktree, ".git"), "gitdir: "+admin)
	writeAutoMemoryFixture(t, filepath.Join(admin, "commondir"), filepath.Join(mainRepo, ".git"))
	checkAutoMemoryDir(t, &Agent{workDir: worktree}, want)
}

func TestAutoMemoryExactKeyBeforeLegacy(t *testing.T) {
	configHome, repo := t.TempDir(), t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", configHome)
	t.Setenv("CLAUDE_CODE_DISABLE_AUTO_MEMORY", "")
	writeAutoMemoryFixture(t, filepath.Join(repo, ".git", "HEAD"), "ref: refs/heads/main")
	workDir := filepath.Join(repo, "old_project.v1")
	if err := os.Mkdir(workDir, 0o755); err != nil {
		t.Fatal(err)
	}
	repoMemory := filepath.Join(configHome, "projects", encodeClaudeProjectKey(repo), "memory")
	legacyKey := strings.NewReplacer("/", "-", "\\", "-", ":", "-").Replace(workDir)
	legacyMemory := filepath.Join(configHome, "projects", legacyKey, "memory")
	exactMemory := filepath.Join(configHome, "projects", encodeClaudeProjectKey(workDir), "memory")
	a := &Agent{workDir: workDir}
	if err := os.MkdirAll(filepath.Dir(legacyMemory), 0o755); err != nil {
		t.Fatal(err)
	}
	checkAutoMemoryDir(t, a, legacyMemory)
	if err := os.MkdirAll(filepath.Dir(repoMemory), 0o755); err != nil {
		t.Fatal(err)
	}
	checkAutoMemoryDir(t, a, repoMemory)
	writeAutoMemoryFixture(t, filepath.Join(legacyMemory, "MEMORY.md"), "existing legacy")
	checkAutoMemoryDir(t, a, legacyMemory)
	writeAutoMemoryFixture(t, filepath.Join(repoMemory, "MEMORY.md"), "existing canonical")
	checkAutoMemoryDir(t, a, repoMemory)
	writeAutoMemoryFixture(t, filepath.Join(exactMemory, "MEMORY.md"), "direct")
	checkAutoMemoryDir(t, a, exactMemory)
}

func TestAutoMemoryCaseSensitiveSibling(t *testing.T) {
	configHome, parent := t.TempDir(), t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", configHome)
	t.Setenv("CLAUDE_CODE_DISABLE_AUTO_MEMORY", "")
	lower, upper := filepath.Join(parent, "foo"), filepath.Join(parent, "Foo")
	if err := os.Mkdir(lower, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(upper, 0o755); os.IsExist(err) {
		t.Skip("filesystem is case-insensitive")
	} else if err != nil {
		t.Fatal(err)
	}
	lowerMemory := filepath.Join(configHome, "projects", encodeClaudeProjectKey(lower), "memory")
	want := filepath.Join(configHome, "projects", encodeClaudeProjectKey(upper), "memory")
	writeAutoMemoryFixture(t, filepath.Join(lowerMemory, "MEMORY.md"), "lower project only")
	checkAutoMemoryDir(t, &Agent{workDir: upper}, want)
	if _, err := os.Stat(want); !os.IsNotExist(err) {
		t.Fatalf("unexpected memory for Foo: %v", err)
	}
}

func TestAutoMemoryConcurrentSetWorkDir(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", configHome)
	t.Setenv("CLAUDE_CODE_DISABLE_AUTO_MEMORY", "")
	dirs := []string{t.TempDir(), t.TempDir()}
	want := func(dir string) string {
		return filepath.Join(configHome, "projects", encodeClaudeProjectKey(dir), "memory")
	}
	a := &Agent{workDir: dirs[0]}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := range 64 {
			a.SetWorkDir(dirs[i%len(dirs)])
		}
	}()
	defer func() { <-done }()
	for range 64 {
		if got := a.AutoMemoryDir(); got != want(dirs[0]) && got != want(dirs[1]) {
			t.Fatalf("unexpected memory directory %q", got)
		}
	}
}
