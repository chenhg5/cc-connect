package codex

import (
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func TestSkillDirs_UsesProjectAgentAndCodexHomes(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	codexHome := filepath.Join(tmp, "codex-home")
	repo := filepath.Join(tmp, "repo")
	workDir := filepath.Join(repo, "nested", "pkg")

	setTestHome(t, home)
	t.Setenv("CODEX_HOME", "")

	for _, dir := range []string{
		filepath.Join(repo, "nested", "pkg"),
		filepath.Join(repo, "nested"),
		repo,
		codexHome,
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(repo, ".git"), []byte("gitdir: fake\n"), 0o644); err != nil {
		t.Fatalf("write .git: %v", err)
	}

	a := &Agent{workDir: workDir, codexHome: codexHome}
	got := a.SkillDirs()
	want := []string{
		filepath.Join(workDir, ".agents", "skills"),
		filepath.Join(workDir, ".codex", "skills"),
		filepath.Join(repo, "nested", ".agents", "skills"),
		filepath.Join(repo, "nested", ".codex", "skills"),
		filepath.Join(repo, ".agents", "skills"),
		filepath.Join(repo, ".codex", "skills"),
		filepath.Join(codexHome, "skills"),
		filepath.Join(codexHome, "skills", ".system"),
		filepath.Join(home, ".agents", "skills"),
	}
	if len(got) != len(want) {
		t.Fatalf("len(SkillDirs()) = %d, want %d\n got=%v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("SkillDirs()[%d] = %q, want %q\nfull=%v", i, got[i], want[i], got)
		}
	}
}

func TestSkillDirs_FallsBackToEnvCodexHome(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	codexHome := filepath.Join(tmp, "profile-home")
	workDir := filepath.Join(tmp, "workspace")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatalf("mkdir workdir: %v", err)
	}

	setTestHome(t, home)
	t.Setenv("CODEX_HOME", codexHome)

	a := &Agent{workDir: workDir}
	got := a.SkillDirs()
	found := false
	for _, dir := range got {
		if dir == filepath.Join(codexHome, "skills") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("SkillDirs() missing CODEX_HOME skills dir: %v", got)
	}
}

func TestSkillDirs_IncludesCodexPluginSkillRoots(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	workDir := filepath.Join(tmp, "workspace")
	codexHome := filepath.Join(tmp, "codex-home")
	pluginSkillsDir := filepath.Join(codexHome, "plugins", "cache", "openai-curated", "github", "hash", "skills")

	setTestHome(t, home)
	t.Setenv("CODEX_HOME", codexHome)
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatalf("mkdir workdir: %v", err)
	}
	if err := os.MkdirAll(pluginSkillsDir, 0o755); err != nil {
		t.Fatalf("mkdir plugin skills dir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(pluginSkillsDir, "github", "references", "template", "skills"), 0o755); err != nil {
		t.Fatalf("mkdir nested asset skills dir: %v", err)
	}

	a := &Agent{workDir: workDir}
	got := map[string]bool{}
	for _, dir := range a.SkillDirs() {
		got[dir] = true
	}

	want := []string{
		filepath.Join(workDir, ".codex", "skills"),
		filepath.Join(codexHome, "skills"),
		filepath.Join(codexHome, "skills", ".system"),
		pluginSkillsDir,
	}
	for _, dir := range want {
		if !got[dir] {
			t.Fatalf("SkillDirs missing %q, dirs=%v", dir, a.SkillDirs())
		}
	}
	if got[filepath.Join(pluginSkillsDir, "github", "references", "template", "skills")] {
		t.Fatalf("SkillDirs must not include nested skills directories inside plugin skill roots: %v", a.SkillDirs())
	}
}

func TestSkillDirs_FollowsCodexPluginSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires administrator on Windows")
	}
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	workDir := filepath.Join(tmp, "workspace")
	codexHome := filepath.Join(tmp, "codex-agent-home")
	mainCodexPlugins := filepath.Join(home, ".codex", "plugins")
	pluginSkillsDir := filepath.Join(codexHome, "plugins", "cache", "openai-curated", "github", "hash", "skills")

	setTestHome(t, home)
	t.Setenv("CODEX_HOME", "")
	for _, dir := range []string{workDir, codexHome, filepath.Join(mainCodexPlugins, "cache", "openai-curated", "github", "hash", "skills")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if err := os.Symlink(mainCodexPlugins, filepath.Join(codexHome, "plugins")); err != nil {
		t.Fatalf("symlink plugins: %v", err)
	}

	a := &Agent{workDir: workDir, codexHome: codexHome}
	got := map[string]bool{}
	for _, dir := range a.SkillDirs() {
		got[dir] = true
	}
	if !got[pluginSkillsDir] {
		t.Fatalf("SkillDirs() missing plugin root through symlink %q, dirs=%v", pluginSkillsDir, a.SkillDirs())
	}
}

func setTestHome(t *testing.T, home string) {
	t.Helper()
	t.Setenv("HOME", home)
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", home)
		t.Setenv("HOMEDRIVE", "")
		t.Setenv("HOMEPATH", "")
	}
}

// TestSkillDirs_RaceFreeAgainstSetWorkDir pins the bug where SkillDirs
// read a.workDir and a.codexHome without holding a.mu, while
// SetWorkDir writes a.workDir under the lock. Run with -race to detect
// the data race; with the production fix the test stays clean.
func TestSkillDirs_RaceFreeAgainstSetWorkDir(t *testing.T) {
	tmp := t.TempDir()
	a := &Agent{workDir: tmp, codexHome: filepath.Join(tmp, "codex")}

	var wg sync.WaitGroup
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%2 == 0 {
				a.SetWorkDir(filepath.Join(tmp, "a"))
			} else {
				a.SetWorkDir(filepath.Join(tmp, "b"))
			}
		}(i)
	}
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = a.SkillDirs()
		}()
	}
	wg.Wait()
}

func TestSkillDirs_DiscoversSystemSkills(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	for _, source := range []string{"default", "env", "explicit"} {
		t.Run(source, func(t *testing.T) {
			codexHome := filepath.Join(home, ".codex")
			explicit := ""
			t.Setenv("CODEX_HOME", "")
			if source == "env" {
				codexHome = filepath.Join(home, "env")
				t.Setenv("CODEX_HOME", codexHome)
			}
			if source == "explicit" {
				codexHome = filepath.Join(home, "explicit")
				explicit = codexHome
				t.Setenv("CODEX_HOME", filepath.Join(home, "ignored"))
			}
			dir := filepath.Join(codexHome, "skills", ".system", "system-demo")
			if err := os.MkdirAll(dir, 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("System instructions"), 0644); err != nil {
				t.Fatal(err)
			}
			r := core.NewSkillRegistry()
			r.SetDirs(codexSkillDirs(home, explicit))
			if r.Resolve("system-demo") == nil {
				t.Fatal("system skill not discovered")
			}
		})
	}
}
