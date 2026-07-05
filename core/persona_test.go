package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolvePersonaClass(t *testing.T) {
	cases := []struct {
		name                string
		project             string
		hasWorkspacePattern bool
		want                PersonaClass
	}{
		{"secretary always secretary", "secretary-seat", false, PersonaClassSecretary},
		{"secretary with workspace pattern still secretary", "secretary-seat", true, PersonaClassSecretary},
		{"execution seat with workspace pattern is write", "dev-pro", true, PersonaClassWrite},
		{"non-execution seat is read", "reviewer-seat", false, PersonaClassRead},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ResolvePersonaClass(tc.project, tc.hasWorkspacePattern); got != tc.want {
				t.Errorf("ResolvePersonaClass(%q, %v) = %q, want %q", tc.project, tc.hasWorkspacePattern, got, tc.want)
			}
		})
	}
}

func TestComposePersona_UsesPreambleFile(t *testing.T) {
	tmpDir := t.TempDir()
	preambleDir := filepath.Join(tmpDir, "_preamble")
	if err := os.MkdirAll(preambleDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(preambleDir, "archive-first.write.md"), []byte("WRITE_PREAMBLE"), 0o644); err != nil {
		t.Fatalf("write preamble: %v", err)
	}

	got := ComposePersona(tmpDir, PersonaClassWrite, "PERSONA_BODY")
	if !strings.HasPrefix(got, "WRITE_PREAMBLE") {
		t.Errorf("expected preamble at head, got:\n%s", got)
	}
	if !strings.Contains(got, "PERSONA_BODY") {
		t.Errorf("expected persona body present, got:\n%s", got)
	}
	if strings.Index(got, "WRITE_PREAMBLE") > strings.Index(got, "PERSONA_BODY") {
		t.Errorf("expected preamble before persona body, got:\n%s", got)
	}
}

func TestComposePersona_FallsBackWhenPreambleMissing(t *testing.T) {
	tmpDir := t.TempDir() // no _preamble dir at all

	got := ComposePersona(tmpDir, PersonaClassRead, "PERSONA_BODY")
	if !strings.HasPrefix(got, archiveFirstFallback) {
		t.Errorf("expected hardcoded fallback at head, got:\n%s", got)
	}
	if !strings.Contains(got, "PERSONA_BODY") {
		t.Errorf("expected persona body still present, got:\n%s", got)
	}
}

func TestComposePersona_EmptyPersonaContentReturnsOnlyPreamble(t *testing.T) {
	got := ComposePersona("", PersonaClassRead, "")
	if got != archiveFirstFallback {
		t.Errorf("expected bare fallback preamble, got:\n%s", got)
	}
}

func TestSyncManagedBlock_CreatesFileWhenMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "AGENTS.md")

	if err := SyncManagedBlock(path, ArchiveFirstMarkerStart, ArchiveFirstMarkerEnd, "hello"); err != nil {
		t.Fatalf("SyncManagedBlock: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, ArchiveFirstMarkerStart) || !strings.Contains(content, "hello") || !strings.Contains(content, ArchiveFirstMarkerEnd) {
		t.Errorf("expected bounded block, got:\n%s", content)
	}
}

func TestSyncManagedBlock_PreservesSurroundingContentAndOverwritesBlock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "AGENTS.md")
	initial := "# Project notes\n\nSome human-written content.\n\n" +
		ArchiveFirstMarkerStart + "\nOLD_PREAMBLE\n" + ArchiveFirstMarkerEnd +
		"\n\nMore human content below.\n"
	if err := os.WriteFile(path, []byte(initial), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	if err := SyncManagedBlock(path, ArchiveFirstMarkerStart, ArchiveFirstMarkerEnd, "NEW_PREAMBLE"); err != nil {
		t.Fatalf("SyncManagedBlock: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	content := string(data)
	if strings.Contains(content, "OLD_PREAMBLE") {
		t.Errorf("expected old block content replaced, got:\n%s", content)
	}
	if !strings.Contains(content, "NEW_PREAMBLE") {
		t.Errorf("expected new block content present, got:\n%s", content)
	}
	if !strings.Contains(content, "Some human-written content.") || !strings.Contains(content, "More human content below.") {
		t.Errorf("expected surrounding human content preserved, got:\n%s", content)
	}
}
