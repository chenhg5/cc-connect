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
	discovered   []core.ModelOption
	// discovered holds the model identifiers the startup probe learned
	// from the installed kimi-code CLI (see probeKimiModels, #1795). Nil
	// when the probe failed (CLI missing, older kimi-cli dialect without
	// the `model list` subcommand, or unparseable output); in that case
	// AvailableModels falls back to kimiBuiltinModels so users on older
	// CLIs keep seeing *some* picker options instead of an empty list.
	mu sync.RWMutex
}

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

	// Also probe the locally installed CLI for its currently-registered
	// model identifiers so the /model picker tracks the installed CLI
	// instead of drifting every release (#1795). When the probe returns
	// nothing (older kimi-cli dialect, binary unreachable, unparseable
	// output) AvailableModels falls back to kimiBuiltinModels.
	discovered := probeKimiModels(context.Background(), cmd, 5*time.Second)

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
		discovered:   discovered,
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

func (a *Agent) discoveredModels() []core.ModelOption {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.discovered
}

// AvailableModels returns the model options for the /model picker.
//
// Resolution order (#1795):
//  1. If a provider is configured with explicit models, use those.
//  2. Otherwise, prefer the startup-probe result so the picker follows the
//     installed kimi-code CLI instead of drifting every release. The probe
//     is opportunistic — it tries `kimi model list`, `kimi models`, and
//     `kimi models list`, and the parser is tolerant of various output
//     layouts so future CLI tweaks don't silently empty the picker.
//  3. Finally, fall back to kimiBuiltinModels. The list is refreshed for
//     every cc-connect release (latest known models first, oldest kept for
//     backward compatibility) so even users on CLIs that don't expose the
//     `model list` subcommand still see current options.
func (a *Agent) AvailableModels(ctx context.Context) []core.ModelOption {
	if models := a.configuredModels(); len(models) > 0 {
		return models
	}
	if models := a.discoveredModels(); len(models) > 0 {
		return models
	}
	return kimiBuiltinModels()
}

// kimiBuiltinModels returns the static fallback list used when no provider
// is configured AND the startup probe could not discover installed models
// (older kimi-cli dialect, CLI unreachable, unparseable probe output, …).
//
// The list is ordered newest-first and includes legacy K2 / K2.5 entries at
// the bottom for users still on those CLIs — refreshing the *entire* list
// to only new models would silently break /model selection for anyone who
// hasn't upgraded yet, while keeping the old list frozen at K2/K2.5 is the
// bug #1795 is reporting. See #1795 for context.
func kimiBuiltinModels() []core.ModelOption {
	return []core.ModelOption{
		{Name: "k3", Desc: "Kimi K3 (current default)"},
		{Name: "k3-256k", Desc: "Kimi K3 256k (long context)"},
		{Name: "kimi-k2-5-preview", Desc: "Kimi K2.5 (balanced)"},
		{Name: "kimi-k2-5", Desc: "Kimi K2.5"},
		{Name: "kimi-k2-0711-preview", Desc: "Kimi K2 (legacy)"},
		{Name: "kimi-k2-0711", Desc: "Kimi K2"},
	}
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
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 256*1024), 256*1024)
	for scanner.Scan() {
		var entry struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		}
		if json.Unmarshal(scanner.Bytes(), &entry) != nil {
			continue
		}
		if entry.Role == "user" || entry.Role == "assistant" {
			msgCount++
			if entry.Role == "user" && entry.Content != "" && summary == "" {
				summary = strings.TrimSpace(entry.Content)
			}
		}
	}
	return msgCount, summary
}

// countWireJSONL parses a Kimi Code CLI agents/main/wire.jsonl transcript.
// Each line is an event such as
//
//	{"type":"context.append_message","message":{"role":"user","content":...},
//	 "origin":{"kind":"user",...}}
//
// We count only user-side turns (origin.kind == "user") rather than every
// appended event, which would also include tool results and streamed
// assistant chunks. This matches the reviewer signal for "how many messages
// are in this session" and keeps the count stable.
func countWireJSONL(f io.Reader) (msgCount int, summary string) {
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 256*1024), 256*1024)
	for scanner.Scan() {
		var entry struct {
			Type    string `json:"type"`
			Message struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"message"`
			Origin struct {
				Kind string `json:"kind"`
			} `json:"origin"`
		}
		if json.Unmarshal(scanner.Bytes(), &entry) != nil {
			continue
		}
		if entry.Type != "context.append_message" || entry.Origin.Kind != "user" {
			continue
		}
		msgCount++
		if entry.Message.Content != "" && summary == "" {
			summary = strings.TrimSpace(entry.Message.Content)
		}
	}
	return msgCount, summary
}

func parseKimiSessionDir(sessionDir, filterWorkDir string) *core.AgentSessionInfo {
	statePath := filepath.Join(sessionDir, "state.json")
	stateData, err := os.ReadFile(statePath)
	if err != nil {
		return nil
	}

	// Two state.json schemas (#1561): legacy kimi-cli stores
	// {"custom_title","archived"}; the Kimi Code CLI stores
	// {"title","workDir","updatedAt",...} with no archived flag.
	var state struct {
		CustomTitle string `json:"custom_title"`
		Title       string `json:"title"`
		Archived    bool   `json:"archived"`
		WorkDir     string `json:"workDir"`
	}
	if json.Unmarshal(stateData, &state) != nil {
		return nil
	}
	if state.Archived {
		return nil
	}

	// The Kimi Code CLI records the session's workDir, so unlike the legacy
	// flavor (which stores no cwd and is always listed) we can honor the
	// caller's workDir filter for it.
	if state.WorkDir != "" && filterWorkDir != "" {
		absStateDir, err := filepath.Abs(state.WorkDir)
		if err != nil {
			absStateDir = state.WorkDir
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
