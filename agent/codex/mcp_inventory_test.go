package codex

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func TestLoadConfiguredMCPInventory_EmptyWhenConfigMissing(t *testing.T) {
	tmp := t.TempDir()
	inventory, err := loadConfiguredMCPInventory(filepath.Join(tmp, "repo"), filepath.Join(tmp, "codex-home"))
	if err != nil {
		t.Fatalf("loadConfiguredMCPInventory() error = %v", err)
	}
	if inventory.Source != "configured" {
		t.Fatalf("inventory source = %q, want configured", inventory.Source)
	}
	if len(inventory.Servers) != 0 {
		t.Fatalf("len(inventory.Servers) = %d, want 0", len(inventory.Servers))
	}
}

func TestLoadConfiguredMCPInventory_AppliesProjectOverlay(t *testing.T) {
	tmp := t.TempDir()
	codexHome := filepath.Join(tmp, "codex-home")
	repo := filepath.Join(tmp, "repo")
	workDir := filepath.Join(repo, "nested")
	if err := os.MkdirAll(codexHome, 0o755); err != nil {
		t.Fatalf("mkdir codex home: %v", err)
	}
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatalf("mkdir workdir: %v", err)
	}

	config := fmt.Sprintf(`
[mcp_servers.docs]
url = "https://global.example/mcp"
enabled_tools = ["read_file", "search"]

[projects.%q.mcp_servers.docs]
enabled = false
required = true
url = "https://repo.example/mcp"

[projects.%q.mcp_servers.local]
command = "uvx"
args = ["demo-server", "--stdio"]
cwd = %q
enabled = true
`, repo, repo, repo)
	if err := os.WriteFile(filepath.Join(codexHome, "config.toml"), []byte(config), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	inventory, err := loadConfiguredMCPInventory(workDir, codexHome)
	if err != nil {
		t.Fatalf("loadConfiguredMCPInventory() error = %v", err)
	}
	if len(inventory.Servers) != 2 {
		t.Fatalf("len(inventory.Servers) = %d, want 2", len(inventory.Servers))
	}

	docs := inventory.Servers[0]
	if docs.Name != "docs" {
		t.Fatalf("inventory.Servers[0].Name = %q, want docs", docs.Name)
	}
	if docs.URL != "https://repo.example/mcp" || docs.Enabled || !docs.Required {
		t.Fatalf("docs server = %#v, want project overlay values", docs)
	}
	if docs.Transport != "remote" {
		t.Fatalf("docs transport = %q, want remote", docs.Transport)
	}
	if len(docs.Tools) != 2 || docs.Tools[0] != "read_file" || docs.Tools[1] != "search" {
		t.Fatalf("docs tools = %v, want inherited enabled_tools", docs.Tools)
	}

	local := inventory.Servers[1]
	if local.Name != "local" {
		t.Fatalf("inventory.Servers[1].Name = %q, want local", local.Name)
	}
	if local.Transport != "stdio" {
		t.Fatalf("local transport = %q, want stdio", local.Transport)
	}
	if local.Command != "uvx demo-server --stdio" {
		t.Fatalf("local command = %q, want combined command+args", local.Command)
	}
	if local.Cwd != repo {
		t.Fatalf("local cwd = %q, want %q", local.Cwd, repo)
	}
}

func TestRuntimeMCPListParams_IncludesCursorAndDetail(t *testing.T) {
	cursor := "next-page"
	got := runtimeMCPListParams(20, &cursor)
	if got["detail"] != "toolsAndAuthOnly" {
		t.Fatalf("detail = %#v, want toolsAndAuthOnly", got["detail"])
	}
	if got["limit"] != 20 {
		t.Fatalf("limit = %#v, want 20", got["limit"])
	}
	if got["cursor"] != cursor {
		t.Fatalf("cursor = %#v, want %q", got["cursor"], cursor)
	}
}

func TestRuntimeMCPServers_ExtractsAuthAndToolNames(t *testing.T) {
	got := runtimeMCPServers(listMCPServerStatusResponse{
		Data: []mcpServerStatus{{
			Name:       "github",
			AuthStatus: "oAuth",
			Tools: map[string]json.RawMessage{
				"search_prs":  nil,
				"fetch_issue": nil,
			},
		}},
	})
	want := []core.MCPServerInfo{{
		Name:       "github",
		AuthStatus: "oAuth",
		Transport:  "runtime",
		Enabled:    true,
		Tools:      []string{"fetch_issue", "search_prs"},
	}}
	if fmt.Sprintf("%#v", got) != fmt.Sprintf("%#v", want) {
		t.Fatalf("runtimeMCPServers() = %#v, want %#v", got, want)
	}
}
