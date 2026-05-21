package core

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSkillRegistryListAll_RecursesIntoGroupedDirectories(t *testing.T) {
	root := t.TempDir()
	writeSkillFile(t, filepath.Join(root, "automation", "telegram-codex-bot", "SKILL.md"), "Telegram bot skill")
	writeSkillFile(t, filepath.Join(root, "productivity", "doc", "SKILL.md"), "Doc skill")
	writeSkillFile(t, filepath.Join(root, ".system", "skill-installer", "SKILL.md"), "System skill")

	r := NewSkillRegistry()
	r.SetDirs([]string{root})

	skills := r.ListAll()

	if len(skills) != 3 {
		t.Fatalf("skills discovered = %d, want 3", len(skills))
	}
	if r.Resolve("telegram-codex-bot") == nil {
		t.Fatalf("expected grouped skill to resolve")
	}
	if r.Resolve("doc") == nil {
		t.Fatalf("expected nested productivity skill to resolve")
	}
	if r.Resolve("skill-installer") == nil {
		t.Fatalf("expected .system skill to resolve")
	}
}

func TestSkillRegistryListAll_FollowsDirectorySymlinks(t *testing.T) {
	root := t.TempDir()
	targetRoot := t.TempDir()
	writeSkillFile(t, filepath.Join(targetRoot, "automation", "telegram-codex-bot", "SKILL.md"), "Telegram bot skill")
	writeSkillFile(t, filepath.Join(targetRoot, "research", "hf-papers", "SKILL.md"), "HF papers skill")

	if err := os.Symlink(filepath.Join(targetRoot, "automation"), filepath.Join(root, "automation")); err != nil {
		t.Fatalf("symlink automation: %v", err)
	}
	if err := os.Symlink(filepath.Join(targetRoot, "research"), filepath.Join(root, "research")); err != nil {
		t.Fatalf("symlink research: %v", err)
	}

	r := NewSkillRegistry()
	r.SetDirs([]string{root})

	skills := r.ListAll()

	if len(skills) != 2 {
		t.Fatalf("skills discovered = %d, want 2", len(skills))
	}
	if r.Resolve("telegram-codex-bot") == nil {
		t.Fatalf("expected symlinked automation skill to resolve")
	}
	if r.Resolve("hf-papers") == nil {
		t.Fatalf("expected symlinked research skill to resolve")
	}
}

func TestSkillRegistryListAll_DoesNotLoopOnDirectorySymlinks(t *testing.T) {
	root := t.TempDir()
	writeSkillFile(t, filepath.Join(root, "automation", "telegram-codex-bot", "SKILL.md"), "Telegram bot skill")

	if err := os.Symlink(filepath.Join(root, "automation"), filepath.Join(root, "automation", "again")); err != nil {
		t.Fatalf("symlink loop: %v", err)
	}

	r := NewSkillRegistry()
	r.SetDirs([]string{root})

	skills := r.ListAll()

	if len(skills) != 1 {
		t.Fatalf("skills discovered = %d, want 1", len(skills))
	}
	if r.Resolve("telegram-codex-bot") == nil {
		t.Fatalf("expected looping symlink tree to still resolve skill")
	}
}

func TestSkillRegistryListAll_DedupesByLeafDirectoryName(t *testing.T) {
	root := t.TempDir()
	writeSkillFile(t, filepath.Join(root, "apple", "helper", "SKILL.md"), "Apple helper")
	writeSkillFile(t, filepath.Join(root, "automation", "helper", "SKILL.md"), "Automation helper")

	r := NewSkillRegistry()
	r.SetDirs([]string{root})

	skills := r.ListAll()

	if len(skills) != 1 {
		t.Fatalf("skills discovered = %d, want 1", len(skills))
	}
	if skills[0].Name != "helper" {
		t.Fatalf("skill name = %q, want helper", skills[0].Name)
	}
}

func TestSkillRegistryListAll_IgnoresRootSkillFile(t *testing.T) {
	root := t.TempDir()
	writeSkillFile(t, filepath.Join(root, "SKILL.md"), "Root skill should be ignored")
	writeSkillFile(t, filepath.Join(root, "group", "visible-skill", "SKILL.md"), "Visible skill")

	r := NewSkillRegistry()
	r.SetDirs([]string{root})

	skills := r.ListAll()

	if len(skills) != 1 {
		t.Fatalf("skills discovered = %d, want 1", len(skills))
	}
	if skills[0].Name != "visible-skill" {
		t.Fatalf("skill name = %q, want visible-skill", skills[0].Name)
	}
}

func writeSkillFile(t *testing.T, path, description string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	data := []byte("---\ndescription: " + description + "\n---\nPrompt body")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
