package claudecode

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/chenhg5/cc-connect/core"
)

func init() {
	core.RegisterAgent("claudecode", New)
}

// Agent drives Claude Code CLI using --input-format stream-json
// and --permission-prompt-tool stdio for bidirectional communication.
//
// Permission modes (maps to Claude's --permission-mode):
//   - "default":           every tool call requires user approval
//   - "acceptEdits":       auto-approve file edit tools, ask for others
//   - "plan":              plan only, no execution until approved
//   - "auto":              Claude's automatic permission classifier
//   - "bypassPermissions": auto-approve everything (alias: yolo)
type Agent struct {
	workDir          string
	model            string
	mode             string // "default" | "acceptEdits" | "plan" | "auto" | "bypassPermissions" | "dontAsk"
	allowedTools     []string
	disallowedTools  []string
	maxContextTokens int // optional: passed as --max-context-tokens when > 0
	providers        []core.ProviderConfig
	activeIdx        int // -1 = no provider set
	sessionEnv       []string
	routerURL        string // Claude Code Router URL (e.g., "http://127.0.0.1:3456")
	routerAPIKey     string // Claude Code Router API key (optional)

	providerProxy  *core.ProviderProxy // local proxy for third-party providers
	proxyLocalURL  string              // local URL of the proxy
	platformPrompt string              // platform-specific formatting instructions

	mu sync.RWMutex
}

func New(opts map[string]any) (core.Agent, error) {
	workDir, _ := opts["work_dir"].(string)
	if workDir == "" {
		workDir = "."
	}
	model, _ := opts["model"].(string)
	mode, _ := opts["mode"].(string)
	mode = normalizePermissionMode(mode)

	var allowedTools []string
	if tools, ok := opts["allowed_tools"].([]any); ok {
		for _, t := range tools {
			if s, ok := t.(string); ok {
				allowedTools = append(allowedTools, s)
			}
		}
	}

	var disallowedTools []string
	if tools, ok := opts["disallowed_tools"].([]any); ok {
		for _, t := range tools {
			if s, ok := t.(string); ok {
				disallowedTools = append(disallowedTools, s)
			}
		}
	}

	maxContextTokens := 0
	switch v := opts["max_context_tokens"].(type) {
	case int:
		if v > 0 {
			maxContextTokens = v
		}
	case int64:
		if v > 0 {
			maxContextTokens = int(v)
		}
	case float64:
		if v > 0 {
			maxContextTokens = int(v)
		}
	}

	// Claude Code Router support
	routerURL, _ := opts["router_url"].(string)
	routerAPIKey, _ := opts["router_api_key"].(string)

	if _, err := exec.LookPath("claude"); err != nil {
		return nil, fmt.Errorf("claudecode: 'claude' CLI not found in PATH, please install Claude Code first")
	}

	return &Agent{
		workDir:          workDir,
		model:            model,
		mode:             mode,
		allowedTools:     allowedTools,
		disallowedTools:  disallowedTools,
		maxContextTokens: maxContextTokens,
		activeIdx:        -1,
		routerURL:        routerURL,
		routerAPIKey:     routerAPIKey,
	}, nil
}

// normalizePermissionMode maps user-friendly aliases to Claude CLI values.
func normalizePermissionMode(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "acceptedits", "accept-edits", "accept_edits", "edit":
		return "acceptEdits"
	case "plan":
		return "plan"
	case "auto":
		return "auto"
	case "bypasspermissions", "bypass-permissions", "bypass_permissions",
		"yolo":
		return "bypassPermissions"
	case "dontask", "dont-ask", "dont_ask":
		return "dontAsk"
	default:
		return "default"
	}
}

func (a *Agent) Name() string           { return "claudecode" }
func (a *Agent) CLIBinaryName() string  { return "claude" }
func (a *Agent) CLIDisplayName() string { return "Claude" }

func (a *Agent) SetWorkDir(dir string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.workDir = dir
	slog.Info("claudecode: work_dir changed", "work_dir", dir)
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
	slog.Info("claudecode: model changed", "model", model)
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
		{Name: "sonnet", Desc: "Claude Sonnet 4 (balanced)"},
		{Name: "opus", Desc: "Claude Opus 4 (most capable)"},
		{Name: "haiku", Desc: "Claude Haiku 3.5 (fastest)"},
	}
}

func (a *Agent) fetchModelsFromAPI(ctx context.Context) []core.ModelOption {
	a.mu.Lock()
	apiKey := ""
	baseURL := ""
	if a.activeIdx >= 0 && a.activeIdx < len(a.providers) {
		apiKey = a.providers[a.activeIdx].APIKey
		baseURL = a.providers[a.activeIdx].BaseURL
	}
	a.mu.Unlock()

	if apiKey == "" {
		apiKey = os.Getenv("ANTHROPIC_API_KEY")
	}
	if apiKey == "" {
		return nil
	}
	if baseURL == "" {
		baseURL = os.Getenv("ANTHROPIC_BASE_URL")
	}
	if baseURL == "" {
		baseURL = "https://api.anthropic.com"
	}
	baseURL = strings.TrimRight(baseURL, "/")

	req, err := http.NewRequestWithContext(ctx, "GET", baseURL+"/v1/models", nil)
	if err != nil {
		return nil
	}
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		slog.Debug("claudecode: failed to fetch models", "error", err)
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}

	var result struct {
		Data []struct {
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil
	}

	var models []core.ModelOption
	for _, m := range result.Data {
		models = append(models, core.ModelOption{Name: m.ID, Desc: m.DisplayName})
	}
	return models
}

func (a *Agent) SetSessionEnv(env []string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sessionEnv = env
}

func (a *Agent) SetPlatformPrompt(prompt string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.platformPrompt = prompt
}

// StartSession creates a persistent interactive Claude Code session.
func (a *Agent) StartSession(ctx context.Context, sessionID string) (core.AgentSession, error) {
	a.mu.Lock()
	tools := make([]string, len(a.allowedTools))
	copy(tools, a.allowedTools)
	disTools := make([]string, len(a.disallowedTools))
	copy(disTools, a.disallowedTools)
	maxTok := a.maxContextTokens
	model := a.model
	extraEnv := a.providerEnvLocked()
	extraEnv = append(extraEnv, a.sessionEnv...)

	// Add Claude Code Router environment variables if configured
	if a.routerURL != "" {
		extraEnv = append(extraEnv, "ANTHROPIC_BASE_URL="+a.routerURL)
		// When using router, we need to prevent proxy interference
		extraEnv = append(extraEnv, "NO_PROXY=127.0.0.1")
		// Disable telemetry and cost warnings for cleaner router integration
		extraEnv = append(extraEnv, "DISABLE_TELEMETRY=true")
		extraEnv = append(extraEnv, "DISABLE_COST_WARNINGS=true")
	}
	if a.routerAPIKey != "" {
		extraEnv = append(extraEnv, "ANTHROPIC_API_KEY="+a.routerAPIKey)
	}

	if a.activeIdx >= 0 && a.activeIdx < len(a.providers) {
		if m := a.providers[a.activeIdx].Model; m != "" {
			model = m
		}
	}
	platformPrompt := a.platformPrompt
	// When router_url is set, --verbose conflicts with --output-format stream-json
	// (verbose emits non-JSON text to stdout that corrupts the JSON stream).
	disableVerbose := a.routerURL != ""
	a.mu.Unlock()

	return newClaudeSession(ctx, a.workDir, model, sessionID, a.mode, tools, disTools, extraEnv, platformPrompt, disableVerbose, maxTok)
}

func (a *Agent) ListSessions(ctx context.Context) ([]core.AgentSessionInfo, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("claudecode: cannot determine home dir: %w", err)
	}

	absWorkDir, err := filepath.Abs(a.workDir)
	if err != nil {
		return nil, fmt.Errorf("claudecode: resolve work_dir: %w", err)
	}

	projectDir := findProjectDir(homeDir, absWorkDir)
	if projectDir == "" {
		return nil, nil
	}

	entries, err := os.ReadDir(projectDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("claudecode: read project dir: %w", err)
	}

	var sessions []core.AgentSessionInfo
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".jsonl") {
			continue
		}

		sessionID := strings.TrimSuffix(name, ".jsonl")
		info, err := entry.Info()
		if err != nil {
			continue
		}

		summary, msgCount := scanSessionMeta(filepath.Join(projectDir, name))

		sessions = append(sessions, core.AgentSessionInfo{
			ID:           sessionID,
			Summary:      summary,
			MessageCount: msgCount,
			ModifiedAt:   info.ModTime(),
		})
	}

	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].ModifiedAt.After(sessions[j].ModifiedAt)
	})

	return sessions, nil
}

func (a *Agent) DeleteSession(_ context.Context, sessionID string) error {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("claudecode: cannot determine home dir: %w", err)
	}
	absWorkDir, err := filepath.Abs(a.workDir)
	if err != nil {
		return fmt.Errorf("claudecode: resolve work_dir: %w", err)
	}
	projectDir := findProjectDir(homeDir, absWorkDir)
	if projectDir == "" {
		return fmt.Errorf("session not found")
	}
	path := filepath.Join(projectDir, sessionID+".jsonl")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return fmt.Errorf("session file not found: %s", sessionID)
	}
	return os.Remove(path)
}

func scanSessionMeta(path string) (string, int) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 256*1024), 256*1024)

	var summary string
	var count int

	for scanner.Scan() {
		var entry struct {
			Type    string `json:"type"`
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			continue
		}
		if entry.Type == "user" || entry.Type == "assistant" {
			count++
			if entry.Type == "user" && entry.Message.Content != "" {
				summary = entry.Message.Content
			}
		}
	}
	summary = stripXMLTags(summary)
	summary = strings.TrimSpace(summary)
	if utf8.RuneCountInString(summary) > 40 {
		summary = string([]rune(summary)[:40]) + "..."
	}
	return summary, count
}

var xmlTagRe = regexp.MustCompile(`<[^>]+>`)

func stripXMLTags(s string) string {
	return xmlTagRe.ReplaceAllString(s, "")
}

// GetSessionHistory reads the Claude Code JSONL transcript and returns user/assistant messages.
func (a *Agent) GetSessionHistory(_ context.Context, sessionID string, limit int) ([]core.HistoryEntry, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	absWorkDir, _ := filepath.Abs(a.workDir)
	projectDir := findProjectDir(homeDir, absWorkDir)
	if projectDir == "" {
		return nil, fmt.Errorf("claudecode: project dir not found")
	}

	path := filepath.Join(projectDir, sessionID+".jsonl")
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("claudecode: open session file: %w", err)
	}
	defer f.Close()

	var entries []core.HistoryEntry
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 256*1024), 256*1024)

	for scanner.Scan() {
		var raw struct {
			Type      string `json:"type"`
			Timestamp string `json:"timestamp"`
			Message   struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(scanner.Bytes(), &raw) != nil {
			continue
		}
		if raw.Type != "user" && raw.Type != "assistant" {
			continue
		}

		ts, _ := time.Parse(time.RFC3339Nano, raw.Timestamp)
		text := extractTextContent(raw.Message.Content)
		if text == "" {
			continue
		}

		entries = append(entries, core.HistoryEntry{
			Role:      raw.Type,
			Content:   text,
			Timestamp: ts,
		})
	}

	if limit > 0 && len(entries) > limit {
		entries = entries[len(entries)-limit:]
	}
	return entries, nil
}

// extractTextContent extracts readable text from Claude Code message content.
// Content can be a plain string or an array of content blocks.
func extractTextContent(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}

	// Try plain string first
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}

	// Try array of content blocks
	var blocks []struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		Thinking string `json:"thinking"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return ""
	}

	for _, b := range blocks {
		if b.Type == "text" && b.Text != "" {
			return b.Text
		}
	}
	return ""
}

func (a *Agent) Stop() error { return nil }

// SetMode changes the permission mode for future sessions.
func (a *Agent) SetMode(mode string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.mode = normalizePermissionMode(mode)
	slog.Info("claudecode: permission mode changed", "mode", a.mode)
}

// GetMode returns the current permission mode.
func (a *Agent) GetMode() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.mode
}

// PermissionModes returns all supported permission modes.
func (a *Agent) PermissionModes() []core.PermissionModeInfo {
	return []core.PermissionModeInfo{
		{Key: "default", Name: "Default", NameZh: "默认", Desc: "Ask permission for every tool call", DescZh: "每次工具调用都需确认"},
		{Key: "acceptEdits", Name: "Accept Edits", NameZh: "接受编辑", Desc: "Auto-approve file edits, ask for others", DescZh: "自动允许文件编辑，其他需确认"},
		{Key: "plan", Name: "Plan Mode", NameZh: "计划模式", Desc: "Plan only, no execution until approved", DescZh: "只做规划不执行，审批后再执行"},
		{Key: "auto", Name: "Auto", NameZh: "自动模式", Desc: "Claude decides when to ask for permission", DescZh: "由 Claude 自动判断何时需要确认"},
		{Key: "bypassPermissions", Name: "YOLO", NameZh: "YOLO 模式", Desc: "Auto-approve everything", DescZh: "全部自动通过"},
		{Key: "dontAsk", Name: "Don't Ask", NameZh: "静默拒绝", Desc: "Auto-deny tools unless pre-approved via allowed_tools or settings.json allow rules", DescZh: "未预授权的工具自动拒绝，不弹确认"},
	}
}

// AddAllowedTools adds tools to the pre-allowed list (takes effect on next session).
func (a *Agent) AddAllowedTools(tools ...string) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	existing := make(map[string]bool)
	for _, t := range a.allowedTools {
		existing[t] = true
	}
	for _, tool := range tools {
		if !existing[tool] {
			a.allowedTools = append(a.allowedTools, tool)
			existing[tool] = true
		}
	}
	slog.Info("claudecode: updated allowed tools", "tools", tools, "total", len(a.allowedTools))
	return nil
}

// GetAllowedTools returns the current list of pre-allowed tools.
func (a *Agent) GetAllowedTools() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	result := make([]string, len(a.allowedTools))
	copy(result, a.allowedTools)
	return result
}

// GetDisallowedTools returns the current list of disallowed tools.
func (a *Agent) GetDisallowedTools() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	result := make([]string, len(a.disallowedTools))
	copy(result, a.disallowedTools)
	return result
}

// ── CommandProvider implementation ────────────────────────────

func (a *Agent) CommandDirs() []string {
	absDir, err := filepath.Abs(a.workDir)
	if err != nil {
		absDir = a.workDir
	}
	dirs := []string{filepath.Join(absDir, ".claude", "commands")}
	if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(home, ".claude", "commands"))
	}
	return dirs
}

// ── SkillProvider implementation ──────────────────────────────

func (a *Agent) SkillDirs() []string {
	absDir, err := filepath.Abs(a.workDir)
	if err != nil {
		absDir = a.workDir
	}
	dirs := []string{filepath.Join(absDir, ".claude", "skills")}
	if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(home, ".claude", "skills"))
	}
	return dirs
}

// ── ContextCompressor implementation ──────────────────────────

func (a *Agent) CompressCommand() string { return "/compact" }

// ── MemoryFileProvider implementation ─────────────────────────

func (a *Agent) ProjectMemoryFile() string {
	absDir, err := filepath.Abs(a.workDir)
	if err != nil {
		absDir = a.workDir
	}
	return filepath.Join(absDir, "CLAUDE.md")
}

func (a *Agent) GlobalMemoryFile() string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(homeDir, ".claude", "CLAUDE.md")
}

func (a *Agent) HasSystemPromptSupport() bool { return true }

// ── ProviderSwitcher implementation ──────────────────────────

func (a *Agent) SetProviders(providers []core.ProviderConfig) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.providers = providers
}

func (a *Agent) SetActiveProvider(name string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.stopProviderProxyLocked()
	if name == "" {
		a.activeIdx = -1
		slog.Info("claudecode: provider cleared")
		return true
	}
	for i, p := range a.providers {
		if p.Name == name {
			a.activeIdx = i
			slog.Info("claudecode: provider switched", "provider", name)
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

// providerEnvLocked returns env vars for the active provider. Caller must hold mu.
//
// When a custom base_url is configured:
//  1. We use ANTHROPIC_AUTH_TOKEN (Bearer) instead of ANTHROPIC_API_KEY
//     (x-api-key). Claude Code validates API keys against api.anthropic.com
//     which hangs for third-party endpoints; Bearer auth skips that check.
//  2. If the provider sets thinking (e.g. "disabled"), a local reverse proxy
//     rewrites the thinking parameter for compatibility with providers that
//     don't support adaptive thinking.
func (a *Agent) providerEnvLocked() []string {
	if a.activeIdx < 0 || a.activeIdx >= len(a.providers) {
		a.stopProviderProxyLocked()
		return nil
	}
	p := a.providers[a.activeIdx]
	var env []string

	if p.BaseURL != "" {
		if p.Thinking != "" {
			if err := a.ensureProviderProxyLocked(p.BaseURL, p.Thinking); err != nil {
				slog.Error("providerproxy: failed to start", "error", err)
				env = append(env, "ANTHROPIC_BASE_URL="+p.BaseURL)
			} else {
				env = append(env, "ANTHROPIC_BASE_URL="+a.proxyLocalURL)
				env = append(env, "NO_PROXY=127.0.0.1")
			}
		} else {
			a.stopProviderProxyLocked()
			env = append(env, "ANTHROPIC_BASE_URL="+p.BaseURL)
		}
		if p.APIKey != "" {
			env = append(env, "ANTHROPIC_AUTH_TOKEN="+p.APIKey)
			env = append(env, "ANTHROPIC_API_KEY=")
		}
	} else {
		a.stopProviderProxyLocked()
		if p.APIKey != "" {
			env = append(env, "ANTHROPIC_API_KEY="+p.APIKey)
		}
	}

	for k, v := range p.Env {
		env = append(env, k+"="+v)
	}
	return env
}

func (a *Agent) ensureProviderProxyLocked(targetURL, thinkingOverride string) error {
	if a.providerProxy != nil && a.proxyLocalURL != "" {
		return nil
	}
	a.stopProviderProxyLocked()
	proxy, localURL, err := core.NewProviderProxy(targetURL, thinkingOverride)
	if err != nil {
		return err
	}
	a.providerProxy = proxy
	a.proxyLocalURL = localURL
	return nil
}

func (a *Agent) stopProviderProxyLocked() {
	if a.providerProxy != nil {
		a.providerProxy.Close()
		a.providerProxy = nil
		a.proxyLocalURL = ""
	}
}

// summarizeInput produces a short human-readable description of tool input.
func summarizeInput(tool string, input any) string {
	m, ok := input.(map[string]any)
	if !ok {
		return ""
	}

	switch tool {
	case "Read", "Edit", "Write":
		if fp, ok := m["file_path"].(string); ok {
			return fp
		}
	case "Bash":
		if cmd, ok := m["command"].(string); ok {
			return cmd
		}
	case "Grep":
		if p, ok := m["pattern"].(string); ok {
			return p
		}
	case "Glob":
		if p, ok := m["pattern"].(string); ok {
			return p
		}
		if p, ok := m["glob_pattern"].(string); ok {
			return p
		}
	}

	b, err := json.Marshal(m)
	if err != nil {
		return ""
	}
	return string(b)
}

// parseUserQuestions extracts structured questions from AskUserQuestion input.
func parseUserQuestions(input map[string]any) []core.UserQuestion {
	questionsRaw, ok := input["questions"].([]any)
	if !ok || len(questionsRaw) == 0 {
		return nil
	}
	var questions []core.UserQuestion
	for _, qRaw := range questionsRaw {
		qMap, ok := qRaw.(map[string]any)
		if !ok {
			continue
		}
		q := core.UserQuestion{
			Question:    strVal(qMap, "question"),
			Header:      strVal(qMap, "header"),
			MultiSelect: boolVal(qMap, "multiSelect"),
		}
		if optsRaw, ok := qMap["options"].([]any); ok {
			for _, oRaw := range optsRaw {
				oMap, ok := oRaw.(map[string]any)
				if !ok {
					continue
				}
				q.Options = append(q.Options, core.UserQuestionOption{
					Label:       strVal(oMap, "label"),
					Description: strVal(oMap, "description"),
				})
			}
		}
		if q.Question != "" {
			questions = append(questions, q)
		}
	}
	return questions
}

func strVal(m map[string]any, key string) string {
	v, _ := m[key].(string)
	return v
}

func boolVal(m map[string]any, key string) bool {
	v, _ := m[key].(bool)
	return v
}

// encodeClaudeProjectKey converts an absolute path to Claude Code's project key format.
// Claude Code encodes paths by:
// 1. Replacing path separators (/ or \) with "-"
// 2. Replacing colons (:) with "-" (Windows drive letters)
// 3. Replacing underscores (_) with "-"
// 4. Replacing all non-ASCII characters with "-"
func encodeClaudeProjectKey(absPath string) string {
	// First, normalize to forward slashes for consistent processing
	normalized := strings.ReplaceAll(absPath, "\\", "/")

	// Build the encoded key character by character
	var result strings.Builder
	for _, r := range normalized {
		if r == '/' || r == ':' || r == '_' {
			result.WriteRune('-')
		} else if r < 128 { // ASCII range (0-127)
			result.WriteRune(r)
		} else {
			// Non-ASCII characters become hyphens
			result.WriteRune('-')
		}
	}
	return result.String()
}

// findProjectDir locates the Claude Code session directory for a given work dir.
// Claude Code stores sessions at ~/.claude/projects/{projectKey}/ where projectKey
// is derived from the absolute path. On Windows, the key format may vary (colon
// handling, slash direction), so we try multiple key candidates and fall back to
// scanning the projects directory.
func findProjectDir(homeDir, absWorkDir string) string {
	projectsBase := filepath.Join(homeDir, ".claude", "projects")

	// Build candidate keys: different ways Claude Code might encode the path.
	// Primary encoding: Claude Code's actual algorithm (non-ASCII → "-")
	candidates := []string{
		encodeClaudeProjectKey(absWorkDir),
		// Legacy candidates for backward compatibility
		strings.ReplaceAll(absWorkDir, string(filepath.Separator), "-"),
		strings.NewReplacer("/", "-", "\\", "-", ":", "-").Replace(absWorkDir),
		strings.NewReplacer("/", "-", "\\", "-", ":", "-", "_", "-").Replace(absWorkDir),
	}
	// Also try with forward slashes (config might use forward slashes on Windows)
	fwd := strings.ReplaceAll(absWorkDir, "\\", "/")
	candidates = append(candidates, strings.ReplaceAll(fwd, "/", "-"))

	for _, key := range candidates {
		dir := filepath.Join(projectsBase, key)
		if _, err := os.Stat(dir); err == nil {
			return dir
		}
	}

	// Fallback: scan the projects directory and find a match by
	// comparing the encoded path (handles variations in encoding).
	entries, err := os.ReadDir(projectsBase)
	if err != nil {
		return ""
	}

	// Use the primary encoding for comparison
	encodedWorkDir := encodeClaudeProjectKey(absWorkDir)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		// Direct match with encoded key
		if entry.Name() == encodedWorkDir {
			return filepath.Join(projectsBase, entry.Name())
		}
		// Case-insensitive match for Windows compatibility
		if strings.EqualFold(entry.Name(), encodedWorkDir) {
			return filepath.Join(projectsBase, entry.Name())
		}
	}

	return ""
}
