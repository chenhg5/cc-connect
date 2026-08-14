package dsh

import (
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/chenhg5/cc-connect/core"
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

// readSettingsModels returns the advisory model catalog dsh knows about:
// the `llm-deepseek.models` section of settings.yaml when present, else nil.
func readSettingsModels() []core.ModelOption {
	s := readDSHSettings()
	if s == nil || len(s.LLMDeepSeek.Models) == 0 {
		return nil
	}
	models := make([]core.ModelOption, 0, len(s.LLMDeepSeek.Models))
	for _, m := range s.LLMDeepSeek.Models {
		if m.ID == "" {
			continue
		}
		option := core.ModelOption{Name: m.ID}
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

// listDSHSessions lists persisted dsh sessions for the given workdir.
// Session summaries are not decoded (dsh stores them as zstd-compressed
// JSONL), so the session id is shown as the summary.
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
		sessions = append(sessions, core.AgentSessionInfo{
			ID:         entry.Name(),
			Summary:    entry.Name(),
			ModifiedAt: info.ModTime(),
		})
	}
	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].ModifiedAt.After(sessions[j].ModifiedAt)
	})
	return sessions, nil
}
