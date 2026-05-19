package cursorsdk

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func init() {
	core.RegisterAgent("cursor_sdk", New)
}

type Agent struct {
	workDir     string
	model       string
	mode        string
	sidecarCmd  string
	sidecarArgs []string
	apiKey      string

	turnTimeout time.Duration
	idleTTL     time.Duration

	mu         sync.RWMutex
	sessionEnv []string
	clients    map[string]*sidecarClient
}

func New(opts map[string]any) (core.Agent, error) {
	workDir := stringOpt(opts, "work_dir", ".")
	model := stringOpt(opts, "model", "")
	mode := stringOpt(opts, "mode", "")
	cmd := stringOpt(opts, "sidecar_cmd", "node")
	if _, err := exec.LookPath(cmd); err != nil {
		return nil, fmt.Errorf("cursor_sdk: sidecar command %q not found: %w", cmd, err)
	}

	_, customSidecarScript := opts["sidecar_script"]
	script := stringOpt(opts, "sidecar_script", defaultSidecarScript())
	absScript, err := filepath.Abs(script)
	if err != nil {
		return nil, fmt.Errorf("cursor_sdk: sidecar script path: %w", err)
	}
	if !customSidecarScript {
		sidecarRoot := filepath.Dir(absScript)
		sdkPkg := filepath.Join(sidecarRoot, "node_modules", "@cursor", "sdk", "package.json")
		if _, statErr := os.Stat(sdkPkg); statErr != nil {
			return nil, fmt.Errorf("cursor_sdk: sidecar dependency @cursor/sdk not installed (%w); run once: cd %q && npm install", statErr, sidecarRoot)
		}
	}
	args := []string{absScript}
	if extra := strings.TrimSpace(stringOpt(opts, "sidecar_args", "")); extra != "" {
		args = append(args, strings.Fields(extra)...)
	}

	timeout := secondsOpt(opts, "turn_timeout_seconds", 0)
	idleTTL := minutesOpt(opts, "idle_ttl_minutes", 0)
	apiKey := stringOpt(opts, "api_key", "")
	if apiKey == "" && strings.TrimSpace(os.Getenv("CURSOR_API_KEY")) == "" {
		return nil, fmt.Errorf("cursor_sdk: CURSOR_API_KEY or api_key is required; Cursor CLI login auth is only supported by the classic cursor adapter")
	}
	return &Agent{
		workDir:     workDir,
		model:       model,
		mode:        mode,
		sidecarCmd:  cmd,
		sidecarArgs: args,
		apiKey:      apiKey,
		turnTimeout: timeout,
		idleTTL:     idleTTL,
		clients:     make(map[string]*sidecarClient),
	}, nil
}

func defaultSidecarScript() string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "agent/cursorsdk/sidecar/index.mjs"
	}
	return filepath.Join(filepath.Dir(file), "sidecar", "index.mjs")
}

func stringOpt(opts map[string]any, key, fallback string) string {
	v, ok := opts[key]
	if !ok || v == nil {
		return fallback
	}
	switch x := v.(type) {
	case string:
		if strings.TrimSpace(x) == "" {
			return fallback
		}
		return x
	default:
		s := fmt.Sprint(x)
		if strings.TrimSpace(s) == "" {
			return fallback
		}
		return s
	}
}

func secondsOpt(opts map[string]any, key string, fallback int) time.Duration {
	v, ok := opts[key]
	if !ok || v == nil {
		if fallback <= 0 {
			return 0
		}
		return time.Duration(fallback) * time.Second
	}
	switch x := v.(type) {
	case int:
		if x <= 0 {
			return 0
		}
		return time.Duration(x) * time.Second
	case int64:
		if x <= 0 {
			return 0
		}
		return time.Duration(x) * time.Second
	case float64:
		if x <= 0 {
			return 0
		}
		return time.Duration(x) * time.Second
	case string:
		n, _ := strconv.Atoi(strings.TrimSpace(x))
		if n <= 0 {
			return 0
		}
		return time.Duration(n) * time.Second
	default:
		return 0
	}
}

func minutesOpt(opts map[string]any, key string, fallback int) time.Duration {
	seconds := secondsOpt(opts, key, fallback)
	if seconds <= 0 {
		return 0
	}
	return seconds * time.Minute / time.Second
}

func (a *Agent) Name() string { return "cursor_sdk" }

func (a *Agent) SetWorkDir(dir string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.workDir = dir
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
}

func (a *Agent) GetModel() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.model
}

func (a *Agent) SetSessionEnv(env []string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sessionEnv = append([]string(nil), env...)
}

func (a *Agent) StartSession(ctx context.Context, sessionID string) (core.AgentSession, error) {
	a.mu.RLock()
	env := append([]string(nil), a.sessionEnv...)
	a.mu.RUnlock()
	return a.StartSessionWithEnv(ctx, sessionID, env)
}

func (a *Agent) StartSessionWithEnv(ctx context.Context, sessionID string, env []string) (core.AgentSession, error) {
	a.mu.Lock()
	workDir := a.workDir
	model := a.model
	mode := a.mode
	apiKey := a.apiKey
	timeout := a.turnTimeout
	idleTTL := a.idleTTL
	env = append([]string(nil), env...)
	poolKey := sidecarPoolKey(env, sessionID)
	client, err := a.ensureClientLocked(ctx, poolKey, env)
	a.mu.Unlock()
	if err != nil {
		return nil, err
	}
	sessionKey := sessionKeyFromEnv(env)
	return newSession(client, workDir, sessionKey, model, mode, apiKey, sessionID, timeout, idleTTL), nil
}

func sidecarPoolKey(env []string, sessionID string) string {
	for _, kv := range env {
		if key, value, ok := strings.Cut(kv, "="); ok && key == "CC_SESSION_KEY" && strings.TrimSpace(value) != "" {
			if userKey := userPoolKeyFromSessionKey(value); userKey != "" {
				return userKey
			}
			return "session:" + shortHash(value)
		}
	}
	if strings.TrimSpace(sessionID) != "" && sessionID != core.ContinueSession {
		return "agent:" + shortHash(sessionID)
	}
	return "default"
}

func sessionKeyFromEnv(env []string) string {
	for _, kv := range env {
		if key, value, ok := strings.Cut(kv, "="); ok && key == "CC_SESSION_KEY" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func userPoolKeyFromSessionKey(sessionKey string) string {
	parts := strings.Split(sessionKey, ":")
	if len(parts) < 3 || parts[0] == "relay" {
		return ""
	}
	userID := strings.TrimSpace(parts[len(parts)-1])
	if userID == "" {
		return ""
	}
	return "user:" + shortHash(parts[0]+":"+userID)
}

func shortHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:16]
}

func (a *Agent) ensureClientLocked(ctx context.Context, poolKey string, env []string) (*sidecarClient, error) {
	if a.clients == nil {
		a.clients = make(map[string]*sidecarClient)
	}
	if c := a.clients[poolKey]; c != nil && c.alive.Load() {
		return c, nil
	}
	delete(a.clients, poolKey)
	c, err := startSidecar(ctx, a.sidecarCmd, a.sidecarArgs, env)
	if err != nil {
		return nil, err
	}
	a.clients[poolKey] = c
	return c, nil
}

func (a *Agent) ListSessions(ctx context.Context) ([]core.AgentSessionInfo, error) {
	a.mu.Lock()
	if len(a.clients) == 0 {
		env := append([]string(nil), a.sessionEnv...)
		if _, err := a.ensureClientLocked(ctx, sidecarPoolKey(env, ""), env); err != nil {
			a.mu.Unlock()
			return nil, err
		}
	}
	clients := make([]*sidecarClient, 0, len(a.clients))
	for key, client := range a.clients {
		if client != nil && client.alive.Load() {
			clients = append(clients, client)
		} else {
			delete(a.clients, key)
		}
	}
	a.mu.Unlock()

	var infos []core.AgentSessionInfo
	for _, client := range clients {
		ch, err := client.call(map[string]any{"op": "list"})
		if err != nil {
			return nil, err
		}
		select {
		case msg, ok := <-ch:
			if !ok {
				return nil, fmt.Errorf("cursor_sdk: sidecar closed")
			}
			for _, s := range msg.Sessions {
				if s.SessionID != "" {
					infos = append(infos, core.AgentSessionInfo{ID: s.SessionID})
				}
			}
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(5 * time.Second):
			return nil, fmt.Errorf("cursor_sdk: list sessions timed out")
		}
	}
	return infos, nil
}

func (a *Agent) Stop() error {
	a.mu.Lock()
	clients := make([]*sidecarClient, 0, len(a.clients))
	for _, client := range a.clients {
		if client != nil {
			clients = append(clients, client)
		}
	}
	a.clients = make(map[string]*sidecarClient)
	a.mu.Unlock()
	var firstErr error
	for _, client := range clients {
		if err := client.close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

type sidecarClient struct {
	ctx    context.Context
	cancel context.CancelFunc
	cmd    *exec.Cmd
	stdin  io.WriteCloser

	mu      sync.Mutex
	nextID  atomic.Uint64
	pending map[string]chan sidecarMessage
	alive   atomic.Bool
}

type sidecarMessage struct {
	ID           string              `json:"id"`
	Event        string              `json:"event"`
	SessionID    string              `json:"sessionId"`
	RunID        string              `json:"runId"`
	Text         string              `json:"text"`
	Error        string              `json:"error"`
	ToolName     string              `json:"toolName"`
	ToolInput    string              `json:"toolInput"`
	InputTokens  int                 `json:"inputTokens"`
	OutputTokens int                 `json:"outputTokens"`
	Sessions     []sidecarSessionRef `json:"sessions"`
}

type sidecarSessionRef struct {
	SessionID string `json:"sessionId"`
}

func startSidecar(ctx context.Context, cmdName string, args []string, extraEnv []string) (*sidecarClient, error) {
	ctx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(ctx, cmdName, args...)
	if len(args) > 0 && strings.HasSuffix(args[0], ".mjs") {
		if d := filepath.Dir(args[0]); d != "" && d != "." {
			cmd.Dir = d
		}
	}
	cmd.Env = core.MergeEnv(os.Environ(), extraEnv)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("cursor_sdk: stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("cursor_sdk: stdout pipe: %w", err)
	}
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("cursor_sdk: start sidecar: %w", err)
	}
	c := &sidecarClient{
		ctx:     ctx,
		cancel:  cancel,
		cmd:     cmd,
		stdin:   stdin,
		pending: make(map[string]chan sidecarMessage),
	}
	c.alive.Store(true)
	go c.readLoop(stdout)
	go func() {
		if err := cmd.Wait(); err != nil && ctx.Err() == nil {
			slog.Error("cursor_sdk: sidecar exited", "error", err)
		}
		c.failAll(fmt.Errorf("sidecar exited"))
		c.alive.Store(false)
	}()
	return c, nil
}

func (c *sidecarClient) call(req map[string]any) (<-chan sidecarMessage, error) {
	if !c.alive.Load() {
		return nil, fmt.Errorf("cursor_sdk: sidecar is not running")
	}
	id := fmt.Sprintf("%d", c.nextID.Add(1))
	req["id"] = id
	ch := make(chan sidecarMessage, 64)

	line, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.pending[id] = ch
	_, err = c.stdin.Write(append(line, '\n'))
	if err != nil {
		delete(c.pending, id)
		close(ch)
	}
	c.mu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("cursor_sdk: write sidecar request: %w", err)
	}
	return ch, nil
}

func (c *sidecarClient) readLoop(stdout io.Reader) {
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	for scanner.Scan() {
		var msg sidecarMessage
		if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
			slog.Warn("cursor_sdk: invalid sidecar json", "error", err)
			continue
		}
		c.mu.Lock()
		ch := c.pending[msg.ID]
		c.mu.Unlock()
		if ch == nil {
			continue
		}
		ch <- msg
		if msg.Event == "result" || msg.Event == "error" || msg.Event == "closed" || msg.Event == "list" || msg.Event == "cancelled" {
			c.mu.Lock()
			delete(c.pending, msg.ID)
			c.mu.Unlock()
			close(ch)
		}
	}
	if err := scanner.Err(); err != nil {
		slog.Error("cursor_sdk: sidecar scanner failed", "error", err)
	}
	c.failAll(fmt.Errorf("sidecar output closed"))
}

func (c *sidecarClient) failAll(err error) {
	c.mu.Lock()
	pending := c.pending
	c.pending = make(map[string]chan sidecarMessage)
	c.mu.Unlock()
	for _, ch := range pending {
		ch <- sidecarMessage{Event: "error", Error: err.Error()}
		close(ch)
	}
}

func (c *sidecarClient) close() error {
	c.cancel()
	if c.stdin != nil {
		_ = c.stdin.Close()
	}
	c.alive.Store(false)
	return nil
}
