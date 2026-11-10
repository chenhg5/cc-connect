package core

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestDirHistory_Add_MovesToFrontAndDedups(t *testing.T) {
	dh := NewDirHistory(t.TempDir())
	dh.Add("proj", "/a")
	dh.Add("proj", "/b")
	dh.Add("proj", "/a")

	got := dh.List("proj")
	want := []string{"/a", "/b"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("List = %v, want %v (re-adding /a must move it to front)", got, want)
	}
}

func TestDirHistory_Add_IgnoresEmpty(t *testing.T) {
	dh := NewDirHistory(t.TempDir())
	dh.Add("proj", "")
	if got := dh.List("proj"); got != nil {
		t.Fatalf("Add with empty dir should be a no-op, got %v", got)
	}
}

func TestDirHistory_Add_TrimsToMaxSize(t *testing.T) {
	dh := NewDirHistory(t.TempDir())
	dh.SetMaxSize(3)
	for _, d := range []string{"/a", "/b", "/c", "/d", "/e"} {
		dh.Add("proj", d)
	}
	got := dh.List("proj")
	// Newest first, trimmed to the three most recent: /e, /d, /c.
	want := []string{"/e", "/d", "/c"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("List = %v, want %v (history should be capped at maxSize)", got, want)
	}
}

func TestDirHistory_SetMaxSize_AppliesToNextAdd(t *testing.T) {
	dh := NewDirHistory(t.TempDir())
	for _, d := range []string{"/a", "/b", "/c"} {
		dh.Add("proj", d)
	}
	// Shrinking maxSize does not retroactively drop entries; the next Add
	// must trim according to the new cap.
	dh.SetMaxSize(2)
	dh.Add("proj", "/d")
	got := dh.List("proj")
	want := []string{"/d", "/c"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("List = %v, want %v after SetMaxSize(2)+Add", got, want)
	}
}

func TestDirHistory_SetMaxSize_ClampsToAtLeastOne(t *testing.T) {
	dh := NewDirHistory(t.TempDir())
	dh.SetMaxSize(0)
	dh.Add("proj", "/a")
	dh.Add("proj", "/b")
	got := dh.List("proj")
	want := []string{"/b"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("List = %v, want %v (maxSize must be clamped to >=1)", got, want)
	}
}

func TestDirHistory_Get_OneBasedAndBoundsCheck(t *testing.T) {
	dh := NewDirHistory(t.TempDir())
	dh.Add("proj", "/a")
	dh.Add("proj", "/b")

	if got := dh.Get("proj", 1); got != "/b" {
		t.Errorf("Get(1) = %q, want %q (index 1 is most recent)", got, "/b")
	}
	if got := dh.Get("proj", 2); got != "/a" {
		t.Errorf("Get(2) = %q, want %q", got, "/a")
	}
	if got := dh.Get("proj", 0); got != "" {
		t.Errorf("Get(0) = %q, want empty (out of range)", got)
	}
	if got := dh.Get("proj", 3); got != "" {
		t.Errorf("Get(3) = %q, want empty (out of range)", got)
	}
	if got := dh.Get("other", 1); got != "" {
		t.Errorf("Get on unknown project = %q, want empty", got)
	}
}

func TestDirHistory_PreviousReturnsIndexTwo(t *testing.T) {
	dh := NewDirHistory(t.TempDir())
	dh.Add("proj", "/a")
	dh.Add("proj", "/b")
	if got := dh.Previous("proj"); got != "/a" {
		t.Fatalf("Previous = %q, want %q", got, "/a")
	}
}

func TestDirHistory_Contains(t *testing.T) {
	dh := NewDirHistory(t.TempDir())
	dh.Add("proj", "/a")
	if !dh.Contains("proj", "/a") {
		t.Errorf("Contains(/a) = false, want true")
	}
	if dh.Contains("proj", "/missing") {
		t.Errorf("Contains(/missing) = true, want false")
	}
	if dh.Contains("other", "/a") {
		t.Errorf("Contains on unknown project = true, want false")
	}
}

func TestDirHistory_List_ReturnsDefensiveCopy(t *testing.T) {
	dh := NewDirHistory(t.TempDir())
	dh.Add("proj", "/a")

	first := dh.List("proj")
	first[0] = "/mutated"

	again := dh.List("proj")
	if again[0] != "/a" {
		t.Fatalf("List returned a slice aliasing internal state; got %q after mutation, want /a", again[0])
	}
}

func TestDirHistory_PerProjectIsolation(t *testing.T) {
	dh := NewDirHistory(t.TempDir())
	dh.Add("proj-a", "/a1")
	dh.Add("proj-b", "/b1")

	if got := dh.List("proj-a"); !reflect.DeepEqual(got, []string{"/a1"}) {
		t.Fatalf("proj-a = %v, want [/a1]", got)
	}
	if got := dh.List("proj-b"); !reflect.DeepEqual(got, []string{"/b1"}) {
		t.Fatalf("proj-b = %v, want [/b1]", got)
	}
}

func TestDirHistory_PersistsAcrossInstances(t *testing.T) {
	dataDir := t.TempDir()
	dh1 := NewDirHistory(dataDir)
	dh1.Add("proj", "/a")
	dh1.Add("proj", "/b")

	// New instance pointing at the same data dir must rehydrate from disk.
	dh2 := NewDirHistory(dataDir)
	got := dh2.List("proj")
	want := []string{"/b", "/a"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("after reload List = %v, want %v", got, want)
	}
}

func TestDirHistory_Load_MissingFileIsNotAnError(t *testing.T) {
	// NewDirHistory on an empty data dir must not panic and List must
	// return nil rather than a zero-length slice with stale data.
	dh := NewDirHistory(t.TempDir())
	if got := dh.List("proj"); got != nil {
		t.Fatalf("List on empty history = %v, want nil", got)
	}
}

func TestDirHistory_Load_CorruptFileIsIgnored(t *testing.T) {
	dataDir := t.TempDir()
	store := filepath.Join(dataDir, dirHistoryFileName)
	if err := os.WriteFile(store, []byte("{not json"), 0o644); err != nil {
		t.Fatalf("seed corrupt file: %v", err)
	}

	dh := NewDirHistory(dataDir)
	// Corrupt file should be logged and ignored; Add must still work.
	dh.Add("proj", "/a")
	if got := dh.List("proj"); !reflect.DeepEqual(got, []string{"/a"}) {
		t.Fatalf("List after corrupt load = %v, want [/a]", got)
	}
}
