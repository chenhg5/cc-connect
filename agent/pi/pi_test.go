package pi

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// ── normalizeMode ────────────────────────────────────────────

func TestNormalizeMode(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"", "default"},
		{"default", "default"},
		{"yolo", "yolo"},
		{"YOLO", "yolo"},
		{"bypass", "yolo"},
		{"auto-approve", "yolo"},
		{"  Yolo  ", "yolo"},
		{"  Bypass ", "yolo"},
		{"unknown", "default"},
		{"something", "default"},
	}
	for _, tt := range tests {
		if got := normalizeMode(tt.in); got != tt.want {
			t.Errorf("normalizeMode(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// ── Agent constructor ────────────────────────────────────────

func TestNew_DefaultValues(t *testing.T) {
	// Isolate from real settings.json so we test pure defaults.
	savedEnv := os.Getenv("PI_CODING_AGENT_DIR")
	t.Cleanup(func() {
		if savedEnv != "" {
			_ = os.Setenv("PI_CODING_AGENT_DIR", savedEnv)
		} else {
			_ = os.Unsetenv("PI_CODING_AGENT_DIR")
		}
	})
	t.Setenv("PI_CODING_AGENT_DIR", t.TempDir())

	// Use a command that exists on all systems.
	ag, err := New(map[string]any{"cmd": "echo"})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	a := ag.(*Agent)
	if a.workDir != "." {
		t.Errorf("workDir = %q, want \".\"", a.workDir)
	}
	if a.model != "" {
		t.Errorf("model = %q, want empty", a.model)
	}
	if a.mode != "default" {
		t.Errorf("mode = %q, want \"default\"", a.mode)
	}
	if a.cmd != "echo" {
		t.Errorf("cmd = %q, want \"echo\"", a.cmd)
	}
}

func TestNew_CustomOptions(t *testing.T) {
	ag, err := New(map[string]any{
		"cmd":      "echo",
		"work_dir": "/tmp",
		"model":    "qwen3.5-plus",
		"mode":     "yolo",
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	a := ag.(*Agent)
	if a.workDir != "/tmp" {
		t.Errorf("workDir = %q", a.workDir)
	}
	if a.model != "qwen3.5-plus" {
		t.Errorf("model = %q", a.model)
	}
	if a.mode != "yolo" {
		t.Errorf("mode = %q", a.mode)
	}
}

func TestWorkspaceAgentOptions_RpcPropagates(t *testing.T) {
	// Regression test: rpc must propagate to per-workspace agents
	// reconstructed by the engine in multi-workspace mode. Without
	// WorkspaceAgentOptions(), engine.go's getOrCreateWorkspaceAgent
	// would silently drop `rpc = true` and the per-workspace agent
	// would default to JSON mode.
	a := &Agent{rpc: true, cmd: "echo", thinking: "high"}
	got := a.WorkspaceAgentOptions()

	if got["rpc"] != true {
		t.Errorf("expected rpc=true in snapshot, got %v", got["rpc"])
	}
	if got["thinking"] != "high" {
		t.Errorf("expected thinking=high in snapshot, got %v", got["thinking"])
	}
	if _, ok := got["work_dir"]; ok {
		t.Errorf("work_dir must not be in snapshot (engine sets it per-workspace)")
	}
}

func TestWorkspaceAgentOptions_DefaultsOmitted(t *testing.T) {
	// Default cmd ("pi") and unset rpc/thinking should be omitted so the
	// snapshot is a minimal delta on top of the project-level opts.
	a := &Agent{cmd: "pi", rpc: false, thinking: ""}
	got := a.WorkspaceAgentOptions()

	if _, ok := got["cmd"]; ok {
		t.Errorf("default cmd should be omitted, got %v", got["cmd"])
	}
	if _, ok := got["rpc"]; ok {
		t.Errorf("rpc=false should be omitted, got %v", got["rpc"])
	}
	if _, ok := got["thinking"]; ok {
		t.Errorf("empty thinking should be omitted, got %v", got["thinking"])
	}
}

func TestNew_CmdNotFound(t *testing.T) {
	_, err := New(map[string]any{"cmd": "nonexistent-binary-xyz-12345"})
	if err == nil {
		t.Fatal("expected error for missing binary")
	}
	if !strings.Contains(err.Error(), "not found in PATH") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestNew_DefaultCmd(t *testing.T) {
	// When cmd is not specified, it defaults to "pi".
	// This will fail if pi is not installed, which is expected in CI.
	_, err := New(map[string]any{"cmd": ""})
	if err == nil {
		// pi is installed — verify the cmd was set
		return
	}
	if !strings.Contains(err.Error(), "'pi' not found") {
		t.Errorf("unexpected error: %v", err)
	}
}

// ── Agent interface methods ──────────────────────────────────

func TestAgent_NameAndDisplay(t *testing.T) {
	a := &Agent{cmd: "pi"}
	if a.Name() != "pi" {
		t.Errorf("Name() = %q", a.Name())
	}
	if a.CLIBinaryName() != "pi" {
		t.Errorf("CLIBinaryName() = %q", a.CLIBinaryName())
	}
	if a.CLIDisplayName() != "Pi" {
		t.Errorf("CLIDisplayName() = %q", a.CLIDisplayName())
	}
}

func TestAgent_ModelGetSet(t *testing.T) {
	a := &Agent{}
	if a.GetModel() != "" {
		t.Errorf("initial model = %q", a.GetModel())
	}
	a.SetModel("gpt-4o")
	if a.GetModel() != "gpt-4o" {
		t.Errorf("after SetModel = %q", a.GetModel())
	}
}

func TestAgent_ModeGetSet(t *testing.T) {
	a := &Agent{mode: "default"}
	if a.GetMode() != "default" {
		t.Errorf("initial mode = %q", a.GetMode())
	}
	a.SetMode("yolo")
	if a.GetMode() != "yolo" {
		t.Errorf("after SetMode(yolo) = %q", a.GetMode())
	}
	a.SetMode("bypass")
	if a.GetMode() != "yolo" {
		t.Errorf("after SetMode(bypass) = %q", a.GetMode())
	}
	a.SetMode("unknown")
	if a.GetMode() != "default" {
		t.Errorf("after SetMode(unknown) = %q", a.GetMode())
	}
}

func TestAgent_AvailableModels(t *testing.T) {
	a := &Agent{}
	// Without settings.json, should return nil (no error logged — just empty).
	models := a.AvailableModels(context.Background())
	if models == nil {
		// No settings file — acceptable in test environments.
		return
	}
	// If settings.json exists with enabledModels, should return them.
	for _, m := range models {
		if m.Name == "" {
			t.Errorf("model with empty Name: %+v", m)
		}
	}
}

func TestReadSettingsModels(t *testing.T) {
	// Save and restore settings path.
	savedEnv := os.Getenv("PI_CODING_AGENT_DIR")
	t.Cleanup(func() {
		if savedEnv != "" {
			_ = os.Setenv("PI_CODING_AGENT_DIR", savedEnv)
		} else {
			_ = os.Unsetenv("PI_CODING_AGENT_DIR")
		}
	})

	tmpDir := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", tmpDir)

	// No settings.json -> error.
	_, err := readSettingsModels()
	if err == nil {
		t.Error("expected error for missing settings.json")
	}

	// Write settings.json with enabledModels.
	settings := map[string]any{
		"enabledModels": []string{
			"provider-a/family-a/model-alpha",
			"provider-a/family-a/model-beta",
			"provider-b/family-b/model-gamma",
		},
		"defaultModel":  "family-a/model-beta",
		"defaultProvider": "provider-a",
	}
	data, _ := json.Marshal(settings)
	if err := os.WriteFile(filepath.Join(tmpDir, "settings.json"), data, 0o644); err != nil {
		t.Fatalf("write settings: %v", err)
	}

	models, err := readSettingsModels()
	if err != nil {
		t.Fatalf("readSettingsModels() error = %v", err)
	}
	if len(models) != 3 {
		t.Fatalf("got %d models, want 3", len(models))
	}

	tests := []struct {
		name  string
		alias string
	}{
		{"provider-a/family-a/model-alpha", "model-alpha"},
		{"provider-a/family-a/model-beta", "model-beta"},
		{"provider-b/family-b/model-gamma", "model-gamma"},
	}
	for i, tt := range tests {
		if models[i].Name != tt.name {
			t.Errorf("models[%d].Name = %q, want %q", i, models[i].Name, tt.name)
		}
		if models[i].Alias != tt.alias {
			t.Errorf("models[%d].Alias = %q, want %q", i, models[i].Alias, tt.alias)
		}
	}
}

func TestReadDefaultModel(t *testing.T) {
	savedEnv := os.Getenv("PI_CODING_AGENT_DIR")
	t.Cleanup(func() {
		if savedEnv != "" {
			_ = os.Setenv("PI_CODING_AGENT_DIR", savedEnv)
		} else {
			_ = os.Unsetenv("PI_CODING_AGENT_DIR")
		}
	})

	tmpDir := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", tmpDir)

	// No file -> error.
	_, err := readDefaultModel()
	if err == nil {
		t.Error("expected error for missing settings.json")
	}

	// Write with both defaultProvider and defaultModel.
	settings := map[string]any{
		"defaultModel":    "family-a/model-beta",
		"defaultProvider": "provider-a",
	}
	data, _ := json.Marshal(settings)
	if err := os.WriteFile(filepath.Join(tmpDir, "settings.json"), data, 0o644); err != nil {
		t.Fatalf("write settings: %v", err)
	}

	model, err := readDefaultModel()
	if err != nil {
		t.Fatalf("readDefaultModel() error = %v", err)
	}
	if model != "family-a/model-beta" {
		t.Errorf("defaultModel = %q, want %q", model, "family-a/model-beta")
	}

	// With model without provider prefix and defaultProvider set.
	settings2 := map[string]any{
		"defaultModel":    "gpt-4o",
		"defaultProvider": "provider-b",
	}
	data2, _ := json.Marshal(settings2)
	if err := os.WriteFile(filepath.Join(tmpDir, "settings.json"), data2, 0o644); err != nil {
		t.Fatalf("write settings2: %v", err)
	}

	model2, _ := readDefaultModel()
	if model2 != "provider-b/gpt-4o" {
		t.Errorf("qualified defaultModel = %q, want %q", model2, "provider-b/gpt-4o")
	}
}

func TestPiSettingsDir(t *testing.T) {
	savedEnv := os.Getenv("PI_CODING_AGENT_DIR")
	defer func() {
		if savedEnv != "" {
			_ = os.Setenv("PI_CODING_AGENT_DIR", savedEnv)
		} else {
			_ = os.Unsetenv("PI_CODING_AGENT_DIR")
		}
	}()

	// With env var set.
	t.Setenv("PI_CODING_AGENT_DIR", "/custom/pi/path")
	if d := piSettingsDir(); d != "/custom/pi/path" {
		t.Errorf("piSettingsDir() = %q, want /custom/pi/path", d)
	}

	// Unset env var -> default.
	_ = os.Unsetenv("PI_CODING_AGENT_DIR")
	home, _ := os.UserHomeDir()
	want := filepath.Join(home, ".pi", "agent")
	if d := piSettingsDir(); d != want {
		t.Errorf("piSettingsDir() = %q, want %q", d, want)
	}
}

func TestSettingsPath(t *testing.T) {
	savedEnv := os.Getenv("PI_CODING_AGENT_DIR")
	defer func() {
		if savedEnv != "" {
			_ = os.Setenv("PI_CODING_AGENT_DIR", savedEnv)
		} else {
			_ = os.Unsetenv("PI_CODING_AGENT_DIR")
		}
	}()

	t.Setenv("PI_CODING_AGENT_DIR", "/custom")
	if p := settingsPath(); p != "/custom/settings.json" {
		t.Errorf("settingsPath() = %q, want /custom/settings.json", p)
	}
}

func TestAgent_SetSessionEnv(t *testing.T) {
	a := &Agent{}
	a.SetSessionEnv([]string{"FOO=bar", "BAZ=qux"})
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.sessionEnv) != 2 {
		t.Errorf("sessionEnv len = %d, want 2", len(a.sessionEnv))
	}
}

func TestAgent_ListSessions(t *testing.T) {
	// Use a temp dir so we don't pick up real Pi sessions from the machine.
	a := &Agent{workDir: t.TempDir()}
	sessions, err := a.ListSessions(context.Background())
	if err != nil {
		t.Errorf("ListSessions() error = %v", err)
	}
	if sessions != nil {
		t.Errorf("ListSessions() = %v, want nil", sessions)
	}
}

func TestAgent_Stop(t *testing.T) {
	a := &Agent{}
	if err := a.Stop(); err != nil {
		t.Errorf("Stop() error = %v", err)
	}
}

func TestAgent_PermissionModes(t *testing.T) {
	a := &Agent{}
	modes := a.PermissionModes()
	if len(modes) != 2 {
		t.Fatalf("PermissionModes() len = %d, want 2", len(modes))
	}
	if modes[0].Key != "default" {
		t.Errorf("modes[0].Key = %q", modes[0].Key)
	}
	if modes[1].Key != "yolo" {
		t.Errorf("modes[1].Key = %q", modes[1].Key)
	}
}

func TestAgent_MemoryFiles(t *testing.T) {
	a := &Agent{workDir: "/tmp/test-project"}
	proj := a.ProjectMemoryFile()
	if !strings.HasSuffix(proj, "AGENTS.md") {
		t.Errorf("ProjectMemoryFile() = %q, want suffix AGENTS.md", proj)
	}
	if !strings.Contains(proj, "test-project") {
		t.Errorf("ProjectMemoryFile() = %q, want to contain work_dir", proj)
	}

	global := a.GlobalMemoryFile()
	if !strings.HasSuffix(global, filepath.Join(".pi", "agent", "AGENTS.md")) {
		t.Errorf("GlobalMemoryFile() = %q", global)
	}
}

func TestAgent_StartSession(t *testing.T) {
	a := &Agent{cmd: "echo", workDir: "/tmp", model: "test-model", mode: "yolo"}
	a.SetSessionEnv([]string{"TEST_VAR=1"})

	sess, err := a.StartSession(context.Background(), "resume-123")
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	defer sess.Close()

	ps := sess.(*piSession)
	if ps.cmd != "echo" {
		t.Errorf("cmd = %q", ps.cmd)
	}
	if ps.model != "test-model" {
		t.Errorf("model = %q", ps.model)
	}
	if ps.mode != "yolo" {
		t.Errorf("mode = %q", ps.mode)
	}
	if ps.CurrentSessionID() != "resume-123" {
		t.Errorf("sessionID = %q, want resume-123", ps.CurrentSessionID())
	}
	if !ps.Alive() {
		t.Error("session should be alive")
	}
}

// ── extractToolInput ─────────────────────────────────────────

func TestExtractToolInput(t *testing.T) {
	tests := []struct {
		name string
		item map[string]any
		want string
	}{
		{"nil arguments", map[string]any{}, ""},
		{"description", map[string]any{"arguments": map[string]any{"description": "List files"}}, "List files"},
		{"command", map[string]any{"arguments": map[string]any{"command": "ls -la"}}, "ls -la"},
		{"file_path", map[string]any{"arguments": map[string]any{"file_path": "/tmp/foo.go"}}, "/tmp/foo.go"},
		{"pattern", map[string]any{"arguments": map[string]any{"pattern": "*.go"}}, "*.go"},
		{"query", map[string]any{"arguments": map[string]any{"query": "find errors"}}, "find errors"},
		{"description takes priority", map[string]any{"arguments": map[string]any{
			"description": "desc", "command": "cmd",
		}}, "desc"},
		{"fallback to json", map[string]any{"arguments": map[string]any{"foo": "bar"}}, `{"foo":"bar"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractToolInput(tt.item)
			if got != tt.want {
				t.Errorf("extractToolInput() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestExtractToolInput_LongFallbackTruncated(t *testing.T) {
	longVal := strings.Repeat("x", 300)
	item := map[string]any{"arguments": map[string]any{"data": longVal}}
	got := extractToolInput(item)
	if len(got) > 210 { // 200 + "..."
		t.Errorf("expected truncated output, got len=%d", len(got))
	}
	if !strings.HasSuffix(got, "...") {
		t.Error("expected '...' suffix")
	}
}

// ── truncStr ─────────────────────────────────────────────────

func TestTruncStr(t *testing.T) {
	tests := []struct {
		in       string
		max      int
		want     string
		wantSame bool
	}{
		{"hello", 10, "hello", true},
		{"hello", 5, "hello", true},
		{"hello", 3, "hel...", false},
		{"", 5, "", true},
		{"日本語テスト", 3, "日本語...", false},
		{"日本語テスト", 10, "日本語テスト", true},
	}
	for _, tt := range tests {
		got := truncStr(tt.in, tt.max)
		if got != tt.want {
			t.Errorf("truncStr(%q, %d) = %q, want %q", tt.in, tt.max, got, tt.want)
		}
	}
}

// ── saveImagesToDisk ─────────────────────────────────────────

func TestSaveImagesToDisk(t *testing.T) {
	attachDir := filepath.Join(t.TempDir(), ".cc-connect", "attachments", "pi-test")
	images := []core.ImageAttachment{
		{MimeType: "image/png", Data: []byte("png-data"), FileName: "test.png"},
		{MimeType: "image/jpeg", Data: []byte("jpg-data")},
		{MimeType: "image/gif", Data: []byte("gif-data")},
		{MimeType: "image/webp", Data: []byte("webp-data")},
		{MimeType: "image/bmp", Data: []byte("bmp-data")}, // unknown mime → .png default
	}

	paths := saveImagesToDisk(attachDir, images)
	if len(paths) != 5 {
		t.Fatalf("got %d paths, want 5", len(paths))
	}

	// First file should use the provided filename.
	if filepath.Base(paths[0]) != "test.png" {
		t.Errorf("paths[0] base = %q, want test.png", filepath.Base(paths[0]))
	}

	// Check extensions of auto-named files.
	if !strings.HasSuffix(paths[1], ".jpg") {
		t.Errorf("jpeg path = %q, want .jpg suffix", paths[1])
	}
	if !strings.HasSuffix(paths[2], ".gif") {
		t.Errorf("gif path = %q, want .gif suffix", paths[2])
	}
	if !strings.HasSuffix(paths[3], ".webp") {
		t.Errorf("webp path = %q, want .webp suffix", paths[3])
	}
	if !strings.HasSuffix(paths[4], ".png") {
		t.Errorf("unknown mime path = %q, want .png suffix", paths[4])
	}

	// Verify file contents.
	data, err := os.ReadFile(paths[0])
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != "png-data" {
		t.Errorf("file content = %q", data)
	}
}

func TestSaveImagesToDisk_Empty(t *testing.T) {
	paths := saveImagesToDisk(t.TempDir(), nil)
	if len(paths) != 0 {
		t.Errorf("expected empty, got %d", len(paths))
	}
}

// TestSaveImagesToDisk_RejectsPathTraversal is a regression test for a path
// traversal vulnerability in saveImagesToDisk: the user-supplied
// ImageAttachment.FileName (sourced from IM upload metadata) was passed
// directly to filepath.Join, so a malicious uploader could escape the
// attachments directory by using `../` segments. This mirrors the same
// issue and fix in core.SaveFilesToDisk.
func TestSaveImagesToDisk_RejectsPathTraversal(t *testing.T) {
	workDir := t.TempDir()
	attachDir := filepath.Join(workDir, ".cc-connect", "attachments")

	images := []core.ImageAttachment{
		// Two levels up — escapes attachments/ and .cc-connect/.
		{MimeType: "image/png", Data: []byte("payload"), FileName: "../../escape.png"},
		// Three levels up — would land outside workDir entirely.
		{MimeType: "image/png", Data: []byte("payload"), FileName: "../../../way-up.png"},
		// Windows-style separators must also be stripped on Linux.
		{MimeType: "image/png", Data: []byte("payload"), FileName: `..\..\winescape.png`},
		// Plain name still works.
		{MimeType: "image/png", Data: []byte("payload"), FileName: "ok.png"},
		// "." sanitizes to empty so the generated-name fallback kicks in,
		// not a write to the attachments directory itself.
		{MimeType: "image/png", Data: []byte("payload"), FileName: "."},
	}

	paths := saveImagesToDisk(attachDir, images)

	// Every returned path must live inside attachDir.
	for _, p := range paths {
		if !strings.HasPrefix(p, attachDir+string(filepath.Separator)) {
			t.Errorf("saveImagesToDisk wrote outside attachments dir: %q (attachDir=%q)", p, attachDir)
		}
	}

	// Walk workDir and assert no file exists outside attachDir.
	if err := filepath.Walk(workDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if !strings.HasPrefix(path, attachDir+string(filepath.Separator)) {
			t.Errorf("found stray attachment outside attachments dir: %q", path)
		}
		return nil
	}); err != nil {
		t.Fatalf("walk: %v", err)
	}

	// Sanity: legitimate "ok.png" must still have been saved.
	if _, err := os.Stat(filepath.Join(attachDir, "ok.png")); err != nil {
		t.Errorf("legitimate ok.png not saved: %v", err)
	}
}

func TestSanitizePiAttachmentName(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"image.png", "image.png"},
		{"subdir/file.png", "file.png"},
		{"../../escape.png", "escape.png"},
		{`..\..\winescape.png`, "winescape.png"},
		{"/etc/passwd", "passwd"},
		{"..", ""},
		{".", ""},
		{"", ""},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got := sanitizePiAttachmentName(tt.in)
			if got != tt.want {
				t.Errorf("sanitizePiAttachmentName(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// ── cleanAttachments ─────────────────────────────────────────

func TestCleanAttachments(t *testing.T) {
	tmpDir := t.TempDir()
	attachDir := filepath.Join(tmpDir, ".cc-connect", "attachments")
	os.MkdirAll(attachDir, 0o755)

	// Create some files.
	os.WriteFile(filepath.Join(attachDir, "old1.png"), []byte("data"), 0o644)
	os.WriteFile(filepath.Join(attachDir, "old2.jpg"), []byte("data"), 0o644)

	// Verify files exist.
	entries, _ := os.ReadDir(attachDir)
	if len(entries) != 2 {
		t.Fatalf("expected 2 files, got %d", len(entries))
	}

	cleanAttachments(attachDir)

	// Directory should be removed.
	if _, err := os.Stat(attachDir); !os.IsNotExist(err) {
		t.Errorf("expected attachments dir removed after clean, got err=%v", err)
	}
}

func TestCleanAttachments_NonexistentDir(t *testing.T) {
	// Should not panic or error on non-existent directory.
	cleanAttachments("/nonexistent/path/xyz")
}

func TestPiSessionAttachmentDirsAreIsolated(t *testing.T) {
	workDir := t.TempDir()
	s1, err := newPiSession(context.Background(), "pi", nil, workDir, "", "", "", false, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	s2, err := newPiSession(context.Background(), "pi", nil, workDir, "", "", "", false, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if s1.attachDir == s2.attachDir {
		t.Fatalf("expected distinct attachment dirs, got %q", s1.attachDir)
	}

	if err := os.MkdirAll(s1.attachDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(s2.attachDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s1.attachDir, "one.txt"), []byte("one"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s2.attachDir, "two.txt"), []byte("two"), 0o644); err != nil {
		t.Fatal(err)
	}

	cleanAttachments(s1.attachDir)

	if _, err := os.Stat(filepath.Join(s2.attachDir, "two.txt")); err != nil {
		t.Fatalf("cleaning one session removed another session's attachment: %v", err)
	}
}

// ── handleEvent ──────────────────────────────────────────────

func newTestSession(opts ...bool) *piSession {
	ctx, cancel := context.WithCancel(context.Background())
	rpc := len(opts) > 0 && opts[0]
	rpcReady := make(chan struct{})
	close(rpcReady) // pre-closed: no real RPC process to wait for
	s := &piSession{
		events:        make(chan core.Event, 64),
		ctx:           ctx,
		cancel:        cancel,
		rpc:           rpc,
		rpcReady:      rpcReady,
		extPending:    make(map[string]string),
		extPendingRev: make(map[string]string),
		extMethod:     make(map[string]string),
	}
	s.alive.Store(true)
	return s
}

func drainEvents(s *piSession) []core.Event {
	var evts []core.Event
	for {
		select {
		case e := <-s.events:
			evts = append(evts, e)
		default:
			return evts
		}
	}
}

func TestHandleEvent_Session(t *testing.T) {
	s := newTestSession()
	defer s.cancel()

	s.handleEvent(map[string]any{"type": "session", "id": "sess-abc-123"})
	if s.CurrentSessionID() != "sess-abc-123" {
		t.Errorf("sessionID = %q", s.CurrentSessionID())
	}
}

func TestHandleEvent_SessionEmptyID(t *testing.T) {
	s := newTestSession()
	defer s.cancel()

	s.handleEvent(map[string]any{"type": "session", "id": ""})
	if s.CurrentSessionID() != "" {
		t.Errorf("sessionID = %q, want empty", s.CurrentSessionID())
	}
}

func TestHandleEvent_SessionNoID(t *testing.T) {
	s := newTestSession()
	defer s.cancel()

	s.handleEvent(map[string]any{"type": "session"})
	if s.CurrentSessionID() != "" {
		t.Errorf("sessionID = %q, want empty", s.CurrentSessionID())
	}
}

func TestHandleEvent_LifecycleEventsNoOp(t *testing.T) {
	s := newTestSession()
	defer s.cancel()

	// agent_start, turn_start, turn_end, message_start are no-ops
	// agent_end now also emits EventResult (RPC mode turn completion marker)
	for _, evType := range []string{"agent_start", "turn_start", "turn_end", "message_start"} {
		s.handleEvent(map[string]any{"type": evType})
	}
	evts := drainEvents(s)
	if len(evts) != 0 {
		t.Errorf("expected no events, got %d", len(evts))
	}
}

func TestHandleEvent_AgentEndEmitsResult(t *testing.T) {
	s := newTestSession(true) // rpc=true: agent_end emits EventResult
	defer s.cancel()

	s.handleEvent(map[string]any{"type": "agent_end", "messages": []any{}})
	evts := drainEvents(s)
	if len(evts) != 1 {
		t.Fatalf("expected 1 EventResult from agent_end, got %d", len(evts))
	}
	if evts[0].Type != core.EventResult {
		t.Errorf("expected EventResult, got %s", evts[0].Type)
	}
}

func TestHandleEvent_UnhandledType(t *testing.T) {
	s := newTestSession()
	defer s.cancel()

	s.handleEvent(map[string]any{"type": "unknown_event"})
	evts := drainEvents(s)
	if len(evts) != 0 {
		t.Errorf("expected no events, got %d", len(evts))
	}
}

// ── handleEvent: compaction_* (regression for /cmp silent-hang bug) ──
//
// Prior to the fix, ctx.compact() was fire-and-forget: pi emits
// compaction_start/compaction_end events on stdout but never sends agent_end.
// Without an explicit handler, cc-connect's processInteractiveEvents hangs
// forever waiting for a turn-end signal that never arrives. The fix in
// handleEvent adds a compaction_end case that synthesizes EventResult so
// the engine can finalize the turn. On errors it also surfaces an
// EventError so the user sees the failure even when no extension does.

func TestHandleEvent_CompactionStartNoEvent(t *testing.T) {
	s := newTestSession(true) // rpc=true
	defer s.cancel()

	s.handleEvent(map[string]any{
		"type":   "compaction_start",
		"reason": "manual",
	})
	// compaction_start is observable-only; no event is emitted.
	if got := drainEvents(s); len(got) != 0 {
		t.Fatalf("compaction_start must not emit any event, got %d: %#v", len(got), got)
	}
}

func TestHandleEvent_CompactionEndEmitsResultInRPCMode(t *testing.T) {
	// Regression: this is the exact scenario that caused /cmp to hang.
	// Before the fix, this case fell through to "unrecognized event type"
	// and processInteractiveEvents never saw EventResult.
	s := newTestSession(true) // rpc=true
	defer s.cancel()

	s.handleEvent(map[string]any{
		"type":    "compaction_end",
		"aborted": false,
	})
	evts := drainEvents(s)
	if len(evts) != 1 {
		t.Fatalf("compaction_end in RPC mode must emit exactly 1 EventResult, got %d: %#v", len(evts), evts)
	}
	if evts[0].Type != core.EventResult {
		t.Errorf("expected EventResult, got %s", evts[0].Type)
	}
	if !evts[0].Done {
		t.Errorf("expected Done=true, got false")
	}
}

func TestHandleEvent_CompactionEndJSONModeNoEvent(t *testing.T) {
	// JSON mode relies on process exit as the turn-end marker; the
	// compaction_end handler is intentionally a no-op there.
	s := newTestSession(false) // rpc=false
	defer s.cancel()

	s.handleEvent(map[string]any{
		"type":    "compaction_end",
		"aborted": false,
	})
	if got := drainEvents(s); len(got) != 0 {
		t.Fatalf("compaction_end in json mode must not emit EventResult, got %d: %#v", len(got), got)
	}
}

func TestHandleEvent_CompactionEndWithErrorMessageEmitsError(t *testing.T) {
	// Covers trigger-compact/auto-trigger paths that have no extension
	// to surface pi's own compaction error to the user.
	s := newTestSession(true)
	defer s.cancel()

	s.handleEvent(map[string]any{
		"type":         "compaction_end",
		"aborted":      false,
		"errorMessage": "Compaction failed: Nothing to compact (session too small)",
	})
	evts := drainEvents(s)
	if len(evts) != 2 {
		t.Fatalf("expected 2 events (EventError + EventResult), got %d: %#v", len(evts), evts)
	}
	if evts[0].Type != core.EventError {
		t.Errorf("first event must be EventError, got %s", evts[0].Type)
	}
	if evts[0].Error == nil || evts[0].Error.Error() != "Compaction failed: Nothing to compact (session too small)" {
		t.Errorf("EventError message not preserved verbatim: %v", evts[0].Error)
	}
	if evts[1].Type != core.EventResult || !evts[1].Done {
		t.Errorf("second event must be EventResult{Done:true}, got %s done=%v", evts[1].Type, evts[1].Done)
	}
}

func TestHandleEvent_CompactionEndEmptyErrorMessageDoesNotEmitError(t *testing.T) {
	// pi sometimes sends {"errorMessage": ""} on success-shaped events;
	// treat empty as "no error" so we don't spam EventError on every turn.
	s := newTestSession(true)
	defer s.cancel()

	s.handleEvent(map[string]any{
		"type":         "compaction_end",
		"aborted":      false,
		"errorMessage": "",
	})
	evts := drainEvents(s)
	if len(evts) != 1 {
		t.Fatalf("empty errorMessage must not emit EventError; got %d events: %#v", len(evts), evts)
	}
	if evts[0].Type != core.EventResult {
		t.Errorf("expected EventResult, got %s", evts[0].Type)
	}
}

// ── handleMessageUpdate: text_delta ──────────────────────────

func TestHandleMessageUpdate_TextDelta(t *testing.T) {
	s := newTestSession()
	defer s.cancel()

	s.handleEvent(map[string]any{
		"type": "message_update",
		"assistantMessageEvent": map[string]any{
			"type":  "text_delta",
			"delta": "Hello world",
		},
	})

	evts := drainEvents(s)
	if len(evts) != 1 {
		t.Fatalf("got %d events, want 1", len(evts))
	}
	if evts[0].Type != core.EventText || evts[0].Content != "Hello world" {
		t.Errorf("event = %+v", evts[0])
	}
}

func TestHandleMessageUpdate_TextDeltaEmpty(t *testing.T) {
	s := newTestSession()
	defer s.cancel()

	s.handleEvent(map[string]any{
		"type": "message_update",
		"assistantMessageEvent": map[string]any{
			"type":  "text_delta",
			"delta": "",
		},
	})

	evts := drainEvents(s)
	if len(evts) != 0 {
		t.Errorf("expected no events for empty delta, got %d", len(evts))
	}
}

// ── handleMessageUpdate: thinking accumulation ───────────────

func TestHandleMessageUpdate_ThinkingAccumulation(t *testing.T) {
	s := newTestSession()
	defer s.cancel()

	// Multiple thinking deltas should be accumulated.
	s.handleEvent(map[string]any{
		"type":                  "message_update",
		"assistantMessageEvent": map[string]any{"type": "thinking_delta", "delta": "Let me "},
	})
	s.handleEvent(map[string]any{
		"type":                  "message_update",
		"assistantMessageEvent": map[string]any{"type": "thinking_delta", "delta": "think about "},
	})
	s.handleEvent(map[string]any{
		"type":                  "message_update",
		"assistantMessageEvent": map[string]any{"type": "thinking_delta", "delta": "this."},
	})

	// No events should be emitted yet.
	evts := drainEvents(s)
	if len(evts) != 0 {
		t.Fatalf("expected no events before thinking_end, got %d", len(evts))
	}

	// thinking_end triggers the accumulated event.
	s.handleEvent(map[string]any{
		"type":                  "message_update",
		"assistantMessageEvent": map[string]any{"type": "thinking_end"},
	})

	evts = drainEvents(s)
	if len(evts) != 1 {
		t.Fatalf("got %d events, want 1", len(evts))
	}
	if evts[0].Type != core.EventThinking {
		t.Errorf("type = %s, want EventThinking", evts[0].Type)
	}
	if evts[0].Content != "Let me think about this." {
		t.Errorf("content = %q", evts[0].Content)
	}
}

func TestHandleMessageUpdate_ThinkingEndEmpty(t *testing.T) {
	s := newTestSession()
	defer s.cancel()

	// thinking_end with no prior deltas should not emit.
	s.handleEvent(map[string]any{
		"type":                  "message_update",
		"assistantMessageEvent": map[string]any{"type": "thinking_end"},
	})

	evts := drainEvents(s)
	if len(evts) != 0 {
		t.Errorf("expected no events for empty thinking, got %d", len(evts))
	}
}

func TestHandleMessageUpdate_ThinkingDeltaEmpty(t *testing.T) {
	s := newTestSession()
	defer s.cancel()

	// Empty deltas should not grow the buffer.
	s.handleEvent(map[string]any{
		"type":                  "message_update",
		"assistantMessageEvent": map[string]any{"type": "thinking_delta", "delta": ""},
	})

	if s.thinkingBuf.Len() != 0 {
		t.Errorf("thinkingBuf.Len() = %d, want 0", s.thinkingBuf.Len())
	}
}

// ── handleMessageUpdate: toolcall_end ────────────────────────

func TestHandleMessageUpdate_ToolcallEnd(t *testing.T) {
	s := newTestSession()
	defer s.cancel()

	s.handleEvent(map[string]any{
		"type": "message_update",
		"assistantMessageEvent": map[string]any{
			"type":         "toolcall_end",
			"contentIndex": float64(1),
			"message": map[string]any{
				"content": []any{
					map[string]any{"type": "thinking", "thinking": "..."},
					map[string]any{
						"type":      "toolCall",
						"name":      "bash",
						"arguments": map[string]any{"command": "ls -la"},
					},
				},
			},
		},
	})

	evts := drainEvents(s)
	if len(evts) != 1 {
		t.Fatalf("got %d events, want 1", len(evts))
	}
	if evts[0].Type != core.EventToolUse {
		t.Errorf("type = %s", evts[0].Type)
	}
	if evts[0].ToolName != "bash" {
		t.Errorf("toolName = %q", evts[0].ToolName)
	}
	if evts[0].ToolInput != "ls -la" {
		t.Errorf("toolInput = %q", evts[0].ToolInput)
	}
}

func TestHandleMessageUpdate_ToolcallEnd_UsesPartialFallback(t *testing.T) {
	s := newTestSession()
	defer s.cancel()

	s.handleEvent(map[string]any{
		"type": "message_update",
		"assistantMessageEvent": map[string]any{
			"type":         "toolcall_end",
			"contentIndex": float64(0),
			"partial": map[string]any{
				"content": []any{
					map[string]any{
						"type":      "toolCall",
						"name":      "read",
						"arguments": map[string]any{"file_path": "/tmp/foo.txt"},
					},
				},
			},
		},
	})

	evts := drainEvents(s)
	if len(evts) != 1 {
		t.Fatalf("got %d events, want 1", len(evts))
	}
	if evts[0].ToolName != "read" || evts[0].ToolInput != "/tmp/foo.txt" {
		t.Errorf("event = %+v", evts[0])
	}
}

func TestHandleMessageUpdate_ToolcallEnd_NonToolCallItem(t *testing.T) {
	s := newTestSession()
	defer s.cancel()

	s.handleEvent(map[string]any{
		"type": "message_update",
		"assistantMessageEvent": map[string]any{
			"type":         "toolcall_end",
			"contentIndex": float64(0),
			"message": map[string]any{
				"content": []any{
					map[string]any{"type": "text", "text": "hello"},
				},
			},
		},
	})

	evts := drainEvents(s)
	if len(evts) != 0 {
		t.Errorf("expected no events for non-toolCall item, got %d", len(evts))
	}
}

func TestHandleMessageUpdate_ToolcallEnd_OutOfBoundsIndex(t *testing.T) {
	s := newTestSession()
	defer s.cancel()

	s.handleEvent(map[string]any{
		"type": "message_update",
		"assistantMessageEvent": map[string]any{
			"type":         "toolcall_end",
			"contentIndex": float64(5),
			"message": map[string]any{
				"content": []any{
					map[string]any{"type": "toolCall", "name": "bash"},
				},
			},
		},
	})

	evts := drainEvents(s)
	if len(evts) != 0 {
		t.Errorf("expected no events for out-of-bounds index, got %d", len(evts))
	}
}

func TestHandleMessageUpdate_ToolcallEnd_NilMessage(t *testing.T) {
	s := newTestSession()
	defer s.cancel()

	s.handleEvent(map[string]any{
		"type": "message_update",
		"assistantMessageEvent": map[string]any{
			"type": "toolcall_end",
			// no "message" or "partial"
		},
	})

	evts := drainEvents(s)
	if len(evts) != 0 {
		t.Errorf("expected no events, got %d", len(evts))
	}
}

func TestHandleMessageUpdate_NilAssistantEvent(t *testing.T) {
	s := newTestSession()
	defer s.cancel()

	s.handleEvent(map[string]any{"type": "message_update"})

	evts := drainEvents(s)
	if len(evts) != 0 {
		t.Errorf("expected no events, got %d", len(evts))
	}
}

func TestHandleMessageUpdate_UnknownSubType(t *testing.T) {
	s := newTestSession()
	defer s.cancel()

	s.handleEvent(map[string]any{
		"type": "message_update",
		"assistantMessageEvent": map[string]any{
			"type": "some_unknown_delta",
		},
	})

	evts := drainEvents(s)
	if len(evts) != 0 {
		t.Errorf("expected no events for unknown sub-type, got %d", len(evts))
	}
}

// ── handleMessageEnd ─────────────────────────────────────────

func TestHandleMessageEnd_ToolResult(t *testing.T) {
	s := newTestSession()
	defer s.cancel()

	s.handleEvent(map[string]any{
		"type": "message_end",
		"message": map[string]any{
			"role":     "toolResult",
			"toolName": "bash",
			"content": []any{
				map[string]any{"type": "text", "text": "file1.go\nfile2.go"},
			},
		},
	})

	evts := drainEvents(s)
	if len(evts) != 1 {
		t.Fatalf("got %d events, want 1", len(evts))
	}
	if evts[0].Type != core.EventToolResult {
		t.Errorf("type = %s", evts[0].Type)
	}
	if evts[0].ToolName != "bash" {
		t.Errorf("toolName = %q", evts[0].ToolName)
	}
	if evts[0].Content != "file1.go\nfile2.go" {
		t.Errorf("content = %q", evts[0].Content)
	}
}

func TestHandleMessageEnd_ToolResultLongOutput(t *testing.T) {
	s := newTestSession()
	defer s.cancel()

	longOutput := strings.Repeat("x", 600)
	s.handleEvent(map[string]any{
		"type": "message_end",
		"message": map[string]any{
			"role":     "toolResult",
			"toolName": "bash",
			"content": []any{
				map[string]any{"type": "text", "text": longOutput},
			},
		},
	})

	evts := drainEvents(s)
	if len(evts) != 1 {
		t.Fatalf("got %d events, want 1", len(evts))
	}
	if len(evts[0].Content) > 510 {
		t.Errorf("content should be truncated, got len=%d", len(evts[0].Content))
	}
}

func TestHandleMessageEnd_ToolResultEmptyContent(t *testing.T) {
	s := newTestSession()
	defer s.cancel()

	s.handleEvent(map[string]any{
		"type": "message_end",
		"message": map[string]any{
			"role":     "toolResult",
			"toolName": "bash",
			"content":  []any{},
		},
	})

	evts := drainEvents(s)
	if len(evts) != 1 {
		t.Fatalf("got %d events, want 1", len(evts))
	}
	if evts[0].Content != "" {
		t.Errorf("content = %q, want empty", evts[0].Content)
	}
}

func TestHandleMessageEnd_AssistantError(t *testing.T) {
	s := newTestSession()
	defer s.cancel()

	s.handleEvent(map[string]any{
		"type": "message_end",
		"message": map[string]any{
			"role":         "assistant",
			"errorMessage": "400 model not supported",
		},
	})

	evts := drainEvents(s)
	if len(evts) != 1 {
		t.Fatalf("got %d events, want 1", len(evts))
	}
	if evts[0].Type != core.EventError {
		t.Errorf("type = %s", evts[0].Type)
	}
	if evts[0].Error == nil || !strings.Contains(evts[0].Error.Error(), "400") {
		t.Errorf("error = %v", evts[0].Error)
	}
}

func TestHandleMessageEnd_AssistantNoError(t *testing.T) {
	s := newTestSession()
	defer s.cancel()

	s.handleEvent(map[string]any{
		"type": "message_end",
		"message": map[string]any{
			"role": "assistant",
		},
	})

	evts := drainEvents(s)
	if len(evts) != 0 {
		t.Errorf("expected no events for assistant without error, got %d", len(evts))
	}
}

func TestHandleMessageEnd_NilMessage(t *testing.T) {
	s := newTestSession()
	defer s.cancel()

	s.handleEvent(map[string]any{"type": "message_end"})

	evts := drainEvents(s)
	if len(evts) != 0 {
		t.Errorf("expected no events, got %d", len(evts))
	}
}

func TestHandleMessageEnd_UserRole(t *testing.T) {
	s := newTestSession()
	defer s.cancel()

	s.handleEvent(map[string]any{
		"type": "message_end",
		"message": map[string]any{
			"role": "user",
		},
	})

	evts := drainEvents(s)
	if len(evts) != 0 {
		t.Errorf("expected no events for user role, got %d", len(evts))
	}
}

// newFakeRPCSession creates a piSession backed by a shell script that mimics
// the Pi RPC protocol: it writes a session event on startup and stays alive
// reading stdin until killed.
func newFakeRPCSession(t *testing.T, sessionID, cmd, workDir string) *piSession {
	t.Helper()
	var rpcCmd []string
	if cmd == "" {
		// Default: use a minimal RPC script
		script := fmt.Sprintf(
			`echo '{"type":"session","id":"%s"}' && while IFS= read -r _; do :; done`,
			sessionID,
		)
		rpcCmd = []string{"sh", "-c", script}
	} else {
		rpcCmd = strings.Fields(cmd)
	}

	s := &piSession{
		cmd:       rpcCmd[0],
		workDir:   workDir,
		events:    make(chan core.Event, 64),
		extraEnv:  nil,
		modelsCW:  nil,
		rpcReady:  make(chan struct{}),
		rpc:       true,
		extPending:    make(map[string]string),
		extPendingRev: make(map[string]string),
		extMethod:     make(map[string]string),
		attachDir: filepath.Join(workDir, ".cc-connect", "attachments", "pi-"+sessionID),
	}
	s.alive.Store(true)
	s.ctx, s.cancel = context.WithCancel(context.Background())

	// Spawn the fake RPC process
	execCmd := exec.CommandContext(s.ctx, rpcCmd[0], rpcCmd[1:]...)
	execCmd.Dir = s.workDir
	stdinPipe, err := execCmd.StdinPipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	s.rpcStdin = stdinPipe
	s.rpcCmd = execCmd

	stdout, err := execCmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	execCmd.Stderr = &s.stderrBuf

	if err := execCmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}

	s.wg.Add(1)
	go s.readLoopRPC(stdout)

	// Wait for ready
	select {
	case <-s.rpcReady:
	case <-time.After(5 * time.Second):
		t.Fatal("fake RPC did not become ready within 5s")
	}

	return s
}

// ── piSession lifecycle ──────────────────────────────────────

// newFakeRPCSessionRealistic creates a piSession backed by a fake pi script
// that mimics the *actual* Pi RPC protocol observed in the pi source tree:
// it pushes a non-session event (extension_ui_request) on stdout first, then
// reads stdin and replies to {"type":"get_state"} with the canonical
// {"type":"response", "command":"get_state", "data":{"sessionId":...}} shape.
//
// This is the protocol real pi uses today; the legacy "session" push event
// is NOT part of it, so tests built on the old newFakeRPCSession would not
// have caught the missing-session-id bug.
//
// newFakeRPCSessionRealistic exercises the full startRPC → writeRPCCommand →
// readLoopRPC → handleEvent → close(rpcReady) chain by going through
// newPiSession, so callers can observe what the engine sees.
func newFakeRPCSessionRealistic(t *testing.T, sessionID string) *piSession {
	t.Helper()
	// Write the script to a temp file so we don't have to fight shell
	// quoting inside Go string literals.
	scriptPath := filepath.Join(t.TempDir(), "fake-pi.sh")
	// Push a non-session event first, then loop reading stdin. When we see
	// the get_state probe we respond with a real get_state response.
	scriptBody := fmt.Sprintf(`#!/bin/sh
echo '{"type":"extension_ui_request","id":"ext-init","method":"setStatus","statusKey":"plan-mode"}'
while IFS= read -r line; do
    case "$line" in
        *get_state*)
            cat <<EOF
{"id":"cc-connect-state-probe","type":"response","command":"get_state","success":true,"data":{"sessionId":"%s","sessionFile":"/tmp/fake.jsonl"}}
EOF
            ;;
    esac
done
`, sessionID)
	if err := os.WriteFile(scriptPath, []byte(scriptBody), 0o755); err != nil {
		t.Fatalf("write fake script: %v", err)
	}

	s, err := newPiSession(context.Background(), scriptPath, nil, t.TempDir(), "", "", "", true, "", nil)
	if err != nil {
		t.Fatalf("newPiSession (realistic fake): %v", err)
	}
	return s
}

func TestPiSession_RPC_StartupProbeStoresSessionID(t *testing.T) {
	// Regression: real Pi does not push a "session" event on stdout — only
	// extension_ui_request events arrive first. Session id is only available
	// via the response to the get_state command that startRPC writes. Before
	// this fix CurrentSessionID() would stay empty even after rpcReady fired,
	// because rpcReady was closed on the first line (which was the
	// extension_ui_request), before the get_state response arrived.
	s := newFakeRPCSessionRealistic(t, "session-from-get-state")
	defer s.Close()

	if got := s.CurrentSessionID(); got != "session-from-get-state" {
		t.Errorf("CurrentSessionID() = %q, want %q (get_state probe did not populate session id)", got, "session-from-get-state")
	}
}

func TestPiSession_RPC_StartupProbe_HandlesFailureResponse(t *testing.T) {
	// Pi may respond to get_state with success=false (e.g. protocol error).
	// handleEvent must log a warning and leave sessionID empty instead of
	// panicking or storing junk. rpcReady therefore does not close, and the
	// constructor returns the standard 30s "did not become ready" error.
	scriptPath := filepath.Join(t.TempDir(), "fake-pi.sh")
	scriptBody := `#!/bin/sh
echo '{"type":"extension_ui_request","id":"ext-init","method":"setStatus","statusKey":"plan-mode"}'
while IFS= read -r line; do
    case "$line" in
        *get_state*)
            echo '{"id":"cc-connect-state-probe","type":"response","command":"get_state","success":false,"error":"session not available"}'
            ;;
    esac
done
`
	if err := os.WriteFile(scriptPath, []byte(scriptBody), 0o755); err != nil {
		t.Fatalf("write fake script: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	s, err := newPiSession(ctx, scriptPath, nil, t.TempDir(), "", "", "", true, "", nil)
	if err == nil {
		defer func() { _ = s.Close() }()
		t.Fatalf("newPiSession succeeded (id=%q); failure response should leave session id empty and time out", s.CurrentSessionID())
	}
	if !strings.Contains(err.Error(), "did not become ready") &&
		!strings.Contains(err.Error(), "context cancelled") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestPiSession_RPC_StartupProbe_DoesNotCloseOnExtensionUI(t *testing.T) {
	// Variant: the fake pi script emits extension_ui_request events forever
	// without ever responding to get_state. rpcReady must NOT close (the
	// session id never arrives), and the constructor must time out instead
	// of returning early with an empty id.
	scriptPath := filepath.Join(t.TempDir(), "fake-pi.sh")
	scriptBody := `#!/bin/sh
i=0
echo '{"type":"extension_ui_request","id":"e0","method":"setStatus","statusKey":"plan-mode"}'
while IFS= read -r line; do
    i=$((i+1))
    echo "{\"type\":\"extension_ui_request\",\"id\":\"e$i\",\"method\":\"setStatus\",\"statusKey\":\"plan-mode\"}"
done
`
	if err := os.WriteFile(scriptPath, []byte(scriptBody), 0o755); err != nil {
		t.Fatalf("write fake script: %v", err)
	}

	// Bound the wait: if the fix is wrong (rpcReady closed too early on the
	// extension_ui_request), newPiSession returns within milliseconds with
	// CurrentSessionID()=="". A correct fix keeps rpcReady open until the
	// get_state response arrives, which never does here, so we hit a
	// timeout — but well before the default 30s.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	start := time.Now()
	s, err := newPiSession(ctx, scriptPath, nil, t.TempDir(), "", "", "", true, "", nil)
	elapsed := time.Since(start)
	if err == nil {
		defer func() { _ = s.Close() }()
		t.Fatalf("newPiSession succeeded (id=%q) after %v; should have waited for get_state", s.CurrentSessionID(), elapsed)
	}
	// The error message is either the 30s timeout ("did not become ready")
	// or our 3s ctx cancellation ("context cancelled") — both are valid
	// proofs that rpcReady did not close prematurely on the first line.
	if !strings.Contains(err.Error(), "did not become ready") &&
		!strings.Contains(err.Error(), "context cancelled") {
		t.Errorf("unexpected error: %v", err)
	}
	if elapsed > 4*time.Second {
		t.Errorf("newPiSession took %v; rpcReady should not have closed on the extension_ui_request first line", elapsed)
	}
}

func TestPiSession_NewWithResumeID(t *testing.T) {
	s := newFakeRPCSession(t, "test-sess-id", "", t.TempDir())
	defer func() { _ = s.Close() }()

	if s.CurrentSessionID() != "test-sess-id" {
		t.Errorf("sessionID = %q, want %q", s.CurrentSessionID(), "test-sess-id")
	}
}

func TestPiSession_ContinueSessionTreatedAsFresh(t *testing.T) {
	// ContinueSession ("__continue__") is a sentinel used by the engine to tell
	// Claude Code to pick up the latest CLI session via --continue. Agents that
	// don't support --continue must treat it as "" (fresh session), otherwise
	// they pass the literal "__continue__" as a session ID which always fails.
	s, err := newPiSession(context.Background(), "echo", nil, "/tmp", "", "default", "", false, core.ContinueSession, nil)
	if err != nil {
		t.Fatalf("newPiSession: %v", err)
	}
	defer s.Close()
	if !s.Alive() {
		t.Error("expected session to be alive")
	}
}

func TestPiSession_NewWithoutResumeID(t *testing.T) {
	s := newFakeRPCSession(t, "fresh-sess", "", t.TempDir())
	defer s.Close()
	if s.CurrentSessionID() != "fresh-sess" {
		t.Errorf("sessionID = %q, want %q", s.CurrentSessionID(), "fresh-sess")
	}
}

func TestPiSession_SendWhenClosed(t *testing.T) {
	s, _ := newPiSession(context.Background(), "echo", nil, "/tmp", "", "default", "", false, "", nil)
	s.Close()

	err := s.Send("hello", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "closed") {
		t.Errorf("expected 'closed' error, got %v", err)
	}
}

func TestPiSession_RespondPermission(t *testing.T) {
	s := newTestSession()
	defer s.cancel()

	if err := s.RespondPermission("id", core.PermissionResult{}); err != nil {
		t.Errorf("RespondPermission() error = %v", err)
	}
}

// ── forwardSelect / forwardConfirm: Questions wiring ────────

func TestForwardSelect_PopulatesQuestions(t *testing.T) {
	s := newTestSession(true) // rpc mode
	defer s.cancel()

	s.forwardSelect("sel-1", map[string]any{
		"title":   "Pick a color",
		"options": []any{"Red", "Green", "Blue"},
	})

	evts := drainEvents(s)
	if len(evts) != 1 {
		t.Fatalf("expected 1 event, got %d", len(evts))
	}
	evt := evts[0]
	if evt.Type != core.EventPermissionRequest {
		t.Fatalf("expected EventPermissionRequest, got %s", evt.Type)
	}
	if evt.ToolName != "extension_select" {
		t.Errorf("ToolName = %q, want extension_select", evt.ToolName)
	}
	if len(evt.Questions) != 1 {
		t.Fatalf("expected 1 question, got %d", len(evt.Questions))
	}
	q := evt.Questions[0]
	if q.Question != "Pick a color" {
		t.Errorf("Question = %q, want %q", q.Question, "Pick a color")
	}
	if q.MultiSelect {
		t.Error("MultiSelect should be false for select")
	}
	if len(q.Options) != 3 {
		t.Fatalf("expected 3 options, got %d", len(q.Options))
	}
	wantLabels := []string{"Red", "Green", "Blue"}
	for i, opt := range q.Options {
		if opt.Label != wantLabels[i] {
			t.Errorf("option[%d].Label = %q, want %q", i, opt.Label, wantLabels[i])
		}
	}
}

func TestForwardSelect_EmptyTitleUsesDefault(t *testing.T) {
	s := newTestSession(true)
	defer s.cancel()

	s.forwardSelect("sel-2", map[string]any{
		"options": []any{"only"},
	})

	evts := drainEvents(s)
	if len(evts) != 1 {
		t.Fatalf("expected 1 event, got %d", len(evts))
	}
	if evts[0].Questions[0].Question != "Select an option" {
		t.Errorf("Question = %q, want %q", evts[0].Questions[0].Question, "Select an option")
	}
}

// TestForwardSelect_ObjectOptionsExtractsLabelAndDescription verifies that
// extension_select options sent as objects (with both label and description)
// are forwarded to the engine as UserQuestionOption{Label, Description},
// not silently dropped. Regression guard for the bug where the cc-connect
// TUI showed option descriptions but the Feishu card rendered label-only
// because forwardSelect only handled string-form options.
func TestForwardSelect_ObjectOptionsExtractsLabelAndDescription(t *testing.T) {
	s := newTestSession(true)
	defer s.cancel()

	s.forwardSelect("sel-3", map[string]any{
		"title": "Pick a database",
		"options": []any{
			map[string]any{
				"label":       "PostgreSQL",
				"description": "Recommended for production",
			},
			map[string]any{
				"label":       "SQLite",
				"description": "Lightweight file-based",
			},
			map[string]any{
				"label":       "MySQL",
				"description": "Popular open-source",
			},
		},
	})

	evts := drainEvents(s)
	if len(evts) != 1 {
		t.Fatalf("expected 1 event, got %d", len(evts))
	}
	evt := evts[0]
	if evt.ToolName != "extension_select" {
		t.Fatalf("ToolName = %q, want extension_select", evt.ToolName)
	}
	if len(evt.Questions) != 1 {
		t.Fatalf("expected 1 question, got %d", len(evt.Questions))
	}
	q := evt.Questions[0]
	if q.Question != "Pick a database" {
		t.Errorf("Question = %q, want %q", q.Question, "Pick a database")
	}

	want := []core.UserQuestionOption{
		{Label: "PostgreSQL", Description: "Recommended for production"},
		{Label: "SQLite", Description: "Lightweight file-based"},
		{Label: "MySQL", Description: "Popular open-source"},
	}
	if len(q.Options) != len(want) {
		t.Fatalf("expected %d options, got %d", len(want), len(q.Options))
	}
	for i, opt := range q.Options {
		if opt.Label != want[i].Label {
			t.Errorf("option[%d].Label = %q, want %q", i, opt.Label, want[i].Label)
		}
		if opt.Description != want[i].Description {
			t.Errorf("option[%d].Description = %q, want %q",
				i, opt.Description, want[i].Description)
		}
	}
}

// TestForwardConfirm_RoutesAsRegularPermission verifies that extension_confirm
// is forwarded as a regular permission request (no Questions field) so the
// engine renders an Allow/Deny card rather than a Yes/No question card. This
// matches the UX of other agents' permission prompts.
func TestForwardConfirm_RoutesAsRegularPermission(t *testing.T) {
	s := newTestSession(true)
	defer s.cancel()

	s.forwardConfirm("cfm-1", map[string]any{
		"title":   "Allow rm -rf?",
		"message": "This is destructive",
	})

	evts := drainEvents(s)
	if len(evts) != 1 {
		t.Fatalf("expected 1 event, got %d", len(evts))
	}
	evt := evts[0]
	if evt.Type != core.EventPermissionRequest {
		t.Fatalf("Type = %s, want EventPermissionRequest", evt.Type)
	}
	if evt.ToolName != "extension_confirm" {
		t.Errorf("ToolName = %q, want extension_confirm", evt.ToolName)
	}
	if len(evt.Questions) != 0 {
		t.Errorf("Questions = %d, want 0 (extension_confirm must NOT route through AskUserQuestion)", len(evt.Questions))
	}
	if evt.ToolInput != "Allow rm -rf?: This is destructive" {
		t.Errorf("ToolInput = %q, want %q", evt.ToolInput, "Allow rm -rf?: This is destructive")
	}
	raw := evt.ToolInputRaw
	if raw["title"] != "Allow rm -rf?" || raw["message"] != "This is destructive" || raw["method"] != "confirm" {
		t.Errorf("ToolInputRaw = %v, want title/message/method set", raw)
	}
}

func TestForwardConfirm_MessageOnly(t *testing.T) {
	s := newTestSession(true)
	defer s.cancel()

	s.forwardConfirm("cfm-2", map[string]any{
		"message": "Allow this?",
	})

	evts := drainEvents(s)
	if len(evts) != 1 {
		t.Fatalf("expected 1 event, got %d", len(evts))
	}
	evt := evts[0]
	if evt.ToolName != "extension_confirm" {
		t.Errorf("ToolName = %q, want extension_confirm", evt.ToolName)
	}
	if len(evt.Questions) != 0 {
		t.Errorf("Questions = %d, want 0", len(evt.Questions))
	}
	if evt.ToolInput != ": Allow this?" {
		t.Errorf("ToolInput = %q, want %q", evt.ToolInput, ": Allow this?")
	}
}

// ── lastAskQuestionAnswer helper ───────────────────────────

func TestLastAskQuestionAnswer(t *testing.T) {
	tests := []struct {
		name string
		in   map[string]any
		want string
	}{
		{"nil", nil, ""},
		{"no answers key", map[string]any{"foo": "bar"}, ""},
		{"empty answers", map[string]any{"answers": map[string]any{}}, ""},
		{"single answer", map[string]any{"answers": map[string]any{"Q?": "Yes"}}, "Yes"},
		{"multiple answers returns any value", map[string]any{"answers": map[string]any{"Q1?": "A", "Q2?": "B"}}, ""},
		{"non-string value ignored", map[string]any{"answers": map[string]any{"Q?": 42}}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := lastAskQuestionAnswer(tt.in)
			if tt.name == "multiple answers returns any value" {
				// Map iteration is non-deterministic; the function logs a
				// warning and returns the first string value found.
				if got != "A" && got != "B" {
					t.Errorf("got %q, want A or B", got)
				}
				return
			}
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPiSession_Events(t *testing.T) {
	s := newTestSession()
	defer s.cancel()

	ch := s.Events()
	if ch == nil {
		t.Fatal("Events() returned nil")
	}
}

func TestPiSession_Close(t *testing.T) {
	s, _ := newPiSession(context.Background(), "echo", nil, "/tmp", "", "default", "", false, "", nil)

	if err := s.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if s.Alive() {
		t.Error("session should not be alive after Close()")
	}
}

// ── Full event stream simulation ─────────────────────────────

func TestHandleEvent_FullConversation(t *testing.T) {
	s := newTestSession(true) // rpc=true: agent_end emits EventResult
	defer s.cancel()

	// Simulate a full pi conversation: session → thinking → text → tool → tool result → text → done
	events := []map[string]any{
		{"type": "session", "id": "conv-123"},
		{"type": "agent_start"},
		{"type": "turn_start"},
		{"type": "message_start"},
		{"type": "message_update", "assistantMessageEvent": map[string]any{
			"type": "thinking_delta", "delta": "I need to ",
		}},
		{"type": "message_update", "assistantMessageEvent": map[string]any{
			"type": "thinking_delta", "delta": "list files.",
		}},
		{"type": "message_update", "assistantMessageEvent": map[string]any{
			"type": "thinking_end",
		}},
		{"type": "message_update", "assistantMessageEvent": map[string]any{
			"type":         "toolcall_end",
			"contentIndex": float64(1),
			"message": map[string]any{
				"content": []any{
					map[string]any{"type": "thinking"},
					map[string]any{"type": "toolCall", "name": "bash", "arguments": map[string]any{"command": "ls"}},
				},
			},
		}},
		{"type": "message_end", "message": map[string]any{
			"role": "assistant", "stopReason": "toolUse",
		}},
		{"type": "message_end", "message": map[string]any{
			"role": "toolResult", "toolName": "bash",
			"content": []any{map[string]any{"type": "text", "text": "file1.go"}},
		}},
		{"type": "turn_end"},
		{"type": "turn_start"},
		{"type": "message_update", "assistantMessageEvent": map[string]any{
			"type": "text_delta", "delta": "Here are your files.",
		}},
		{"type": "message_end", "message": map[string]any{
			"role": "assistant", "stopReason": "stop",
		}},
		{"type": "turn_end"},
		{"type": "agent_end"},
	}

	for _, ev := range events {
		s.handleEvent(ev)
	}

	evts := drainEvents(s)

	// Expected: thinking(accumulated), tool_use, tool_result, text, result(agent_end)
	if len(evts) != 5 {
		var types []string
		for _, e := range evts {
			types = append(types, string(e.Type))
		}
		t.Fatalf("got %d events %v, want 5 (thinking, tool_use, tool_result, text, result)", len(evts), types)
	}

	if evts[0].Type != core.EventThinking || evts[0].Content != "I need to list files." {
		t.Errorf("evts[0] = %+v, want EventThinking(I need to list files.)", evts[0])
	}
	if evts[1].Type != core.EventToolUse || evts[1].ToolName != "bash" {
		t.Errorf("evts[1] = %+v, want EventToolUse(bash)", evts[1])
	}
	if evts[2].Type != core.EventToolResult || evts[2].Content != "file1.go" {
		t.Errorf("evts[2] = %+v, want EventToolResult(file1.go)", evts[2])
	}
	if evts[3].Type != core.EventText || evts[3].Content != "Here are your files." {
		t.Errorf("evts[3] = %+v, want EventText(Here are your files.)", evts[3])
	}
	if evts[4].Type != core.EventResult || !evts[4].Done {
		t.Errorf("evts[4] = %+v, want EventResult{Done:true}", evts[4])
	}

	if s.CurrentSessionID() != "conv-123" {
		t.Errorf("sessionID = %q", s.CurrentSessionID())
	}
}

// ── readLoop with real process ───────────────────────────────

func TestPiSession_ReadLoopWithEcho(t *testing.T) {
	// Create a process that emits Pi RPC JSONL: session, text delta, agent_end.
	sessionJSON, _ := json.Marshal(map[string]any{"type": "session", "id": "echo-sess"})
	textJSON, _ := json.Marshal(map[string]any{
		"type": "message_update",
		"assistantMessageEvent": map[string]any{"type": "text_delta", "delta": "hi"},
	})
	agentEndJSON, _ := json.Marshal(map[string]any{"type": "agent_end"})

	// Build one long string with all events separated by newlines
	allData := string(sessionJSON) + "\n" + string(textJSON) + "\n" + string(agentEndJSON) + "\n"

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	s := &piSession{
		cmd:       "echo",
		workDir:   t.TempDir(),
		rpc:       true,
		events:    make(chan core.Event, 64),
		rpcReady:  make(chan struct{}),
		extPending:    make(map[string]string),
		extPendingRev: make(map[string]string),
		extMethod:     make(map[string]string),
	}
	s.alive.Store(true)
	s.ctx, s.cancel = context.WithCancel(ctx)
	s.modelsCW = nil

	// Create a pipe and feed the data through it
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	go func() {
		_, _ = w.Write([]byte(allData))
		_ = w.Close()
	}()

	// Prevent close from trying to kill a process
	s.rpcCmd = nil

	s.wg.Add(1)
	go s.readLoopRPC(r)

	// Wait for session event
	select {
	case <-s.rpcReady:
	case <-ctx.Done():
		t.Fatal("rpcReady timeout")
	}

	// Collect events with timeout
	var evts []core.Event
loop:
	for {
		select {
		case ev, ok := <-s.Events():
			if !ok {
				break loop
			}
			evts = append(evts, ev)
			if ev.Type == core.EventResult {
				break loop
			}
		case <-ctx.Done():
			t.Fatal("timeout waiting for events")
		}
	}

	s.cancel()
	s.wg.Wait()

	// Should have at least a text event and a result event (from agent_end).
	hasText := false
	hasResult := false
	for _, ev := range evts {
		if ev.Type == core.EventText && ev.Content == "hi" {
			hasText = true
		}
		if ev.Type == core.EventResult {
			hasResult = true
		}
	}
	if !hasText {
		t.Error("missing text event")
	}
	if !hasResult {
		t.Error("missing result event (from agent_end)")
	}
	if s.CurrentSessionID() != "echo-sess" {
		t.Errorf("sessionID = %q, want echo-sess", s.CurrentSessionID())
	}
}

// ── loadModelsContextWindows ─────────────────────────────────

func TestLoadModelsContextWindows(t *testing.T) {
	savedEnv := os.Getenv("PI_CODING_AGENT_DIR")
	t.Cleanup(func() {
		if savedEnv != "" {
			_ = os.Setenv("PI_CODING_AGENT_DIR", savedEnv)
		} else {
			_ = os.Unsetenv("PI_CODING_AGENT_DIR")
		}
	})

	tmpDir := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", tmpDir)

	modelsJSON := map[string]any{
		"providers": map[string]any{
			"provider-a": map[string]any{
				"models": []any{
					map[string]any{"id": "model-alpha", "contextWindow": float64(128_000)},
					map[string]any{"id": "model-beta", "contextWindow": float64(200_000)},
				},
			},
			"provider-b": map[string]any{
				"models": []any{
					map[string]any{"id": "model-gamma", "contextWindow": float64(1_000_000)},
				},
			},
		},
	}
	data, _ := json.Marshal(modelsJSON)
	if err := os.WriteFile(filepath.Join(tmpDir, "models.json"), data, 0o644); err != nil {
		t.Fatalf("write models.json: %v", err)
	}

	m := loadModelsContextWindows()
	if m == nil {
		t.Fatal("loadModelsContextWindows returned nil")
	}

	// Bare IDs.
	if m["model-alpha"] != 128_000 {
		t.Errorf("model-alpha = %d, want 128_000", m["model-alpha"])
	}
	if m["model-beta"] != 200_000 {
		t.Errorf("model-beta = %d, want 200_000", m["model-beta"])
	}
	if m["model-gamma"] != 1_000_000 {
		t.Errorf("model-gamma = %d, want 1_000_000", m["model-gamma"])
	}

	// Fully-qualified provider/ID.
	if m["provider-a/model-alpha"] != 128_000 {
		t.Errorf("provider-a/model-alpha = %d, want 128_000", m["provider-a/model-alpha"])
	}
	if m["provider-b/model-gamma"] != 1_000_000 {
		t.Errorf("provider-b/model-gamma = %d, want 1_000_000", m["provider-b/model-gamma"])
	}
}

func TestLoadModelsContextWindows_FileNotFound(t *testing.T) {
	savedEnv := os.Getenv("PI_CODING_AGENT_DIR")
	t.Cleanup(func() {
		if savedEnv != "" {
			_ = os.Setenv("PI_CODING_AGENT_DIR", savedEnv)
		} else {
			_ = os.Unsetenv("PI_CODING_AGENT_DIR")
		}
	})

	tmpDir := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", tmpDir)
	// No models.json written.

	m := loadModelsContextWindows()
	if m != nil {
		t.Errorf("expected nil for missing models.json, got %v", m)
	}
}

func TestLoadModelsContextWindows_MalformedJSON(t *testing.T) {
	savedEnv := os.Getenv("PI_CODING_AGENT_DIR")
	t.Cleanup(func() {
		if savedEnv != "" {
			_ = os.Setenv("PI_CODING_AGENT_DIR", savedEnv)
		} else {
			_ = os.Unsetenv("PI_CODING_AGENT_DIR")
		}
	})

	tmpDir := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", tmpDir)
	if err := os.WriteFile(filepath.Join(tmpDir, "models.json"), []byte("{invalid"), 0o644); err != nil {
		t.Fatalf("write models.json: %v", err)
	}

	m := loadModelsContextWindows()
	if m != nil {
		t.Errorf("expected nil for malformed JSON, got %v", m)
	}
}

// ── handleAgentEnd ───────────────────────────────────────────

func TestHandleAgentEnd(t *testing.T) {
	s := newTestSession()
	defer s.cancel()
	s.modelsCW = map[string]int{
		"test-model":               200_000,
		"test-provider/test-model": 200_000,
	}

	s.handleEvent(map[string]any{
		"type": "agent_end",
		"messages": []any{
			map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{"type": "text", "text": "hello"},
				},
			},
			map[string]any{
				"role":  "assistant",
				"model": "test-model",
				"content": []any{
					map[string]any{"type": "text", "text": "hi there"},
				},
				"usage": map[string]any{
					"input":      float64(5000),
					"output":     float64(300),
					"cacheRead":  float64(40000),
					"cacheWrite": float64(2000),
				},
			},
		},
	})

	usage := s.GetContextUsage()
	if usage == nil {
		t.Fatal("GetContextUsage returned nil after agent_end with usage")
	}

	// UsedTokens = input + cacheWrite + cacheRead (mirrors claudecode pattern).
	wantUsed := 5000 + 2000 + 40000 // 47000
	if usage.UsedTokens != wantUsed {
		t.Errorf("UsedTokens = %d, want %d", usage.UsedTokens, wantUsed)
	}
	// TotalTokens = UsedTokens + output.
	wantTotal := wantUsed + 300 // 47300
	if usage.TotalTokens != wantTotal {
		t.Errorf("TotalTokens = %d, want %d", usage.TotalTokens, wantTotal)
	}
	if usage.InputTokens != 5000 {
		t.Errorf("InputTokens = %d, want 5000", usage.InputTokens)
	}
	if usage.OutputTokens != 300 {
		t.Errorf("OutputTokens = %d, want 300", usage.OutputTokens)
	}
	if usage.CachedInputTokens != 40000 {
		t.Errorf("CachedInputTokens = %d, want 40000", usage.CachedInputTokens)
	}
	if usage.CacheCreationInputTokens != 2000 {
		t.Errorf("CacheCreationInputTokens = %d, want 2000", usage.CacheCreationInputTokens)
	}
	if usage.ContextWindow != 200_000 {
		t.Errorf("ContextWindow = %d, want 200_000", usage.ContextWindow)
	}
}

func TestHandleAgentEnd_NoMessages(t *testing.T) {
	s := newTestSession()
	defer s.cancel()

	s.handleEvent(map[string]any{
		"type":     "agent_end",
		"messages": []any{},
	})

	if s.GetContextUsage() != nil {
		t.Error("GetContextUsage should be nil when agent_end has no messages")
	}
}

func TestHandleAgentEnd_NoAssistantMessage(t *testing.T) {
	s := newTestSession()
	defer s.cancel()

	s.handleEvent(map[string]any{
		"type": "agent_end",
		"messages": []any{
			map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{"type": "text", "text": "hello"},
				},
			},
		},
	})

	if s.GetContextUsage() != nil {
		t.Error("GetContextUsage should be nil when no assistant message exists")
	}
}

func TestHandleAgentEnd_NoUsage(t *testing.T) {
	s := newTestSession()
	defer s.cancel()

	s.handleEvent(map[string]any{
		"type": "agent_end",
		"messages": []any{
			map[string]any{
				"role":  "assistant",
				"model": "test-model",
				"content": []any{
					map[string]any{"type": "text", "text": "hi"},
				},
				// no "usage" key
			},
		},
	})

	if s.GetContextUsage() != nil {
		t.Error("GetContextUsage should be nil when assistant message has no usage")
	}
}

func TestHandleAgentEnd_FallbackContextWindow(t *testing.T) {
	s := newTestSession()
	defer s.cancel()
	// Empty modelsCW (no entry for "unknown-model").
	s.modelsCW = map[string]int{}

	s.handleEvent(map[string]any{
		"type": "agent_end",
		"messages": []any{
			map[string]any{
				"role":  "assistant",
				"model": "unknown-model",
				"usage": map[string]any{
					"input":      float64(1000),
					"output":     float64(100),
					"cacheRead":  float64(0),
					"cacheWrite": float64(0),
				},
			},
		},
	})

	usage := s.GetContextUsage()
	if usage == nil {
		t.Fatal("GetContextUsage returned nil")
	}
	if usage.ContextWindow != 200_000 {
		t.Errorf("ContextWindow = %d, want 200_000 (fallback)", usage.ContextWindow)
	}
}

func TestHandleAgentEnd_NilModelsCW(t *testing.T) {
	s := newTestSession()
	defer s.cancel()
	// modelsCW is nil (not loaded).
	s.modelsCW = nil

	s.handleEvent(map[string]any{
		"type": "agent_end",
		"messages": []any{
			map[string]any{
				"role":  "assistant",
				"model": "any-model",
				"usage": map[string]any{
					"input":      float64(500),
					"output":     float64(50),
					"cacheRead":  float64(0),
					"cacheWrite": float64(0),
				},
			},
		},
	})

	usage := s.GetContextUsage()
	if usage == nil {
		t.Fatal("GetContextUsage returned nil")
	}
	if usage.ContextWindow != 200_000 {
		t.Errorf("ContextWindow = %d, want 200_000 (nil-map fallback)", usage.ContextWindow)
	}
}

// ── GetContextUsage ──────────────────────────────────────────

func TestGetContextUsage_NilBeforeAgentEnd(t *testing.T) {
	s := newTestSession()
	defer s.cancel()

	if usage := s.GetContextUsage(); usage != nil {
		t.Errorf("GetContextUsage should be nil before any agent_end, got %+v", usage)
	}
}

func TestGetContextUsage_ReturnsCopy(t *testing.T) {
	s := newTestSession()
	defer s.cancel()
	s.modelsCW = map[string]int{"m": 100_000}

	s.handleEvent(map[string]any{
		"type": "agent_end",
		"messages": []any{
			map[string]any{
				"role":  "assistant",
				"model": "m",
				"usage": map[string]any{
					"input":      float64(100),
					"output":     float64(50),
					"cacheRead":  float64(0),
					"cacheWrite": float64(0),
				},
			},
		},
	})

	u1 := s.GetContextUsage()
	u2 := s.GetContextUsage()
	if u1 == u2 {
		t.Error("GetContextUsage should return different pointers (copy)")
	}
	if u1.UsedTokens != u2.UsedTokens {
		t.Error("copies should have same values")
	}
}

func TestHandleAgentEnd_WalksBackwardsForLastAssistant(t *testing.T) {
	s := newTestSession()
	defer s.cancel()
	s.modelsCW = map[string]int{"model-a": 100_000, "model-b": 200_000}

	// Two assistant messages — only the last one's usage should be captured.
	s.handleEvent(map[string]any{
		"type": "agent_end",
		"messages": []any{
			map[string]any{
				"role":  "assistant",
				"model": "model-a",
				"usage": map[string]any{
					"input":  float64(100),
					"output": float64(10),
				},
			},
			map[string]any{
				"role":  "assistant",
				"model": "model-b",
				"usage": map[string]any{
					"input":      float64(8000),
					"output":     float64(500),
					"cacheRead":  float64(3000),
					"cacheWrite": float64(1000),
				},
			},
		},
	})

	usage := s.GetContextUsage()
	if usage == nil {
		t.Fatal("GetContextUsage returned nil")
	}
	// Should use model-b (last assistant), not model-a.
	if usage.ContextWindow != 200_000 {
		t.Errorf("ContextWindow = %d, want 200_000 (from model-b)", usage.ContextWindow)
	}
	if usage.InputTokens != 8000 {
		t.Errorf("InputTokens = %d, want 8000 (from model-b)", usage.InputTokens)
	}
	if usage.OutputTokens != 500 {
		t.Errorf("OutputTokens = %d, want 500 (from model-b)", usage.OutputTokens)
	}
}

func TestHandleAgentEnd_SkipsAssistantWithoutUsage(t *testing.T) {
	s := newTestSession()
	defer s.cancel()
	s.modelsCW = map[string]int{"real-model": 500_000}

	// First assistant has no usage, second has usage — walk backwards should
	// skip the first and pick the second.
	s.handleEvent(map[string]any{
		"type": "agent_end",
		"messages": []any{
			map[string]any{
				"role":  "assistant",
				"model": "no-usage-model",
				// no "usage"
			},
			map[string]any{
				"role":  "assistant",
				"model": "real-model",
				"usage": map[string]any{
					"input":      float64(3000),
					"output":     float64(200),
					"cacheRead":  float64(1000),
					"cacheWrite": float64(500),
				},
			},
		},
	})

	usage := s.GetContextUsage()
	if usage == nil {
		t.Fatal("GetContextUsage returned nil — should have found the second assistant")
	}
	if usage.ContextWindow != 500_000 {
		t.Errorf("ContextWindow = %d, want 500_000", usage.ContextWindow)
	}
	if usage.InputTokens != 3000 {
		t.Errorf("InputTokens = %d, want 3000", usage.InputTokens)
	}
}
