package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestReadHeartbeatMD(t *testing.T) {
	dir := t.TempDir()

	if got := readHeartbeatMD(dir); got != "" {
		t.Errorf("expected empty, got %q", got)
	}

	content := "- check inbox\n- check tasks"
	if err := os.WriteFile(filepath.Join(dir, "HEARTBEAT.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := readHeartbeatMD(dir); got != content {
		t.Errorf("expected %q, got %q", content, got)
	}

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
	hs := NewHeartbeatScheduler("")
	hs.Register("test", HeartbeatConfig{Enabled: false, SessionKey: "tg:1:1"}, nil, "")
	if len(hs.entries) != 0 {
		t.Errorf("expected 0 entries for disabled config, got %d", len(hs.entries))
	}
}

func TestHeartbeatScheduler_RegisterSkipsEmptySessionKey(t *testing.T) {
	hs := NewHeartbeatScheduler("")
	hs.Register("test", HeartbeatConfig{Enabled: true, SessionKey: ""}, nil, "")
	if len(hs.entries) != 0 {
		t.Errorf("expected 0 entries for empty session_key, got %d", len(hs.entries))
	}
}

func TestHeartbeatScheduler_RegisterDefaults(t *testing.T) {
	hs := NewHeartbeatScheduler("")
	hs.Register("test", HeartbeatConfig{
		Enabled:    true,
		SessionKey: "telegram:123:123",
	}, nil, "/tmp/test")

	if len(hs.entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(hs.entries))
	}
	entry := hs.entries["test"]
	if entry == nil {
		t.Fatal("expected entry for 'test'")
	}
	if entry.config.IntervalMins != 30 {
		t.Errorf("expected default interval 30, got %d", entry.config.IntervalMins)
	}
	if entry.config.TimeoutMins != 30 {
		t.Errorf("expected default timeout 30, got %d", entry.config.TimeoutMins)
	}
}

func TestHeartbeatScheduler_Status(t *testing.T) {
	hs := NewHeartbeatScheduler("")
	hs.Register("proj", HeartbeatConfig{
		Enabled:      true,
		SessionKey:   "tg:1:1",
		IntervalMins: 15,
		OnlyWhenIdle: true,
	}, nil, "")

	st := hs.Status("proj")
	if st == nil {
		t.Fatal("expected status")
	}
	if st.IntervalMins != 15 {
		t.Errorf("expected interval 15, got %d", st.IntervalMins)
	}
	if !st.OnlyWhenIdle {
		t.Error("expected only_when_idle true")
	}
	if st.RunCount != 0 {
		t.Errorf("expected 0 runs, got %d", st.RunCount)
	}

	if hs.Status("nonexistent") != nil {
		t.Error("expected nil for nonexistent project")
	}
}

func TestHeartbeatScheduler_PauseResume(t *testing.T) {
	hs := NewHeartbeatScheduler("")
	hs.Register("proj", HeartbeatConfig{
		Enabled:    true,
		SessionKey: "tg:1:1",
	}, nil, "")

	if !hs.Pause("proj") {
		t.Error("pause should succeed")
	}
	st := hs.Status("proj")
	if !st.Paused {
		t.Error("expected paused")
	}

	if !hs.Resume("proj") {
		t.Error("resume should succeed")
	}
	st = hs.Status("proj")
	if st.Paused {
		t.Error("expected not paused")
	}

	if hs.Pause("nonexistent") {
		t.Error("pause nonexistent should fail")
	}
}

func TestHeartbeatScheduler_SetInterval(t *testing.T) {
	hs := NewHeartbeatScheduler("")
	hs.Register("proj", HeartbeatConfig{
		Enabled:    true,
		SessionKey: "tg:1:1",
	}, nil, "")

	if !hs.SetInterval("proj", 10) {
		t.Error("set interval should succeed")
	}
	st := hs.Status("proj")
	if st.IntervalMins != 10 {
		t.Errorf("expected 10, got %d", st.IntervalMins)
	}

	if hs.SetInterval("proj", 0) {
		t.Error("set interval 0 should fail")
	}
	if hs.SetInterval("nonexistent", 5) {
		t.Error("set interval nonexistent should fail")
	}
}

func TestHeartbeatScheduler_Persistence(t *testing.T) {
	dataDir := t.TempDir()

	// Create scheduler, register, pause, change interval
	hs1 := NewHeartbeatScheduler(dataDir)
	hs1.Register("proj-a", HeartbeatConfig{
		Enabled:      true,
		SessionKey:   "tg:1:1",
		IntervalMins: 30,
	}, nil, "")
	hs1.Register("proj-b", HeartbeatConfig{
		Enabled:      true,
		SessionKey:   "tg:2:2",
		IntervalMins: 15,
	}, nil, "")

	hs1.Pause("proj-a")
	hs1.SetInterval("proj-b", 60)

	// Verify state file exists
	stateFile := filepath.Join(dataDir, "heartbeat_state.json")
	data, err := os.ReadFile(stateFile)
	if err != nil {
		t.Fatalf("state file should exist: %v", err)
	}
	var states map[string]*heartbeatPersisted
	if err := json.Unmarshal(data, &states); err != nil {
		t.Fatalf("parse state file: %v", err)
	}
	if !states["proj-a"].Paused {
		t.Error("proj-a should be paused in state file")
	}
	if states["proj-b"].IntervalMins != 60 {
		t.Errorf("proj-b interval should be 60, got %d", states["proj-b"].IntervalMins)
	}

	// Create new scheduler from same dataDir → should restore state
	hs2 := NewHeartbeatScheduler(dataDir)
	hs2.Register("proj-a", HeartbeatConfig{
		Enabled:      true,
		SessionKey:   "tg:1:1",
		IntervalMins: 30,
	}, nil, "")
	hs2.Register("proj-b", HeartbeatConfig{
		Enabled:      true,
		SessionKey:   "tg:2:2",
		IntervalMins: 15,
	}, nil, "")

	stA := hs2.Status("proj-a")
	if !stA.Paused {
		t.Error("proj-a should be paused after restore")
	}
	stB := hs2.Status("proj-b")
	if stB.IntervalMins != 60 {
		t.Errorf("proj-b interval should be 60 after restore, got %d", stB.IntervalMins)
	}

	// Resume proj-a and reset proj-b interval → no overrides → state file removed
	hs2.Resume("proj-a")
	hs2.SetInterval("proj-b", 15) // back to original
	if _, err := os.Stat(stateFile); !os.IsNotExist(err) {
		t.Error("state file should be removed when no overrides remain")
	}
}
