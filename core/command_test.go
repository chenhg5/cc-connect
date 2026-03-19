package core

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCommandRegistry_AddAndResolve(t *testing.T) {
	r := NewCommandRegistry()
	r.Add("greet", "Say hello", "Hello {{1}}", "", "", "config")

	cmd, ok := r.Resolve("greet")
	if !ok {
		t.Fatal("expected to resolve 'greet'")
	}
	if cmd.Name != "greet" {
		t.Errorf("Name = %q, want greet", cmd.Name)
	}
	if cmd.Prompt != "Hello {{1}}" {
		t.Errorf("Prompt = %q", cmd.Prompt)
	}
}

func TestCommandRegistry_CaseInsensitive(t *testing.T) {
	r := NewCommandRegistry()
	r.Add("Hello", "test", "prompt", "", "", "config")

	_, ok := r.Resolve("hello")
	if !ok {
		t.Error("resolve should be case-insensitive")
	}
}

func TestCommandRegistry_Remove(t *testing.T) {
	r := NewCommandRegistry()
	r.Add("tmp", "temp", "prompt", "", "", "config")

	if !r.Remove("tmp") {
		t.Error("Remove should return true")
	}
	if r.Remove("tmp") {
		t.Error("second Remove should return false")
	}
	if _, ok := r.Resolve("tmp"); ok {
		t.Error("should not resolve after remove")
	}
}

func TestCommandRegistry_ClearSource(t *testing.T) {
	r := NewCommandRegistry()
	r.Add("a", "", "", "", "", "config")
	r.Add("b", "", "", "", "", "config")
	r.Add("c", "", "", "", "", "agent")

	r.ClearSource("config")

	if _, ok := r.Resolve("a"); ok {
		t.Error("'a' should be cleared")
	}
	if _, ok := r.Resolve("c"); !ok {
		t.Error("'c' from agent source should remain")
	}
}

func TestCommandRegistry_AgentDirResolve(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "deploy.md"), []byte("Deploy to production"), 0644)

	r := NewCommandRegistry()
	r.SetAgentDirs([]string{dir})

	cmd, ok := r.Resolve("deploy")
	if !ok {
		t.Fatal("expected to resolve 'deploy' from agent dir")
	}
	if cmd.Source != "agent" {
		t.Errorf("Source = %q, want agent", cmd.Source)
	}
	if cmd.Prompt != "Deploy to production" {
		t.Errorf("Prompt = %q", cmd.Prompt)
	}
}

func TestCommandRegistry_ConfigOverridesAgent(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "deploy.md"), []byte("agent prompt"), 0644)

	r := NewCommandRegistry()
	r.SetAgentDirs([]string{dir})
	r.Add("deploy", "config deploy", "config prompt", "", "", "config")

	cmd, ok := r.Resolve("deploy")
	if !ok {
		t.Fatal("expected to resolve")
	}
	if cmd.Source != "config" {
		t.Errorf("config command should take priority, got source=%q", cmd.Source)
	}
}

func TestCommandRegistry_PathTraversal(t *testing.T) {
	dir := t.TempDir()
	parent := filepath.Dir(dir)
	os.WriteFile(filepath.Join(parent, "secret.md"), []byte("secret"), 0644)

	r := NewCommandRegistry()
	r.SetAgentDirs([]string{dir})

	_, ok := r.Resolve("../secret")
	if ok {
		t.Error("path traversal should be blocked")
	}
}

func TestCommandRegistry_ListAll(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "build.md"), []byte("Build project"), 0644)

	r := NewCommandRegistry()
	r.Add("test", "Run tests", "go test ./...", "", "", "config")
	r.SetAgentDirs([]string{dir})

	all := r.ListAll()
	if len(all) < 2 {
		t.Errorf("expected at least 2 commands, got %d", len(all))
	}

	names := map[string]bool{}
	for _, c := range all {
		names[c.Name] = true
	}
	if !names["test"] {
		t.Error("missing 'test' command")
	}
	if !names["build"] {
		t.Error("missing 'build' command from agent dir")
	}
}

func TestCommandRegistry_TomlResolve(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "review.toml"), []byte("prompt = \"Review the code\"\ndescription = \"Code review command\"\n"), 0644)

	r := NewCommandRegistry()
	r.SetAgentDirs([]string{dir})

	cmd, ok := r.Resolve("review")
	if !ok {
		t.Fatal("expected to resolve 'review' from toml file")
	}
	if cmd.Prompt != "Review the code" {
		t.Errorf("Prompt = %q, want 'Review the code'", cmd.Prompt)
	}
	if cmd.Description != "Code review command" {
		t.Errorf("Description = %q, want 'Code review command'", cmd.Description)
	}
	if cmd.Source != "agent" {
		t.Errorf("Source = %q, want 'agent'", cmd.Source)
	}
}

func TestCommandRegistry_TomlNamespaced(t *testing.T) {
	dir := t.TempDir()
	subDir := filepath.Join(dir, "git")
	os.MkdirAll(subDir, 0755)
	os.WriteFile(filepath.Join(subDir, "commit.toml"), []byte("prompt = \"Generate a commit message\"\ndescription = \"Git commit helper\"\n"), 0644)

	r := NewCommandRegistry()
	r.SetAgentDirs([]string{dir})

	// Resolve via colon-namespaced name
	cmd, ok := r.Resolve("git:commit")
	if !ok {
		t.Fatal("expected to resolve 'git:commit' from git/commit.toml")
	}
	if cmd.Name != "git:commit" {
		t.Errorf("Name = %q, want 'git:commit'", cmd.Name)
	}
	if cmd.Prompt != "Generate a commit message" {
		t.Errorf("Prompt = %q", cmd.Prompt)
	}
}

func TestCommandRegistry_TomlListAll(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "review.toml"), []byte("prompt = \"Review\"\ndescription = \"Code review\"\n"), 0644)
	os.WriteFile(filepath.Join(dir, "build.md"), []byte("Build project"), 0644)
	subDir := filepath.Join(dir, "git")
	os.MkdirAll(subDir, 0755)
	os.WriteFile(filepath.Join(subDir, "commit.toml"), []byte("prompt = \"Commit\"\n"), 0644)

	r := NewCommandRegistry()
	r.SetAgentDirs([]string{dir})

	all := r.ListAll()
	names := map[string]bool{}
	for _, c := range all {
		names[c.Name] = true
	}
	if !names["review"] {
		t.Error("missing 'review' toml command")
	}
	if !names["build"] {
		t.Error("missing 'build' md command")
	}
	if !names["git:commit"] {
		t.Error("missing 'git:commit' namespaced toml command")
	}
}

func TestCommandRegistry_MdOverridesToml(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "deploy.md"), []byte("MD deploy prompt"), 0644)
	os.WriteFile(filepath.Join(dir, "deploy.toml"), []byte("prompt = \"TOML deploy prompt\"\n"), 0644)

	r := NewCommandRegistry()
	r.SetAgentDirs([]string{dir})

	cmd, ok := r.Resolve("deploy")
	if !ok {
		t.Fatal("expected to resolve 'deploy'")
	}
	// .md should take priority over .toml
	if cmd.Prompt != "MD deploy prompt" {
		t.Errorf("Prompt = %q, want 'MD deploy prompt' (.md should override .toml)", cmd.Prompt)
	}
}

func TestNameFromRelPath(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"deploy", "deploy"},
		{filepath.Join("git", "commit"), "git:commit"},
		{filepath.Join("refactor", "pure"), "refactor:pure"},
	}
	for _, tt := range tests {
		got := nameFromRelPath(tt.input)
		if got != tt.want {
			t.Errorf("nameFromRelPath(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestExpandPrompt_NoPlaceholders(t *testing.T) {
	got := ExpandPrompt("Do the thing", []string{"arg1"})
	if got != "Do the thing\n\narg1" {
		t.Errorf("got %q", got)
	}
}

func TestExpandPrompt_NoArgs(t *testing.T) {
	got := ExpandPrompt("Just a template", nil)
	if got != "Just a template" {
		t.Errorf("got %q", got)
	}
}

func TestExpandPrompt_Positional(t *testing.T) {
	got := ExpandPrompt("Find user {{1}} in {{2}}", []string{"john", "prod"})
	if got != "Find user john in prod" {
		t.Errorf("got %q", got)
	}
}

func TestExpandPrompt_PositionalDefault(t *testing.T) {
	got := ExpandPrompt("Deploy to {{1:staging}}", nil)
	if got != "Deploy to staging" {
		t.Errorf("got %q", got)
	}
}

func TestExpandPrompt_Star(t *testing.T) {
	got := ExpandPrompt("Search: {{2*}}", []string{"prefix", "foo", "bar", "baz"})
	if got != "Search: foo bar baz" {
		t.Errorf("got %q", got)
	}
}

func TestExpandPrompt_Args(t *testing.T) {
	got := ExpandPrompt("Run with: {{args}}", []string{"a", "b", "c"})
	if got != "Run with: a b c" {
		t.Errorf("got %q", got)
	}
}

func TestExpandPrompt_ArgsDefault(t *testing.T) {
	got := ExpandPrompt("Run with: {{args:defaults}}", nil)
	if got != "Run with: defaults" {
		t.Errorf("got %q", got)
	}
}

func TestMatchSubCommand(t *testing.T) {
	candidates := []string{"list", "add", "del", "delete"}

	tests := []struct {
		input string
		want  string
	}{
		{"list", "list"},
		{"l", "list"},
		{"a", "add"},
		{"del", "del"},
		{"delete", "delete"},
		{"d", "d"}, // ambiguous: del, delete
		{"xyz", "xyz"},
	}

	for _, tt := range tests {
		got := matchSubCommand(tt.input, candidates)
		if got != tt.want {
			t.Errorf("matchSubCommand(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestMatchPrefix(t *testing.T) {
	candidates := []struct {
		names []string
		id    string
	}{
		{[]string{"help"}, "help"},
		{[]string{"provider", "pro"}, "provider"},
		{[]string{"list", "ls"}, "list"},
		{[]string{"new"}, "new"},
	}

	tests := []struct {
		input string
		want  string
	}{
		{"help", "help"},
		{"h", "help"},
		{"provider", "provider"},
		{"pro", "provider"},
		{"p", "provider"},
		{"list", "list"},
		{"ls", "list"},
		{"l", "list"},
		{"new", "new"},
		{"n", "new"},
		{"xyz", ""},
	}

	for _, tt := range tests {
		got := matchPrefix(tt.input, candidates)
		if got != tt.want {
			t.Errorf("matchPrefix(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestAllowList(t *testing.T) {
	tests := []struct {
		allow  string
		user   string
		expect bool
	}{
		{"", "anyone", true},
		{"*", "anyone", true},
		{"user1", "user1", true},
		{"user1", "USER1", true},
		{"user1,user2", "user2", true},
		{"user1, user2", "user2", true},
		{"user1", "user3", false},
		{" user1 , user2 ", "user2", true},
	}

	for _, tt := range tests {
		got := AllowList(tt.allow, tt.user)
		if got != tt.expect {
			t.Errorf("AllowList(%q, %q) = %v, want %v", tt.allow, tt.user, got, tt.expect)
		}
	}
}
