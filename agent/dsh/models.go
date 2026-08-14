package dsh

import (
	"bufio"
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
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
