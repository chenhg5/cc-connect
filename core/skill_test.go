package core

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// Per the Claude Code CLI convention (issue #1304), only the depth-1 layout
// `skills/<name>/SKILL.md` is registered. Nested SKILL.md files are assets,
// not installable skills.

func TestSkillRegistryListAll_FindsDepth1Skills(t *testing.T) {
	root := t.TempDir()
	writeSkillFile(t, filepath.Join(root, "telegram-codex-bot", "SKILL.md"), "Telegram bot skill")
	writeSkillFile(t, filepath.Join(root, "doc", "SKILL.md"), "Doc skill")
	writeSkillFile(t, filepath.Join(root, "skill-installer", "SKILL.md"), "System skill")

	r := NewSkillRegistry()
	r.SetDirs([]string{root})

	skills := r.ListAll()

	if len(skills) != 3 {
		t.Fatalf("skills discovered = %d, want 3", len(skills))
	}
	if r.Resolve("telegram-codex-bot") == nil {
		t.Fatalf("expected telegram-codex-bot to resolve")
	}
	if r.Resolve("doc") == nil {
		t.Fatalf("expected doc to resolve")
	}
	if r.Resolve("skill-installer") == nil {
		t.Fatalf("expected skill-installer to resolve")
	}
}

func TestSkillRegistryListAll_IgnoresNestedSkillFiles(t *testing.T) {
	root := t.TempDir()
	// Depth-1 skill — should be registered.
	writeSkillFile(t, filepath.Join(root, "frontend-design", "SKILL.md"), "Frontend design skill")
	// Nested SKILL.md files inside the skill — should NOT be registered.
	// This is the exact layout from issue #1304 that leaked 101 phantom
	// slash commands into Discord's command menu.
	writeSkillFile(t, filepath.Join(root, "frontend-design", "references", "finance-report", "SKILL.md"), "Finance report template")
	writeSkillFile(t, filepath.Join(root, "frontend-design", "references", "html-ppt-knowledge-arch-blueprint", "SKILL.md"), "PPT knowledge template")
	// Nested SKILL.md at arbitrary depth — also ignored.
	writeSkillFile(t, filepath.Join(root, "frontend-design", "references", "deep", "deeper", "SKILL.md"), "Deeply nested")

	r := NewSkillRegistry()
	r.SetDirs([]string{root})

	skills := r.ListAll()

	if len(skills) != 1 {
		names := make([]string, 0, len(skills))
		for _, s := range skills {
			names = append(names, s.Name)
		}
		t.Fatalf("skills discovered = %d (%v), want 1", len(skills), names)
	}
	if skills[0].Name != "frontend-design" {
		t.Fatalf("skill name = %q, want frontend-design", skills[0].Name)
	}
	if r.Resolve("finance-report") != nil {
		t.Fatalf("nested SKILL.md must not resolve as a skill")
	}
	if r.Resolve("html-ppt-knowledge-arch-blueprint") != nil {
		t.Fatalf("nested SKILL.md must not resolve as a skill")
	}
	if r.Resolve("deeper") != nil {
		t.Fatalf("nested SKILL.md at any depth must not resolve as a skill")
	}
}

func TestSkillRegistryListAll_IgnoresSubdirWithoutSkillFile(t *testing.T) {
	root := t.TempDir()
	// Has SKILL.md — registered.
	writeSkillFile(t, filepath.Join(root, "real-skill", "SKILL.md"), "Real skill")
	// Missing SKILL.md — silently skipped (and we don't recurse to look for one).
	if err := os.MkdirAll(filepath.Join(root, "no-skill-file", "references"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Has SKILL.md but at depth-2 — also skipped (no recursion).
	writeSkillFile(t, filepath.Join(root, "real-skill", "references", "SKILL.md"), "Asset")

	r := NewSkillRegistry()
	r.SetDirs([]string{root})

	skills := r.ListAll()

	if len(skills) != 1 {
		t.Fatalf("skills discovered = %d, want 1", len(skills))
	}
	if skills[0].Name != "real-skill" {
		t.Fatalf("skill name = %q, want real-skill", skills[0].Name)
	}
}

func TestSkillRegistryListAll_FollowsDirectorySymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires administrator on Windows")
	}
	root := t.TempDir()
	targetRoot := t.TempDir()
	writeSkillFile(t, filepath.Join(targetRoot, "telegram-codex-bot", "SKILL.md"), "Telegram bot skill")
	writeSkillFile(t, filepath.Join(targetRoot, "hf-papers", "SKILL.md"), "HF papers skill")

	// Symlink each individual skill directory at depth-1.
	if err := os.Symlink(filepath.Join(targetRoot, "telegram-codex-bot"), filepath.Join(root, "telegram-codex-bot")); err != nil {
		t.Fatalf("symlink telegram-codex-bot: %v", err)
	}
	if err := os.Symlink(filepath.Join(targetRoot, "hf-papers"), filepath.Join(root, "hf-papers")); err != nil {
		t.Fatalf("symlink hf-papers: %v", err)
	}

	r := NewSkillRegistry()
	r.SetDirs([]string{root})

	skills := r.ListAll()

	if len(skills) != 2 {
		t.Fatalf("skills discovered = %d, want 2", len(skills))
	}
	if r.Resolve("telegram-codex-bot") == nil {
		t.Fatalf("expected symlinked depth-1 skill to resolve")
	}
	if r.Resolve("hf-papers") == nil {
		t.Fatalf("expected symlinked depth-1 skill to resolve")
	}
}

func TestSkillRegistryListAll_DoesNotLoopOnDirectorySymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires administrator on Windows")
	}
	root := t.TempDir()
	writeSkillFile(t, filepath.Join(root, "telegram-codex-bot", "SKILL.md"), "Telegram bot skill")
	// Self-referential depth-1 symlink: `self-loop` points back at itself.
	// Discovery must not hang (no recursion past depth-1, so this is the
	// natural safety — but we still verify the registry returns and the
	// legitimate skill is registered).
	if err := os.Symlink(filepath.Join(root, "self-loop"), filepath.Join(root, "self-loop")); err != nil {
		t.Skipf("filesystem does not permit self-referential symlink: %v", err)
	}

	r := NewSkillRegistry()
	r.SetDirs([]string{root})

	skills := r.ListAll()

	if len(skills) != 1 {
		t.Fatalf("skills discovered = %d, want 1", len(skills))
	}
	if r.Resolve("telegram-codex-bot") == nil {
		t.Fatalf("expected legitimate skill to still resolve alongside symlink loop")
	}
}

func TestSkillRegistryListAll_DedupesByLeafDirectoryName(t *testing.T) {
	root := t.TempDir()
	// Two different depth-1 skills happen to share a lower-case name with the
	// same normalised key — dedupe by lower-cased name across all configured
	// directories.
	otherRoot := t.TempDir()
	writeSkillFile(t, filepath.Join(root, "helper", "SKILL.md"), "Root helper")
	writeSkillFile(t, filepath.Join(otherRoot, "helper", "SKILL.md"), "Other helper")

	r := NewSkillRegistry()
	r.SetDirs([]string{root, otherRoot})

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
	// A SKILL.md placed directly at the scan root must be ignored — only
	// subdirectories count as skill candidates.
	writeSkillFile(t, filepath.Join(root, "SKILL.md"), "Root skill should be ignored")
	writeSkillFile(t, filepath.Join(root, "visible-skill", "SKILL.md"), "Visible skill")

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
