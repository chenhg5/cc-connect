package opencode

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func writeOpencodeSkill(t *testing.T, root, name, frontName, desc, body string) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: " + frontName + "\ndescription: " + desc + "\n---\n" + body + "\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeValidOpencodeSkill(t *testing.T, root, name string) {
	t.Helper()
	writeOpencodeSkill(t, root, name, name, "Valid "+name+" skill", "Instructions for "+name)
}

func writeOpencodeConfig(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "opencode.json"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func listOpencodeSkills(t *testing.T, a *Agent) []*core.Skill {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	skills, err := a.ListSkills(ctx)
	if err != nil {
		t.Fatalf("ListSkills: %v", err)
	}
	return skills
}

func skillNames(skills []*core.Skill) []string {
	names := make([]string, 0, len(skills))
	for _, s := range skills {
		names = append(names, s.Name)
	}
	return names
}

func containsName(skills []*core.Skill, name string) bool {
	for _, s := range skills {
		if s.Name == name {
			return true
		}
	}
	return false
}

func TestListSkills_AllowReturnsPrompt(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	repo := filepath.Join(tmp, "repo")
	workDir := filepath.Join(repo, "pkg")
	setTestHome(t, home)
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeValidOpencodeSkill(t, filepath.Join(repo, ".opencode", "skills"), "my-skill")

	a := &Agent{workDir: workDir}
	skills := listOpencodeSkills(t, a)
	if !containsName(skills, "my-skill") {
		t.Fatalf("expected my-skill in %v", skillNames(skills))
	}
	for _, s := range skills {
		if s.Name == "my-skill" && !strings.Contains(s.Prompt, "Instructions for my-skill") {
			t.Fatalf("prompt not loaded: %+v", s)
		}
	}
	var _ core.SkillCatalogProvider = a
}

func TestListSkills_DenyExcludesWithoutInjection(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	repo := filepath.Join(tmp, "repo")
	setTestHome(t, home)
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	skillsRoot := filepath.Join(repo, ".opencode", "skills")
	writeValidOpencodeSkill(t, skillsRoot, "allowed-skill")
	writeValidOpencodeSkill(t, skillsRoot, "denied-skill")
	// Denied-but-malformed skill must also stay hidden without breaking the catalog.
	malformedDenied := filepath.Join(skillsRoot, "denied-broken")
	if err := os.MkdirAll(malformedDenied, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(malformedDenied, "SKILL.md"), []byte("no frontmatter at all"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeOpencodeConfig(t, repo, `{"permission": {"skill": {"*": "allow", "denied-*": "deny"}}}`)

	a := &Agent{workDir: repo}
	skills := listOpencodeSkills(t, a)
	if !containsName(skills, "allowed-skill") {
		t.Fatalf("allowed skill missing: %v", skillNames(skills))
	}
	for _, forbidden := range []string{"denied-skill", "denied-broken"} {
		if containsName(skills, forbidden) {
			t.Fatalf("denied skill %q leaked: %v", forbidden, skillNames(skills))
		}
	}
}

func TestListSkills_AskIsExcluded(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	repo := filepath.Join(tmp, "repo")
	setTestHome(t, home)
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	skillsRoot := filepath.Join(repo, ".opencode", "skills")
	writeValidOpencodeSkill(t, skillsRoot, "open-skill")
	writeValidOpencodeSkill(t, skillsRoot, "ask-skill")
	writeOpencodeConfig(t, repo, `{"permission": {"skill": {"*": "allow", "ask-skill": "ask"}}}`)

	a := &Agent{workDir: repo}
	skills := listOpencodeSkills(t, a)
	if !containsName(skills, "open-skill") {
		t.Fatalf("open skill missing: %v", skillNames(skills))
	}
	if containsName(skills, "ask-skill") {
		t.Fatalf("ask skill must not be auto-injected: %v", skillNames(skills))
	}
}

func TestListSkills_DisabledToolReturnsEmptyAuthoritative(t *testing.T) {
	for _, cfg := range []string{
		`{"tools": {"skill": false}}`,
		`{"permission": {"skill": "deny"}}`,
		`{"permission": "deny"}`,
	} {
		tmp := t.TempDir()
		home := filepath.Join(tmp, "home")
		repo := filepath.Join(tmp, "repo")
		setTestHome(t, home)
		if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
		writeValidOpencodeSkill(t, filepath.Join(repo, ".opencode", "skills"), "any-skill")
		writeOpencodeConfig(t, repo, cfg)

		a := &Agent{workDir: repo}
		skills := listOpencodeSkills(t, a)
		if len(skills) != 0 {
			t.Fatalf("config %s: expected empty catalog, got %v", cfg, skillNames(skills))
		}
	}
}

func TestListSkills_AgentOverrideTakesPrecedence(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	repo := filepath.Join(tmp, "repo")
	setTestHome(t, home)
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeValidOpencodeSkill(t, filepath.Join(repo, ".opencode", "skills"), "gated-skill")
	writeOpencodeConfig(t, repo, `{
		"permission": {"skill": {"*": "deny"}},
		"agent": {"plan": {"permission": {"skill": {"gated-skill": "allow"}}}}
	}`)

	plan := &Agent{workDir: repo, agentName: "plan"}
	if got := listOpencodeSkills(t, plan); !containsName(got, "gated-skill") {
		t.Fatalf("agent override missing: %v", skillNames(got))
	}
	build := &Agent{workDir: repo, agentName: "build"}
	if got := listOpencodeSkills(t, build); containsName(got, "gated-skill") {
		t.Fatalf("global deny leaked to other agent: %v", skillNames(got))
	}
}

func TestListSkills_MalformedSkippedNeverInjected(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	repo := filepath.Join(tmp, "repo")
	setTestHome(t, home)
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	skillsRoot := filepath.Join(repo, ".opencode", "skills")
	writeValidOpencodeSkill(t, skillsRoot, "good-skill")
	// Missing frontmatter.
	bad1 := filepath.Join(skillsRoot, "bad-skill")
	if err := os.MkdirAll(bad1, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bad1, "SKILL.md"), []byte("just body, no frontmatter"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Name mismatch.
	writeOpencodeSkill(t, skillsRoot, "mismatch-skill", "other-name", "Some desc", "Body")
	// Invalid name (uppercase/underscore violates opencode regex).
	writeOpencodeSkill(t, skillsRoot, "Bad_Name", "Bad_Name", "Some desc", "Body")
	// Empty body.
	writeOpencodeSkill(t, skillsRoot, "empty-skill", "empty-skill", "Some desc", "")
	// Missing description.
	writeOpencodeSkill(t, skillsRoot, "nodesc-skill", "nodesc-skill", "", "Body without desc")

	a := &Agent{workDir: repo}
	skills := listOpencodeSkills(t, a)
	if !containsName(skills, "good-skill") {
		t.Fatalf("good skill missing: %v", skillNames(skills))
	}
	for _, bad := range []string{"bad-skill", "mismatch-skill", "Bad_Name", "empty-skill", "nodesc-skill"} {
		if containsName(skills, bad) {
			t.Fatalf("malformed skill %q injected: %v", bad, skillNames(skills))
		}
	}
}

func TestListSkills_DisableClaudeCodeSkillsEnv(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	repo := filepath.Join(tmp, "repo")
	setTestHome(t, home)
	t.Setenv("OPENCODE_DISABLE_CLAUDE_CODE_SKILLS", "1")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeValidOpencodeSkill(t, filepath.Join(repo, ".opencode", "skills"), "native-skill")
	writeValidOpencodeSkill(t, filepath.Join(repo, ".claude", "skills"), "claude-only")

	a := &Agent{workDir: repo}
	skills := listOpencodeSkills(t, a)
	if !containsName(skills, "native-skill") {
		t.Fatalf("native skill missing: %v", skillNames(skills))
	}
	if containsName(skills, "claude-only") {
		t.Fatalf("claude skill leaked despite disable env: %v", skillNames(skills))
	}
	for _, d := range a.SkillDirs() {
		if strings.Contains(filepath.ToSlash(d), "/.claude/skills") {
			t.Fatalf("SkillDirs leaked claude dir despite disable env: %v", d)
		}
	}
}

func TestListSkills_FailClosedOnBadConfig(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	repo := filepath.Join(tmp, "repo")
	setTestHome(t, home)
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeValidOpencodeSkill(t, filepath.Join(repo, ".opencode", "skills"), "any-skill")
	writeOpencodeConfig(t, repo, `{"permission": {"skill": "allow"`)

	a := &Agent{workDir: repo}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := a.ListSkills(ctx); err == nil {
		t.Fatal("invalid opencode.json must fail closed with error, not fallback to dirs")
	}

	t.Setenv("OPENCODE_CONFIG_CONTENT", `{"permission": {"skill": "allow"`)
	b := &Agent{workDir: repo}
	// Point project config away so only the inline content is bad: use a clean repo.
	cleanRepo := filepath.Join(tmp, "clean")
	if err := os.MkdirAll(filepath.Join(cleanRepo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = b
	c := &Agent{workDir: cleanRepo}
	if _, err := c.ListSkills(ctx); err == nil {
		t.Fatal("invalid OPENCODE_CONFIG_CONTENT must fail closed")
	}
}
