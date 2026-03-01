package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/chenhg5/cc-connect/config"
)

func runProviderCommand(args []string) {
	if len(args) == 0 {
		printProviderUsage()
		os.Exit(1)
	}

	switch args[0] {
	case "add":
		runProviderAdd(args[1:])
	case "list":
		runProviderList(args[1:])
	case "remove":
		runProviderRemove(args[1:])
	case "import":
		runProviderImport(args[1:])
	case "help", "--help", "-h":
		printProviderUsage()
	default:
		fmt.Fprintf(os.Stderr, "Unknown provider subcommand: %s\n\n", args[0])
		printProviderUsage()
		os.Exit(1)
	}
}

func printProviderUsage() {
	fmt.Println(`Usage: cc-connect provider <command> [options]

Commands:
  add      Add a new API provider to a project
  list     List providers for a project
  remove   Remove a provider from a project
  import   Import providers from cc-switch

Examples:
  cc-connect provider add --project my-backend --name relay --api-key sk-xxx
  cc-connect provider add --project my-backend --name bedrock --env CLAUDE_CODE_USE_BEDROCK=1,AWS_PROFILE=bedrock
  cc-connect provider list --project my-backend
  cc-connect provider remove --project my-backend --name relay
  cc-connect provider import --project my-backend`)
}

// initConfigPath resolves the config path and sets config.ConfigPath.
func initConfigPath(flagValue string) {
	config.ConfigPath = resolveConfigPath(flagValue)
}

func runProviderAdd(args []string) {
	fs := flag.NewFlagSet("provider add", flag.ExitOnError)
	configFile := fs.String("config", "", "path to config file")
	project := fs.String("project", "", "project name (required)")
	name := fs.String("name", "", "provider name (required)")
	apiKey := fs.String("api-key", "", "API key")
	baseURL := fs.String("base-url", "", "API base URL (optional)")
	model := fs.String("model", "", "model name override (optional)")
	envStr := fs.String("env", "", "extra env vars as KEY=VAL,KEY2=VAL2 (optional)")
	fs.Parse(args)

	if *project == "" || *name == "" {
		fmt.Fprintln(os.Stderr, "Error: --project and --name are required")
		fs.Usage()
		os.Exit(1)
	}

	initConfigPath(*configFile)

	p := config.ProviderConfig{
		Name:    *name,
		APIKey:  *apiKey,
		BaseURL: *baseURL,
		Model:   *model,
	}
	if *envStr != "" {
		p.Env = parseEnvStr(*envStr)
	}

	if err := config.AddProviderToConfig(*project, p); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("✅ Provider %q added to project %q\n", *name, *project)
	if *baseURL != "" {
		fmt.Printf("   Base URL: %s\n", *baseURL)
	}
	if *model != "" {
		fmt.Printf("   Model: %s\n", *model)
	}
	if len(p.Env) > 0 {
		fmt.Printf("   Extra env: %v\n", p.Env)
	}
	fmt.Printf("\nTo activate: use /provider switch %s in chat.\n", *name)
}

func runProviderList(args []string) {
	fs := flag.NewFlagSet("provider list", flag.ExitOnError)
	configFile := fs.String("config", "", "path to config file")
	project := fs.String("project", "", "project name (lists all projects if empty)")
	fs.Parse(args)

	initConfigPath(*configFile)

	if *project != "" {
		listProjectProviders(*project)
		return
	}

	projects, err := config.ListProjects()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	for _, p := range projects {
		fmt.Printf("── %s ──\n", p)
		listProjectProviders(p)
		fmt.Println()
	}
}

func listProjectProviders(projectName string) {
	providers, active, err := config.GetProjectProviders(projectName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return
	}

	if len(providers) == 0 {
		fmt.Println("  (no providers)")
		return
	}

	for _, p := range providers {
		marker := "  "
		if p.Name == active {
			marker = "▶ "
		}
		info := p.Name
		if p.BaseURL != "" {
			info += fmt.Sprintf(" (base_url: %s)", p.BaseURL)
		}
		if p.Model != "" {
			info += fmt.Sprintf(" [model: %s]", p.Model)
		}
		apiKeyHint := "(not set)"
		if p.APIKey != "" {
			if len(p.APIKey) > 8 {
				apiKeyHint = p.APIKey[:4] + "..." + p.APIKey[len(p.APIKey)-4:]
			} else {
				apiKeyHint = "****"
			}
		}
		fmt.Printf("%s%s  api_key: %s\n", marker, info, apiKeyHint)
	}
}

func runProviderRemove(args []string) {
	fs := flag.NewFlagSet("provider remove", flag.ExitOnError)
	configFile := fs.String("config", "", "path to config file")
	project := fs.String("project", "", "project name (required)")
	name := fs.String("name", "", "provider name (required)")
	fs.Parse(args)

	if *project == "" || *name == "" {
		fmt.Fprintln(os.Stderr, "Error: --project and --name are required")
		fs.Usage()
		os.Exit(1)
	}

	initConfigPath(*configFile)

	if err := config.RemoveProviderFromConfig(*project, *name); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("✅ Provider %q removed from project %q\n", *name, *project)
}

// ── Import from cc-switch ──────────────────────────────────────

func runProviderImport(args []string) {
	fs := flag.NewFlagSet("provider import", flag.ExitOnError)
	configFile := fs.String("config", "", "path to config file")
	project := fs.String("project", "", "target project name (auto-detect if only one)")
	dbPath := fs.String("db-path", "", "path to cc-switch database (auto-detect)")
	appType := fs.String("type", "", "filter by agent type: claude or codex (imports all if empty)")
	fs.Parse(args)

	initConfigPath(*configFile)

	// Resolve cc-switch DB path
	db := *dbPath
	if db == "" {
		db = findCCSwitchDB()
		if db == "" {
			fmt.Fprintln(os.Stderr, "Error: cc-switch database not found. Searched:")
			for _, p := range ccSwitchDBCandidates() {
				fmt.Fprintf(os.Stderr, "  - %s\n", p)
			}
			fmt.Fprintln(os.Stderr, "\nSpecify manually with --db-path")
			os.Exit(1)
		}
	}
	if _, err := os.Stat(db); err != nil {
		fmt.Fprintf(os.Stderr, "Error: database not found at %s\n", db)
		os.Exit(1)
	}

	// Check sqlite3 is available
	if _, err := exec.LookPath("sqlite3"); err != nil {
		fmt.Fprintln(os.Stderr, "Error: 'sqlite3' CLI not found in PATH")
		fmt.Fprintln(os.Stderr, "Install it: apt install sqlite3 (Debian/Ubuntu) or brew install sqlite3 (macOS)")
		os.Exit(1)
	}

	// Resolve target project
	targetProject := *project
	if targetProject == "" {
		projects, err := config.ListProjects()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error reading config: %v\n", err)
			os.Exit(1)
		}
		if len(projects) == 1 {
			targetProject = projects[0]
		} else if len(projects) == 0 {
			fmt.Fprintln(os.Stderr, "Error: no projects in config file")
			os.Exit(1)
		} else {
			fmt.Fprintln(os.Stderr, "Error: multiple projects found, specify one with --project:")
			for _, p := range projects {
				fmt.Fprintf(os.Stderr, "  - %s\n", p)
			}
			os.Exit(1)
		}
	}

	fmt.Printf("Importing from: %s\n", db)
	fmt.Printf("Target project: %s\n\n", targetProject)

	// Query cc-switch database
	query := "SELECT id, app_type, name, settings_config, is_current FROM providers"
	if *appType != "" {
		query += fmt.Sprintf(" WHERE app_type = '%s'", *appType)
	}
	cmd := exec.Command("sqlite3", db, "-json", query)
	output, err := cmd.Output()
	if err != nil {
		stderr := ""
		if exitErr, ok := err.(*exec.ExitError); ok {
			stderr = string(exitErr.Stderr)
		}
		fmt.Fprintf(os.Stderr, "Error querying database: %v\n%s\n", err, stderr)
		os.Exit(1)
	}

	var rows []ccSwitchRow
	if err := json.Unmarshal(output, &rows); err != nil {
		fmt.Fprintf(os.Stderr, "Error parsing database output: %v\n", err)
		os.Exit(1)
	}

	if len(rows) == 0 {
		fmt.Println("No providers found in cc-switch database.")
		return
	}

	imported := 0
	skipped := 0
	for _, row := range rows {
		provider, err := convertCCSwitchProvider(row)
		if err != nil {
			fmt.Printf("  ⚠ Skip %q (%s): %v\n", row.Name, row.AppType, err)
			skipped++
			continue
		}

		if err := config.AddProviderToConfig(targetProject, provider); err != nil {
			if strings.Contains(err.Error(), "already exists") {
				fmt.Printf("  ⏭ Skip %q: already exists\n", provider.Name)
				skipped++
			} else {
				fmt.Fprintf(os.Stderr, "  ✗ Failed to add %q: %v\n", provider.Name, err)
				skipped++
			}
			continue
		}

		activeTag := ""
		if row.IsCurrent == 1 {
			activeTag = " (was active in cc-switch)"
		}
		fmt.Printf("  ✅ %s [%s] → %s%s\n", row.Name, row.AppType, provider.Name, activeTag)
		imported++
	}

	fmt.Printf("\nDone: %d imported, %d skipped\n", imported, skipped)
	if imported > 0 {
		fmt.Println("\nActivate a provider with: /provider switch <name> in chat")
	}
}

type ccSwitchRow struct {
	ID             string `json:"id"`
	AppType        string `json:"app_type"`
	Name           string `json:"name"`
	SettingsConfig string `json:"settings_config"`
	IsCurrent      int    `json:"is_current"`
}

func convertCCSwitchProvider(row ccSwitchRow) (config.ProviderConfig, error) {
	var sc map[string]any
	if err := json.Unmarshal([]byte(row.SettingsConfig), &sc); err != nil {
		return config.ProviderConfig{}, fmt.Errorf("invalid settings_config JSON: %w", err)
	}

	p := config.ProviderConfig{
		Name: strings.ToLower(strings.ReplaceAll(strings.TrimSpace(row.Name), " ", "-")),
	}

	switch row.AppType {
	case "claude":
		return convertClaudeProvider(p, sc)
	case "codex":
		return convertCodexProvider(p, sc)
	default:
		return config.ProviderConfig{}, fmt.Errorf("unsupported app_type %q (only claude and codex are supported)", row.AppType)
	}
}

func convertClaudeProvider(p config.ProviderConfig, sc map[string]any) (config.ProviderConfig, error) {
	env, _ := sc["env"].(map[string]any)
	if env == nil {
		return p, fmt.Errorf("no env in settings_config")
	}

	if key, ok := env["ANTHROPIC_AUTH_TOKEN"].(string); ok && key != "" {
		p.APIKey = key
	}
	if url, ok := env["ANTHROPIC_BASE_URL"].(string); ok && url != "" {
		p.BaseURL = url
	}
	if model, ok := env["ANTHROPIC_MODEL"].(string); ok && model != "" {
		p.Model = model
	}

	// Carry over any extra env vars (e.g. ANTHROPIC_DEFAULT_HAIKU_MODEL)
	extra := make(map[string]string)
	known := map[string]bool{"ANTHROPIC_AUTH_TOKEN": true, "ANTHROPIC_BASE_URL": true, "ANTHROPIC_MODEL": true}
	for k, v := range env {
		if !known[k] {
			if s, ok := v.(string); ok && s != "" {
				extra[k] = s
			}
		}
	}
	if len(extra) > 0 {
		p.Env = extra
	}

	if p.APIKey == "" && len(p.Env) == 0 {
		return p, fmt.Errorf("no API key or env found")
	}
	return p, nil
}

func convertCodexProvider(p config.ProviderConfig, sc map[string]any) (config.ProviderConfig, error) {
	// API key from auth.OPENAI_API_KEY
	if auth, ok := sc["auth"].(map[string]any); ok {
		if key, ok := auth["OPENAI_API_KEY"].(string); ok && key != "" {
			p.APIKey = key
		}
	}

	// base_url and model from config TOML string
	if cfgStr, ok := sc["config"].(string); ok && cfgStr != "" {
		p.BaseURL, p.Model = parseCodexConfigTOML(cfgStr)
	}

	if p.APIKey == "" {
		return p, fmt.Errorf("no OPENAI_API_KEY found")
	}
	return p, nil
}

// parseCodexConfigTOML extracts base_url and model from a Codex config.toml string.
// It handles both flat `base_url = "..."` and upstream-style `[model_providers.X]` sections.
func parseCodexConfigTOML(cfgStr string) (baseURL, model string) {
	for _, line := range strings.Split(cfgStr, "\n") {
		line = strings.TrimSpace(line)
		if k, v, ok := parseTOMLKV(line); ok {
			switch k {
			case "base_url":
				if baseURL == "" {
					baseURL = v
				}
			case "model":
				if model == "" {
					model = v
				}
			}
		}
	}
	return
}

func parseTOMLKV(line string) (key, value string, ok bool) {
	idx := strings.Index(line, "=")
	if idx < 0 || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "[") {
		return "", "", false
	}
	key = strings.TrimSpace(line[:idx])
	value = strings.TrimSpace(line[idx+1:])
	value = strings.Trim(value, "\"'")
	return key, value, true
}

func findCCSwitchDB() string {
	for _, p := range ccSwitchDBCandidates() {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

func ccSwitchDBCandidates() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}

	candidates := []string{
		filepath.Join(home, ".cc-switch", "cc-switch.db"),
	}

	switch runtime.GOOS {
	case "linux":
		dataHome := os.Getenv("XDG_DATA_HOME")
		if dataHome == "" {
			dataHome = filepath.Join(home, ".local", "share")
		}
		candidates = append(candidates, filepath.Join(dataHome, "cc-switch", "cc-switch.db"))
	case "darwin":
		candidates = append(candidates, filepath.Join(home, "Library", "Application Support", "cc-switch", "cc-switch.db"))
	}

	return candidates
}

func parseEnvStr(s string) map[string]string {
	env := make(map[string]string)
	for _, pair := range strings.Split(s, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		if idx := strings.IndexByte(pair, '='); idx > 0 {
			env[pair[:idx]] = pair[idx+1:]
		}
	}
	return env
}
