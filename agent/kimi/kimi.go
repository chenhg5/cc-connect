package kimi

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/chenhg5/cc-connect/core"
)

func init() {
	core.RegisterAgent("kimi", New)
}

// Agent drives Kimi Code CLI in non-interactive mode via `--prompt` (and,
// when supported by the installed binary, `--print --output-format stream-json`).
//
// The legacy kimi-cli requires the `--print` flag for `--output-format` to
// take effect, while the newer Kimi Code CLI removed `--print` entirely and
// uses `--prompt` alone to enter non-interactive mode (see #1456). We probe
// `kimi --help` once at construction to detect which surface is installed and
// adapt the args we pass at Send() time.
//
// Modes:
//   - "default": standard mode (non-interactive `--prompt` auto-approves tools)
//   - "yolo":    auto-approve all tool calls
//   - "plan":    read-only plan mode
//   - "quiet":   final-message-only; uses --quiet on legacy kimi-cli, local
//     event suppression on the Kimi Code CLI (which dropped --quiet, #1561)
type Agent struct {
	workDir      string
	model        string
	mode         string
	cmd          string   // CLI binary name, default "kimi"
	cliExtraArgs []string // extra args from cmd after the binary name
	configEnv    []string // env vars from [projects.agent.options.env]
	timeout      time.Duration
	providers    []core.ProviderConfig
	activeIdx    int // -1 = no provider set
	sessionEnv   []string
	flagSupport  kimiFlagSupport // detected once at New() via `kimi --help`
	mu           sync.RWMutex
}

var _ core.HistoryProvider = (*Agent)(nil)

func New(opts map[string]any) (core.Agent, error) {
	workDir, _ := opts["work_dir"].(string)
	if workDir == "" {
		workDir = "."
	}
	model, _ := opts["model"].(string)
	mode, _ := opts["mode"].(string)
	mode = normalizeMode(mode)
	cmd, extraArgs := core.ParseCmdOpts(opts, "kimi")

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
			slog.Debug("kimi: timeout_mins has unexpected type", "type", fmt.Sprintf("%T", v))
		}
	}
	var timeout time.Duration
	if timeoutMins > 0 {
		timeout = time.Duration(timeoutMins) * time.Minute
	}

	if _, err := exec.LookPath(cmd); err != nil {
		return nil, fmt.Errorf("kimi: %q CLI not found in PATH, install with: pip install kimi-cli", cmd)
	}

	// Probe once so Send() can build args that match the installed CLI
	// surface (see #1456). The probe has its own timeout; failures fall
	// back to assuming the modern CLI (no --print).
	flagSupport := probeKimiFlags(context.Background(), cmd, 5*time.Second)

	return &Agent{
		workDir:      workDir,
		model:        model,
		mode:         mode,
		cmd:          cmd,
		cliExtraArgs: extraArgs,
		configEnv:    core.ParseConfigEnv(opts),
		timeout:      timeout,
		activeIdx:    -1,
		flagSupport:  flagSupport,
	}, nil
}

func normalizeMode(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "yolo", "force", "bypass", "auto":
		return "yolo"
	case "plan":
		return "plan"
	case "quiet":
		return "quiet"
	default:
		return "default"
	}
}

func (a *Agent) Name() string           { return "kimi" }
func (a *Agent) CLIBinaryName() string  { return a.cmd }
func (a *Agent) CLIDisplayName() string { return "Kimi" }

func (a *Agent) SetWorkDir(dir string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.workDir = dir
	slog.Info("kimi: work_dir changed", "work_dir", dir)
}

func (a *Agent) GetWorkDir() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.workDir
}

func (a *Agent) SetModel(model string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.model = model
	// Workspace-scoped model changes are intentionally not persisted through
	// core's provider save callback. Keep the active provider's in-memory model
	// in sync so StartSession does not overwrite the selection with its old
	// value on the next turn.
	if a.activeIdx >= 0 && a.activeIdx < len(a.providers) {
		a.providers[a.activeIdx].Model = model
	}
	slog.Info("kimi: model changed", "model", model)
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
	if models := a.discoverModels(ctx); len(models) > 0 {
		return models
	}
	if models := a.configuredModels(); len(models) > 0 {
		return models
	}
	return []core.ModelOption{
		{Name: "kimi-k2-0711-preview", Desc: "Kimi K2 (most capable)"},
		{Name: "kimi-k2-0711", Desc: "Kimi K2"},
		{Name: "kimi-k2-5-preview", Desc: "Kimi K2.5 (balanced)"},
		{Name: "kimi-k2-5", Desc: "Kimi K2.5"},
	}
}

type kimiModelDiscoverySnapshot struct {
	cmd       string
	extraArgs []string
	workDir   string
	env       []string
}

func (a *Agent) modelDiscoverySnapshot() kimiModelDiscoverySnapshot {
	a.mu.RLock()
	defer a.mu.RUnlock()

	env := append([]string(nil), a.configEnv...)
	env = append(env, a.providerEnvLocked()...)
	env = append(env, a.sessionEnv...)
	return kimiModelDiscoverySnapshot{
		cmd:       a.cmd,
		extraArgs: append([]string(nil), a.cliExtraArgs...),
		workDir:   a.workDir,
		env:       env,
	}
}

// discoverModels asks Kimi Code for the same model aliases shown by its own
// model picker. The command output also contains provider configuration, so it
// is decoded into a narrow struct and is never logged.
func (a *Agent) discoverModels(ctx context.Context) []core.ModelOption {
	snapshot := a.modelDiscoverySnapshot()
	if snapshot.cmd == "" {
		return nil
	}

	args := append(snapshot.extraArgs, "provider", "list", "--json")
	cmd := exec.CommandContext(ctx, snapshot.cmd, args...)
	cmd.Dir = snapshot.workDir
	if len(snapshot.env) > 0 {
		cmd.Env = append(os.Environ(), snapshot.env...)
	}
	out, err := cmd.Output()
	if err != nil {
		slog.Debug("kimi: model discovery failed", "error", err)
		return nil
	}

	models, err := parseKimiModels(out)
	if err != nil {
		slog.Debug("kimi: parse model discovery output failed", "error", err)
		return nil
	}
	return models
}

func parseKimiModels(data []byte) ([]core.ModelOption, error) {
	var catalog struct {
		Models map[string]struct {
			DisplayName string `json:"displayName"`
		} `json:"models"`
	}
	if err := json.Unmarshal(data, &catalog); err != nil {
		return nil, fmt.Errorf("decode provider list: %w", err)
	}

	models := make([]core.ModelOption, 0, len(catalog.Models))
	for alias, model := range catalog.Models {
		alias = strings.TrimSpace(alias)
		if alias == "" {
			continue
		}
		desc := strings.TrimSpace(model.DisplayName)
		if desc == alias {
			desc = ""
		}
		models = append(models, core.ModelOption{Name: alias, Desc: desc})
	}
	sort.Slice(models, func(i, j int) bool {
		return models[i].Name < models[j].Name
	})
	return models, nil
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
	flagSupport := a.flagSupport
	if a.activeIdx >= 0 && a.activeIdx < len(a.providers) {
		if m := a.providers[a.activeIdx].Model; m != "" {
			model = m
		}
	}
	a.mu.Unlock()

	return newKimiSession(ctx, cmd, extraArgs, workDir, model, mode, sessionID, extraEnv, timeout, flagSupport)
}

func (a *Agent) ListSessions(_ context.Context) ([]core.AgentSessionInfo, error) {
	return listKimiSessions(a.workDir)
}

// GetSessionHistory reads the transcript produced by either the legacy
// kimi-cli or the current Kimi Code CLI.
func (a *Agent) GetSessionHistory(_ context.Context, sessionID string, limit int) ([]core.HistoryEntry, error) {
	sessionDir := findKimiSessionDir(sessionID)
	if sessionDir == "" {
		return nil, fmt.Errorf("kimi: session not found: %s", sessionID)
	}

	contextPath := filepath.Join(sessionDir, "context.jsonl")
	entries, err := readKimiHistoryFile(contextPath, readKimiContextHistory)
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("kimi: read legacy session history: %w", err)
	}
	if len(entries) == 0 {
		wirePath := filepath.Join(sessionDir, "agents", "main", "wire.jsonl")
		entries, err = readKimiHistoryFile(wirePath, readKimiWireHistory)
		if err != nil {
			return nil, fmt.Errorf("kimi: read session history: %w", err)
		}
	}

	if limit > 0 && len(entries) > limit {
		entries = entries[len(entries)-limit:]
	}
	return entries, nil
}

func readKimiHistoryFile(path string, parse func(io.Reader) ([]core.HistoryEntry, error)) ([]core.HistoryEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return parse(f)
}

func (a *Agent) DeleteSession(_ context.Context, sessionID string) error {
	path := findKimiSessionDir(sessionID)
	if path == "" {
		return fmt.Errorf("session not found: %s", sessionID)
	}
	return os.RemoveAll(path)
}

func (a *Agent) Stop() error { return nil }

// ── ModeSwitcher ────────────────────────────────────────────────

func (a *Agent) SetMode(mode string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.mode = normalizeMode(mode)
	slog.Info("kimi: mode changed", "mode", a.mode)
}

func (a *Agent) GetMode() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.mode
}

func (a *Agent) PermissionModes() []core.PermissionModeInfo {
	return []core.PermissionModeInfo{
		{Key: "default", Name: "Default", NameZh: "默认", Desc: "Standard mode (print output)", DescZh: "标准模式（打印输出）"},
		{Key: "yolo", Name: "YOLO", NameZh: "全自动", Desc: "Auto-approve all tool calls", DescZh: "自动批准所有工具调用"},
		{Key: "plan", Name: "Plan", NameZh: "规划模式", Desc: "Read-only plan mode, no execution", DescZh: "只读规划模式，不做修改"},
		{Key: "quiet", Name: "Quiet", NameZh: "静默", Desc: "Quiet mode (final message only)", DescZh: "静默模式（仅最终消息）"},
	}
}

// ── SkillProvider implementation ──────────────────────────────

func (a *Agent) SkillDirs() []string {
	absDir, err := filepath.Abs(a.workDir)
	if err != nil {
		absDir = a.workDir
	}
	dirs := []string{filepath.Join(absDir, ".kimi", "skills")}
	if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(home, ".kimi", "skills"))
	}
	return dirs
}

// ── ContextCompressor implementation ──────────────────────────

func (a *Agent) CompressCommand() string { return "" }

// ── MemoryFileProvider implementation ─────────────────────────

func (a *Agent) ProjectMemoryFile() string {
	absDir, err := filepath.Abs(a.workDir)
	if err != nil {
		absDir = a.workDir
	}
	return filepath.Join(absDir, "AGENTS.md")
}

func (a *Agent) GlobalMemoryFile() string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(homeDir, ".kimi", "AGENTS.md")
}

// ── ProviderSwitcher ────────────────────────────────────────────

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
		slog.Info("kimi: provider cleared")
		return true
	}
	for i, p := range a.providers {
		if p.Name == name {
			a.activeIdx = i
			slog.Info("kimi: provider switched", "provider", name)
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
		env = append(env, "KIMI_API_KEY="+p.APIKey)
	}
	for k, v := range p.Env {
		env = append(env, k+"="+v)
	}
	return env
}

// ── Session listing ─────────────────────────────────────────────

// kimiSessionsBaseDirs returns the session storage roots of both CLI
// flavors: legacy kimi-cli keeps sessions under ~/.kimi/sessions, the Kimi
// Code CLI uses ~/.kimi-code/sessions (#1561). Both are scanned so /list and
// /delete work no matter which binary produced the session.
func kimiSessionsBaseDirs() []string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	return []string{
		filepath.Join(homeDir, ".kimi", "sessions"),
		filepath.Join(homeDir, ".kimi-code", "sessions"),
	}
}

func listKimiSessions(workDir string) ([]core.AgentSessionInfo, error) {
	absWorkDir, err := filepath.Abs(workDir)
	if err != nil {
		absWorkDir = workDir
	}

	var sessions []core.AgentSessionInfo
	for _, sessionsBase := range kimiSessionsBaseDirs() {
		entries, err := os.ReadDir(sessionsBase)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("kimi: read sessions dir: %w", err)
		}

		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			projectDir := filepath.Join(sessionsBase, entry.Name())
			sessionEntries, err := os.ReadDir(projectDir)
			if err != nil {
				continue
			}
			for _, se := range sessionEntries {
				if !se.IsDir() {
					continue
				}
				sessionDir := filepath.Join(projectDir, se.Name())
				info := parseKimiSessionDir(sessionDir, absWorkDir)
				if info != nil {
					sessions = append(sessions, *info)
				}
			}
		}
	}

	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].ModifiedAt.After(sessions[j].ModifiedAt)
	})

	return sessions, nil
}

// parseKimiTranscript counts a session's conversation messages and extracts a
// summary from its transcript file. The legacy kimi-cli writes the transcript
// to context.jsonl; the Kimi Code CLI instead stores it at
// agents/main/wire.jsonl (#1561). We read whichever exists so /list does not
// report 0 messages for modern sessions (review feedback on #1564).
func parseKimiTranscript(sessionDir string) (msgCount int, summary string) {
	contextPath := filepath.Join(sessionDir, "context.jsonl")
	if f, err := os.Open(contextPath); err == nil {
		defer func() { _ = f.Close() }()
		msgCount, summary = countContextJSONL(f)
	}
	if msgCount == 0 {
		// Kimi Code CLI fallback — no context.jsonl, so count from wire.jsonl.
		wirePath := filepath.Join(sessionDir, "agents", "main", "wire.jsonl")
		if f, err := os.Open(wirePath); err == nil {
			defer func() { _ = f.Close() }()
			m, s := countWireJSONL(f)
			if m > msgCount {
				msgCount = m
			}
			if summary == "" {
				summary = s
			}
		}
	}
	return msgCount, summary
}

// countContextJSONL parses a legacy kimi-cli context.jsonl transcript,
// counting user/assistant messages and taking the first user text as summary.
func countContextJSONL(f io.Reader) (msgCount int, summary string) {
	msgCount, summary, err := countKimiJSONL(f, false)
	if err != nil {
		slog.Warn("kimi: count context.jsonl", "error", err)
	}
	return msgCount, summary
}

// countWireJSONL parses a Kimi Code CLI agents/main/wire.jsonl transcript.
// Each line is an event such as
//
//	{"type":"context.append_message","message":{"role":"user","content":...},
//	 "origin":{"kind":"user",...}}
//
// We count only user-side turns rather than every appended event, which would
// also include tool results and streamed assistant chunks. Current Kimi Code
// versions put origin under message and encode content as an array, while
// older versions used a top-level origin and string content.
func countWireJSONL(f io.Reader) (msgCount int, summary string) {
	msgCount, summary, err := countKimiJSONL(f, true)
	if err != nil {
		slog.Warn("kimi: count wire.jsonl", "error", err)
	}
	return msgCount, summary
}

// countKimiJSONL only retains the count and first user prompt. Session listing
// must not materialize every assistant response just to show a message count.
func countKimiJSONL(r io.Reader, wire bool) (msgCount int, summary string, err error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 256*1024), 10*1024*1024)
	for scanner.Scan() {
		var raw struct {
			Type    string          `json:"type"`
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
			Message struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(scanner.Bytes(), &raw) != nil {
			continue
		}
		role, content := raw.Role, raw.Content
		if wire {
			if raw.Type != "context.append_message" || raw.Message.Role != "user" {
				continue
			}
			role, content = raw.Message.Role, raw.Message.Content
		} else if role != "user" && role != "assistant" {
			continue
		}
		text := extractKimiText(content)
		if text == "" {
			continue
		}
		msgCount++
		if role == "user" && summary == "" {
			summary = text
		}
	}
	return msgCount, summary, scanner.Err()
}

func readKimiContextHistory(r io.Reader) ([]core.HistoryEntry, error) {
	var entries []core.HistoryEntry
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 256*1024), 10*1024*1024)
	for scanner.Scan() {
		var raw struct {
			Role      string          `json:"role"`
			Content   json.RawMessage `json:"content"`
			Timestamp json.RawMessage `json:"timestamp"`
			Time      json.RawMessage `json:"time"`
		}
		if json.Unmarshal(scanner.Bytes(), &raw) != nil {
			continue
		}
		if raw.Role != "user" && raw.Role != "assistant" {
			continue
		}
		content := extractKimiText(raw.Content)
		if content == "" {
			continue
		}
		timestamp := parseKimiTimestamp(raw.Timestamp)
		if timestamp.IsZero() {
			timestamp = parseKimiTimestamp(raw.Time)
		}
		entries = append(entries, core.HistoryEntry{
			Role:      raw.Role,
			Content:   content,
			Timestamp: timestamp,
		})
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan context.jsonl: %w", err)
	}
	return entries, nil
}

func readKimiWireHistory(r io.Reader) ([]core.HistoryEntry, error) {
	var entries []core.HistoryEntry
	var assistantTurnID string
	var assistantText strings.Builder
	var assistantTimestamp time.Time

	flushAssistant := func() {
		content := strings.TrimSpace(assistantText.String())
		if content != "" {
			entries = append(entries, core.HistoryEntry{
				Role:      "assistant",
				Content:   content,
				Timestamp: assistantTimestamp,
			})
		}
		assistantTurnID = ""
		assistantText.Reset()
		assistantTimestamp = time.Time{}
	}

	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 256*1024), 10*1024*1024)
	for scanner.Scan() {
		var raw struct {
			Type    string          `json:"type"`
			Time    json.RawMessage `json:"time"`
			Message struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"message"`
			Event struct {
				Type   string `json:"type"`
				TurnID string `json:"turnId"`
				Part   struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"part"`
			} `json:"event"`
		}
		if json.Unmarshal(scanner.Bytes(), &raw) != nil {
			continue
		}

		switch raw.Type {
		case "context.append_message":
			if raw.Message.Role != "user" && raw.Message.Role != "assistant" {
				continue
			}
			content := extractKimiText(raw.Message.Content)
			if content == "" {
				continue
			}
			flushAssistant()
			entries = append(entries, core.HistoryEntry{
				Role:      raw.Message.Role,
				Content:   content,
				Timestamp: parseKimiTimestamp(raw.Time),
			})
		case "context.append_loop_event":
			if raw.Event.Type != "content.part" || raw.Event.Part.Type != "text" || raw.Event.Part.Text == "" {
				continue
			}
			if assistantText.Len() > 0 && raw.Event.TurnID != assistantTurnID {
				flushAssistant()
			}
			if assistantText.Len() == 0 {
				assistantTurnID = raw.Event.TurnID
				assistantTimestamp = parseKimiTimestamp(raw.Time)
			}
			assistantText.WriteString(raw.Event.Part.Text)
		}
	}
	flushAssistant()
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan wire.jsonl: %w", err)
	}
	return entries, nil
}

func extractKimiText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}

	var text string
	if json.Unmarshal(raw, &text) == nil {
		return strings.TrimSpace(text)
	}

	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) != nil {
		return ""
	}
	texts := make([]string, 0, len(parts))
	for _, part := range parts {
		if part.Type == "text" && strings.TrimSpace(part.Text) != "" {
			texts = append(texts, strings.TrimSpace(part.Text))
		}
	}
	return strings.Join(texts, "\n")
}

func parseKimiTimestamp(raw json.RawMessage) time.Time {
	if len(raw) == 0 {
		return time.Time{}
	}

	var millis int64
	if json.Unmarshal(raw, &millis) == nil {
		return time.UnixMilli(millis)
	}

	var value string
	if json.Unmarshal(raw, &value) != nil {
		return time.Time{}
	}
	if timestamp, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return timestamp
	}
	return time.Time{}
}

func parseKimiSessionDir(sessionDir, filterWorkDir string) *core.AgentSessionInfo {
	statePath := filepath.Join(sessionDir, "state.json")
	stateData, err := os.ReadFile(statePath)
	if err != nil {
		return nil
	}

	// Legacy kimi-cli stores custom_title; Kimi Code uses title and records
	// the working directory as cwd (formerly workDir).
	var state struct {
		CustomTitle string `json:"custom_title"`
		Title       string `json:"title"`
		Archived    bool   `json:"archived"`
		WorkDir     string `json:"workDir"`
		CWD         string `json:"cwd"`
	}
	if json.Unmarshal(stateData, &state) != nil {
		return nil
	}
	if state.Archived {
		return nil
	}

	// The Kimi Code CLI records the session's cwd, so unlike the legacy
	// flavor (which stores no cwd and is always listed) we can honor the
	// caller's workDir filter for it.
	workDir := state.CWD
	if workDir == "" {
		workDir = state.WorkDir
	}
	if workDir != "" && filterWorkDir != "" {
		absStateDir, err := filepath.Abs(workDir)
		if err != nil {
			absStateDir = workDir
		}
		if absStateDir != filterWorkDir {
			return nil
		}
	}

	sessionID := filepath.Base(sessionDir)

	info, err := os.Stat(sessionDir)
	if err != nil {
		return nil
	}

	msgCount, summary := parseKimiTranscript(sessionDir)

	if summary == "" {
		summary = state.CustomTitle
	}
	if summary == "" {
		summary = state.Title
	}
	if utf8.RuneCountInString(summary) > 60 {
		summary = string([]rune(summary)[:60]) + "..."
	}

	return &core.AgentSessionInfo{
		ID:           sessionID,
		Summary:      summary,
		MessageCount: msgCount,
		ModifiedAt:   info.ModTime(),
	}
}

func findKimiSessionDir(sessionID string) string {
	for _, sessionsBase := range kimiSessionsBaseDirs() {
		entries, err := os.ReadDir(sessionsBase)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			projectDir := filepath.Join(sessionsBase, entry.Name())
			sessionEntries, err := os.ReadDir(projectDir)
			if err != nil {
				continue
			}
			for _, se := range sessionEntries {
				if !se.IsDir() {
					continue
				}
				if se.Name() == sessionID {
					return filepath.Join(projectDir, se.Name())
				}
			}
		}
	}
	return ""
}
