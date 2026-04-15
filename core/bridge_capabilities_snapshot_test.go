package core

import "testing"

func TestBridgeBuildCapabilitiesSnapshotIncludesProjectCatalog(t *testing.T) {
	prevVersion, prevCommit, prevBuildTime := CurrentVersion, CurrentCommit, CurrentBuildTime
	CurrentVersion = "v9.9.9"
	CurrentCommit = "abc123"
	CurrentBuildTime = "2026-04-11T00:00:00Z"
	defer func() {
		CurrentVersion = prevVersion
		CurrentCommit = prevCommit
		CurrentBuildTime = prevBuildTime
	}()

	bs := NewBridgeServer(0, "", "/bridge/ws", nil)
	bp := bs.NewPlatform("test-proj")
	e := NewEngine("test-proj", &stubAgent{}, []Platform{bp}, "", LangEnglish)
	e.AddCommand("deploy", "Deploy app", "ship it", "", "", "config")
	bs.RegisterEngine("test-proj", e, bp)

	snapshot := bs.buildCapabilitiesSnapshot()
	if snapshot.Type != bridgeCapabilitiesSnapshotType {
		t.Fatalf("type = %q, want %q", snapshot.Type, bridgeCapabilitiesSnapshotType)
	}
	if snapshot.Version != 1 {
		t.Fatalf("version = %d, want 1", snapshot.Version)
	}
	if snapshot.Host.CCConnectVersion != "v9.9.9" {
		t.Fatalf("cc_connect_version = %q, want %q", snapshot.Host.CCConnectVersion, "v9.9.9")
	}
	if snapshot.Host.Commit != "abc123" {
		t.Fatalf("commit = %q, want %q", snapshot.Host.Commit, "abc123")
	}
	if snapshot.Host.BuildTime != "2026-04-11T00:00:00Z" {
		t.Fatalf("build_time = %q, want %q", snapshot.Host.BuildTime, "2026-04-11T00:00:00Z")
	}
	if len(snapshot.Projects) != 1 {
		t.Fatalf("projects = %d, want 1", len(snapshot.Projects))
	}
	if snapshot.Projects[0].Project != "test-proj" {
		t.Fatalf("project = %q, want %q", snapshot.Projects[0].Project, "test-proj")
	}

	foundDeploy := false
	for _, cmd := range snapshot.Projects[0].Commands {
		if cmd.Name == "deploy" {
			foundDeploy = true
			break
		}
	}
	if !foundDeploy {
		t.Fatal("expected deploy command in project capabilities snapshot")
	}
}
