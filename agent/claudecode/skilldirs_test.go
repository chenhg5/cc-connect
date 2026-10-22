package claudecode

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestSkillDirs_UsesClaudeConfigDirAndProjectParents(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	configHome := filepath.Join(tmp, "profile-home")
	repo := filepath.Join(tmp, "repo")
	workDir := filepath.Join(repo, "nested", "pkg")

	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", configHome)

	for _, dir := range []string{
		filepath.Join(repo, "nested", "pkg"),
		filepath.Join(repo, "nested"),
		repo,
		configHome,
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(repo, ".git"), []byte("gitdir: fake\n"), 0o644); err != nil {
		t.Fatalf("write .git: %v", err)
	}

	a := &Agent{workDir: workDir}
	got := a.SkillDirs()
	want := []string{
		filepath.Join(workDir, ".claude", "skills"),
		filepath.Join(repo, "nested", ".claude", "skills"),
		filepath.Join(repo, ".claude", "skills"),
		filepath.Join(configHome, "skills"),
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

func TestSkillDirs_FallsBackToHomeClaudeDir(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	workDir := filepath.Join(tmp, "workspace")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatalf("mkdir workdir: %v", err)
	}
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")

	a := &Agent{workDir: workDir}
	got := a.SkillDirs()
	wantLast := filepath.Join(home, ".claude", "skills")
	if got[len(got)-1] != wantLast {
		t.Fatalf("last SkillDirs() = %q, want %q\nfull=%v", got[len(got)-1], wantLast, got)
	}
}

func TestSkillDirs_IncludesClaudePluginSkillRoots(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	configHome := filepath.Join(tmp, "profile-home")
	workDir := filepath.Join(tmp, "workspace")
	configPluginSkills := filepath.Join(configHome, "plugins", "cache", "claude-plugins-official", "notion", "0.1.0", "skills")
	explicitPluginSkills := filepath.Join(tmp, "external-plugins", "vendor", "plugin-dev", "skills")
	explicitSkillsRoot := filepath.Join(tmp, "direct", "skills")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatalf("mkdir workdir: %v", err)
	}
	for _, dir := range []string{configPluginSkills, explicitPluginSkills, explicitSkillsRoot} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if err := os.MkdirAll(filepath.Join(configPluginSkills, "notion", "references", "template", "skills"), 0o755); err != nil {
		t.Fatalf("mkdir nested asset skills dir: %v", err)
	}
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", configHome)

	a := &Agent{
		workDir:    workDir,
		pluginDirs: []string{filepath.Join(tmp, "external-plugins"), explicitSkillsRoot},
	}
	got := map[string]bool{}
	for _, dir := range a.SkillDirs() {
		got[dir] = true
	}

	want := []string{
		filepath.Join(configHome, "skills"),
		configPluginSkills,
		explicitPluginSkills,
		explicitSkillsRoot,
	}
	for _, dir := range want {
		if !got[dir] {
			t.Fatalf("SkillDirs missing %q, dirs=%v", dir, a.SkillDirs())
		}
	}
	if got[filepath.Join(configPluginSkills, "notion", "references", "template", "skills")] {
		t.Fatalf("SkillDirs must not include nested skills directories inside plugin skill roots: %v", a.SkillDirs())
	}
}

func TestSkillDirs_FollowsClaudePluginSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires administrator on Windows")
	}
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	configHome := filepath.Join(tmp, "profile-home")
	workDir := filepath.Join(tmp, "workspace")
	mainClaudePlugins := filepath.Join(home, ".claude", "plugins")
	pluginSkillsDir := filepath.Join(configHome, "plugins", "cache", "claude-plugins-official", "notion", "0.1.0", "skills")

	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", configHome)
	for _, dir := range []string{workDir, configHome, filepath.Join(mainClaudePlugins, "cache", "claude-plugins-official", "notion", "0.1.0", "skills")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if err := os.Symlink(mainClaudePlugins, filepath.Join(configHome, "plugins")); err != nil {
		t.Fatalf("symlink plugins: %v", err)
	}

	a := &Agent{workDir: workDir}
	got := map[string]bool{}
	for _, dir := range a.SkillDirs() {
		got[dir] = true
	}
	if !got[pluginSkillsDir] {
		t.Fatalf("SkillDirs() missing plugin root through symlink %q, dirs=%v", pluginSkillsDir, a.SkillDirs())
	}
}
