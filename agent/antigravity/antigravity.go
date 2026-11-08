package antigravity

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	_ "modernc.org/sqlite"

	"github.com/chenhg5/cc-connect/core"
)

func init() {
	core.RegisterAgent("antigravity", New)
}

// Agent drives the Antigravity CLI (agy) in headless mode.
//
// Modes (maps to agy approval and sandbox flags):
//   - "default":   ask for each tool through the cc-connect permission bridge
//   - "yolo":      auto-approve all tools (--dangerously-skip-permissions)
//   - "plan":      read-only plan mode with terminal sandbox constraints (--sandbox)
type Agent struct {
	workDir      string
	model        string
	mode         string
	cmd          string   // CLI binary name, default "agy"
	cliExtraArgs []string // extra args from cmd after the binary name
	configEnv    []string // env vars from [projects.agent.options.env]
	timeout      time.Duration
	providers    []core.ProviderConfig
	activeIdx    int
	sessionEnv   []string
	mu           sync.RWMutex
}

func New(opts map[string]any) (core.Agent, error) {
	workDir, _ := opts["work_dir"].(string)
	if workDir == "" {
		workDir = "."
	}
	model, _ := opts["model"].(string)
	mode, _ := opts["mode"].(string)
	mode = normalizeMode(mode)
	cmd, extraArgs := core.ParseCmdOpts(opts, "agy")

	var timeoutMins int64
	switch v := opts["timeout_mins"].(type) {
	case int64:
		timeoutMins = v
	case int:
		timeoutMins = int64(v)
	case float64:
		timeoutMins = int64(v)
	default:
		if v != nil {
			slog.Debug("antigravity: timeout_mins has unexpected type", "type", fmt.Sprintf("%T", v))
		}
	}
	var timeout time.Duration
	if timeoutMins > 0 {
		timeout = time.Duration(timeoutMins) * time.Minute
	}

	if _, err := exec.LookPath(cmd); err != nil {
		return nil, fmt.Errorf("antigravity: %q CLI not found in PATH, install from: https://antigravity.google/docs/cli-overview", cmd)
	}

	return &Agent{
		workDir:      workDir,
		model:        model,
		mode:         mode,
		cmd:          cmd,
		cliExtraArgs: extraArgs,
		configEnv:    core.ParseConfigEnv(opts),
		timeout:      timeout,
		activeIdx:    -1,
	}, nil
}

func normalizeMode(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "yolo", "auto", "force", "bypasspermissions":
		return "yolo"
	case "plan", "sandbox":
		return "plan"
	default:
		return "default"
	}
}

func (a *Agent) Name() string { return "antigravity" }

func (a *Agent) SetWorkDir(dir string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.workDir = dir
	slog.Info("antigravity: work_dir changed", "work_dir", dir)
}

func (a *Agent) GetWorkDir() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.workDir
}

func (a *Agent) SetModel(model string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.model = model
	slog.Info("antigravity: model changed", "model", model)
}

func (a *Agent) GetModel() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return core.GetProviderModel(a.providers, a.activeIdx, a.model)
}

func (a *Agent) configuredModels() []core.ModelOption {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return core.GetProviderModels(a.providers, a.activeIdx)
}

func (a *Agent) AvailableModels(ctx context.Context) []core.ModelOption {
	if models := a.configuredModels(); len(models) > 0 {
		return models
	}
	if models := a.fetchModelsFromAPI(ctx); len(models) > 0 {
		return models
	}
	return []core.ModelOption{
		{Name: "gemini-3.1-pro-preview", Desc: "Gemini 3.1 Pro Preview"},
		{Name: "gemini-3-flash-preview", Desc: "Gemini 3 Flash Preview"},
		{Name: "gemini-2.5-pro", Desc: "Gemini 2.5 Pro"},
		{Name: "gemini-2.5-flash", Desc: "Gemini 2.5 Flash"},
	}
}

func (a *Agent) fetchModelsFromAPI(ctx context.Context) []core.ModelOption {
	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		apiKey = os.Getenv("GOOGLE_API_KEY")
	}
	if apiKey == "" {
		return nil
	}

	url := "https://generativelanguage.googleapis.com/v1beta/models?key=" + apiKey
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		slog.Debug("antigravity: failed to fetch models", "error", err)
		return nil
	}
	defer func() {
		_ = resp.Body.Close()
	}()
	if resp.StatusCode != http.StatusOK {
		return nil
	}

	var result struct {
		Models []struct {
			Name        string `json:"name"`
			DisplayName string `json:"displayName"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil
	}

	var models []core.ModelOption
	for _, m := range result.Models {
		id := strings.TrimPrefix(m.Name, "models/")
		if !strings.HasPrefix(id, "gemini-") {
			continue
		}
		models = append(models, core.ModelOption{Name: id, Desc: m.DisplayName})
	}
	sort.Slice(models, func(i, j int) bool { return models[i].Name > models[j].Name })
	return models
}

func (a *Agent) SetSessionEnv(env []string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sessionEnv = env
}

func (a *Agent) StartSession(ctx context.Context, sessionID string) (core.AgentSession, error) {
	a.mu.Lock()
	model := a.model
	mode := a.mode
	cmd := a.cmd
	extraArgs := append([]string{}, a.cliExtraArgs...)
	workDir := a.workDir
	timeout := a.timeout
	extraEnv := append([]string(nil), a.configEnv...)
	extraEnv = append(extraEnv, a.providerEnvLocked()...)
	extraEnv = append(extraEnv, a.sessionEnv...)
	if a.activeIdx >= 0 && a.activeIdx < len(a.providers) {
		if m := a.providers[a.activeIdx].Model; m != "" {
			model = m
		}
	}
	a.mu.Unlock()

	return newAntigravitySession(ctx, cmd, extraArgs, workDir, model, mode, sessionID, extraEnv, timeout)
}

func (a *Agent) ListSessions(_ context.Context) ([]core.AgentSessionInfo, error) {
	return listAntigravitySessions(a.GetWorkDir())
}

func (a *Agent) DeleteSession(_ context.Context, sessionID string) error {
	if !isValidAntigravitySessionID(sessionID) {
		return fmt.Errorf("antigravity: invalid session ID: %q", sessionID)
	}
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("antigravity: cannot determine home dir: %w", err)
	}

	workDir := a.GetWorkDir()
	deleted := false
	cliRoot := filepath.Join(homeDir, ".gemini", "antigravity-cli")

	if allowed, err := canDeleteAntigravityCLISession(cliRoot, workDir, sessionID); err != nil {
		return err
	} else if allowed {
		brainDir := filepath.Join(cliRoot, "brain")
		brainTarget := filepath.Join(brainDir, sessionID)
		if !isPathWithinDir(brainDir, brainTarget) {
			return fmt.Errorf("antigravity: session path escapes brain directory: %q", sessionID)
		}
		if _, err := os.Lstat(brainTarget); err == nil {
			if err := os.RemoveAll(brainTarget); err != nil {
				return fmt.Errorf("antigravity: remove brain dir: %w", err)
			}
			deleted = true
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("antigravity: stat brain dir: %w", err)
		}

		convsDir := filepath.Join(cliRoot, "conversations")
		if entries, err := os.ReadDir(convsDir); err == nil {
			for _, entry := range entries {
				if !matchesAntigravityConversationFile(entry.Name(), sessionID) {
					continue
				}
				target := filepath.Join(convsDir, entry.Name())
				if !isPathWithinDir(convsDir, target) {
					return fmt.Errorf("antigravity: conversation file escapes directory: %q", entry.Name())
				}
				if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
					return fmt.Errorf("antigravity: remove conversation file %s: %w", entry.Name(), err)
				}
				deleted = true
			}
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("antigravity: read conversations dir: %w", err)
		}

		removedMeta, err := removeSessionFromAntigravityCaches(cliRoot, sessionID)
		if err != nil {
			return err
		}
		if removedMeta {
			deleted = true
		}
	}

	chatsDir := filepath.Join(homeDir, ".gemini", "tmp", antigravityProjectSlug(workDir), "chats")
	if entries, err := os.ReadDir(chatsDir); err == nil {
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
				continue
			}
			fpath := filepath.Join(chatsDir, entry.Name())
			if !isPathWithinDir(chatsDir, fpath) {
				continue
			}
			file, err := os.Open(fpath)
			if err != nil {
				continue
			}

			matched := false
			scanner := bufio.NewScanner(file)
			scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
			if scanner.Scan() {
				var sf struct {
					SessionID string `json:"sessionId"`
				}
				if json.Unmarshal([]byte(scanner.Text()), &sf) == nil && sf.SessionID == sessionID {
					matched = true
				}
			}
			scanErr := scanner.Err()
			_ = file.Close()
			if scanErr != nil || !matched {
				continue
			}
			if err := os.Remove(fpath); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("antigravity: remove legacy chat file: %w", err)
			}
			deleted = true
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("antigravity: read legacy chats dir: %w", err)
	}

	if !deleted {
		return fmt.Errorf("session file not found: %s", sessionID)
	}
	return nil
}

func (a *Agent) Stop() error { return nil }

func (a *Agent) SetMode(mode string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.mode = normalizeMode(mode)
	slog.Info("antigravity: mode changed", "mode", a.mode)
}

func (a *Agent) GetMode() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.mode
}

func (a *Agent) PermissionModes() []core.PermissionModeInfo {
	return []core.PermissionModeInfo{
		{Key: "default", Name: "Default", NameZh: "默认", Desc: "Ask for approval through cc-connect on each tool use", DescZh: "每次工具调用都通过 cc-connect 请求确认"},
		{Key: "yolo", Name: "YOLO", NameZh: "全自动", Desc: "Auto-approve all tool calls", DescZh: "自动批准所有工具调用"},
		{Key: "plan", Name: "Plan", NameZh: "规划模式", Desc: "Read-only plan mode in sandbox", DescZh: "只读沙箱规划模式"},
	}
}

func (a *Agent) CommandDirs() []string {
	workDir := a.GetWorkDir()
	absDir, err := filepath.Abs(workDir)
	if err != nil {
		absDir = workDir
	}
	dirs := []string{filepath.Join(absDir, ".gemini", "commands")}
	if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(home, ".gemini", "commands"))
	}
	return dirs
}

func (a *Agent) SkillDirs() []string {
	workDir := a.GetWorkDir()
	absDir, err := filepath.Abs(workDir)
	if err != nil {
		absDir = workDir
	}
	dirs := []string{filepath.Join(absDir, ".gemini", "skills")}
	if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(home, ".gemini", "skills"))
	}
	return dirs
}

func (a *Agent) CompressCommand() string { return "" }

func (a *Agent) ProjectMemoryFile() string {
	workDir := a.GetWorkDir()
	absDir, err := filepath.Abs(workDir)
	if err != nil {
		absDir = workDir
	}
	return filepath.Join(absDir, "GEMINI.md")
}

func (a *Agent) GlobalMemoryFile() string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(homeDir, ".gemini", "GEMINI.md")
}

func (a *Agent) SetProviders(providers []core.ProviderConfig) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.providers = providers
}

func (a *Agent) SetActiveProvider(name string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if name == "" {
		a.activeIdx = -1
		slog.Info("antigravity: provider cleared")
		return true
	}
	for i, p := range a.providers {
		if p.Name == name {
			a.activeIdx = i
			slog.Info("antigravity: provider switched", "provider", name)
			return true
		}
	}
	return false
}

func (a *Agent) GetActiveProvider() *core.ProviderConfig {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.activeIdx < 0 || a.activeIdx >= len(a.providers) {
		return nil
	}
	p := a.providers[a.activeIdx]
	return &p
}

func (a *Agent) ListProviders() []core.ProviderConfig {
	a.mu.Lock()
	defer a.mu.Unlock()
	result := make([]core.ProviderConfig, len(a.providers))
	copy(result, a.providers)
	return result
}

func (a *Agent) providerEnvLocked() []string {
	if a.activeIdx < 0 || a.activeIdx >= len(a.providers) {
		return nil
	}
	p := a.providers[a.activeIdx]
	var env []string
	if p.APIKey != "" {
		env = append(env, "GEMINI_API_KEY="+p.APIKey)
	}
	for k, v := range p.Env {
		env = append(env, k+"="+v)
	}
	return env
}

func antigravityProjectSlug(workDir string) string {
	abs, err := filepath.Abs(workDir)
	if err != nil {
		abs = workDir
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return slugify(filepath.Base(abs))
	}

	data, err := os.ReadFile(filepath.Join(homeDir, ".gemini", "projects.json"))
	if err == nil {
		var registry struct {
			Projects map[string]string `json:"projects"`
		}
		if json.Unmarshal(data, &registry) == nil {
			normalized := filepath.Clean(abs)
			if slug, ok := registry.Projects[normalized]; ok {
				return slug
			}
		}
	}

	return slugify(filepath.Base(abs))
}

func slugify(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	result := b.String()
	for strings.Contains(result, "--") {
		result = strings.ReplaceAll(result, "--", "-")
	}
	result = strings.Trim(result, "-")
	if result == "" {
		result = "project"
	}
	return result
}

type sessionFile struct {
	SessionID   string    `json:"sessionId"`
	ProjectHash string    `json:"projectHash"`
	StartTime   time.Time `json:"startTime"`
	LastUpdated time.Time `json:"lastUpdated"`
	Kind        string    `json:"kind"`
}

type sessionMessagePart struct {
	Text string `json:"text"`
}

type sessionLine struct {
	Type    string               `json:"type"` // "user", "assistant"
	Content []sessionMessagePart `json:"content"`
	Set     *struct {
		LastUpdated time.Time `json:"lastUpdated"`
	} `json:"$set"`
}

type agyConversationMetadataFile struct {
	Conversations map[string]agyConversationMetadataEntry `json:"conversations"`
}

type agyConversationMetadataEntry struct {
	Summary          agyConversationSummary `json:"summary"`
	IsInternal       bool                   `json:"is_internal"`
	LastModifiedTime string                 `json:"last_modified_time"`
}

type agyConversationSummary struct {
	ID            string   `json:"ID"`
	Title         string   `json:"Title"`
	Preview       string   `json:"Preview"`
	NumSteps      int      `json:"NumSteps"`
	CreatedAt     string   `json:"CreatedAt"`
	UpdatedAt     string   `json:"UpdatedAt"`
	WorkspaceURIs []string `json:"WorkspaceURIs"`
	Internal      bool     `json:"Internal"`
}

type agyHistoryEntry struct {
	Display        string `json:"display"`
	Timestamp      int64  `json:"timestamp"`
	Workspace      string `json:"workspace"`
	ConversationID string `json:"conversationId"`
	Type           string `json:"type"`
}

func isValidAntigravitySessionID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' {
			continue
		}
		return false
	}
	return true
}

func isPathWithinDir(parentDir, targetPath string) bool {
	parentAbs, err := filepath.Abs(parentDir)
	if err != nil {
		parentAbs = filepath.Clean(parentDir)
	}
	targetAbs, err := filepath.Abs(targetPath)
	if err != nil {
		targetAbs = filepath.Clean(targetPath)
	}
	rel, err := filepath.Rel(parentAbs, targetAbs)
	if err != nil {
		return false
	}
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return false
	}
	return true
}

func matchesAntigravityConversationFile(fileName, sessionID string) bool {
	if !isValidAntigravitySessionID(sessionID) {
		return false
	}
	for _, ext := range []string{".db", ".db-wal", ".db-shm", ".db-journal", ".pb", ".json", ".jsonl"} {
		if fileName == sessionID+ext {
			return true
		}
	}
	return false
}

func isASCIIAlpha(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

func hasWindowsDrivePrefix(s string) bool {
	return len(s) >= 2 && isASCIIAlpha(s[0]) && s[1] == ':'
}

func normalizeAntigravityWorkspacePath(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	if strings.HasPrefix(strings.ToLower(s), "file://") {
		u, err := url.Parse(s)
		if err != nil {
			return ""
		}
		s = u.Path
		if len(s) >= 3 && s[0] == '/' && isASCIIAlpha(s[1]) && s[2] == ':' {
			s = s[1:]
		}
	} else if !hasWindowsDrivePrefix(s) && !filepath.IsAbs(s) {
		if abs, err := filepath.Abs(s); err == nil {
			s = abs
		}
	}
	if hasWindowsDrivePrefix(s) {
		slashed := strings.ReplaceAll(s[2:], "\\", "/")
		cleaned := path.Clean("/" + strings.TrimLeft(slashed, "/"))
		return strings.ToLower(s[:1]) + ":" + cleaned
	}
	return filepath.Clean(s)
}

func antigravityWorkspaceMatches(workDir, candidate string) bool {
	a := normalizeAntigravityWorkspacePath(workDir)
	b := normalizeAntigravityWorkspacePath(candidate)
	if a == "" || b == "" {
		return false
	}
	if a == b {
		return true
	}
	if evalA, err := filepath.EvalSymlinks(a); err == nil {
		a = normalizeAntigravityWorkspacePath(evalA)
	}
	if evalB, err := filepath.EvalSymlinks(b); err == nil {
		b = normalizeAntigravityWorkspacePath(evalB)
	}
	return a != "" && a == b
}

func parseAntigravityTimestamp(values ...string) time.Time {
	layouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05.999999999-07:00",
		"2006-01-02 15:04:05.999999999Z07:00",
		"2006-01-02 15:04:05-07:00",
		"2006-01-02 15:04:05",
	}
	for _, raw := range values {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		for _, layout := range layouts {
			if t, err := time.Parse(layout, raw); err == nil && !t.IsZero() && t.Year() > 1 {
				return t
			}
		}
	}
	return time.Time{}
}

func extractFileURIsFromProto(data []byte) []string {
	var uris []string
	i := 0
	for i < len(data) {
		tag, n := readProtoVarint(data[i:])
		if n <= 0 {
			break
		}
		i += n
		wireType := tag & 0x07
		switch wireType {
		case 0: // Varint
			_, vn := readProtoVarint(data[i:])
			if vn <= 0 {
				return uris
			}
			i += vn
		case 1: // 64-bit
			if i+8 > len(data) {
				return uris
			}
			i += 8
		case 2: // Length-delimited
			length, ln := readProtoVarint(data[i:])
			if ln <= 0 || length > uint64(len(data)-i-ln) {
				return uris
			}
			i += ln
			sub := data[i : i+int(length)]
			i += int(length)
			if strings.HasPrefix(string(sub), "file://") {
				uris = append(uris, string(sub))
			} else if len(sub) > 0 {
				uris = append(uris, extractFileURIsFromProto(sub)...)
			}
		case 5: // 32-bit
			if i+4 > len(data) {
				return uris
			}
			i += 4
		default:
			return uris
		}
	}
	return uris
}

func readProtoVarint(buf []byte) (uint64, int) {
	var x uint64
	var s uint
	for i, b := range buf {
		if i >= 10 {
			return 0, 0
		}
		if b < 0x80 {
			return x | uint64(b)<<s, i + 1
		}
		x |= uint64(b&0x7f) << s
		s += 7
	}
	return 0, 0
}

func loadAntigravityMetadataEntries(cliRoot string) (map[string]agyConversationMetadataEntry, error) {
	result := make(map[string]agyConversationMetadataEntry)

	metaPath := filepath.Join(cliRoot, "cache", "conversation_metadata.json")
	if data, err := os.ReadFile(metaPath); err == nil {
		var meta agyConversationMetadataFile
		if err := json.Unmarshal(data, &meta); err == nil {
			for rawID, entry := range meta.Conversations {
				id := strings.TrimSpace(entry.Summary.ID)
				if id == "" {
					id = strings.TrimSpace(rawID)
				}
				if !isValidAntigravitySessionID(id) {
					continue
				}
				if rawID != "" && isValidAntigravitySessionID(rawID) && rawID != id {
					continue
				}
				entry.Summary.ID = id
				result[id] = entry
			}
		} else {
			slog.Debug("antigravity: failed to parse conversation_metadata.json", "error", err)
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("antigravity: read conversation_metadata.json: %w", err)
	}

	dbPath := filepath.Join(cliRoot, "conversation_summaries.db")
	if _, err := os.Stat(dbPath); err == nil {
		dsn := "file:" + filepath.ToSlash(dbPath) + "?_pragma=busy_timeout(2000)&mode=ro"
		if db, err := sql.Open("sqlite", dsn); err == nil {
			rows, qErr := db.Query(`SELECT conversation_id, title, preview, step_count, CAST(last_modified_time AS TEXT), workspace_uris, parent_conversation_id, nesting_depth, raw_summary FROM conversation_summaries`)
			if qErr == nil {
				for rows.Next() {
					var convID, title, preview, lastMod, wsURIsRaw, parentID string
					var stepCount, nestingDepth int
					var rawSummary []byte
					if err := rows.Scan(&convID, &title, &preview, &stepCount, &lastMod, &wsURIsRaw, &parentID, &nestingDepth, &rawSummary); err != nil {
						continue
					}
					id := strings.TrimSpace(convID)
					if !isValidAntigravitySessionID(id) {
						continue
					}
					var uris []string
					_ = json.Unmarshal([]byte(wsURIsRaw), &uris)
					if len(uris) == 0 && len(rawSummary) > 0 {
						uris = extractFileURIsFromProto(rawSummary)
					}
					isInternal := strings.TrimSpace(parentID) != "" || nestingDepth > 0
					dbEntry := agyConversationMetadataEntry{
						Summary: agyConversationSummary{
							ID:            id,
							Title:         title,
							Preview:       preview,
							NumSteps:      stepCount,
							UpdatedAt:     lastMod,
							WorkspaceURIs: uris,
							Internal:      isInternal,
						},
						IsInternal:       isInternal,
						LastModifiedTime: lastMod,
					}
					if existing, ok := result[id]; ok {
						if len(dbEntry.Summary.WorkspaceURIs) == 0 {
							dbEntry.Summary.WorkspaceURIs = existing.Summary.WorkspaceURIs
						}
						oldMod := parseAntigravityTimestamp(existing.Summary.UpdatedAt, existing.LastModifiedTime, existing.Summary.CreatedAt)
						newMod := parseAntigravityTimestamp(lastMod)
						if newMod.Before(oldMod) {
							continue
						}
					}
					result[id] = dbEntry
				}
				_ = rows.Close()
			}
			_ = db.Close()
		}
	}

	lastPath := filepath.Join(cliRoot, "cache", "last_conversations.json")
	if data, err := os.ReadFile(lastPath); err == nil {
		var lastMap map[string]string
		if json.Unmarshal(data, &lastMap) == nil {
			for ws, rawID := range lastMap {
				id := strings.TrimSpace(rawID)
				if !isValidAntigravitySessionID(id) || strings.TrimSpace(ws) == "" {
					continue
				}
				if existing, ok := result[id]; ok {
					existing.Summary.WorkspaceURIs = append(existing.Summary.WorkspaceURIs, ws)
					result[id] = existing
				}
			}
		}
	}

	return result, nil
}

func firstNonEmptyLine(s string) string {
	for _, chunk := range strings.Split(strings.TrimSpace(s), "\n") {
		chunk = strings.TrimSpace(chunk)
		if chunk != "" {
			return chunk
		}
	}
	return ""
}

func truncateSessionSummary(summary, fallbackID string) string {
	summary = firstNonEmptyLine(summary)
	if summary == "" {
		summary = fallbackID
	}
	if utf8.RuneCountInString(summary) > 60 {
		summary = string([]rune(summary)[:60]) + "..."
	}
	return summary
}

func writeFileAtomic(targetPath string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(targetPath)
	tmp, err := os.CreateTemp(dir, ".cc-connect-tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, targetPath); err != nil {
		return err
	}
	cleanup = false
	return nil
}

func canDeleteAntigravityCLISession(cliRoot, workDir, sessionID string) (bool, error) {
	hasWorkspaceRecord := false
	matchedWorkDir := false

	metaEntries, err := loadAntigravityMetadataEntries(cliRoot)
	if err != nil {
		return false, err
	}
	if entry, ok := metaEntries[sessionID]; ok && len(entry.Summary.WorkspaceURIs) > 0 {
		hasWorkspaceRecord = true
		for _, uri := range entry.Summary.WorkspaceURIs {
			if antigravityWorkspaceMatches(workDir, uri) {
				matchedWorkDir = true
				break
			}
		}
	}

	if !matchedWorkDir {
		lastPath := filepath.Join(cliRoot, "cache", "last_conversations.json")
		if data, err := os.ReadFile(lastPath); err == nil {
			var lastMap map[string]string
			if json.Unmarshal(data, &lastMap) == nil {
				for ws, id := range lastMap {
					if strings.TrimSpace(id) != sessionID {
						continue
					}
					hasWorkspaceRecord = true
					if antigravityWorkspaceMatches(workDir, ws) {
						matchedWorkDir = true
						break
					}
				}
			}
		} else if !os.IsNotExist(err) {
			return false, fmt.Errorf("antigravity: read last_conversations: %w", err)
		}
	}

	if !matchedWorkDir {
		historyPath := filepath.Join(cliRoot, "history.jsonl")
		if file, err := os.Open(historyPath); err == nil {
			scanner := bufio.NewScanner(file)
			scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
			for scanner.Scan() {
				var entry agyHistoryEntry
				if json.Unmarshal(scanner.Bytes(), &entry) == nil && strings.TrimSpace(entry.ConversationID) == sessionID {
					if strings.TrimSpace(entry.Workspace) != "" {
						hasWorkspaceRecord = true
						if antigravityWorkspaceMatches(workDir, entry.Workspace) {
							matchedWorkDir = true
							break
						}
					}
				}
			}
			_ = file.Close()
		} else if !os.IsNotExist(err) {
			return false, fmt.Errorf("antigravity: read history.jsonl: %w", err)
		}
	}

	if hasWorkspaceRecord && !matchedWorkDir {
		return false, nil
	}
	return true, nil
}

func removeSessionFromAntigravityCaches(cliRoot, sessionID string) (bool, error) {
	removed := false
	cacheDir := filepath.Join(cliRoot, "cache")

	presenceDir := filepath.Join(cliRoot, "presence")
	lockPath := filepath.Join(presenceDir, sessionID+".lock")
	if isPathWithinDir(presenceDir, lockPath) {
		if err := os.Remove(lockPath); err == nil {
			removed = true
		} else if !os.IsNotExist(err) {
			return false, fmt.Errorf("antigravity: remove presence lock: %w", err)
		}
	}

	dbPath := filepath.Join(cliRoot, "conversation_summaries.db")
	if _, err := os.Stat(dbPath); err == nil {
		dsn := "file:" + filepath.ToSlash(dbPath) + "?_pragma=busy_timeout(2000)"
		if db, err := sql.Open("sqlite", dsn); err == nil {
			if res, execErr := db.Exec(`DELETE FROM conversation_summaries WHERE conversation_id = ?`, sessionID); execErr == nil {
				if n, _ := res.RowsAffected(); n > 0 {
					removed = true
				}
			}
			_, _ = db.Exec(`DELETE FROM conversations_fts WHERE conversation_id = ?`, sessionID)
			_ = db.Close()
		}
	}

	metaPath := filepath.Join(cacheDir, "conversation_metadata.json")
	if data, err := os.ReadFile(metaPath); err == nil {
		var root map[string]json.RawMessage
		if json.Unmarshal(data, &root) == nil && root != nil {
			var convs map[string]json.RawMessage
			if rawConvs, ok := root["conversations"]; ok && json.Unmarshal(rawConvs, &convs) == nil && convs != nil {
				changed := false
				for rawKey, rawVal := range convs {
					if strings.TrimSpace(rawKey) == sessionID {
						delete(convs, rawKey)
						changed = true
						continue
					}
					var entry agyConversationMetadataEntry
					if json.Unmarshal(rawVal, &entry) == nil && strings.TrimSpace(entry.Summary.ID) == sessionID {
						delete(convs, rawKey)
						changed = true
					}
				}
				if changed {
					updatedConvs, err := json.Marshal(convs)
					if err != nil {
						return false, fmt.Errorf("antigravity: marshal conversation metadata: %w", err)
					}
					root["conversations"] = updatedConvs
					updatedRoot, err := json.MarshalIndent(root, "", "  ")
					if err != nil {
						return false, fmt.Errorf("antigravity: marshal conversation metadata root: %w", err)
					}
					if err := writeFileAtomic(metaPath, append(updatedRoot, '\n'), 0o600); err != nil {
						return false, fmt.Errorf("antigravity: write conversation metadata: %w", err)
					}
					removed = true
				}
			}
		}
	} else if !os.IsNotExist(err) {
		return false, fmt.Errorf("antigravity: read conversation metadata: %w", err)
	}

	lastPath := filepath.Join(cacheDir, "last_conversations.json")
	if data, err := os.ReadFile(lastPath); err == nil {
		var lastMap map[string]string
		if json.Unmarshal(data, &lastMap) == nil && lastMap != nil {
			changed := false
			for ws, id := range lastMap {
				if strings.TrimSpace(id) == sessionID {
					delete(lastMap, ws)
					changed = true
				}
			}
			if changed {
				updated, err := json.MarshalIndent(lastMap, "", "  ")
				if err != nil {
					return false, fmt.Errorf("antigravity: marshal last_conversations: %w", err)
				}
				if err := writeFileAtomic(lastPath, append(updated, '\n'), 0o600); err != nil {
					return false, fmt.Errorf("antigravity: write last_conversations: %w", err)
				}
				removed = true
			}
		}
	} else if !os.IsNotExist(err) {
		return false, fmt.Errorf("antigravity: read last_conversations: %w", err)
	}

	historyPath := filepath.Join(cliRoot, "history.jsonl")
	if data, err := os.ReadFile(historyPath); err == nil {
		lines := strings.Split(string(data), "\n")
		var kept []string
		changed := false
		for _, line := range lines {
			if strings.TrimSpace(line) == "" {
				continue
			}
			var entry agyHistoryEntry
			if json.Unmarshal([]byte(line), &entry) == nil && strings.TrimSpace(entry.ConversationID) == sessionID {
				changed = true
				continue
			}
			kept = append(kept, line)
		}
		if changed {
			out := ""
			if len(kept) > 0 {
				out = strings.Join(kept, "\n") + "\n"
			}
			if err := writeFileAtomic(historyPath, []byte(out), 0o600); err != nil {
				return false, fmt.Errorf("antigravity: write history.jsonl: %w", err)
			}
			removed = true
		}
	} else if !os.IsNotExist(err) {
		return false, fmt.Errorf("antigravity: read history.jsonl: %w", err)
	}

	return removed, nil
}

func listAntigravitySessions(workDir string) ([]core.AgentSessionInfo, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("antigravity: cannot determine home dir: %w", err)
	}

	byID := make(map[string]core.AgentSessionInfo)
	internalIDs := make(map[string]bool)
	cliRoot := filepath.Join(homeDir, ".gemini", "antigravity-cli")

	// 1. Read workspace-scoped sessions from Antigravity CLI metadata (conversation_summaries.db and conversation_metadata.json).
	metaEntries, err := loadAntigravityMetadataEntries(cliRoot)
	if err != nil {
		return nil, err
	}
	for id, entry := range metaEntries {
		if entry.IsInternal || entry.Summary.Internal {
			internalIDs[id] = true
			continue
		}
		matchedWorkspace := false
		for _, uri := range entry.Summary.WorkspaceURIs {
			if antigravityWorkspaceMatches(workDir, uri) {
				matchedWorkspace = true
				break
			}
		}
		if !matchedWorkspace {
			continue
		}
		modTime := parseAntigravityTimestamp(entry.Summary.UpdatedAt, entry.LastModifiedTime, entry.Summary.CreatedAt)
		rawSummary := firstNonEmptyLine(entry.Summary.Preview)
		if rawSummary == "" {
			rawSummary = firstNonEmptyLine(entry.Summary.Title)
		}
		byID[id] = core.AgentSessionInfo{
			ID:           id,
			Summary:      truncateSessionSummary(rawSummary, id),
			MessageCount: entry.Summary.NumSteps,
			ModifiedAt:   modTime,
		}
	}

	// 2. Read workspace-scoped prompt history from Antigravity CLI history.jsonl.
	historyPath := filepath.Join(cliRoot, "history.jsonl")
	if file, err := os.Open(historyPath); err == nil {
		type histAgg struct {
			summary      string
			slashSummary string
			count        int
			modTime      time.Time
		}
		var order []string
		aggByID := make(map[string]*histAgg)

		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}
			var entry agyHistoryEntry
			if json.Unmarshal([]byte(line), &entry) != nil {
				continue
			}
			id := strings.TrimSpace(entry.ConversationID)
			if !isValidAntigravitySessionID(id) || internalIDs[id] {
				continue
			}
			if !antigravityWorkspaceMatches(workDir, entry.Workspace) {
				continue
			}
			agg, ok := aggByID[id]
			if !ok {
				agg = &histAgg{}
				aggByID[id] = agg
				order = append(order, id)
			}
			agg.count++
			if entry.Timestamp > 0 {
				ts := time.UnixMilli(entry.Timestamp)
				if ts.After(agg.modTime) {
					agg.modTime = ts
				}
			}
			display := firstNonEmptyLine(entry.Display)
			if display != "" {
				if entry.Type == "slash_command" {
					if agg.slashSummary == "" {
						agg.slashSummary = display
					}
				} else if agg.summary == "" {
					agg.summary = display
				}
			}
		}
		_ = file.Close()

		for _, id := range order {
			agg := aggByID[id]
			summary := agg.summary
			if summary == "" {
				summary = agg.slashSummary
			}
			if existing, ok := byID[id]; ok {
				if existing.Summary == id && summary != "" {
					existing.Summary = truncateSessionSummary(summary, id)
				}
				if existing.MessageCount == 0 && agg.count > 0 {
					existing.MessageCount = agg.count
				}
				if existing.ModifiedAt.IsZero() || agg.modTime.After(existing.ModifiedAt) {
					existing.ModifiedAt = agg.modTime
				}
				byID[id] = existing
			} else {
				byID[id] = core.AgentSessionInfo{
					ID:           id,
					Summary:      truncateSessionSummary(summary, id),
					MessageCount: agg.count,
					ModifiedAt:   agg.modTime,
				}
			}
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("antigravity: open history.jsonl: %w", err)
	}

	// 3. Keep compatibility with legacy ~/.gemini/tmp/<slug>/chats/*.jsonl files.
	slug := antigravityProjectSlug(workDir)
	chatsDir := filepath.Join(homeDir, ".gemini", "tmp", slug, "chats")

	entries, err := os.ReadDir(chatsDir)
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("antigravity: read chats dir: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}

		fpath := filepath.Join(chatsDir, entry.Name())
		file, err := os.Open(fpath)
		if err != nil {
			continue
		}

		var sf sessionFile
		var summary string
		msgCount := 0
		hasUserMsg := false

		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
		lineNum := 0
		for scanner.Scan() {
			line := scanner.Text()
			if line == "" {
				continue
			}

			if lineNum == 0 {
				if err := json.Unmarshal([]byte(line), &sf); err != nil || sf.SessionID == "" {
					break
				}
				lineNum++
				continue
			}

			var sl sessionLine
			if json.Unmarshal([]byte(line), &sl) == nil {
				if sl.Set != nil && !sl.Set.LastUpdated.IsZero() {
					sf.LastUpdated = sl.Set.LastUpdated
				} else if sl.Type != "" {
					msgCount++
					if sl.Type == "user" {
						hasUserMsg = true
						if summary == "" && len(sl.Content) > 0 {
							summary = firstNonEmptyLine(sl.Content[0].Text)
						}
					}
				}
			}
			lineNum++
		}
		if err := scanner.Err(); err != nil {
			_ = file.Close()
			continue
		}
		_ = file.Close()

		if !isValidAntigravitySessionID(sf.SessionID) || sf.Kind == "subagent" || !hasUserMsg {
			continue
		}
		if _, exists := byID[sf.SessionID]; exists {
			continue
		}

		modTime := sf.LastUpdated
		if modTime.IsZero() {
			modTime = sf.StartTime
		}

		byID[sf.SessionID] = core.AgentSessionInfo{
			ID:           sf.SessionID,
			Summary:      truncateSessionSummary(summary, sf.SessionID),
			MessageCount: msgCount,
			ModifiedAt:   modTime,
		}
	}

	if len(byID) == 0 {
		return nil, nil
	}

	sessions := make([]core.AgentSessionInfo, 0, len(byID))
	for _, s := range byID {
		sessions = append(sessions, s)
	}
	sort.Slice(sessions, func(i, j int) bool {
		if sessions[i].ModifiedAt.Equal(sessions[j].ModifiedAt) {
			return sessions[i].ID < sessions[j].ID
		}
		return sessions[i].ModifiedAt.After(sessions[j].ModifiedAt)
	})

	return sessions, nil
}
