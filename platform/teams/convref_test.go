package teams

import (
	"os"
	"path/filepath"
	"testing"
)

func sampleRef() storedReplyRef {
	return storedReplyRef{
		ServiceURL:     "https://smba.trafficmanager.net/emea/",
		ConversationID: "19:conv@thread.tacv2",
		BotAccount:     channelAccount{ID: "28:app-id", Name: "bot"},
	}
}

func TestConvRefStore_UpsertThenLookup(t *testing.T) {
	s := newConvRefStore("")
	ref := sampleRef()
	s.upsert("teams:conv", ref)

	got, ok := s.lookup("teams:conv")
	if !ok {
		t.Fatal("lookup: expected the upserted reference")
	}
	if got != ref {
		t.Fatalf("lookup = %+v, want %+v", got, ref)
	}
}

func TestConvRefStore_LookupMiss(t *testing.T) {
	s := newConvRefStore("")
	if _, ok := s.lookup("teams:unknown"); ok {
		t.Fatal("lookup of unknown key should miss")
	}
}

func TestConvRefStore_PersistRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "teams", "proj-convrefs.json")
	ref := sampleRef()

	newConvRefStore(path).upsert("teams:conv", ref)

	// A fresh store at the same path must load the persisted reference — proves
	// references survive a process restart.
	got, ok := newConvRefStore(path).lookup("teams:conv")
	if !ok {
		t.Fatal("reload: expected the persisted reference")
	}
	if got != ref {
		t.Fatalf("reloaded = %+v, want %+v", got, ref)
	}
}

func TestConvRefStore_WriteOnlyOnChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "convrefs.json")
	s := newConvRefStore(path)
	ref := sampleRef()
	s.upsert("teams:conv", ref)

	// Clobber the file, then upsert the identical reference: if upsert is
	// write-rare, it must NOT rewrite the file, so the sentinel survives.
	if err := os.WriteFile(path, []byte("SENTINEL"), 0o600); err != nil {
		t.Fatalf("clobber: %v", err)
	}
	s.upsert("teams:conv", ref)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(data) != "SENTINEL" {
		t.Fatalf("upsert of identical ref rewrote the file: %q", data)
	}

	// A genuine change must rewrite the file.
	changed := ref
	changed.ServiceURL = "https://smba.trafficmanager.net/amer/"
	s.upsert("teams:conv", changed)
	data, err = os.ReadFile(path)
	if err != nil {
		t.Fatalf("read after change: %v", err)
	}
	if string(data) == "SENTINEL" {
		t.Fatal("upsert of a changed ref did not rewrite the file")
	}
	if got, _ := s.lookup("teams:conv"); got != changed {
		t.Fatalf("lookup after change = %+v, want %+v", got, changed)
	}
}

func TestConvRefStore_EmptyPathInMemoryOnly(t *testing.T) {
	dir := t.TempDir()
	// convRefPath returns "" when dataDir or project is empty => persistence off.
	if p := convRefPath("", "proj"); p != "" {
		t.Fatalf("convRefPath with empty dataDir = %q, want empty", p)
	}
	if p := convRefPath(dir, ""); p != "" {
		t.Fatalf("convRefPath with empty project = %q, want empty", p)
	}

	s := newConvRefStore("")
	s.upsert("teams:conv", sampleRef())
	if _, ok := s.lookup("teams:conv"); !ok {
		t.Fatal("in-memory store should still serve lookups")
	}
	// No file should have been created anywhere under dir.
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatalf("empty-path store wrote files: %v", entries)
	}
}

func TestConvRefStore_CorruptFileStartsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "convrefs.json")
	if err := os.WriteFile(path, []byte("{not valid json"), 0o600); err != nil {
		t.Fatalf("seed corrupt: %v", err)
	}
	s := newConvRefStore(path) // must not panic or error
	if _, ok := s.lookup("teams:conv"); ok {
		t.Fatal("corrupt store should start empty")
	}
	// And it should still be usable afterwards.
	s.upsert("teams:conv", sampleRef())
	if _, ok := s.lookup("teams:conv"); !ok {
		t.Fatal("store should be usable after recovering from corrupt file")
	}
}

func TestConvRefStore_PersistsWithOwnerOnlyMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "convrefs.json")
	newConvRefStore(path).upsert("teams:conv", sampleRef())

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	// The stored serviceURL routes the bot's bearer token, so the file is
	// owner-only (0600) — stricter than the engagement store's 0644.
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("store file mode = %o, want 0600", perm)
	}
}

func TestConvRefStore_NullJSONStartsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "convrefs.json")
	if err := os.WriteFile(path, []byte("null"), 0o600); err != nil {
		t.Fatalf("seed null: %v", err)
	}
	s := newConvRefStore(path)
	// The nil-map guard in load() means a subsequent upsert must not panic on the
	// nil map a bare `null` would otherwise unmarshal into.
	s.upsert("teams:conv", sampleRef())
	if _, ok := s.lookup("teams:conv"); !ok {
		t.Fatal("store should be usable after loading a `null` file")
	}
}

func TestConvRefStore_NilSafe(t *testing.T) {
	var s *convRefStore
	s.upsert("teams:conv", sampleRef()) // must not panic
	if _, ok := s.lookup("teams:conv"); ok {
		t.Fatal("nil store lookup should miss")
	}
}
