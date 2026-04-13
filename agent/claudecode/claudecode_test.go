package claudecode

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func TestNew_ParsesRunAsUserAndRunAsEnv(t *testing.T) {
	opts := map[string]any{
		"work_dir":    "/tmp/claudecode-test",
		"run_as_user": "partseeker-coder",
		"run_as_env":  []any{"PGSSLROOTCERT", "PGSSLMODE"},
	}
	a, err := New(opts)
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	ag, ok := a.(*Agent)
	if !ok {
		t.Fatalf("agent is not *Agent: %T", a)
	}
	if ag.spawnOpts.RunAsUser != "partseeker-coder" {
		t.Errorf("spawnOpts.RunAsUser = %q, want %q", ag.spawnOpts.RunAsUser, "partseeker-coder")
	}
	if got := ag.spawnOpts.EnvAllowlist; len(got) != 2 || got[0] != "PGSSLROOTCERT" || got[1] != "PGSSLMODE" {
		t.Errorf("spawnOpts.EnvAllowlist = %v, want [PGSSLROOTCERT PGSSLMODE]", got)
	}
}

func TestNew_RunAsUserSkipsClaudeLookPath(t *testing.T) {
	// With run_as_user set, the supervisor's PATH lookup for "claude" is
	// skipped because the target user's PATH is what matters. Verify that
	// New() doesn't fail even when claude isn't on this test process's PATH.
	opts := map[string]any{
		"work_dir":    "/tmp/claudecode-test",
		"run_as_user": "target-that-definitely-exists",
	}
	// Note: this test relies on New() NOT calling exec.LookPath("claude")
	// when run_as_user is set. If claude IS on PATH in the test env,
	// either branch of the code returns success and the test still passes.
	if _, err := New(opts); err != nil {
		// The only other reason New() could fail for these opts is the
		// LookPath check — fail loudly if that's what happened.
		t.Errorf("New with run_as_user returned error (LookPath not skipped?): %v", err)
	}
	_ = core.AgentSystemPrompt // keep the core import used
}

func TestParseUserQuestions_ValidInput(t *testing.T) {
	input := map[string]any{
		"questions": []any{
			map[string]any{
				"question":    "Which database?",
				"header":      "Setup",
				"multiSelect": false,
				"options": []any{
					map[string]any{"label": "PostgreSQL", "description": "Production"},
					map[string]any{"label": "SQLite", "description": "Dev"},
				},
			},
		},
	}
	qs := parseUserQuestions(input)
	if len(qs) != 1 {
		t.Fatalf("expected 1 question, got %d", len(qs))
	}
	q := qs[0]
	if q.Question != "Which database?" {
		t.Errorf("question = %q", q.Question)
	}
	if q.Header != "Setup" {
		t.Errorf("header = %q", q.Header)
	}
	if q.MultiSelect {
		t.Error("expected multiSelect=false")
	}
	if len(q.Options) != 2 {
		t.Fatalf("expected 2 options, got %d", len(q.Options))
	}
	if q.Options[0].Label != "PostgreSQL" {
		t.Errorf("option[0].label = %q", q.Options[0].Label)
	}
	if q.Options[1].Description != "Dev" {
		t.Errorf("option[1].description = %q", q.Options[1].Description)
	}
}

func TestParseUserQuestions_EmptyInput(t *testing.T) {
	qs := parseUserQuestions(map[string]any{})
	if len(qs) != 0 {
		t.Errorf("expected 0 questions, got %d", len(qs))
	}
}

func TestParseUserQuestions_NoQuestionText(t *testing.T) {
	input := map[string]any{
		"questions": []any{
			map[string]any{"header": "Setup"},
		},
	}
	qs := parseUserQuestions(input)
	if len(qs) != 0 {
		t.Errorf("expected 0 questions (no question text), got %d", len(qs))
	}
}

func TestParseUserQuestions_MultiSelect(t *testing.T) {
	input := map[string]any{
		"questions": []any{
			map[string]any{
				"question":    "Select features",
				"multiSelect": true,
				"options": []any{
					map[string]any{"label": "Auth"},
					map[string]any{"label": "Logging"},
				},
			},
		},
	}
	qs := parseUserQuestions(input)
	if len(qs) != 1 {
		t.Fatalf("expected 1 question, got %d", len(qs))
	}
	if !qs[0].MultiSelect {
		t.Error("expected multiSelect=true")
	}
}

func TestNormalizePermissionMode(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		// dontAsk aliases
		{"dontAsk", "dontAsk"},
		{"dontask", "dontAsk"},
		{"dont-ask", "dontAsk"},
		{"dont_ask", "dontAsk"},
		// auto
		{"auto", "auto"},
		// bypassPermissions aliases
		{"bypassPermissions", "bypassPermissions"},
		{"yolo", "bypassPermissions"},
		// acceptEdits aliases
		{"acceptEdits", "acceptEdits"},
		{"edit", "acceptEdits"},
		// plan
		{"plan", "plan"},
		// default fallback
		{"", "default"},
		{"unknown", "default"},
	}
	for _, tt := range tests {
		got := normalizePermissionMode(tt.input)
		if got != tt.want {
			t.Errorf("normalizePermissionMode(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestClaudeSessionSetLiveMode(t *testing.T) {
	cs := &claudeSession{}
	cs.setPermissionMode("default")
	if cs.autoApprove.Load() || cs.acceptEditsOnly.Load() || cs.dontAsk.Load() {
		t.Fatal("expected default mode flags to be off")
	}

	if !cs.SetLiveMode("acceptEdits") {
		t.Fatal("SetLiveMode(acceptEdits) = false, want true")
	}
	if !cs.acceptEditsOnly.Load() || cs.autoApprove.Load() || cs.dontAsk.Load() {
		t.Fatal("acceptEdits flags not set correctly")
	}

	if cs.SetLiveMode("auto") {
		t.Fatal("SetLiveMode(auto) = true, want false")
	}

	cs.SetLiveMode("dontAsk")
	if !cs.dontAsk.Load() || cs.autoApprove.Load() || cs.acceptEditsOnly.Load() {
		t.Fatal("dontAsk flags not set correctly")
	}

	cs.SetLiveMode("bypassPermissions")
	if !cs.autoApprove.Load() || cs.acceptEditsOnly.Load() || cs.dontAsk.Load() {
		t.Fatal("bypassPermissions alias flags not set correctly")
	}
}

func TestClaudeSessionSetLiveMode_AutoSessionRequiresRestart(t *testing.T) {
	cs := &claudeSession{}
	cs.setPermissionMode("auto")
	if cs.SetLiveMode("default") {
		t.Fatal("SetLiveMode(default) from auto session = true, want false")
	}
}

func TestAgent_PermissionModes(t *testing.T) {
	a := &Agent{}
	modes := a.PermissionModes()
	if len(modes) == 0 {
		t.Fatal("PermissionModes() returned no modes")
	}

	foundAuto := false
	foundBypass := false
	for _, mode := range modes {
		if mode.Key == "auto" {
			foundAuto = true
		}
		if mode.Key == "bypassPermissions" {
			foundBypass = true
		}
	}
	if !foundAuto {
		t.Fatal("PermissionModes() missing auto mode")
	}
	if !foundBypass {
		t.Fatal("PermissionModes() missing bypassPermissions mode")
	}
}

func TestIsClaudeEditTool(t *testing.T) {
	for _, tool := range []string{"Edit", "Write", "NotebookEdit", "MultiEdit"} {
		if !isClaudeEditTool(tool) {
			t.Fatalf("isClaudeEditTool(%q) = false, want true", tool)
		}
	}
	if isClaudeEditTool("Bash") {
		t.Fatal("isClaudeEditTool(Bash) = true, want false")
	}
}

func TestSummarizeInput_AskUserQuestion(t *testing.T) {
	input := map[string]any{
		"questions": []any{
			map[string]any{
				"question": "Which framework?",
				"options": []any{
					map[string]any{"label": "React"},
					map[string]any{"label": "Vue"},
				},
			},
		},
	}
	result := summarizeInput("AskUserQuestion", input)
	if result == "" {
		t.Error("expected non-empty summary for AskUserQuestion")
	}
}

func TestAgent_Name(t *testing.T) {
	a := &Agent{}
	if got := a.Name(); got != "claudecode" {
		t.Errorf("Name() = %q, want %q", got, "claudecode")
	}
}

func TestAgent_CLIBinaryName(t *testing.T) {
	a := &Agent{}
	if got := a.CLIBinaryName(); got != "claude" {
		t.Errorf("CLIBinaryName() = %q, want %q", got, "claude")
	}
}

func TestAgent_CLIDisplayName(t *testing.T) {
	a := &Agent{}
	if got := a.CLIDisplayName(); got != "Claude" {
		t.Errorf("CLIDisplayName() = %q, want %q", got, "Claude")
	}
}

func TestAgent_SetWorkDir(t *testing.T) {
	a := &Agent{}
	a.SetWorkDir("/tmp/test")
	if got := a.GetWorkDir(); got != "/tmp/test" {
		t.Errorf("GetWorkDir() = %q, want %q", got, "/tmp/test")
	}
}

func TestAgent_SetModel(t *testing.T) {
	a := &Agent{}
	a.SetModel("claude-sonnet-4-20250514")
	if got := a.GetModel(); got != "claude-sonnet-4-20250514" {
		t.Errorf("GetModel() = %q, want %q", got, "claude-sonnet-4-20250514")
	}
}

func TestAgent_SetSessionEnv(t *testing.T) {
	a := &Agent{}
	a.SetSessionEnv([]string{"KEY=value"})
	if len(a.sessionEnv) != 1 || a.sessionEnv[0] != "KEY=value" {
		t.Errorf("sessionEnv = %v, want [KEY=value]", a.sessionEnv)
	}
}

func TestAgent_SetPlatformPrompt(t *testing.T) {
	a := &Agent{}
	a.SetPlatformPrompt("You are a helpful assistant on Feishu.")
	if a.platformPrompt != "You are a helpful assistant on Feishu." {
		t.Errorf("platformPrompt = %q, want %q", a.platformPrompt, "You are a helpful assistant on Feishu.")
	}
}

func TestAgent_SetMode(t *testing.T) {
	a := &Agent{}

	a.SetMode("auto")
	if got := a.GetMode(); got != "auto" {
		t.Fatalf("GetMode() after SetMode(auto) = %q, want auto", got)
	}

	a.SetMode("yolo")
	if got := a.GetMode(); got != "bypassPermissions" {
		t.Fatalf("GetMode() after SetMode(yolo) = %q, want bypassPermissions", got)
	}
}

func TestStripXMLTags(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"<tag>content</tag>", "content"},
		{"no tags", "no tags"},
		{"<a>hello</a><b>world</b>", "helloworld"},
		{"<nested><inner>text</inner></nested>", "text"},
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := stripXMLTags(tt.input)
			if got != tt.expected {
				t.Errorf("stripXMLTags(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

// verify Agent implements core.Agent
var _ core.Agent = (*Agent)(nil)

func TestEncodeClaudeProjectKey(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "simple ASCII path",
			input:    "/Users/username/Documents/project",
			expected: "-Users-username-Documents-project",
		},
		{
			name:     "path with Chinese characters",
			input:    "/Users/username/Documents/项目文件夹",
			expected: "-Users-username-Documents------", // 6 hyphens: 1 for "/" + 5 for Chinese chars
		},
		{
			name:     "path with Japanese characters",
			input:    "/Users/username/Documents/プロジェクト",
			expected: "-Users-username-Documents-------", // 6 hyphens: 1 for "/" + 5 for Japanese chars
		},
		{
			name:     "path with emoji",
			input:    "/Users/username/Documents/🎉project",
			expected: "-Users-username-Documents--project", // 2 hyphens: 1 for "/" + 1 for emoji
		},
		{
			name:     "Windows path with colon",
			input:    "C:\\Users\\username\\Documents",
			expected: "C--Users-username-Documents",
		},
		{
			name:     "path with underscore",
			input:    "/Users/username/my_project",
			expected: "-Users-username-my-project",
		},
		{
			name:     "mixed ASCII and non-ASCII",
			input:    "/Users/username/中文folder/english文件夹",
			expected: "-Users-username---folder-english---", // "/中文" = 3 hyphens, "/文件夹" = 4 hyphens
		},
		{
			name:     "empty path",
			input:    "",
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := encodeClaudeProjectKey(tt.input)
			if got != tt.expected {
				t.Errorf("encodeClaudeProjectKey(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestFindProjectDir_NonASCIIPath(t *testing.T) {
	// This test verifies that findProjectDir can handle non-ASCII paths
	// by creating a mock projects directory structure
	homeDir := t.TempDir()
	projectsBase := filepath.Join(homeDir, ".claude", "projects")

	// Test case: Chinese characters in path
	chineseWorkDir := "/Users/test/Documents/项目文件夹"
	expectedKey := encodeClaudeProjectKey(chineseWorkDir)

	// Create the mock project directory
	mockProjectDir := filepath.Join(projectsBase, expectedKey)
	if err := os.MkdirAll(mockProjectDir, 0755); err != nil {
		t.Fatalf("failed to create mock project dir: %v", err)
	}

	// Verify findProjectDir finds the directory
	found := findProjectDir(homeDir, chineseWorkDir)
	if found != mockProjectDir {
		t.Errorf("findProjectDir(%q, %q) = %q, want %q", homeDir, chineseWorkDir, found, mockProjectDir)
	}
}

func TestFindProjectDir_ASCIIPath(t *testing.T) {
	// Verify ASCII paths still work correctly
	homeDir := t.TempDir()
	projectsBase := filepath.Join(homeDir, ".claude", "projects")

	asciiWorkDir := "/Users/test/Documents/project"
	expectedKey := encodeClaudeProjectKey(asciiWorkDir)

	mockProjectDir := filepath.Join(projectsBase, expectedKey)
	if err := os.MkdirAll(mockProjectDir, 0755); err != nil {
		t.Fatalf("failed to create mock project dir: %v", err)
	}

	found := findProjectDir(homeDir, asciiWorkDir)
	if found != mockProjectDir {
		t.Errorf("findProjectDir(%q, %q) = %q, want %q", homeDir, asciiWorkDir, found, mockProjectDir)
	}
}

func TestFindProjectDir_NotFound(t *testing.T) {
	homeDir := t.TempDir()
	// Don't create any project directories

	workDir := "/Users/test/Documents/nonexistent"
	found := findProjectDir(homeDir, workDir)
	if found != "" {
		t.Errorf("findProjectDir for nonexistent project = %q, want empty string", found)
	}
}
