package dsh

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/chenhg5/cc-connect/core"
	"github.com/klauspost/compress/zstd"
	"gopkg.in/yaml.v3"
)

// dshHomeDir returns the DeepSeek Harness user-data root ($DSH_HOME or
// ~/.dsh), which hosts settings.yaml, .credentials.yaml and skills/.
func dshHomeDir() string {
	if d := os.Getenv("DSH_HOME"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".dsh")
}

// dshSettings models the relevant sections of dsh's settings.yaml.
type dshSettings struct {
	AgentDefaultModel struct {
		Provider        string `yaml:"provider"`
		Model           string `yaml:"model"`
		ReasoningEffort string `yaml:"reasoningEffort"`
	} `yaml:"agent-default-model"`
	LLMDeepSeek struct {
		Models []struct {
			ID   string `yaml:"id"`
			Name string `yaml:"name"`
		} `yaml:"models"`
	} `yaml:"llm-deepseek"`
	AgentPresets struct {
		Default string `yaml:"default"`
	} `yaml:"agent-presets"`
}

type dshModelCatalogEntry struct {
	Provider    string `json:"provider"`
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

type dshModelCatalogResponse struct {
	Type             string                 `json:"type"`
	Models           []dshModelCatalogEntry `json:"models"`
	ReasoningEfforts []string               `json:"reasoningEfforts"`
}

// readRuntimeModelCatalog asks the running dsh composition for the same model
// directory that Web uses. This deliberately avoids reimplementing pi-ai's
// built-in catalogs, Nix-injected model rows, and settings/profile merge rules
// in Go.
func (a *Agent) readRuntimeModelCatalog(ctx context.Context) []core.ModelOption {
	response := a.readRuntimeModelCatalogResponse(ctx)
	if response == nil || len(response.Models) == 0 {
		return nil
	}
	return modelOptionsFromCatalog(response.Models)
}

func (a *Agent) readRuntimeReasoningEfforts(ctx context.Context) []string {
	response := a.readRuntimeModelCatalogResponse(ctx)
	if response == nil {
		return nil
	}
	seen := make(map[string]struct{}, len(response.ReasoningEfforts))
	levels := make([]string, 0, len(response.ReasoningEfforts))
	for _, raw := range response.ReasoningEfforts {
		level := normalizeReasoningEffort(raw)
		if level == "" {
			continue
		}
		if _, exists := seen[level]; exists {
			continue
		}
		seen[level] = struct{}{}
		levels = append(levels, level)
	}
	return levels
}

func (a *Agent) readRuntimeModelCatalogResponse(ctx context.Context) *dshModelCatalogResponse {
	a.mu.Lock()
	cmdName := a.cmd
	extraArgs := append([]string(nil), a.cliExtraArgs...)
	configEnv := append([]string(nil), a.configEnv...)
	provider := a.provider
	model := a.model
	workDir := a.workDir
	a.mu.Unlock()
	if provider == "" {
		provider = readDefaultProvider()
	}
	if model == "" {
		model, _ = readDefaultModel()
	}

	args := append(extraArgs, "--profile", "headless")
	if provider != "" {
		args = append(args, "--provider", provider)
	}
	if model != "" {
		args = append(args, "--model", model)
	}
	args = append(args, "--list-models")
	cmd := exec.CommandContext(ctx, cmdName, args...)
	cmd.Dir = workDir
	cmd.Env = core.MergeEnv(os.Environ(), configEnv)
	output, err := cmd.Output()
	if err != nil {
		slog.Debug("dsh: runtime model catalog unavailable", "error", err)
		return nil
	}

	var response dshModelCatalogResponse
	for _, line := range bytes.Split(output, []byte{'\n'}) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var candidate dshModelCatalogResponse
		if json.Unmarshal(line, &candidate) == nil && candidate.Type == "models" {
			response = candidate
			break
		}
	}
	if len(response.Models) == 0 && len(response.ReasoningEfforts) == 0 {
		return nil
	}
	return &response
}

func modelOptionsFromCatalog(entries []dshModelCatalogEntry) []core.ModelOption {
	seen := make(map[string]struct{}, len(entries))
	aliasCounts := make(map[string]int)
	aliases := make([]string, len(entries))
	for i, entry := range entries {
		id := strings.TrimSpace(entry.ID)
		provider := strings.TrimSpace(entry.Provider)
		if id == "" || provider == "" {
			continue
		}
		alias := id
		if idx := strings.LastIndex(alias, "/"); idx >= 0 && idx+1 < len(alias) {
			alias = alias[idx+1:]
		}
		aliases[i] = alias
		aliasCounts[strings.ToLower(alias)]++
	}

	models := make([]core.ModelOption, 0, len(entries))
	for i, entry := range entries {
		id := strings.TrimSpace(entry.ID)
		provider := strings.TrimSpace(entry.Provider)
		if id == "" || provider == "" {
			continue
		}
		key := provider + "\x00" + id
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}

		desc := strings.TrimSpace(entry.Name)
		if desc == "" {
			desc = strings.TrimSpace(entry.Description)
		}
		if desc == "" {
			desc = id
		}
		desc += " [" + provider + "]"
		option := core.ModelOption{Name: id, Desc: desc, Provider: provider}
		if alias := aliases[i]; alias != "" && aliasCounts[strings.ToLower(alias)] == 1 && alias != id {
			option.Alias = alias
		}
		models = append(models, option)
	}
	return models
}

func appendDeepSeekFallback(models, configured []core.ModelOption) []core.ModelOption {
	for _, model := range models {
		if model.Provider == "deepseek-official" {
			return models
		}
	}
	if len(configured) == 0 {
		configured = []core.ModelOption{
			{Name: "deepseek-v4-flash", Desc: "DeepSeek-V4-Flash", Provider: "deepseek-official"},
			{Name: "deepseek-v4-pro", Desc: "DeepSeek-V4-Pro", Provider: "deepseek-official"},
		}
	}
	return append(models, configured...)
}

// dshPresetMetadata is the display-only part of a native dsh preset.
type dshPresetMetadata struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Order       int    `yaml:"order"`
}

// AvailablePresets discovers the same two roots used by dsh's native roster:
// the installed deployment presets first, then the user's ~/.dsh presets.
// The Go side only presents the roster to cc-connect; dsh remains the source
// of truth for parsing and mounting a selected composition.
func (a *Agent) AvailablePresets(_ context.Context) []core.PresetOption {
	defaultID := ""
	if settings := readDSHSettings(); settings != nil {
		defaultID = strings.TrimSpace(settings.AgentPresets.Default)
	}

	roots := []struct {
		path  string
		trust string
	}{
		{path: dshInstallPresetRoot(a.cmd), trust: "system"},
		{path: filepath.Join(dshHomeDir(), ".agent-presets"), trust: "user"},
	}

	seen := make(map[string]struct{})
	var presets []core.PresetOption
	for _, root := range roots {
		entries, err := os.ReadDir(root.path)
		if err != nil {
			continue
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
		for _, entry := range entries {
			id := entry.Name()
			if !entry.IsDir() || !validPresetID(id) {
				continue
			}
			if _, exists := seen[id]; exists {
				continue
			}
			seen[id] = struct{}{}

			option := core.PresetOption{ID: id, Trust: root.trust, Default: id == defaultID}
			composition := filepath.Join(root.path, id, "agent.cordis.yml")
			if info, statErr := os.Stat(composition); statErr != nil || !info.Mode().IsRegular() {
				option.Broken = "agent.cordis.yml is missing"
			} else if data, readErr := os.ReadFile(filepath.Join(root.path, id, "preset.yml")); readErr == nil {
				var metadata dshPresetMetadata
				if yamlErr := yaml.Unmarshal(data, &metadata); yamlErr == nil {
					option.Name = strings.TrimSpace(metadata.Name)
					option.Description = strings.TrimSpace(metadata.Description)
				}
			}
			presets = append(presets, option)
		}
	}
	return presets
}

func validPresetID(id string) bool {
	if id == "" {
		return false
	}
	first := id[0]
	if (first < 'a' || first > 'z') && (first < '0' || first > '9') {
		return false
	}
	for _, r := range id[1:] {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
			return false
		}
	}
	return true
}

func dshInstallPresetRoot(cmd string) string {
	resolved, err := exec.LookPath(cmd)
	if err != nil {
		return ""
	}
	resolved, err = filepath.EvalSymlinks(resolved)
	if err != nil {
		return ""
	}
	// Published dsh installs keep presets below apps/cli/config, while the
	// source-tree layout used by package-based installs keeps them beside the
	// agent-presets package. Follow the installation that is actually invoked.
	installRoot := filepath.Dir(filepath.Dir(resolved))
	for _, root := range []string{
		filepath.Join(installRoot, "apps", "cli", "config", "agent-presets"),
		filepath.Join(installRoot, "packages", "preset", "agent-presets", "presets"),
	} {
		if info, statErr := os.Stat(root); statErr == nil && info.IsDir() {
			return root
		}
	}
	return ""
}

// settingsPath returns $DSH_HOME/settings.yaml.
func settingsPath() string {
	home := dshHomeDir()
	if home == "" {
		return ""
	}
	return filepath.Join(home, "settings.yaml")
}

// readDSHSettings parses dsh's settings.yaml. Returns nil on any error
// (callers fall back to defaults).
func readDSHSettings() *dshSettings {
	path := settingsPath()
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		slog.Debug("dsh: read settings", "path", path, "error", err)
		return nil
	}
	var s dshSettings
	if err := yaml.Unmarshal(data, &s); err != nil {
		slog.Warn("dsh: parse settings", "path", path, "error", err)
		return nil
	}
	return &s
}

// readDefaultModel returns the model configured in dsh's
// agent-default-model settings (e.g. "deepseek-v4-flash").
func readDefaultModel() (string, error) {
	s := readDSHSettings()
	if s == nil || s.AgentDefaultModel.Model == "" {
		return "", nil
	}
	return s.AgentDefaultModel.Model, nil
}

func readDefaultProvider() string {
	s := readDSHSettings()
	if s == nil {
		return ""
	}
	return strings.TrimSpace(s.AgentDefaultModel.Provider)
}

func readDefaultReasoningEffort() string {
	s := readDSHSettings()
	if s == nil {
		return ""
	}
	return normalizeReasoningEffort(s.AgentDefaultModel.ReasoningEffort)
}

// readSettingsModels returns the advisory model catalog dsh knows about:
// the `llm-deepseek.models` section of settings.yaml when present, else nil.
func readSettingsModels(provider string) []core.ModelOption {
	s := readDSHSettings()
	if s == nil || len(s.LLMDeepSeek.Models) == 0 {
		return nil
	}
	models := make([]core.ModelOption, 0, len(s.LLMDeepSeek.Models))
	for _, m := range s.LLMDeepSeek.Models {
		if m.ID == "" {
			continue
		}
		option := core.ModelOption{Name: m.ID, Provider: provider}
		if m.Name != "" {
			option.Desc = m.Name
		}
		// Derive a short alias from the last segment after the final "/".
		if idx := strings.LastIndex(m.ID, "/"); idx >= 0 && idx+1 < len(m.ID) {
			option.Alias = m.ID[idx+1:]
		}
		models = append(models, option)
	}
	sort.Slice(models, func(i, j int) bool { return models[i].Name < models[j].Name })
	return models
}

// ── Session listing ──────────────────────────────────────────

// dshSessionsRoot returns $DSH_HOME/sessions/<encoded-abs-workdir>.
// dsh encodes the absolute workdir by replacing "/" with "-" and wrapping
// it in "--" (e.g. /home/user/project → --home-user-project--).
func dshSessionsRoot(workDir string) string {
	home := dshHomeDir()
	if home == "" {
		return ""
	}
	absDir, err := filepath.Abs(workDir)
	if err != nil {
		return ""
	}
	encoded := "--" + strings.ReplaceAll(strings.TrimPrefix(absDir, "/"), "/", "-") + "--"
	return filepath.Join(home, "sessions", encoded)
}

// listDSHSessions lists persisted dsh sessions for the given workdir with the
// same titles dsh web shows: the last `session/title` event in the session
// log (fallback: the first user message). dsh stores each session as a
// zstd-compressed JSONL log under $DSH_HOME/sessions/<project>/<id>/.
func listDSHSessions(workDir string) ([]core.AgentSessionInfo, error) {
	root := dshSessionsRoot(workDir)
	if root == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var sessions []core.AgentSessionInfo
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		title, msgCount := readDSHSessionSummary(filepath.Join(root, entry.Name()))
		sessions = append(sessions, core.AgentSessionInfo{
			ID:           entry.Name(),
			Summary:      title,
			MessageCount: msgCount,
			ModifiedAt:   info.ModTime(),
		})
	}
	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].ModifiedAt.After(sessions[j].ModifiedAt)
	})
	return sessions, nil
}

// readDSHSessionSummary opens a dsh session directory's log file
// (session.jsonl.zstd or the uncompressed session.jsonl), decodes it, and
// returns the session title (last `session/title` event, falling back to the
// first user message) plus the number of assistant messages (≈ turns).
func readDSHSessionSummary(sessionDir string) (title string, msgCount int) {
	zpath := filepath.Join(sessionDir, "session.jsonl.zstd")
	plain := filepath.Join(sessionDir, "session.jsonl")

	var data []byte
	var err error
	if _, statErr := os.Stat(zpath); statErr == nil {
		data, err = os.ReadFile(zpath)
		if err == nil {
			dec, decErr := zstd.NewReader(nil, zstd.WithDecoderMaxMemory(64<<20))
			if decErr == nil {
				defer dec.Close()
				if out, dErr := dec.DecodeAll(data, nil); dErr == nil {
					data = out
				} else {
					slog.Debug("dsh: zstd decode session", "path", zpath, "error", dErr)
					return "", 0
				}
			}
		}
	} else if _, statErr := os.Stat(plain); statErr == nil {
		data, err = os.ReadFile(plain)
	}
	if err != nil || len(data) == 0 {
		return "", 0
	}

	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	var lastTitle string
	var firstUser string
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var evt struct {
			Type string `json:"type"`
			Data struct {
				Title string `json:"title"`
			} `json:"data"`
		}
		if err := json.Unmarshal(line, &evt); err != nil {
			continue
		}
		switch evt.Type {
		case "session/title":
			if t := strings.TrimSpace(evt.Data.Title); t != "" {
				lastTitle = t
			}
		case "assistant/message":
			msgCount++
		case "user/message":
			if firstUser == "" {
				firstUser = firstUserMessageText(line)
			}
		}
	}
	if lastTitle != "" {
		return truncStr(lastTitle, 80), msgCount
	}
	if firstUser != "" {
		return truncStr(firstUser, 80), msgCount
	}
	return "", msgCount
}

// firstUserMessageText extracts the concatenated text blocks of a
// user/message event line.
func firstUserMessageText(line []byte) string {
	var evt struct {
		Data struct {
			Message struct {
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"message"`
		} `json:"data"`
	}
	if err := json.Unmarshal(line, &evt); err != nil {
		return ""
	}
	var sb strings.Builder
	for _, block := range evt.Data.Message.Content {
		if block.Type == "text" && block.Text != "" {
			if sb.Len() > 0 {
				sb.WriteString(" ")
			}
			sb.WriteString(block.Text)
		}
	}
	return strings.TrimSpace(sb.String())
}
