package core

import (
	"path/filepath"
	"strings"
	"testing"
)

// registerWorkspaceStoreAgent registers a throwaway agent type so
// getOrCreateWorkspaceAgent can build a per-workspace instance.
func registerWorkspaceStoreAgent(t *testing.T, agentName string) {
	t.Helper()
	RegisterAgent(agentName, func(opts map[string]any) (Agent, error) {
		workDir, _ := opts["work_dir"].(string)
		return &sendWorkDirAgent{
			name:    agentName,
			workDir: workDir,
			session: newResultAgentSession("workspace session"),
		}, nil
	})
}

// A workspace session manager has to inherit the engine's "no store path means
// no persistence" contract. filepath.Dir("") is ".", so deriving a path from an
// empty store path used to drop "<engine>_ws_<hash>.json" into the process
// working directory — that is how `go test ./core/` littered core/ with
// test_ws_*.json files.
func TestGetOrCreateWorkspaceAgent_EmptyStorePathStaysUnpersisted(t *testing.T) {
	agentName := "test-ws-store-unpersisted"
	registerWorkspaceStoreAgent(t, agentName)

	p := &stubPlatformEngine{n: "telegram"}
	e := NewEngine("test", &sendWorkDirAgent{name: agentName}, []Platform{p}, "", LangEnglish)

	_, sessions, err := e.getOrCreateWorkspaceAgent(t.TempDir())
	if err != nil {
		t.Fatalf("getOrCreateWorkspaceAgent() error = %v", err)
	}
	if got := sessions.StorePath(); got != "" {
		t.Fatalf("workspace store path = %q, want empty when the engine has no store path", got)
	}
}

// The derived path keeps living beside the engine's own store, so a configured
// deployment still persists workspace sessions.
func TestGetOrCreateWorkspaceAgent_DerivesStorePathBesideEngineStore(t *testing.T) {
	agentName := "test-ws-store-derived"
	registerWorkspaceStoreAgent(t, agentName)

	dir := t.TempDir()
	p := &stubPlatformEngine{n: "telegram"}
	e := NewEngine("test", &sendWorkDirAgent{name: agentName}, []Platform{p},
		filepath.Join(dir, "sessions.json"), LangEnglish)

	_, sessions, err := e.getOrCreateWorkspaceAgent(t.TempDir())
	if err != nil {
		t.Fatalf("getOrCreateWorkspaceAgent() error = %v", err)
	}
	got := sessions.StorePath()
	if filepath.Dir(got) != dir {
		t.Fatalf("workspace store dir = %q, want %q", filepath.Dir(got), dir)
	}
	if base := filepath.Base(got); !strings.HasPrefix(base, "test_ws_") || !strings.HasSuffix(base, ".json") {
		t.Fatalf("workspace store name = %q, want test_ws_<hash>.json", base)
	}
}
