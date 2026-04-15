package core

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSkillRegistry_ListAll_RecursivelyDiscoversNestedSkills(t *testing.T) {
	root := t.TempDir()

	nestedSkillDir := filepath.Join(root, ".system", "openai-docs")
	if err := os.MkdirAll(nestedSkillDir, 0o755); err != nil {
		t.Fatalf("mkdir nested skill dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(nestedSkillDir, "SKILL.md"), []byte("---\ndescription: OpenAI docs\n---\nUse docs"), 0o644); err != nil {
		t.Fatalf("write nested SKILL.md: %v", err)
	}

	directSkillDir := filepath.Join(root, "tech-doc-writer")
	if err := os.MkdirAll(directSkillDir, 0o755); err != nil {
		t.Fatalf("mkdir direct skill dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(directSkillDir, "SKILL.md"), []byte("---\ndescription: Tech docs\n---\nWrite docs"), 0o644); err != nil {
		t.Fatalf("write direct SKILL.md: %v", err)
	}

	r := NewSkillRegistry()
	r.SetDirs([]string{root})

	all := r.ListAll()
	if len(all) != 2 {
		t.Fatalf("skills length = %d, want 2", len(all))
	}

	names := map[string]bool{}
	for _, s := range all {
		names[s.Name] = true
	}
	if !names["openai-docs"] {
		t.Fatalf("nested skill not discovered, names=%v", names)
	}
	if !names["tech-doc-writer"] {
		t.Fatalf("direct skill not discovered, names=%v", names)
	}
}
