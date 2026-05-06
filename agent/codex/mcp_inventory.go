package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/chenhg5/cc-connect/core"
)

func (a *Agent) GetConfiguredMCPInventory(_ context.Context) (*core.MCPInventory, error) {
	a.mu.RLock()
	workDir := a.workDir
	codexHome := a.codexHome
	a.mu.RUnlock()
	return loadConfiguredMCPInventory(workDir, codexHome)
}

type listMCPServerStatusResponse struct {
	Data       []mcpServerStatus `json:"data"`
	NextCursor *string           `json:"nextCursor"`
}

type mcpServerStatus struct {
	Name       string                     `json:"name"`
	Tools      map[string]json.RawMessage `json:"tools"`
	AuthStatus string                     `json:"authStatus"`
}

func (s *appServerSession) GetRuntimeMCPInventory(ctx context.Context, limit int) (*core.MCPInventory, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 100
	}

	inventory := &core.MCPInventory{Source: "runtime"}
	var cursor *string
	for {
		params := runtimeMCPListParams(limit, cursor)
		var resp listMCPServerStatusResponse
		if err := s.request("mcpServerStatus/list", params, &resp); err != nil {
			return nil, fmt.Errorf("codex app-server mcpServerStatus/list: %w", err)
		}

		inventory.Servers = append(inventory.Servers, runtimeMCPServers(resp)...)

		if resp.NextCursor == nil || strings.TrimSpace(*resp.NextCursor) == "" {
			break
		}
		cursor = resp.NextCursor
	}

	sort.Slice(inventory.Servers, func(i, j int) bool {
		return inventory.Servers[i].Name < inventory.Servers[j].Name
	})
	return inventory, nil
}

func runtimeMCPListParams(limit int, cursor *string) map[string]any {
	params := map[string]any{
		"detail": "toolsAndAuthOnly",
		"limit":  limit,
	}
	if cursor != nil && strings.TrimSpace(*cursor) != "" {
		params["cursor"] = strings.TrimSpace(*cursor)
	}
	return params
}

func runtimeMCPServers(resp listMCPServerStatusResponse) []core.MCPServerInfo {
	servers := make([]core.MCPServerInfo, 0, len(resp.Data))
	for _, status := range resp.Data {
		server := core.MCPServerInfo{
			Name:       status.Name,
			AuthStatus: status.AuthStatus,
			Transport:  "runtime",
			Enabled:    true,
		}
		for name := range status.Tools {
			server.Tools = append(server.Tools, name)
		}
		sort.Strings(server.Tools)
		servers = append(servers, server)
	}
	return servers
}

func loadConfiguredMCPInventory(workDir, explicitCodexHome string) (*core.MCPInventory, error) {
	configPath := codexConfigPath(explicitCodexHome)
	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return &core.MCPInventory{Source: "configured"}, nil
		}
		return nil, fmt.Errorf("read Codex config: %w", err)
	}

	var raw map[string]any
	if _, err := toml.Decode(string(data), &raw); err != nil {
		return nil, fmt.Errorf("decode Codex config: %w", err)
	}

	merged := mergeMCPServerTables(
		extractNamedTable(raw, "mcp_servers"),
		selectProjectMCPServers(raw, workDir),
	)

	inventory := &core.MCPInventory{Source: "configured"}
	for name, table := range merged {
		server := core.MCPServerInfo{
			Name:       name,
			Transport:  configuredMCPTransport(table),
			Enabled:    configuredMCPEnabled(table),
			Required:   getBool(table["required"]),
			Command:    configuredMCPCommand(table),
			URL:        getString(table["url"]),
			Cwd:        getString(table["cwd"]),
			AuthStatus: "",
			Tools:      getStringSlice(table["enabled_tools"]),
		}
		sort.Strings(server.Tools)
		inventory.Servers = append(inventory.Servers, server)
	}

	sort.Slice(inventory.Servers, func(i, j int) bool {
		return inventory.Servers[i].Name < inventory.Servers[j].Name
	})
	return inventory, nil
}

func codexConfigPath(explicitCodexHome string) string {
	codexHome := strings.TrimSpace(explicitCodexHome)
	if codexHome == "" {
		codexHome = strings.TrimSpace(os.Getenv("CODEX_HOME"))
	}
	if codexHome == "" {
		home, err := os.UserHomeDir()
		if err == nil {
			codexHome = filepath.Join(home, ".codex")
		}
	}
	return filepath.Join(codexHome, "config.toml")
}

func selectProjectMCPServers(raw map[string]any, workDir string) map[string]map[string]any {
	projectsAny, ok := raw["projects"]
	if !ok {
		return nil
	}
	projects, ok := toStringAnyMap(projectsAny)
	if !ok {
		return nil
	}

	absWorkDir, err := filepath.Abs(workDir)
	if err != nil {
		absWorkDir = filepath.Clean(workDir)
	}

	bestKey := ""
	bestTable := map[string]map[string]any(nil)
	for key, projectAny := range projects {
		projectTable, ok := toStringAnyMap(projectAny)
		if !ok || !pathContains(key, absWorkDir) {
			continue
		}
		mcpServers := extractNamedTable(projectTable, "mcp_servers")
		if len(mcpServers) == 0 {
			continue
		}
		if len(key) > len(bestKey) {
			bestKey = key
			bestTable = mcpServers
		}
	}
	return bestTable
}

func pathContains(root, child string) bool {
	if root == "" || child == "" {
		return false
	}
	root = filepath.Clean(root)
	child = filepath.Clean(child)
	rel, err := filepath.Rel(root, child)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)))
}

func mergeMCPServerTables(tables ...map[string]map[string]any) map[string]map[string]any {
	out := map[string]map[string]any{}
	for _, table := range tables {
		for name, server := range table {
			existing := out[name]
			if existing == nil {
				existing = map[string]any{}
			}
			for key, value := range server {
				existing[key] = value
			}
			out[name] = existing
		}
	}
	return out
}

func extractNamedTable(raw map[string]any, key string) map[string]map[string]any {
	tableAny, ok := raw[key]
	if !ok {
		return nil
	}
	table, ok := toStringAnyMap(tableAny)
	if !ok {
		return nil
	}
	out := make(map[string]map[string]any, len(table))
	for name, entry := range table {
		entryTable, ok := toStringAnyMap(entry)
		if !ok {
			continue
		}
		out[name] = entryTable
	}
	return out
}

func toStringAnyMap(v any) (map[string]any, bool) {
	switch m := v.(type) {
	case map[string]any:
		return m, true
	default:
		return nil, false
	}
}

func configuredMCPTransport(server map[string]any) string {
	switch {
	case strings.TrimSpace(getString(server["url"])) != "":
		return "remote"
	case strings.TrimSpace(getString(server["command"])) != "":
		return "stdio"
	default:
		return "unknown"
	}
}

func configuredMCPEnabled(server map[string]any) bool {
	value, ok := server["enabled"]
	if !ok {
		return true
	}
	return getBool(value)
}

func configuredMCPCommand(server map[string]any) string {
	command := strings.TrimSpace(getString(server["command"]))
	args := getStringSlice(server["args"])
	if command == "" {
		return ""
	}
	if len(args) == 0 {
		return command
	}
	return command + " " + strings.Join(args, " ")
}

func getString(v any) string {
	switch s := v.(type) {
	case string:
		return strings.TrimSpace(s)
	default:
		return ""
	}
}

func getBool(v any) bool {
	switch b := v.(type) {
	case bool:
		return b
	default:
		return false
	}
}

func getStringSlice(v any) []string {
	switch xs := v.(type) {
	case []string:
		out := make([]string, 0, len(xs))
		for _, item := range xs {
			if s := strings.TrimSpace(item); s != "" {
				out = append(out, s)
			}
		}
		return out
	case []any:
		out := make([]string, 0, len(xs))
		for _, item := range xs {
			if s, ok := item.(string); ok {
				s = strings.TrimSpace(s)
				if s != "" {
					out = append(out, s)
				}
			}
		}
		return out
	default:
		return nil
	}
}
