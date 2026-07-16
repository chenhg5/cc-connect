package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseSkillDirs_StringSlice(t *testing.T) {
	got, err := ParseSkillDirs([]string{"shared", "seat", ""})
	if err != nil {
		t.Fatalf("ParseSkillDirs: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %#v", got)
	}
	if !strings.HasSuffix(got[0], "shared") || !strings.HasSuffix(got[1], "seat") {
		t.Fatalf("got %#v", got)
	}
}

func TestParseSkillDirs_AnySlice(t *testing.T) {
	got, err := ParseSkillDirs([]any{"/shared", "/seat"})
	if err != nil {
		t.Fatalf("ParseSkillDirs: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %#v", got)
	}
}

func TestParseSkillDirs_Invalid(t *testing.T) {
	if _, err := ParseSkillDirs(42); err == nil {
		t.Fatal("expected error for int")
	}
	if _, err := ParseSkillDirs([]any{"ok", 1}); err == nil {
		t.Fatal("expected error for mixed array")
	}
}

func TestMergeSkillDirs_ConfigBeforeAgent(t *testing.T) {
	config := filepath.Join("cfg", "skills")
	agent := filepath.Join("agent", "skills")
	got := MergeSkillDirs([]string{config}, []string{agent})
	if len(got) != 2 {
		t.Fatalf("got %#v", got)
	}
	if !strings.HasSuffix(got[0], config) || !strings.HasSuffix(got[1], agent) {
		t.Fatalf("got %#v", got)
	}
}

func TestEngineApplyConfigSkillDirs_WithStubProvider(t *testing.T) {
	dir := t.TempDir()
	shared := writeTestSkillDir(t, dir, "shared", "grilling", "grill body")
	agentDir := writeTestSkillDir(t, dir, "agent-root", "agent-only", "agent body")

	agent := &stubSkillProviderAgent{dirs: []string{agentDir}}
	e := NewEngine("test", agent, nil, "", LangEnglish)
	if err := e.ApplyConfigSkillDirs([]string{shared}); err != nil {
		t.Fatalf("ApplyConfigSkillDirs: %v", err)
	}

	skills := e.ListSkills()
	if len(skills) != 2 {
		t.Fatalf("len(skills)=%d want 2: %+v", len(skills), skills)
	}
	if skills[0].Name != "grilling" {
		t.Fatalf("first skill = %q, want grilling (config dir first)", skills[0].Name)
	}
}

func TestEngineApplyConfigSkillDirs_NoProvider(t *testing.T) {
	dir := t.TempDir()
	shared := writeTestSkillDir(t, dir, "shared", "handoff", "handoff body")

	e := NewEngine("test", &stubAgent{}, nil, "", LangEnglish)
	if err := e.ApplyConfigSkillDirs([]string{shared}); err != nil {
		t.Fatalf("ApplyConfigSkillDirs: %v", err)
	}
	if len(e.ListSkills()) != 1 || e.ListSkills()[0].Name != "handoff" {
		t.Fatalf("skills = %+v", e.ListSkills())
	}
}

type stubSkillProviderAgent struct {
	stubAgent
	dirs []string
}

func (a *stubSkillProviderAgent) SkillDirs() []string {
	return append([]string(nil), a.dirs...)
}

func writeTestSkillDir(t *testing.T, root, subdir, name, body string) string {
	t.Helper()
	skillRoot := filepath.Join(root, subdir)
	skillDir := filepath.Join(skillRoot, name)
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: " + name + "\n---\n" + body
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return skillRoot
}

func TestNexusSharedSkillsDiscovery(t *testing.T) {
	shared := filepath.Join(`F:\nexus`, "data", "skills", "shared", "productivity")
	if _, err := os.Stat(shared); err != nil {
		t.Skip("nexus shared skills dir not present:", err)
	}
	e := NewEngine("chef-seat", &stubAgent{}, nil, "", LangEnglish)
	if err := e.ApplyConfigSkillDirs([]string{shared}); err != nil {
		t.Fatalf("ApplyConfigSkillDirs: %v", err)
	}
	got := map[string]bool{}
	for _, s := range e.ListSkills() {
		got[s.Name] = true
	}
	for _, want := range []string{"grill-me", "grilling"} {
		if !got[want] {
			t.Fatalf("missing skill %q in %#v", want, got)
		}
	}
}

func TestNexusChefSeatSkillsDiscovery(t *testing.T) {
	chefSeat := filepath.Join(`F:\nexus`, "data", "skills", "chef-seat")
	shared := filepath.Join(`F:\nexus`, "data", "skills", "shared", "productivity")
	if _, err := os.Stat(chefSeat); err != nil {
		t.Skip("nexus chef-seat skills dir not present:", err)
	}
	e := NewEngine("chef-seat", &stubAgent{}, nil, "", LangEnglish)
	if err := e.ApplyConfigSkillDirs([]string{chefSeat, shared}); err != nil {
		t.Fatalf("ApplyConfigSkillDirs: %v", err)
	}
	got := map[string]bool{}
	for _, s := range e.ListSkills() {
		got[s.Name] = true
	}
	for _, want := range []string{"grill-me", "grilling", "handoff"} {
		if !got[want] {
			t.Fatalf("missing skill %q in %#v", want, got)
		}
	}
}
