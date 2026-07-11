package copilot

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func init() {
	core.RegisterAgent("copilot", New)
}

// Agent drives GitHub Copilot CLI using --headless --stdio --no-auto-update
// for persistent JSON-RPC 2.0 communication over Content-Length framed stdio.
//
// Permission modes:
//   - "default":           every tool call requires user approval
//   - "bypassPermissions": auto-approve everything (alias: yolo)
type Agent struct {
	workDir      string
	cmd          string   // CLI binary name (default: "copilot")
	cliExtraArgs []string // extra args from cmd after the binary name
	configEnv    []string // env vars from [projects.agent.options.env]
	model        string
	mode         string // "default" | "bypassPermissions"
	providers    []core.ProviderConfig
	activeIdx    int // -1 = no provider set
	sessionEnv   []string

	mu sync.RWMutex
}

func New(opts map[string]any) (core.Agent, error) {
	workDir, _ := opts["work_dir"].(string)
	if workDir == "" {
		workDir = "."
	}
	cmd, extraArgs := core.ParseCmdOpts(opts, "copilot")
	model, _ := opts["model"].(string)
	mode, _ := opts["mode"].(string)
	mode = normalizeMode(mode)

	if _, err := exec.LookPath(cmd); err != nil {
		return nil, fmt.Errorf("copilot: %q CLI not found in PATH, please install it first", cmd)
	}

	return &Agent{
		workDir:      workDir,
		cmd:          cmd,
		cliExtraArgs: extraArgs,
		configEnv:    core.ParseConfigEnv(opts),
		model:        model,
		mode:         mode,
		activeIdx:    -1,
	}, nil
}

func normalizeMode(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "bypasspermissions", "bypass-permissions", "bypass_permissions", "yolo":
		return "bypassPermissions"
	default:
		return "default"
	}
}

func (a *Agent) Name() string           { return "copilot" }
func (a *Agent) CLIBinaryName() string  { return a.cmd }
func (a *Agent) CLIDisplayName() string { return "GitHub Copilot" }

func (a *Agent) SetWorkDir(dir string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.workDir = dir
	slog.Info("copilot: work_dir changed", "work_dir", dir)
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
	slog.Info("copilot: model changed", "model", model)
}

func (a *Agent) GetModel() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return core.GetProviderModel(a.providers, a.activeIdx, a.model)
}

func (a *Agent) AvailableModels(_ context.Context) []core.ModelOption {
	if models := a.configuredModels(); len(models) > 0 {
		return models
	}
	return []core.ModelOption{
		{Name: "gpt-4.1", Desc: "GPT-4.1"},
		{Name: "claude-sonnet-4.6", Desc: "Claude Sonnet 4.6"},
		{Name: "o3", Desc: "O3"},
	}
}

func (a *Agent) configuredModels() []core.ModelOption {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return core.GetProviderModels(a.providers, a.activeIdx)
}

func (a *Agent) SetSessionEnv(env []string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sessionEnv = env
}

func (a *Agent) SetMode(mode string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.mode = normalizeMode(mode)
	slog.Info("copilot: permission mode changed", "mode", a.mode)
}

func (a *Agent) GetMode() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.mode
}

func (a *Agent) PermissionModes() []core.PermissionModeInfo {
	return []core.PermissionModeInfo{
		{Key: "default", Name: "Default", NameZh: "默认", Desc: "Ask permission for every tool call", DescZh: "每次工具调用都需确认"},
		{Key: "bypassPermissions", Name: "YOLO", NameZh: "YOLO 模式", Desc: "Auto-approve everything", DescZh: "全部自动通过"},
	}
}

// StartSession creates a persistent interactive Copilot session.
func (a *Agent) StartSession(ctx context.Context, sessionID string) (core.AgentSession, error) {
	a.mu.RLock()
	model := a.model
	mode := a.mode
	workDir := a.workDir
	cmd := a.cmd
	extraArgs := append([]string{}, a.cliExtraArgs...)
	extraEnv := append([]string(nil), a.configEnv...)
	extraEnv = append(extraEnv, a.providerEnvLocked()...)
	extraEnv = append(extraEnv, a.sessionEnv...)
	provider := a.providerConfigLocked()
	if a.activeIdx >= 0 && a.activeIdx < len(a.providers) {
		if m := a.providers[a.activeIdx].Model; m != "" {
			model = m
		}
	}
	a.mu.RUnlock()

	return newCopilotSession(ctx, workDir, cmd, extraArgs, model, mode, sessionID, extraEnv, provider)
}

// listSessionsProbeTimeout bounds how long we wait for a session.list probe.
var listSessionsProbeTimeout = 15 * time.Second

// copilotSessionMetadata represents one session entry returned by session.list.
type copilotSessionMetadata struct {
	SessionID    string  `json:"sessionId"`
	StartTime    string  `json:"startTime"`
	ModifiedTime string  `json:"modifiedTime"`
	Summary      *string `json:"summary,omitempty"`
}

// copilotListSessionsResponse is the session.list response payload.
type copilotListSessionsResponse struct {
	Sessions []copilotSessionMetadata `json:"sessions"`
}

// copilotDeleteSessionResponse is the session.delete response payload.
type copilotDeleteSessionResponse struct {
	Success bool    `json:"success"`
	Error   *string `json:"error,omitempty"`
}

// probeSession is a short-lived copilot probe process with a managed read loop.
type probeSession struct {
	rpc    *rpcClient
	cancel context.CancelFunc
	done   chan struct{}
}

type probeSnapshot struct {
	cmd  string
	workDir string
	env     []string
}

// newProbeSession spawns a copilot --headless --stdio probe process, starts a
// read loop, and returns a probeSession. Caller must call close() when done.
func newProbeSession(ctx context.Context, snapshot probeSnapshot) (*probeSession, error) {
	probeCtx, cancel := context.WithCancel(ctx)

	cmd := exec.CommandContext(probeCtx, snapshot.cmd, "--headless", "--stdio", "--no-auto-update")
	cmd.Dir = snapshot.workDir
	cmd.Env = snapshot.env

	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("copilot probe: stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("copilot probe: stdout pipe: %w", err)
	}
	cmd.Stderr = io.Discard

	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("copilot probe: start: %w", err)
	}

	rpc := newRPCClient(stdin)
	reader := newLSPReader(stdout)
	done := make(chan struct{})

	go func() {
		defer func() {
			_ = stdin.Close()
			_ = cmd.Wait()
			close(done)
		}()
		for {
			body, err := reader.readMessage()
			if err != nil {
				return
			}
			var resp jsonRPCResponse
			if json.Unmarshal(body, &resp) == nil &&
				len(resp.ID) > 0 && string(resp.ID) != "null" {
				rpc.dispatch(&resp)
			}
		}
	}()

	return &probeSession{rpc: rpc, cancel: cancel, done: done}, nil
}

// call sends a JSON-RPC request and blocks until a response or ctx expires.
func (ps *probeSession) call(ctx context.Context, method string, params any) (*jsonRPCResponse, error) {
	_, ch := ps.rpc.call(method, params)
	select {
	case resp := <-ch:
		return resp, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// close cancels the probe context, terminating the process and read loop.
func (ps *probeSession) close() {
	ps.cancel()
	<-ps.done
}

// ListSessions returns past sessions by spawning a short-lived copilot probe
// that performs ping + session.list, then exits. Returns nil gracefully if
// the binary is missing or the RPC fails.
func (a *Agent) ListSessions(ctx context.Context) ([]core.AgentSessionInfo, error) {
	a.mu.RLock()
	snapshot := a.probeSnapshotLocked()
	a.mu.RUnlock()

	if _, err := exec.LookPath(snapshot.cmd); err != nil {
		return nil, nil
	}

	probeCtx, cancel := context.WithTimeout(ctx, listSessionsProbeTimeout)
	defer cancel()

	probe, err := newProbeSession(probeCtx, snapshot)
	if err != nil {
		slog.Debug("copilot: ListSessions probe spawn failed", "error", err)
		return nil, nil
	}
	defer probe.close()

	// Ping
	pingResp, err := probe.call(probeCtx, "ping", nil)
	if err != nil || pingResp.Error != nil {
		slog.Debug("copilot: ListSessions ping failed", "error", err)
		return nil, nil
	}

	// session.list
	listResp, err := probe.call(probeCtx, "session.list", map[string]any{})
	if err != nil {
		return nil, nil
	}
	if listResp.Error != nil {
		slog.Debug("copilot: session.list RPC error", "code", listResp.Error.Code, "msg", listResp.Error.Message)
		return nil, nil
	}

	var result copilotListSessionsResponse
	if err := json.Unmarshal(listResp.Result, &result); err != nil {
		slog.Debug("copilot: session.list parse error", "error", err)
		return nil, nil
	}

	out := make([]core.AgentSessionInfo, 0, len(result.Sessions))
	for _, s := range result.Sessions {
		info := core.AgentSessionInfo{ID: s.SessionID}
		if s.Summary != nil {
			info.Summary = *s.Summary
		}
		if t, err := time.Parse(time.RFC3339, s.ModifiedTime); err == nil {
			info.ModifiedAt = t
		} else if t, err := time.Parse(time.RFC3339Nano, s.ModifiedTime); err == nil {
			info.ModifiedAt = t
		}
		out = append(out, info)
	}
	slog.Info("copilot: ListSessions", "count", len(out))
	return out, nil
}

// WorkspaceAgentOptions implements core.WorkspaceSnapshotter.
// Returns the options needed to recreate an equivalent session for workspace reuse.
func (a *Agent) WorkspaceAgentOptions() map[string]any {
	a.mu.RLock()
	defer a.mu.RUnlock()

	opts := map[string]any{
		"mode": a.mode,
	}
	if a.model != "" {
		opts["model"] = a.model
	}
	if a.cmd != "copilot" && a.cmd != "" {
		opts["cmd"] = a.cmd
	}
	return opts
}

func (a *Agent) Stop() error { return nil }

// DeleteSession implements core.SessionDeleter by calling session.delete
// via a short-lived probe process. Returns nil gracefully if the binary is
// missing or the RPC is unsupported.
func (a *Agent) DeleteSession(ctx context.Context, sessionID string) error {
	if sessionID == "" {
		return nil
	}

	a.mu.RLock()
	snapshot := a.probeSnapshotLocked()
	a.mu.RUnlock()

	if _, err := exec.LookPath(snapshot.cmd); err != nil {
		return nil
	}

	probeCtx, cancel := context.WithTimeout(ctx, listSessionsProbeTimeout)
	defer cancel()

	probe, err := newProbeSession(probeCtx, snapshot)
	if err != nil {
		slog.Debug("copilot: DeleteSession probe spawn failed", "error", err)
		return nil
	}
	defer probe.close()

	pingResp, err := probe.call(probeCtx, "ping", nil)
	if err != nil || pingResp.Error != nil {
		slog.Debug("copilot: DeleteSession ping failed", "error", err)
		return nil
	}

	delResp, err := probe.call(probeCtx, "session.delete", map[string]any{"sessionId": sessionID})
	if err != nil {
		return nil
	}

	if delResp.Error != nil {
		// method-not-found or invalid-request means unsupported
		if delResp.Error.Code == -32601 || delResp.Error.Code == -32600 {
			return nil
		}
		return fmt.Errorf("copilot: session.delete: %s", delResp.Error.Message)
	}

	var result copilotDeleteSessionResponse
	if err := json.Unmarshal(delResp.Result, &result); err != nil {
		// Ignore parse errors - treat as success
		return nil
	}
	if !result.Success {
		if result.Error != nil {
			return fmt.Errorf("copilot: session.delete failed: %s", *result.Error)
		}
		return fmt.Errorf("copilot: session.delete failed: unknown error")
	}
	slog.Info("copilot: session deleted", "sessionId", sessionID)
	return nil
}

// GetSessionHistory implements core.HistoryProvider.
// Copilot does not expose a history RPC; return empty gracefully.
func (a *Agent) GetSessionHistory(_ context.Context, _ string, _ int) ([]core.HistoryEntry, error) {
	return nil, nil
}

// CompressCommand implements core.ContextCompressor.
// Copilot has no built-in compact/compress command.
func (a *Agent) CompressCommand() string { return "" }

// ── ProviderSwitcher implementation ──────────────────────────

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
		slog.Info("copilot: provider cleared")
		return true
	}
	for i, p := range a.providers {
		if p.Name == name {
			a.activeIdx = i
			slog.Info("copilot: provider switched", "provider", name)
			return true
		}
	}
	return false
}

func (a *Agent) GetActiveProvider() *core.ProviderConfig {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.activeIdx < 0 || a.activeIdx >= len(a.providers) {
		return nil
	}
	p := a.providers[a.activeIdx]
	return &p
}

func (a *Agent) ListProviders() []core.ProviderConfig {
	a.mu.RLock()
	defer a.mu.RUnlock()
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
	if p.BaseURL != "" {
		env = append(env, "COPILOT_PROVIDER_BASE_URL="+p.BaseURL)
	}
	if p.APIKey != "" {
		env = append(env, "COPILOT_PROVIDER_API_KEY="+p.APIKey)
	}
	if p.Model != "" {
		env = append(env, "COPILOT_MODEL="+p.Model)
	}
	if p.CodexWireAPI != "" {
		env = append(env, "COPILOT_PROVIDER_WIRE_API="+p.CodexWireAPI)
	}
	for k, v := range p.Env {
		env = append(env, k+"="+v)
	}
	return env
}

func (a *Agent) probeSnapshotLocked() probeSnapshot {
	env := os.Environ()
	if extraEnv := a.providerEnvLocked(); len(extraEnv) > 0 {
		env = core.MergeEnv(env, extraEnv)
	}
	if len(a.sessionEnv) > 0 {
		env = core.MergeEnv(env, a.sessionEnv)
	}
	return probeSnapshot{cmd: a.cmd, workDir: a.workDir, env: env}
}

func (a *Agent) providerConfigLocked() *copilotWireProviderConfig {
	if a.activeIdx < 0 || a.activeIdx >= len(a.providers) {
		return nil
	}
	p := a.providers[a.activeIdx]
	if p.BaseURL == "" {
		return nil
	}
	provider := &copilotWireProviderConfig{
		Type:    "openai",
		BaseURL: p.BaseURL,
		APIKey:  p.APIKey,
		ModelID: p.Model,
		Headers: p.CodexHTTPHeaders,
		WireAPI: p.CodexWireAPI,
	}
	if typ := strings.TrimSpace(p.Env["COPILOT_PROVIDER_TYPE"]); typ != "" {
		provider.Type = typ
	}
	if wireModel := strings.TrimSpace(p.Env["COPILOT_PROVIDER_WIRE_MODEL"]); wireModel != "" {
		provider.WireModel = wireModel
	}
	if bearer := strings.TrimSpace(p.Env["COPILOT_PROVIDER_BEARER_TOKEN"]); bearer != "" {
		provider.BearerToken = bearer
	}
	return provider
}

// Compile-time interface assertions.
var (
	_ core.Agent                           = (*Agent)(nil)
	_ core.AgentDoctorInfo                 = (*Agent)(nil)
	_ core.WorkspaceAgentOptionSnapshotter = (*Agent)(nil)
	_ core.SessionDeleter                  = (*Agent)(nil)
	_ core.HistoryProvider                 = (*Agent)(nil)
	_ core.ContextCompressor               = (*Agent)(nil)
	_ core.ProviderSwitcher                = (*Agent)(nil)
)
