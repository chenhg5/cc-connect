package codex

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeCodexAppState(t *testing.T, home string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".codex-global-state.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestWorkspaceCatalogPreservesProjectOrderAndExpandsRoots(t *testing.T) {
	home := t.TempDir()
	rootA := t.TempDir()
	rootB := t.TempDir()
	missing := filepath.Join(t.TempDir(), "missing")
	writeCodexAppState(t, home, map[string]any{
		"local-projects": map[string]any{
			"p1": map[string]any{"id": "p1", "name": "First", "rootPaths": []string{rootA}},
			"p2": map[string]any{"id": "p2", "name": "Second", "rootPaths": []string{rootB, missing}},
			"p3": map[string]any{"id": "p3", "name": "Unordered", "rootPaths": []string{t.TempDir()}},
		},
		"project-order": []string{"p2", "p1"},
	})
	agent := &Agent{codexHome: home}
	items, err := agent.ListWorkspaces(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 4 {
		t.Fatalf("workspaces = %d, want 4", len(items))
	}
	if items[0].ProjectID != "p2" || items[0].RootIndex != 0 || items[1].ProjectID != "p2" || items[1].RootIndex != 1 || items[2].ProjectID != "p1" || items[3].ProjectID != "p3" {
		t.Fatalf("workspace order = %#v", items)
	}
	if !items[0].Available || items[1].Available || !strings.Contains(items[1].Error, "no such file") {
		t.Fatalf("availability = %#v", items[:2])
	}
	if items[0].Ref == "" || items[0].Ref == items[1].Ref {
		t.Fatalf("workspace refs are not opaque and unique: %#v", items[:2])
	}
	resolved, err := agent.ResolveWorkspace(context.Background(), items[0].Ref)
	if err != nil || resolved.RootPath != rootB {
		t.Fatalf("ResolveWorkspace() = %#v, %v", resolved, err)
	}
	if _, err := agent.ResolveWorkspace(context.Background(), "ws_forged"); err == nil {
		t.Fatal("ResolveWorkspace accepted a forged reference")
	}
}

func TestWorkspaceCatalogRejectsMalformedSchema(t *testing.T) {
	tests := []struct {
		name  string
		state any
	}{
		{name: "missing project order", state: map[string]any{"local-projects": map[string]any{}}},
		{name: "missing project", state: map[string]any{"local-projects": map[string]any{}, "project-order": []string{"p1"}}},
		{name: "duplicate project", state: map[string]any{"local-projects": map[string]any{"p1": map[string]any{"id": "p1", "name": "P", "rootPaths": []string{"/tmp"}}}, "project-order": []string{"p1", "p1"}}},
		{name: "relative root", state: map[string]any{"local-projects": map[string]any{"p1": map[string]any{"id": "p1", "name": "P", "rootPaths": []string{"relative"}}}, "project-order": []string{"p1"}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			home := t.TempDir()
			writeCodexAppState(t, home, test.state)
			agent := &Agent{codexHome: home}
			if _, err := agent.ListWorkspaces(context.Background()); err == nil {
				t.Fatal("ListWorkspaces accepted malformed state")
			}
		})
	}
}
