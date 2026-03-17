package codex

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func writeTranscript(t *testing.T, path, sessionID, cwd, prompt string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}

	data := `{"type":"session_meta","payload":{"id":"` + sessionID + `","cwd":"` + cwd + `","source":"cli","originator":"codex_cli_rs"}}` + "\n" +
		`{"type":"response_item","payload":{"role":"user","content":[{"type":"input_text","text":"` + prompt + `"}]}}` + "\n" +
		`{"type":"response_item","payload":{"role":"assistant","content":[{"type":"output_text","text":"ok"}]}}` + "\n"

	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatalf("write transcript %s: %v", path, err)
	}
}

func sessionByID(sessions []core.AgentSessionInfo, id string) *core.AgentSessionInfo {
	for i := range sessions {
		if sessions[i].ID == id {
			return &sessions[i]
		}
	}
	return nil
}

func TestLoadCodexThreadNamesLastEntryWinsAndSkipsInvalid(t *testing.T) {
	indexPath := filepath.Join(t.TempDir(), "session_index.jsonl")
	data := "" +
		`{"id":"session-1","thread_name":"旧名字"}` + "\n" +
		`not-json` + "\n" +
		`{"id":"session-2","thread_name":""}` + "\n" +
		`{"id":"session-1","thread_name":"服务器运维"}` + "\n"

	if err := os.WriteFile(indexPath, []byte(data), 0o644); err != nil {
		t.Fatalf("write session index: %v", err)
	}

	names := loadCodexThreadNames(indexPath)
	if got := names["session-1"]; got != "服务器运维" {
		t.Fatalf("names[session-1] = %q, want %q", got, "服务器运维")
	}
	if _, ok := names["session-2"]; ok {
		t.Fatalf("session-2 should be skipped when thread_name is empty: %#v", names)
	}
}

func TestListCodexSessionsUsesNativeDisplayNameAndSummaryFallback(t *testing.T) {
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)

	workDir := filepath.Join(codexHome, "workspace")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}

	indexData := "" +
		`{"id":"session-1","thread_name":"服务器运维"}` + "\n" +
		`{"id":"session-2","thread_name":""}` + "\n"
	if err := os.WriteFile(filepath.Join(codexHome, "session_index.jsonl"), []byte(indexData), 0o644); err != nil {
		t.Fatalf("write session index: %v", err)
	}

	writeTranscript(t, filepath.Join(codexHome, "sessions", "2026", "03", "17", "session-1.jsonl"), "session-1", workDir, "PLEASE IMPLEMENT THIS PLAN")
	writeTranscript(t, filepath.Join(codexHome, "sessions", "2026", "03", "17", "session-2.jsonl"), "session-2", workDir, "原始摘要")
	writeTranscript(t, filepath.Join(codexHome, "sessions", "2026", "03", "17", "session-3.jsonl"), "session-3", filepath.Join(codexHome, "other"), "不应匹配")

	sessions, err := listCodexSessions(workDir)
	if err != nil {
		t.Fatalf("listCodexSessions: %v", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("session count = %d, want 2", len(sessions))
	}

	session1 := sessionByID(sessions, "session-1")
	if session1 == nil {
		t.Fatalf("missing session-1 in %#v", sessions)
	}
	if session1.DisplayName != "服务器运维" {
		t.Fatalf("session-1 display name = %q, want %q", session1.DisplayName, "服务器运维")
	}
	if session1.Summary != "PLEASE IMPLEMENT THIS PLAN" {
		t.Fatalf("session-1 summary = %q, want original summary", session1.Summary)
	}

	session2 := sessionByID(sessions, "session-2")
	if session2 == nil {
		t.Fatalf("missing session-2 in %#v", sessions)
	}
	if session2.DisplayName != "" {
		t.Fatalf("session-2 display name = %q, want empty fallback", session2.DisplayName)
	}
	if session2.Summary != "原始摘要" {
		t.Fatalf("session-2 summary = %q, want %q", session2.Summary, "原始摘要")
	}
}

func TestResolveCodexDisplayNamePrefersNativeThreadNameVerbatim(t *testing.T) {
	meta := codexThreadMeta{
		DisplayName: `导出书库笔记到桌面文件夹」}**Wait** The final is:`,
		Title:       `我要换设备了，刚刚把旧的tab10已经连接到mac，书库和笔记请帮我导出到桌面（新建一个文件夹）`,
	}

	got := resolveCodexDisplayName(meta)
	want := `导出书库笔记到桌面文件夹」}**Wait** The final is:`
	if got != want {
		t.Fatalf("resolveCodexDisplayName() = %q, want %q", got, want)
	}
}

func TestParseCodexSessionFileUsesFirstSessionMetaOnly(t *testing.T) {
	dir := t.TempDir()
	workDir := filepath.Join(dir, "workspace")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}

	path := filepath.Join(dir, "session.jsonl")
	data := "" +
		`{"type":"session_meta","payload":{"id":"session-first","cwd":"` + workDir + `"}}` + "\n" +
		`{"type":"response_item","payload":{"role":"user","content":[{"type":"input_text","text":"首条提示"}]}}` + "\n" +
		`{"type":"session_meta","payload":{"id":"session-last","cwd":"/tmp/other"}}` + "\n"
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatalf("write session file: %v", err)
	}

	info := parseCodexSessionFile(path, workDir, map[string]codexThreadMeta{
		"session-first": {DisplayName: "网络维护"},
		"session-last":  {DisplayName: "错误命中"},
	})
	if info == nil {
		t.Fatal("parseCodexSessionFile() returned nil")
	}
	if info.ID != "session-first" {
		t.Fatalf("info.ID = %q, want %q", info.ID, "session-first")
	}
	if info.DisplayName != "网络维护" {
		t.Fatalf("info.DisplayName = %q, want %q", info.DisplayName, "网络维护")
	}
}
