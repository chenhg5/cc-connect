package core

import (
	"path/filepath"
	"testing"
	"time"
)

func TestGroupAgentWorkdirsGroupsByCwd(t *testing.T) {
	projectA := filepath.Join(t.TempDir(), "project-a")
	projectB := filepath.Join(t.TempDir(), "project-b")
	newer := time.Date(2026, 5, 24, 12, 0, 0, 0, time.UTC)
	older := newer.Add(-time.Hour)

	got := GroupAgentWorkdirs([]AgentSessionInfo{
		{ID: "a1", Cwd: projectA, Summary: "first", MessageCount: 2, ModifiedAt: older},
		{ID: "a2", Cwd: projectA, Summary: "latest", MessageCount: 3, ModifiedAt: newer},
		{ID: "b1", Cwd: projectB, Summary: "other", MessageCount: 1, ModifiedAt: older.Add(-time.Hour)},
	}, nil)

	if len(got) != 2 {
		t.Fatalf("len = %d, want 2: %#v", len(got), got)
	}
	if got[0].Cwd != projectA {
		t.Fatalf("first cwd = %q, want %q", got[0].Cwd, projectA)
	}
	if got[0].SessionCount != 2 || got[0].MessageCount != 5 {
		t.Fatalf("first counts = %d/%d, want 2/5", got[0].SessionCount, got[0].MessageCount)
	}
	if got[0].LatestSummary != "latest" {
		t.Fatalf("latest summary = %q, want latest", got[0].LatestSummary)
	}
	if got[1].Cwd != projectB {
		t.Fatalf("second cwd = %q, want %q", got[1].Cwd, projectB)
	}
}

func TestGroupAgentWorkdirsGroupsByProviderRoots(t *testing.T) {
	rootA := filepath.Join(t.TempDir(), "project-a")
	nestedA := filepath.Join(rootA, "service")
	rootB := filepath.Join(t.TempDir(), "project-b")
	loose := filepath.Join(t.TempDir(), "loose")
	newer := time.Date(2026, 5, 24, 12, 0, 0, 0, time.UTC)

	got := GroupAgentWorkdirs([]AgentSessionInfo{
		{ID: "a1", Cwd: rootA, Summary: "root", MessageCount: 2, ModifiedAt: newer.Add(-time.Hour)},
		{ID: "a2", Cwd: nestedA, Summary: "nested", MessageCount: 2, ModifiedAt: newer},
		{ID: "b1", Cwd: rootB, Summary: "b", MessageCount: 2, ModifiedAt: newer.Add(-2 * time.Hour)},
		{ID: "c1", Cwd: loose, Summary: "loose", MessageCount: 2, ModifiedAt: newer.Add(time.Hour)},
	}, []string{rootB, rootA})

	if len(got) != 3 {
		t.Fatalf("len = %d, want 3: %#v", len(got), got)
	}
	if got[0].Cwd != rootB || got[1].Cwd != rootA || got[2].Cwd != loose {
		t.Fatalf("cwd order = [%q %q %q], want provider roots then loose", got[0].Cwd, got[1].Cwd, got[2].Cwd)
	}
	if got[1].SessionCount != 2 || got[1].LatestSummary != "nested" {
		t.Fatalf("rootA = %#v, want 2 sessions with nested latest", got[1])
	}
}

func TestMatchSessionWorkdirProjectRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "project")
	nested := filepath.Join(root, "service")

	if !MatchSessionWorkdir(nested, root, true) {
		t.Fatalf("nested session should match project root")
	}
	if MatchSessionWorkdir(nested, root, false) {
		t.Fatalf("nested session should not match exact cwd")
	}
}
