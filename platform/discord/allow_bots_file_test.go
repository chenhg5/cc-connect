package discord

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAllowBotsFile_ReadsIDsAndReloadsOnChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "allow.txt")
	f := newAllowBotsFile(path)
	if f.has("111") {
		t.Fatal("missing file must allow nobody")
	}
	if err := os.WriteFile(path, []byte("# host-created webhooks\n111\n\n  222  \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !f.has("111") || !f.has("222") || f.has("333") || f.has("") || f.has("# host-created webhooks") {
		t.Fatalf("ids = %v", f.ids)
	}
	// host appends a new webhook ID: picked up without restart
	if err := os.WriteFile(path, []byte("# host-created webhooks\n111\n\n  222  \n333\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !f.has("333") {
		t.Fatal("appended id not picked up")
	}
	// same size, new mtime: reloaded too
	if err := os.WriteFile(path, []byte("# host-created webhooks\n111\n\n  222  \n444\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}
	if f.has("333") || !f.has("444") {
		t.Fatalf("not reloaded on mtime change: %v", f.ids)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if f.has("111") {
		t.Fatal("removed file must allow nobody")
	}
}

func TestAllowBotsFile_EmptyPathIsNil(t *testing.T) {
	if newAllowBotsFile("  ") != nil {
		t.Fatal("empty path should disable the file")
	}
	var f *allowBotsFile
	if f.has("1") {
		t.Fatal("nil file allows nobody")
	}
}

func TestBotAllowed_MergesAllowBotsAndFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "allow.txt")
	if err := os.WriteFile(path, []byte("900\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := &Platform{allowBots: "100,200", allowBotsFile: newAllowBotsFile(path)}
	for id, want := range map[string]bool{"100": true, "200": true, "900": true, "300": false} {
		if got := p.botAllowed(id); got != want {
			t.Errorf("botAllowed(%s) = %v, want %v", id, got, want)
		}
	}
	// no allow_bots: only the file counts (an empty allow_bots used to mean "no bots")
	p = &Platform{allowBotsFile: newAllowBotsFile(path)}
	if !p.botAllowed("900") || p.botAllowed("100") {
		t.Error("file-only allow list wrong")
	}
	p = &Platform{}
	if p.botAllowed("900") {
		t.Error("nothing configured must allow no bots")
	}
}

func TestNew_AllowBotsFileOption(t *testing.T) {
	path := filepath.Join(t.TempDir(), "allow.txt")
	if err := os.WriteFile(path, []byte("777\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	pl, err := New(map[string]any{"token": "x", "allow_bots_file": path})
	if err != nil {
		t.Fatal(err)
	}
	p := platformOf(t, pl)
	if !p.botAllowed("777") || p.botAllowed("1") {
		t.Fatal("allow_bots_file option not wired")
	}
}

func platformOf(t *testing.T, pl any) *Platform {
	t.Helper()
	switch v := pl.(type) {
	case *Platform:
		return v
	case *progressPlatform:
		return v.Platform
	}
	t.Fatalf("platform type = %T", pl)
	return nil
}
