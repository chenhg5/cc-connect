package core

import "testing"

func TestWorkspaceDirNameForChannel_PreservesSimpleNames(t *testing.T) {
	got := workspaceDirNameForChannel("C001", "project-a")
	if got != "project-a" {
		t.Fatalf("workspaceDirNameForChannel() = %q, want %q", got, "project-a")
	}
}

func TestWorkspaceDirNameForChannel_SanitizesTraversalAndSeparators(t *testing.T) {
	got := workspaceDirNameForChannel("C002", "../研发/项目:alpha")
	if got != "研发-项目-alpha" {
		t.Fatalf("workspaceDirNameForChannel() = %q, want %q", got, "研发-项目-alpha")
	}
}

func TestWorkspaceDirNameForChannel_FallsBackToChannelID(t *testing.T) {
	got := workspaceDirNameForChannel("oc_123", " / ")
	if got != "channel-oc_123" {
		t.Fatalf("workspaceDirNameForChannel() = %q, want %q", got, "channel-oc_123")
	}
}

func TestWorkspaceDirNameForChannel_TruncatesWithoutBreakingUTF8(t *testing.T) {
	input := "项目项目项目项目项目项目项目项目项目项目项目项目项目项目项目项目项目项目项目项目项目项目项目项目项目项目"
	got := workspaceDirNameForChannel("C003", input)
	if len(got) == 0 {
		t.Fatal("expected non-empty directory name")
	}
	if len(got) > maxWorkspaceDirNameBytes {
		t.Fatalf("expected %d bytes max, got %d", maxWorkspaceDirNameBytes, len(got))
	}
}
