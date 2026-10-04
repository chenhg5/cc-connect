package codex

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/chenhg5/cc-connect/core"
	"github.com/gorilla/websocket"
)

func TestManagedRuntimeSettingsQueryDoesNotOverrideSettingsOrActiveTurn(t *testing.T) {
	cwd := t.TempDir()
	var mu sync.Mutex
	reads := 0
	socket := managedFixture(t, func(ws *websocket.Conn) {
		fixtureRPC(t, ws, func(m daemonMessage) (any, bool) {
			if m.Method != "thread/resume" {
				t.Errorf("unexpected control method: %s", m.Method)
				return nil, false
			}
			var params map[string]any
			_ = json.Unmarshal(m.Params, &params)
			mu.Lock()
			reads++
			n := reads
			mu.Unlock()
			if params["threadId"] != "target" || (n == 1 && len(params) != 1) || (n > 1 && (len(params) != 2 || params["excludeTurns"] != true)) {
				t.Errorf("settings query overrides runtime: %s", m.Params)
			}
			snapshot := fixtureSnapshot(cwd, "target", "active")
			if n > 1 {
				snapshot["model"] = "externally-updated-model"
				snapshot["reasoningEffort"] = "high"
			}
			snapshot["modelProvider"] = "server-provider"
			snapshot["approvalPolicy"] = "on-request"
			snapshot["sandbox"] = map[string]any{"type": "read-only"}
			return snapshot, true
		})
	})
	a := fixtureManagedAgent(t, socket, cwd, map[string]any{"model": "future-model", "mode": "yolo"})
	as, err := a.AttachSession(context.Background(), "target")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := as.Close(); err != nil {
			t.Errorf("Close cleanup: %v", err)
		}
	}()
	settings, err := as.(core.AgentRuntimeSettingsReader).ReadRuntimeSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if settings.Model != "externally-updated-model" || settings.ReasoningEffort != "high" || settings.Provider != "server-provider" || settings.ApprovalPolicy != "on-request" || settings.Sandbox != "read-only" {
		t.Fatalf("settings not read from server: %+v", settings)
	}
	if as.(core.SharedAgentSession).RuntimeState().TurnID != "active" || !as.Alive() {
		t.Fatal("query changed active runtime")
	}
	if a.GetModel() != "future-model" {
		t.Fatal("reading runtime changed creation defaults")
	}
	if as.(interface{ GetModel() string }).GetModel() != "externally-updated-model" {
		t.Fatal("runtime footer retained stale model after authoritative query")
	}
}

func TestManagedDefaultsConfigureNewThreadWithoutWritingDaemonCredentials(t *testing.T) {
	cwd, home := t.TempDir(), t.TempDir()
	created := make(chan map[string]any, 1)
	socket := managedFixture(t, func(ws *websocket.Conn) {
		fixtureRPC(t, ws, func(m daemonMessage) (any, bool) {
			if m.Method != "thread/start" {
				t.Errorf("unexpected method %s", m.Method)
				return nil, false
			}
			var params map[string]any
			_ = json.Unmarshal(m.Params, &params)
			created <- params
			return fixtureSnapshot(cwd, "new-thread", ""), true
		})
	})
	a := fixtureManagedAgent(t, socket, cwd, map[string]any{"codex_home": home})
	a.SetProviders([]core.ProviderConfig{{Name: "configured-in-daemon", APIKey: "test-secret-not-to-be-written", BaseURL: "https://example.invalid"}})
	a.SetActiveProvider("configured-in-daemon")
	a.SetModel("future-model")
	a.SetReasoningEffort("high")
	a.SetMode("full-auto")
	as, err := a.StartSession(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := as.Close(); err != nil {
			t.Errorf("Close cleanup: %v", err)
		}
	}()
	params := <-created
	if params["model"] != "future-model" || params["modelProvider"] != "configured-in-daemon" || params["sandbox"] != "workspace-write" || params["approvalPolicy"] != "never" || params["config"].(map[string]any)["model_reasoning_effort"] != "high" {
		t.Fatalf("creation defaults not applied: %v", params)
	}
	for _, name := range []string{"auth.json", "config.toml"} {
		if _, err := os.Stat(filepath.Join(home, name)); !os.IsNotExist(err) {
			t.Fatalf("managed creation wrote %s: %v", name, err)
		}
	}
}

func TestManagedProviderCatalogEditsPreserveSelectionByName(t *testing.T) {
	a := fixtureManagedAgent(t, "/unused", t.TempDir(), nil)
	a.SetProviders([]core.ProviderConfig{{Name: "one"}, {Name: "two"}})
	a.SetActiveProvider("two")
	a.SetProviders([]core.ProviderConfig{{Name: "two"}})
	if p := a.GetActiveProvider(); p == nil || p.Name != "two" {
		t.Fatal("removing earlier provider lost active default")
	}
	a.SetProviders([]core.ProviderConfig{{Name: "three"}})
	if a.GetActiveProvider() != nil {
		t.Fatal("removed provider silently selected a different provider")
	}
}
