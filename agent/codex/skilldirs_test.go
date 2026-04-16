package codex

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSkillDirs_IncludesCodexSpecificSkillRoots(t *testing.T) {
	workDir := t.TempDir()
	codexHome := t.TempDir()

	t.Setenv("CODEX_HOME", codexHome)

	pluginSkillsDir := filepath.Join(codexHome, "plugins", "cache", "openai-curated", "github", "hash", "skills")
	if err := os.MkdirAll(pluginSkillsDir, 0o755); err != nil {
		t.Fatalf("mkdir plugin skills dir: %v", err)
	}

	a := &Agent{workDir: workDir}
	dirs := a.SkillDirs()

	want := map[string]bool{
		filepath.Join(workDir, ".codex", "skills"):        true,
		filepath.Join(workDir, ".claude", "skills"):       true,
		filepath.Join(codexHome, "skills"):                true,
		filepath.Join(codexHome, "superpowers", "skills"): true,
		pluginSkillsDir: true,
	}

	got := map[string]bool{}
	for _, dir := range dirs {
		got[dir] = true
	}

	for dir := range want {
		if !got[dir] {
			t.Fatalf("SkillDirs missing %q, dirs=%v", dir, dirs)
		}
	}
}
