package telegram

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMenuConfig_PinUnpin(t *testing.T) {
	cfg := &MenuConfig{chatID: 1, dataDir: t.TempDir()}

	pinned, err := cfg.TogglePinned("new")
	if err != nil || !pinned {
		t.Fatalf("expected pinned=true err=nil, got pinned=%v err=%v", pinned, err)
	}
	if len(cfg.Pinned) != 1 || cfg.Pinned[0] != "new" {
		t.Error("expected Pinned=[new]")
	}

	pinned, err = cfg.TogglePinned("new")
	if err != nil || pinned {
		t.Fatalf("expected unpinned, got pinned=%v err=%v", pinned, err)
	}
	if len(cfg.Pinned) != 0 {
		t.Error("expected empty Pinned after unpin")
	}
}

func TestMenuConfig_PinMax4(t *testing.T) {
	cfg := &MenuConfig{chatID: 1, dataDir: t.TempDir()}
	for _, cmd := range []string{"a", "b", "c", "d"} {
		cfg.TogglePinned(cmd)
	}
	_, err := cfg.TogglePinned("e")
	if err == nil {
		t.Error("expected error when exceeding max 4 pinned commands")
	}
}

func TestMenuConfig_HideCat(t *testing.T) {
	cfg := &MenuConfig{chatID: 1, dataDir: t.TempDir()}

	hidden := cfg.ToggleHiddenCat("advanced")
	if !hidden {
		t.Error("expected hidden=true")
	}
	if !cfg.IsCatHidden("advanced") {
		t.Error("expected advanced to be hidden")
	}

	// session cannot be hidden
	hidden = cfg.ToggleHiddenCat("session")
	if hidden || cfg.IsCatHidden("session") {
		t.Error("session should never be hidden")
	}
}

func TestMenuConfig_SaveLoad(t *testing.T) {
	dir := t.TempDir()
	cfg := LoadMenuConfig(42, dir)
	cfg.TogglePinned("stop")
	cfg.ToggleHiddenCat("advanced")
	if err := cfg.Save(); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	// Verify file exists
	if _, err := os.Stat(filepath.Join(dir, "menu_config_42.json")); err != nil {
		t.Fatalf("config file not created: %v", err)
	}

	// Reload
	cfg2 := LoadMenuConfig(42, dir)
	if len(cfg2.Pinned) != 1 || cfg2.Pinned[0] != "stop" {
		t.Errorf("loaded Pinned mismatch: %v", cfg2.Pinned)
	}
	if !cfg2.IsCatHidden("advanced") {
		t.Error("loaded HiddenCats mismatch")
	}
}

func TestMenuConfig_Reset(t *testing.T) {
	cfg := LoadMenuConfig(1, t.TempDir())
	cfg.TogglePinned("new")
	cfg.ToggleHiddenCat("advanced")
	cfg.Reset()
	if len(cfg.Pinned) != 0 || len(cfg.HiddenCats) != 0 {
		t.Error("Reset should clear all config")
	}
}
