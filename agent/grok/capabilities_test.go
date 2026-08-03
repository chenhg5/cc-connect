package grok

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func TestGrokCapabilityDirsMatchNativeDiscoveryOrder(t *testing.T) {
	home := filepath.Join(t.TempDir(), "user-home")
	repoRoot := filepath.Join(t.TempDir(), "repo")
	workDir := filepath.Join(repoRoot, "packages", "app")
	mustMkdirAll(t, filepath.Join(repoRoot, ".git"))
	mustMkdirAll(t, workDir)

	agent := &Agent{
		workDir: workDir,
		configEnv: []string{
			"HOME=" + home,
			"GROK_HOME=.state/grok",
		},
		activeIdx: -1,
	}

	canonicalWorkDir, err := filepath.EvalSymlinks(workDir)
	if err != nil {
		t.Fatal(err)
	}
	levels := []string{canonicalWorkDir, filepath.Dir(canonicalWorkDir), filepath.Dir(filepath.Dir(canonicalWorkDir))}
	resolvedGrokHome := filepath.Join(canonicalWorkDir, ".state", "grok")
	wantSkills := projectCapabilityDirs(levels, "skills", true, true)
	wantSkills = append(wantSkills,
		filepath.Join(resolvedGrokHome, "skills"),
		filepath.Join(home, ".agents", "skills"),
		filepath.Join(home, ".claude", "skills"),
		filepath.Join(home, ".cursor", "skills"),
	)
	assertPathsEqual(t, agent.SkillDirs(), wantSkills)

	wantCommands := projectCapabilityDirs(levels, "commands", true, true)
	wantCommands = append(wantCommands,
		filepath.Join(resolvedGrokHome, "commands"),
		filepath.Join(home, ".agents", "commands"),
		filepath.Join(home, ".claude", "commands"),
		filepath.Join(home, ".cursor", "commands"),
	)
	assertPathsEqual(t, agent.CommandDirs(), wantCommands)
}

func TestGrokCapabilityDirsRespectCompatConfigAndEnvironment(t *testing.T) {
	home := filepath.Join(t.TempDir(), "user-home")
	workDir := filepath.Join(t.TempDir(), "repo")
	grokHome := filepath.Join(home, ".grok")
	mustMkdirAll(t, filepath.Join(workDir, ".git"))
	mustMkdirAll(t, grokHome)
	config := []byte("[compat.claude]\nskills = false\n[compat.cursor]\nskills = true\n")
	if err := os.WriteFile(filepath.Join(grokHome, "config.toml"), config, 0o600); err != nil {
		t.Fatal(err)
	}
	configured := &Agent{
		workDir: workDir,
		configEnv: []string{
			"HOME=" + home,
			"GROK_HOME=",
		},
		activeIdx: -1,
	}
	configuredSkills := configured.SkillDirs()
	assertCompatDirPresence(t, configuredSkills, ".claude", false)
	assertCompatDirPresence(t, configuredSkills, ".cursor", true)
	configuredCommands := configured.CommandDirs()
	assertCompatDirPresence(t, configuredCommands, ".claude", false)
	assertCompatDirPresence(t, configuredCommands, ".cursor", true)

	agent := &Agent{
		workDir: workDir,
		configEnv: []string{
			"HOME=" + home,
			"GROK_HOME=",
			"GROK_CLAUDE_SKILLS_ENABLED=true",
			"GROK_CURSOR_SKILLS_ENABLED=false",
		},
		activeIdx: -1,
	}

	for _, dirs := range [][]string{agent.SkillDirs(), agent.CommandDirs()} {
		assertCompatDirPresence(t, dirs, ".claude", true)
		assertCompatDirPresence(t, dirs, ".cursor", false)
	}
}

func TestGrokCapabilityDirsResolveSymlinkedWorkDirBeforeRepoWalk(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires administrator on Windows")
	}
	root := t.TempDir()
	repoRoot := filepath.Join(root, "repo")
	deepWorkDir := filepath.Join(repoRoot, "packages", "app")
	linkedWorkDir := filepath.Join(root, "linked-workdir")
	mustMkdirAll(t, filepath.Join(repoRoot, ".git"))
	mustMkdirAll(t, deepWorkDir)
	if err := os.Symlink(deepWorkDir, linkedWorkDir); err != nil {
		t.Fatal(err)
	}

	agent := &Agent{
		workDir: linkedWorkDir,
		configEnv: []string{
			"HOME=" + filepath.Join(root, "home"),
			"GROK_HOME=" + filepath.Join(root, "grok-home"),
		},
		activeIdx: -1,
	}
	canonicalWorkDir, err := filepath.EvalSymlinks(deepWorkDir)
	if err != nil {
		t.Fatal(err)
	}
	levels := []string{canonicalWorkDir, filepath.Dir(canonicalWorkDir), filepath.Dir(filepath.Dir(canonicalWorkDir))}
	wantPrefix := projectCapabilityDirs(levels, "skills", true, true)
	got := agent.SkillDirs()
	if len(got) < len(wantPrefix) {
		t.Fatalf("skill dirs length = %d, want at least %d: %v", len(got), len(wantPrefix), got)
	}
	assertPathsEqual(t, got[:len(wantPrefix)], wantPrefix)
}

func TestSkillDiscoveryConfigResolvesNativeSkillsTablePaths(t *testing.T) {
	processHome := filepath.Join(t.TempDir(), "process-home")
	effectiveHome := filepath.Join(t.TempDir(), "effective-home")
	workDir := filepath.Join(t.TempDir(), "repo")
	grokHome := filepath.Join(workDir, ".state", "grok")
	mustMkdirAll(t, workDir)
	mustMkdirAll(t, grokHome)
	t.Setenv("HOME", processHome)

	config := []byte("[skills]\n" +
		"paths = [\"~/team-skills\", \"relative/skills\", \"relative/direct/SKILL.md\"]\n" +
		"ignore = [\"~/team-skills/wip\", \"relative/skills/hidden\"]\n" +
		"disabled = [\"wip-skill\", \"local_only\"]\n")
	if err := os.WriteFile(filepath.Join(grokHome, "config.toml"), config, 0o600); err != nil {
		t.Fatal(err)
	}

	agent := &Agent{
		workDir: workDir,
		configEnv: []string{
			"HOME=" + effectiveHome,
			"GROK_HOME=.state/grok",
		},
		activeIdx: -1,
	}
	got := agent.SkillDiscoveryConfig()
	canonicalWorkDir, err := filepath.EvalSymlinks(workDir)
	if err != nil {
		t.Fatal(err)
	}

	assertPathsEqual(t, got.Dirs, agent.SkillDirs())
	assertPathsEqual(t, got.Paths, []string{
		filepath.Join(effectiveHome, "team-skills"),
		filepath.Join(canonicalWorkDir, "relative", "skills"),
		filepath.Join(canonicalWorkDir, "relative", "direct", "SKILL.md"),
	})
	assertPathsEqual(t, got.IgnorePaths, []string{
		filepath.Join(effectiveHome, "team-skills", "wip"),
		filepath.Join(canonicalWorkDir, "relative", "skills", "hidden"),
	})
	assertPathsEqual(t, got.DisabledNames, []string{"wip-skill", "local_only"})
}

func TestSkillDiscoveryConfigFallsBackWhenSkillsTableIsAbsent(t *testing.T) {
	workDir := t.TempDir()
	grokHome := filepath.Join(t.TempDir(), "grok-home")
	mustMkdirAll(t, grokHome)
	if err := os.WriteFile(filepath.Join(grokHome, "config.toml"), []byte("[compat.claude]\nskills = false\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	agent := &Agent{
		workDir:   workDir,
		configEnv: []string{"GROK_HOME=" + grokHome},
		activeIdx: -1,
	}
	got := agent.SkillDiscoveryConfig()

	assertPathsEqual(t, got.Dirs, agent.SkillDirs())
	if len(got.Paths) != 0 || len(got.IgnorePaths) != 0 || len(got.DisabledNames) != 0 {
		t.Fatalf("unexpected extended skill config: %+v", got)
	}
}

func TestSkillDiscoveryConfigCanonicalizesExistingPaths(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires administrator on Windows")
	}
	workDir := t.TempDir()
	grokHome := filepath.Join(workDir, ".grok-state")
	target := filepath.Join(workDir, "actual-skills")
	linked := filepath.Join(workDir, "linked-skills")
	mustMkdirAll(t, filepath.Join(target, "ignored"))
	mustMkdirAll(t, grokHome)
	if err := os.Symlink(target, linked); err != nil {
		t.Fatal(err)
	}
	config := []byte("[skills]\npaths = [\"linked-skills\"]\nignore = [\"linked-skills/ignored\"]\n")
	if err := os.WriteFile(filepath.Join(grokHome, "config.toml"), config, 0o600); err != nil {
		t.Fatal(err)
	}

	agent := &Agent{
		workDir:   workDir,
		configEnv: []string{"GROK_HOME=" + grokHome},
		activeIdx: -1,
	}
	got := agent.SkillDiscoveryConfig()

	canonicalTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatal(err)
	}
	assertPathsEqual(t, got.Paths, []string{canonicalTarget})
	assertPathsEqual(t, got.IgnorePaths, []string{filepath.Join(canonicalTarget, "ignored")})
}

func TestSkillDiscoveryConfigDrivesEngineRegistry(t *testing.T) {
	workDir := t.TempDir()
	grokHome := filepath.Join(workDir, ".state", "grok")
	configuredRoot := filepath.Join(workDir, "configured-skills")
	mustMkdirAll(t, grokHome)
	writeCapabilitySkill(t, filepath.Join(workDir, ".grok", "skills", "conventional", "SKILL.md"))
	writeCapabilitySkill(t, filepath.Join(configuredRoot, "nested", "configured", "SKILL.md"))
	writeCapabilitySkill(t, filepath.Join(configuredRoot, "ignored", "hidden", "SKILL.md"))
	writeCapabilitySkill(t, filepath.Join(configuredRoot, "disabled-skill", "SKILL.md"))
	writeCapabilitySkill(t, filepath.Join(configuredRoot, "disabled_skill", "SKILL.md"))
	writeCapabilityCommand(t, filepath.Join(workDir, ".grok", "commands", "grok-visible.md"))
	writeCapabilityCommand(t, filepath.Join(workDir, ".agents", "commands", "ignored-agent.md"))
	writeCapabilityCommand(t, filepath.Join(workDir, ".claude", "commands", "disabled-claude.md"))
	writeCapabilityCommand(t, filepath.Join(workDir, ".cursor", "commands", "cursor-visible.md"))
	config := []byte("[skills]\n" +
		"paths = [\"configured-skills\"]\n" +
		"ignore = [\"configured-skills/ignored\", \".agents/commands/ignored-agent.md\"]\n" +
		"disabled = [\"disabled_skill\", \"disabled-claude\"]\n")
	if err := os.WriteFile(filepath.Join(grokHome, "config.toml"), config, 0o600); err != nil {
		t.Fatal(err)
	}

	agent := &Agent{
		workDir:   workDir,
		configEnv: []string{"GROK_HOME=.state/grok"},
		activeIdx: -1,
	}
	engine := core.NewEngine("grok-skills", agent, nil, filepath.Join(t.TempDir(), "sessions.json"), core.LangEnglish)

	got := make(map[string]bool)
	for _, skill := range engine.ListSkills() {
		got[skill.Name] = true
	}
	for _, name := range []string{"conventional", "configured", "disabled-skill"} {
		if !got[name] {
			t.Errorf("expected %q in engine skill registry: %v", name, got)
		}
	}
	for _, name := range []string{"hidden", "disabled_skill"} {
		if got[name] {
			t.Errorf("expected %q to be unavailable in engine skill registry: %v", name, got)
		}
	}

	commands := make(map[string]bool)
	for _, command := range engine.GetAllCommands() {
		commands[command.Command] = true
	}
	for _, name := range []string{"grok-visible", "cursor-visible"} {
		if !commands[name] {
			t.Errorf("expected native flat command %q in engine registry", name)
		}
	}
	for _, name := range []string{"ignored-agent", "disabled-claude"} {
		if commands[name] {
			t.Errorf("filtered native flat command %q remained in engine registry", name)
		}
	}
}

func writeCapabilitySkill(t *testing.T, path string) {
	t.Helper()
	mustMkdirAll(t, filepath.Dir(path))
	if err := os.WriteFile(path, []byte("---\ndescription: test skill\n---\nPrompt body"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeCapabilityCommand(t *testing.T, path string) {
	t.Helper()
	mustMkdirAll(t, filepath.Dir(path))
	if err := os.WriteFile(path, []byte("Command prompt"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func projectCapabilityDirs(levels []string, kind string, claude, cursor bool) []string {
	var dirs []string
	for _, level := range levels {
		dirs = append(dirs,
			filepath.Join(level, ".grok", kind),
			filepath.Join(level, ".agents", kind),
		)
		if claude {
			dirs = append(dirs, filepath.Join(level, ".claude", kind))
		}
		if cursor {
			dirs = append(dirs, filepath.Join(level, ".cursor", kind))
		}
	}
	return dirs
}

func assertPathsEqual(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("paths length = %d, want %d\n got=%v\nwant=%v", len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("paths[%d] = %q, want %q\n got=%v\nwant=%v", i, got[i], want[i], got, want)
		}
	}
}

func assertCompatDirPresence(t *testing.T, dirs []string, vendor string, want bool) {
	t.Helper()
	found := false
	for _, dir := range dirs {
		if filepath.Base(filepath.Dir(dir)) == vendor {
			found = true
			break
		}
	}
	if found != want {
		t.Fatalf("compat directory %s presence = %v, want %v in %v", vendor, found, want, dirs)
	}
}
