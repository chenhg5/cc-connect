package core

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadHeartbeatMD(t *testing.T) {
	dir := t.TempDir()

	// No file → empty string
	if got := readHeartbeatMD(dir); got != "" {
		t.Errorf("expected empty, got %q", got)
	}

	// Write HEARTBEAT.md
	content := "- check inbox\n- check tasks"
	if err := os.WriteFile(filepath.Join(dir, "HEARTBEAT.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := readHeartbeatMD(dir); got != content {
		t.Errorf("expected %q, got %q", content, got)
	}

	// Empty work dir → empty string
	if got := readHeartbeatMD(""); got != "" {
		t.Errorf("expected empty for empty workdir, got %q", got)
	}
}

func TestReadHeartbeatMD_LowerCase(t *testing.T) {
	dir := t.TempDir()
	content := "- check status"
	if err := os.WriteFile(filepath.Join(dir, "heartbeat.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := readHeartbeatMD(dir); got != content {
		t.Errorf("expected %q, got %q", content, got)
	}
}

func TestHeartbeatScheduler_RegisterSkipsDisabled(t *testing.T) {
	hs := NewHeartbeatScheduler()
	hs.Register("test", HeartbeatConfig{Enabled: false, SessionKey: "tg:1:1"}, nil, "")
	if len(hs.entries) != 0 {
		t.Errorf("expected 0 entries for disabled config, got %d", len(hs.entries))
	}
}

func TestHeartbeatScheduler_RegisterSkipsEmptySessionKey(t *testing.T) {
	hs := NewHeartbeatScheduler()
	hs.Register("test", HeartbeatConfig{Enabled: true, SessionKey: ""}, nil, "")
	if len(hs.entries) != 0 {
		t.Errorf("expected 0 entries for empty session_key, got %d", len(hs.entries))
	}
}

func TestHeartbeatScheduler_RegisterDefaults(t *testing.T) {
	hs := NewHeartbeatScheduler()
	hs.Register("test", HeartbeatConfig{
		Enabled:    true,
		SessionKey: "telegram:123:123",
	}, nil, "/tmp/test")

	if len(hs.entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(hs.entries))
	}
	entry := hs.entries[0]
	if entry.config.IntervalMins != 30 {
		t.Errorf("expected default interval 30, got %d", entry.config.IntervalMins)
	}
	if entry.config.TimeoutMins != 30 {
		t.Errorf("expected default timeout 30, got %d", entry.config.TimeoutMins)
	}
}
