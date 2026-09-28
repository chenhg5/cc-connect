package opencode

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// Server transport
// ---------------
//
// The default transport runs one `opencode run --format json` process per turn
// (see session.go). That model cannot inject a message into a turn that is
// already in flight: sending again starts a *second* `run` process, which
// OpenCode treats as a competing run in the same session and which pre-empts the
// first one (its in-flight tool call is interrupted), so `/ps` loses the work
// already underway.
//
// The server transport instead drives a long-lived `opencode serve` instance
// over its HTTP API. A prompt posted to a running session is appended to that
// session's live loop and is picked up by the model at the next step boundary —
// the in-flight step completes normally and the supplement becomes additional
// guidance for the same turn. This is what `/ps` (a.k.a. `/btw`) has always
// promised ("inject messages into busy sessions without interrupting", #138).
//
// Selected with `opencode_transport = "server"` in [projects.agent.options].
const (
	opencodeTransportRun    = "run"
	opencodeTransportServer = "server"

	// opencodeServerUser is the fixed username OpenCode's server expects for
	// HTTP basic auth when OPENCODE_SERVER_PASSWORD is set.
	opencodeServerUser = "opencode"

	opencodeServerStartTimeout = 30 * time.Second
	opencodeServerRetryDelay   = time.Second
	// opencodeServerDefaultIdleTTL is how long a released server is kept for a
	// follow-up turn before its process is stopped. opencode_server_idle_ttl
	// overrides it per project.
	opencodeServerDefaultIdleTTL = 10 * time.Minute
	// opencodeServerIdleTTLDisabled marks a project that turned idle recycling
	// off: released servers then live until the daemon stops or replaces them.
	opencodeServerIdleTTLDisabled = time.Duration(-1)
	opencodeServerStopTimeout     = 5 * time.Second
	// streamReadyTimeout bounds how long a turn waits for the event stream before
	// posting its prompt.
	streamReadyTimeout = 3 * time.Second
	// opencodeServerLogTail bounds how much server output we keep for error
	// messages (a page of text is plenty for start failures).
	opencodeServerLogTail = 8 * 1024
)

// opencodeServeConfig describes the `opencode serve` process backing one
// workspace.
type opencodeServeConfig struct {
	cmd       string
	extraArgs []string
	workDir   string
	// extraEnv is merged into the server process environment when it starts and
	// is frozen from then on (project config, provider credentials, per-session
	// CC_PROJECT/CC_SESSION_KEY/CC_DATA_DIR). It is part of serverKey, so a
	// server is only ever shared by sessions that injected the same
	// environment — see serverKey.
	extraEnv []string
	// providerScope identifies the credentials this server was started with (the
	// active provider's name and key). OpenCode resolves the config's
	// {env:ANTHROPIC_API_KEY} placeholder once, when the process starts, so a
	// server may only be shared by sessions using the same provider — see
	// serverKey.
	providerScope string
	// stallTimeout is how long a turn may stay silent before the watchdog aborts
	// it (0 = use the default). Not part of serverKey: it does not affect the
	// server process itself.
	stallTimeout time.Duration
	// serverIdleTTL is how long a released server is kept for a follow-up turn
	// before its process is stopped (0 = use the default; negative = keep it for
	// the life of the daemon). Not part of serverKey: it does not affect the
	// process itself, only when it is reaped, so a shared server uses the value
	// of the project that started it.
	serverIdleTTL time.Duration
}

// opencodeServer is a ref-counted `opencode serve` process for one
// (cmd, workDir) pair. Multiple sessions in the same workspace share it.
type opencodeServer struct {
	baseURL  string
	password string
	key      string

	mu        sync.Mutex
	cmd       *exec.Cmd
	refs      int
	idleTTL   time.Duration // <=0 keeps a released server forever
	idleTimer *time.Timer
	stopped   bool
	logTail   *tailBuffer
	waitDone  chan struct{}
	exited    atomic.Bool
}

// isExited reports whether the process is gone (killed, crashed or stopped by
// us). A session that finds its server exited re-acquires a fresh one instead of
// retrying against a dead port forever.
func (srv *opencodeServer) isExited() bool {
	if srv == nil {
		return true
	}
	srv.mu.Lock()
	stopped := srv.stopped
	srv.mu.Unlock()
	return stopped || srv.exited.Load()
}

var (
	opencodeServersMu sync.Mutex
	opencodeServers   = map[string]*opencodeServer{}
	// opencodeStartMu serializes startups per key, so two sessions created at the
	// same moment in one workspace share a single `opencode serve` instead of
	// racing to start one each. Entries are tiny and bounded by the number of
	// workspaces, so they are kept for the process lifetime.
	opencodeStartMu = map[string]*sync.Mutex{}
)

// startLock returns the per-key startup mutex.
func startLock(key string) *sync.Mutex {
	opencodeServersMu.Lock()
	defer opencodeServersMu.Unlock()
	mu, ok := opencodeStartMu[key]
	if !ok {
		mu = &sync.Mutex{}
		opencodeStartMu[key] = mu
	}
	return mu
}

// liveServer returns a running server for key, if one is registered.
func liveServer(key string) *opencodeServer {
	opencodeServersMu.Lock()
	srv := opencodeServers[key]
	opencodeServersMu.Unlock()
	if srv == nil || srv.isExited() {
		return nil
	}
	return srv
}

// serverKey identifies a server instance by the binary, its extra args, the
// workspace directory it serves and the environment it was started with.
//
// The provider scope is part of the key because the credentials reach OpenCode
// through the process environment: one server per workspace is not enough when
// a workspace hosts chats on different providers, or the second provider
// inherits the first one's key (observed live as "APIError: Invalid token" for
// a chat on aiapi whose workspace server had been started for deepseek).
//
// The injected environment is part of the key for the same reason one level up:
// `opencode serve` freezes its environment when it starts, so any server shared
// beyond the environment it was started with hands the newcomer whatever the
// first one injected. That covers project config (OPENCODE_CONFIG,
// OPENCODE_PERMISSION), provider credentials, and the per-session
// CC_PROJECT/CC_SESSION_KEY/CC_DATA_DIR that `cc-connect send`, `cron` and the
// relay rely on. Sessions with an identical environment still share one server;
// anything else gets its own.
func serverKey(cfg opencodeServeConfig) string {
	provSum := sha256.Sum256([]byte(cfg.providerScope))
	envSum := sha256.Sum256([]byte(strings.Join(sortedEnv(cfg.extraEnv), "\x00")))
	return cfg.cmd + "\x00" + strings.Join(cfg.extraArgs, "\x00") + "\x00" + cfg.workDir +
		"\x00" + hex.EncodeToString(provSum[:8]) + "\x00" + hex.EncodeToString(envSum[:8])
}

// sortedEnv copies env sorted by entry, so the server key does not depend on the
// order the agent happened to assemble the slice in.
func sortedEnv(env []string) []string {
	out := append([]string(nil), env...)
	sort.Strings(out)
	return out
}

// acquireOpencodeServer returns a running server for cfg, starting one when
// none is live. The caller must call releaseOpencodeServer when done.
func acquireOpencodeServer(ctx context.Context, cfg opencodeServeConfig) (*opencodeServer, error) {
	key := serverKey(cfg)

	// Two passes at most: a server can exit between the liveness check and the
	// reference bump, in which case we simply start a fresh one.
	for attempt := 0; attempt < 2; attempt++ {
		if srv := liveServer(key); srv != nil {
			acquired, err := retainServer(key, srv)
			if err == nil {
				return acquired, nil
			}
			if !errors.Is(err, errServerGone) {
				return nil, err
			}
		}

		// Serialize startups per workspace: without this, two sessions created at
		// the same moment would each spawn a server for the same directory.
		lock := startLock(key)
		lock.Lock()

		if srv := liveServer(key); srv != nil { // started while we waited
			acquired, err := retainServer(key, srv)
			lock.Unlock()
			if err == nil {
				return acquired, nil
			}
			if !errors.Is(err, errServerGone) {
				return nil, err
			}
			continue
		}

		srv, err := startOpencodeServer(ctx, cfg)
		if err != nil {
			lock.Unlock()
			return nil, err
		}
		srv.mu.Lock()
		srv.refs = 1
		srv.mu.Unlock()

		opencodeServersMu.Lock()
		if existing, ok := opencodeServers[key]; ok && !existing.isExited() {
			// Defensive: never keep two servers for one workspace.
			opencodeServersMu.Unlock()
			lock.Unlock()
			srv.stop()
			continue
		}
		opencodeServers[key] = srv
		opencodeServersMu.Unlock()
		lock.Unlock()
		return srv, nil
	}

	return nil, errServerGone
}

// retainServer bumps the reference count of a live server and cancels any
// pending idle shutdown.
func retainServer(key string, srv *opencodeServer) (*opencodeServer, error) {
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if srv.stopped || srv.exited.Load() {
		// Died between the liveness check and here: forget it and start over.
		forgetServer(key, srv)
		return nil, errServerGone
	}
	if srv.idleTimer != nil {
		if !srv.idleTimer.Stop() {
			// The idle reap already fired. Its callback takes the server out of
			// the map and stops the process, so this is not a server we can hand
			// a turn to but a dying one: starting a fresh process is cheaper than
			// failing the turn against a process that is going away. The timer is
			// the only way to observe this — a fired timer whose callback is
			// still queued leaves stopped/exited unset.
			forgetServer(key, srv)
			return nil, errServerGone
		}
		srv.idleTimer = nil
	}
	srv.refs++
	return srv, nil
}

// forgetServer drops srv from the shared map if it is still the entry for key.
func forgetServer(key string, srv *opencodeServer) {
	opencodeServersMu.Lock()
	if cur, ok := opencodeServers[key]; ok && cur == srv {
		delete(opencodeServers, key)
	}
	opencodeServersMu.Unlock()
}

// errServerGone signals that a server exited while being acquired.
var errServerGone = errors.New("opencode server: process exited during acquire")

// errNoLiveServer signals that this conversation currently has no server
// attached. Idle conversations are reaped (references are held per turn, not per
// session), so the event-stream reader must wait for the next turn instead of
// starting a process of its own.
var errNoLiveServer = errors.New("opencode server: no live server attached")

// releaseOpencodeServer drops a reference; the process is kept alive for the
// configured idle TTL so a follow-up turn in the same workspace does not pay
// the startup cost again, then stopped. A project can turn the recycling off
// (idleTTL <= 0), in which case a released server lives until the daemon stops
// or replaces it.
func releaseOpencodeServer(srv *opencodeServer) {
	if srv == nil {
		return
	}
	srv.mu.Lock()
	if srv.refs > 0 {
		srv.refs--
	}
	remaining := srv.refs
	if remaining == 0 && !srv.stopped && srv.idleTTL > 0 {
		if srv.idleTimer != nil {
			srv.idleTimer.Stop()
		}
		srv.idleTimer = time.AfterFunc(srv.idleTTL, func() {
			opencodeServersMu.Lock()
			if cur, ok := opencodeServers[srv.key]; ok && cur == srv {
				delete(opencodeServers, srv.key)
			}
			opencodeServersMu.Unlock()
			srv.stop()
		})
	}
	srv.mu.Unlock()
}

// stopAllOpencodeServers stops every server this process started. Wired to
// Agent.Stop so a daemon shutdown does not leave orphan `opencode serve`
// processes behind.
func stopAllOpencodeServers() {
	opencodeServersMu.Lock()
	all := make([]*opencodeServer, 0, len(opencodeServers))
	for _, srv := range opencodeServers {
		all = append(all, srv)
	}
	opencodeServers = map[string]*opencodeServer{}
	opencodeServersMu.Unlock()

	for _, srv := range all {
		srv.stop()
	}
}

func (srv *opencodeServer) stop() {
	srv.mu.Lock()
	if srv.stopped {
		srv.mu.Unlock()
		return
	}
	srv.stopped = true
	if srv.idleTimer != nil {
		srv.idleTimer.Stop()
		srv.idleTimer = nil
	}
	cmd := srv.cmd
	srv.mu.Unlock()

	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = terminateCmd(cmd)
	if srv.waitDone == nil {
		return
	}
	select {
	case <-srv.waitDone:
	case <-time.After(opencodeServerStopTimeout):
		if err := forceKillCmd(cmd); err != nil {
			slog.Debug("opencode server: kill failed", "error", err)
		}
		select {
		case <-srv.waitDone:
		case <-time.After(opencodeServerStopTimeout):
			slog.Warn("opencode server: process did not exit after kill")
		}
	}
}

// startOpencodeServer launches `opencode serve` and waits until it reports the
// address it is listening on.
func startOpencodeServer(ctx context.Context, cfg opencodeServeConfig) (*opencodeServer, error) {
	if cfg.cmd == "" {
		cfg.cmd = "opencode"
	}

	password, err := randomServerPassword()
	if err != nil {
		return nil, fmt.Errorf("opencode server: generate password: %w", err)
	}

	args := append(append([]string{}, cfg.extraArgs...), "serve", "--hostname", "127.0.0.1", "--port", "0")
	cmd := exec.Command(cfg.cmd, args...) //nolint:gosec // cmd comes from configured agent options, same as the run transport
	prepareCmdForKill(cmd)
	cmd.Dir = cfg.workDir
	env := os.Environ()
	if len(cfg.extraEnv) > 0 {
		env = core.MergeEnv(env, cfg.extraEnv)
	}
	env = core.MergeEnv(env, []string{"OPENCODE_SERVER_PASSWORD=" + password})
	cmd.Env = env

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("opencode server: stdout pipe: %w", err)
	}
	tail := newTailBuffer(opencodeServerLogTail)
	cmd.Stderr = tail

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("opencode server: start %s: %w", cfg.cmd, err)
	}

	baseURL, err := waitForServerURL(ctx, stdout, tail, cmd)
	if err != nil {
		_ = forceKillCmd(cmd)
		_, _ = cmd.Process.Wait()
		return nil, err
	}

	srv := &opencodeServer{
		baseURL:  baseURL,
		password: password,
		key:      serverKey(cfg),
		cmd:      cmd,
		idleTTL:  serverIdleTTLOrDefault(cfg.serverIdleTTL),
		logTail:  tail,
		waitDone: make(chan struct{}),
	}
	// Reap the process and drop it from the registry as soon as it exits, so a
	// later Send starts a fresh server instead of talking to a dead port.
	go func() {
		_ = cmd.Wait()
		srv.exited.Store(true)
		close(srv.waitDone)
		opencodeServersMu.Lock()
		if cur, ok := opencodeServers[srv.key]; ok && cur == srv {
			delete(opencodeServers, srv.key)
		}
		opencodeServersMu.Unlock()
		slog.Warn("opencode server: process exited", "url", baseURL, "dir", cfg.workDir)
	}()
	slog.Info("opencode server: listening", "url", baseURL, "dir", cfg.workDir)
	return srv, nil
}

var opencodeListenRe = regexp.MustCompile(`listening on (http://[^\s]+)`)

// waitForServerURL scans the server's stdout until it announces its address.
func waitForServerURL(ctx context.Context, stdout io.Reader, tail *tailBuffer, cmd *exec.Cmd) (string, error) {
	type result struct {
		url string
		err error
	}
	ch := make(chan result, 1)

	go func() {
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 0, 32*1024), 1024*1024)
		for scanner.Scan() {
			line := scanner.Text()
			tail.WriteString(line + "\n")
			if m := opencodeListenRe.FindStringSubmatch(line); m != nil {
				addr, err := ensureLoopbackURL(strings.TrimSuffix(m[1], "/"))
				if err != nil {
					ch <- result{err: err}
					return
				}
				ch <- result{url: addr}
				return
			}
		}
		if err := scanner.Err(); err != nil {
			ch <- result{err: fmt.Errorf("opencode server: read stdout: %w", err)}
			return
		}
		ch <- result{err: errors.New("opencode server: process exited before announcing its address")}
	}()

	timeout := time.NewTimer(opencodeServerStartTimeout)
	defer timeout.Stop()

	select {
	case r := <-ch:
		if r.err != nil {
			if tail != nil && tail.String() != "" {
				return "", fmt.Errorf("%w: %s", r.err, strings.TrimSpace(tail.String()))
			}
			return "", r.err
		}
		return r.url, nil
	case <-timeout.C:
		return "", fmt.Errorf("opencode server: did not start within %s: %s", opencodeServerStartTimeout, strings.TrimSpace(tail.String()))
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func randomServerPassword() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// ensureLoopbackURL rejects a listen address that is not on the loopback
// interface. The address is scraped from the child's own stdout, so without this
// check a misbehaving (or replaced) `opencode` binary could point every request —
// and the basic-auth password they carry — at a remote host.
func ensureLoopbackURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("opencode server: parse listen address %q: %w", raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("opencode server: listen address %q is not http(s)", raw)
	}
	host := u.Hostname()
	if host == "localhost" {
		return u.String(), nil
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return u.String(), nil
	}
	return "", fmt.Errorf("opencode server: refusing to talk to non-loopback address %q", raw)
}

// opencodeHTTPClient is used for every call to the local server. Redirects are
// never followed: each request carries the basic-auth password, which must not be
// forwarded to whatever host a redirect points at. A 3xx therefore reaches the
// caller as the non-2xx response it is.
var opencodeHTTPClient = &http.Client{
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

// do performs an authenticated JSON request against the server.
func (srv *opencodeServer) do(ctx context.Context, method, path string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("opencode server: encode request: %w", err)
		}
		reader = bytes.NewReader(payload)
	}

	req, err := http.NewRequestWithContext(ctx, method, srv.baseURL+path, reader)
	if err != nil {
		return fmt.Errorf("opencode server: build request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.SetBasicAuth(opencodeServerUser, srv.password)

	resp, err := opencodeHTTPClient.Do(req)
	if err != nil {
		// A transport error usually means the server died; its last output lines
		// are the most useful diagnostic we have.
		if tail := srv.diagnostics(); tail != "" {
			return fmt.Errorf("opencode server: %s %s: %w (server output: %s)", method, path, err, tail)
		}
		return fmt.Errorf("opencode server: %s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("opencode server: %s %s: HTTP %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(snippet)))
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("opencode server: decode %s %s: %w", method, path, err)
	}
	return nil
}

// diagnostics returns the tail of the server's output, trimmed, for error
// messages. The server's password is scrubbed out in case the child echoes its
// environment: these strings end up in logs and in user-visible errors.
func (srv *opencodeServer) diagnostics() string {
	if srv.logTail == nil {
		return ""
	}
	tail := strings.TrimSpace(srv.logTail.String())
	if srv.password != "" {
		tail = strings.ReplaceAll(tail, srv.password, "***")
	}
	return truncate(tail, 300)
}

// createSession creates an OpenCode session rooted at directory.
//
// Note: the model is deliberately not set here. POST /session takes a different
// model shape ({id, providerID}) than POST /session/{id}/message
// ({providerID, modelID}), and sending the wrong one is rejected with a bare
// `{"_tag":"BadRequest"}`. Each message carries the model instead, which is the
// value that actually governs the turn.
func (srv *opencodeServer) createSession(ctx context.Context, directory, agentName, mode string) (string, error) {
	body := map[string]any{}
	if agentName != "" {
		body["agent"] = agentName
	}
	// yolo parity: the run transport passes --dangerously-skip-permissions, so
	// the server transport allows every tool through the session ruleset.
	// Without it a headless yolo turn would block on an unanswered permission
	// request. Other modes keep OpenCode's own permission behaviour, exactly as
	// the run transport does by omitting the flag.
	if mode == "yolo" {
		body["permission"] = yoloPermissionRuleset()
	}

	path := "/session"
	if directory != "" {
		path += "?directory=" + url.QueryEscape(directory)
	}

	var created struct {
		ID string `json:"id"`
	}
	if err := srv.do(ctx, http.MethodPost, path, body, &created); err != nil {
		return "", err
	}
	if created.ID == "" {
		return "", errors.New("opencode server: create session returned no id")
	}
	return created.ID, nil
}

// sendMessage posts a prompt to a session. When the session already has a turn
// in flight, OpenCode appends the message to that turn instead of interrupting
// it — this is what makes /ps non-destructive.
func (srv *opencodeServer) sendMessage(ctx context.Context, sessionID string, parts []map[string]any, agentName, model string) error {
	body := map[string]any{"parts": parts}
	if agentName != "" {
		body["agent"] = agentName
	}
	if m := parseProviderScopedModel(model); m != nil {
		body["model"] = m
	}
	var out map[string]any
	return srv.do(ctx, http.MethodPost, "/session/"+url.PathEscape(sessionID)+"/message", body, &out)
}

func (srv *opencodeServer) abortSession(ctx context.Context, sessionID string) error {
	return srv.do(ctx, http.MethodPost, "/session/"+url.PathEscape(sessionID)+"/abort", map[string]any{}, nil)
}

// updateSessionPermissions replaces a session's permission ruleset.
//
// The ruleset is a property of the *session*, and OpenCode only takes it when
// the session is created. A cc-connect session is usually attached to an
// OpenCode conversation that already exists (resume), so the create-time
// ruleset never applied there — and without it every external-directory access
// asks for an approval that no bridge user can give, hanging the turn forever.
func (srv *opencodeServer) updateSessionPermissions(ctx context.Context, sessionID string, directory string, ruleset []map[string]any) error {
	path := "/session/" + url.PathEscape(sessionID)
	if directory != "" {
		path += "?directory=" + url.QueryEscape(directory)
	}
	return srv.do(ctx, http.MethodPatch, path, map[string]any{"permission": ruleset}, nil)
}

// replyPermission answers a pending permission request. reply is one of
// "once", "always" or "reject" — the same vocabulary OpenCode's TUI uses.
func (srv *opencodeServer) replyPermission(ctx context.Context, requestID, reply string) error {
	body := map[string]any{"reply": reply}
	return srv.do(ctx, http.MethodPost, "/permission/"+url.PathEscape(requestID)+"/reply", body, nil)
}

// listMessages returns a session's messages, each with its role, so the
// transport can rebuild its role map after a stream gap.
func (srv *opencodeServer) listMessages(ctx context.Context, sessionID, directory string) ([]map[string]any, error) {
	path := "/session/" + url.PathEscape(sessionID) + "/message"
	if directory != "" {
		path += "?directory=" + url.QueryEscape(directory)
	}
	var out []map[string]any
	if err := srv.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// yoloPermissionRuleset is the server-side equivalent of the run transport's
// --dangerously-skip-permissions: every tool may run without a prompt.
//
// The kinds are listed explicitly rather than through a single "*" rule so the
// headless denies for the interactive tools survive: over the bridge nobody can
// answer OpenCode's question dialog or a plan-mode switch, and a denied tool
// just reports back to the model instead of hanging the turn.
func yoloPermissionRuleset() []map[string]any {
	rules := []map[string]any{
		{"permission": "question", "pattern": "*", "action": "deny"},
		{"permission": "plan_enter", "pattern": "*", "action": "deny"},
		{"permission": "plan_exit", "pattern": "*", "action": "deny"},
	}
	for _, kind := range []string{
		"read", "edit", "glob", "grep", "list", "bash", "task",
		"external_directory", "todowrite", "webfetch", "websearch",
		"lsp", "doom_loop", "skill",
	} {
		rules = append(rules, map[string]any{"permission": kind, "pattern": "*", "action": "allow"})
	}
	return rules
}

// events opens the server's event stream (SSE).
func (srv *opencodeServer) events(ctx context.Context) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.baseURL+"/event", nil)
	if err != nil {
		return nil, fmt.Errorf("opencode server: build event request: %w", err)
	}
	req.Header.Set("Accept", "text/event-stream")
	req.SetBasicAuth(opencodeServerUser, srv.password)

	resp, err := opencodeHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("opencode server: open event stream: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		_ = resp.Body.Close()
		return nil, fmt.Errorf("opencode server: event stream HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(snippet)))
	}
	return resp.Body, nil
}

// parseProviderScopedModel splits an OpenCode model reference such as
// "openai/gpt-5.6-sol" into the API's providerID/modelID pair. A bare model
// name cannot be expressed through the API, so it is left to the session
// default.
func parseProviderScopedModel(model string) map[string]any {
	model = strings.TrimSpace(model)
	if model == "" {
		return nil
	}
	idx := strings.Index(model, "/")
	if idx <= 0 || idx == len(model)-1 {
		return nil
	}
	return map[string]any{"providerID": model[:idx], "modelID": model[idx+1:]}
}

// tailBuffer keeps the last N bytes written to it (server diagnostics).
type tailBuffer struct {
	mu  sync.Mutex
	max int
	buf bytes.Buffer
}

func newTailBuffer(max int) *tailBuffer { return &tailBuffer{max: max} }

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, err := t.buf.Write(p); err != nil {
		return 0, err
	}
	if t.buf.Len() > t.max {
		trimmed := t.buf.Bytes()[t.buf.Len()-t.max:]
		t.buf.Reset()
		t.buf.Write(trimmed)
	}
	return len(p), nil
}

func (t *tailBuffer) WriteString(s string) {
	_, _ = t.Write([]byte(s))
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.buf.String()
}

// ---------------------------------------------------------------------------
// serverSession
// ---------------------------------------------------------------------------

// stallTimeoutOrDefault keeps the default watchdog threshold unless the project
// configured one (opencode_stall_timeout). A negative value is the project
// turning the watchdog off, and is passed through.
func stallTimeoutOrDefault(configured time.Duration) time.Duration {
	if configured == 0 {
		return serverStallTimeout
	}
	return configured
}

// serverIdleTTLOrDefault keeps the default idle TTL unless the project
// configured one (opencode_server_idle_ttl). A negative value is the project
// turning recycling off, and is passed through.
func serverIdleTTLOrDefault(configured time.Duration) time.Duration {
	if configured == 0 {
		return opencodeServerDefaultIdleTTL
	}
	return configured
}

// serverStallNotice is what the user sees when a silent turn is aborted. It
// mirrors the run transport's stall notice; kept local so this transport does
// not depend on the run transport's watchdog.
const serverStallNotice = "⚠️ 任务处理超时（长时间无响应），已自动终止。请重试；若任务较大，建议拆成更小的步骤分步发送。"

// serverStallTimeout/serverStallTick mirror the run transport's stall watchdog
// (stallTimeout). A turn whose stream goes quiet is aborted with a visible
// notice instead of hanging until the engine's idle timeout — two hours by
// default, during which the session stays busy and every later message queues
// behind it.
const (
	serverStallTimeout = 5 * time.Minute
	serverStallTick    = 30 * time.Second
	// serverStallTimeoutDisabled marks a project that turned the watchdog off
	// (opencode_stall_timeout = "off"). Unset parses to 0, so the off state needs
	// its own value or the default would swallow it.
	serverStallTimeoutDisabled = time.Duration(-1)
)

// serverSession implements core.AgentSession on top of the OpenCode server API.
//
// Event parsing is delegated to an embedded opencodeSession, whose handlers
// already understand OpenCode's part vocabulary (text / reasoning / tool /
// step-start / step-finish) — the server's `message.part.updated` payloads use
// exactly that shape, so only the envelope and the transport differ.
type serverSession struct {
	inner    *opencodeSession
	serveCfg opencodeServeConfig

	srvMu sync.Mutex
	srv   *opencodeServer

	// turnWake is signalled when a turn attaches (or replaces) the server, so a
	// reader waiting out an idle reap notices that there is something to attach
	// to again.
	turnWake chan struct{}
	// leaseMu guards turnRelease: the reference this session holds for the turn
	// that is currently running. It is dropped once, either by the turn itself or
	// by Close.
	leaseMu     sync.Mutex
	turnRelease func()

	// streamMu guards streamOpen/streamReady. A turn waits for the stream before
	// posting its prompt so the first events of the turn cannot be missed.
	streamMu    sync.Mutex
	streamOpen  bool
	streamReady chan struct{}

	workDir   string
	model     string
	agentName string
	mode      string

	sessionMu sync.Mutex
	sessionID string
	// permsApplied records that this session's permission ruleset was already
	// pushed to the server (once per attached conversation).
	permsApplied bool

	sendMu       sync.Mutex
	turnInFlight atomic.Bool

	// turnMu guards the per-turn bookkeeping: turnGen identifies the current
	// turn, turnRequests counts the message requests still running for it (a
	// mid-turn /ps posts a second one), and turnFinished records the generation
	// whose EventResult was already emitted. Without a generation a request that
	// finishes after a newer turn started (or the first of two overlapping ones)
	// would end the wrong turn / end it early.
	turnMu       sync.Mutex
	turnGen      int64
	turnRequests int
	turnFinished int64

	msgMu         sync.Mutex
	assistantMsgs map[string]struct{}
	userMsgs      map[string]struct{}
	emittedTools  map[string]struct{}
	// msgTurn records the generation in which an assistant message was first
	// seen. Only the running turn's messages are replayed after a stream gap;
	// re-tagging an older answer with the new generation would duplicate it.
	msgTurn map[string]int64
	// partText records, per text part id, how much of its text was already
	// emitted, so a replay sends only the tail the stream gap swallowed.
	partText map[string]int
	// seenParts records the part ids already dispatched, so a replay cannot
	// re-emit a tool result or a step notice that already reached the engine.
	seenParts map[string]struct{}

	sseCancel context.CancelFunc
	wg        sync.WaitGroup
	closeOnce sync.Once
	// streamLossReported bounds stream-loss reporting to once per turn.
	streamLossReported atomic.Bool
	// lastSessionEvent is the timestamp of the newest event belonging to this
	// session; turnStartedAt is when the current turn began. Together they drive
	// the stall watchdog.
	lastSessionEvent atomic.Int64
	turnStartedAt    atomic.Int64
	stallReported    atomic.Bool
	// abortedTurn records that this session deliberately stopped the turn (stall
	// watchdog, /stop). OpenCode then reports the abort back as a
	// MessageAbortedError on the stream, which must not be relayed to the chat as
	// a failure: the user already got the reason.
	abortedTurn atomic.Bool
	// stallTimeout is how long a turn may stay silent before it is aborted
	// (field, not const, so tests can shorten it).
	stallTimeout time.Duration
	// finalStep holds the last reason="stop" step-finish part, used to flush the
	// buffered answer (and its token totals) when the session goes idle.
	finalStep atomic.Value

	// eventMu guards eventsClosed, which stops the transport from sending on the
	// engine's event channel once Close has started tearing the session down
	// (the channel itself is closed by the inner session).
	eventMu      sync.Mutex
	eventsClosed bool
}

// newServerSession builds a session for the workspace. The server process is
// started by the first turn (see acquireTurnServer): a conversation that never
// runs a prompt must not keep an `opencode serve` process alive, and a
// conversation that goes idle releases its process again.
func newServerSession(ctx context.Context, serveCfg opencodeServeConfig, model, mode, agentName, resumeID string) (*serverSession, error) {
	return newServerSessionOn(ctx, nil, serveCfg, model, mode, agentName, resumeID)
}

// newServerSessionOn builds a session around srv, which may be nil: the first
// turn then attaches one. Split out from newServerSession so tests can drive the
// session against a stub server.
func newServerSessionOn(ctx context.Context, srv *opencodeServer, serveCfg opencodeServeConfig, model, mode, agentName, resumeID string) (*serverSession, error) {
	inner, err := newOpencodeSession(ctx, serveCfg.cmd, serveCfg.extraArgs, serveCfg.workDir, model, mode, agentName, resumeID, serveCfg.extraEnv)
	if err != nil {
		return nil, err
	}

	sessionCtx, cancel := context.WithCancel(ctx)
	s := &serverSession{
		inner:         inner,
		serveCfg:      serveCfg,
		srv:           srv,
		workDir:       serveCfg.workDir,
		model:         model,
		agentName:     agentName,
		mode:          mode,
		sessionID:     resumeID,
		assistantMsgs: map[string]struct{}{},
		userMsgs:      map[string]struct{}{},
		emittedTools:  map[string]struct{}{},
		msgTurn:       map[string]int64{},
		partText:      map[string]int{},
		seenParts:     map[string]struct{}{},
		sseCancel:     cancel,
		stallTimeout:  stallTimeoutOrDefault(serveCfg.stallTimeout),
		turnWake:      make(chan struct{}, 1),
	}

	s.wg.Add(1)
	go s.readEventStream(sessionCtx)
	go s.stallWatchdog(sessionCtx, serverStallTick)
	return s, nil
}

func (s *serverSession) Send(prompt string, messageID string, images []core.ImageAttachment, files []core.FileAttachment) error {
	if !s.inner.alive.Load() {
		return errors.New("session is closed")
	}

	if len(files) > 0 {
		filePaths := core.SaveFilesToDisk(s.workDir, messageID, files)
		prompt = core.AppendFileRefs(prompt, filePaths)
	}
	parts := []map[string]any{{"type": "text", "text": prompt}}
	for _, img := range images {
		mime := img.MimeType
		if mime == "" {
			mime = "image/png"
		}
		parts = append(parts, map[string]any{
			"type":     "file",
			"mime":     mime,
			"filename": img.FileName,
			"url":      "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(img.Data),
		})
	}

	srv, releaseTurn, err := s.acquireTurnServer(s.inner.ctx)
	if err != nil {
		s.emitError(err)
		return err
	}
	// The reference lasts for this turn. Ownership moves to the request goroutine
	// below; every early return here has to drop it.
	handedOff := false
	defer func() {
		if !handedOff {
			releaseTurn()
		}
	}()
	// Let the event-stream reader attach to the server this turn just started, and
	// wait for it so the prompt's first events (a permission request in
	// particular) are not emitted before anyone is listening.
	s.signalTurn()
	s.waitStreamReady(s.inner.ctx, streamReadyTimeout)

	sessionID, created, err := s.ensureSession()
	if err != nil {
		s.emitError(err)
		return err
	}
	if created {
		// Announce the freshly created session id. The engine persists
		// agent_session_id only from text/result events, so a turn that is
		// aborted before producing any text (e.g. /stop while a tool call is
		// running) would otherwise drop the id and the next message would start a
		// fresh conversation. Empty content renders nothing.
		s.sendEvent(core.Event{Type: core.EventText, Content: "", SessionID: sessionID})
	}

	s.sendMu.Lock()
	defer s.sendMu.Unlock()

	// A prompt that arrives while a turn is already running (a mid-turn /ps)
	// supplements that turn rather than starting a new one, so it shares the
	// turn's generation: the answer, its result and its stall budget belong to the
	// turn, not to the individual request. Without that distinction a request that
	// returns early (or a stale one) would end the turn that is running now.
	newTurn := !s.turnInFlight.Load()
	var gen int64
	if newTurn {
		gen = s.beginTurn()
	} else {
		gen = s.joinTurn()
	}

	// A new prompt starts (or supplements) a turn: allow exactly one EventResult
	// for it, matching the run transport.
	s.inner.resultSent.Store(false)
	s.inner.expectingContinue.Store(false)
	s.turnInFlight.Store(true)
	// Each turn gets its own budget for one stream-loss report, so an outage that
	// happened while the session was idle cannot swallow the report for a turn.
	s.streamLossReported.Store(false)
	s.finalStep = atomic.Value{}
	// Per-turn stall bookkeeping: a timestamp from the previous turn would make
	// the watchdog abort this one immediately.
	s.lastSessionEvent.Store(0)
	s.turnStartedAt.Store(time.Now().UnixNano())
	s.stallReported.Store(false)
	s.abortedTurn.Store(false)

	done := make(chan error, 1)
	handedOff = true
	go func() {
		// The turn owns the server reference until the request returns, which is
		// when the server-side turn is over. Releasing it here (rather than at
		// session close) is what keeps an idle conversation from holding a process.
		defer releaseTurn()
		// The request blocks until the turn completes (the server answers with
		// the final assistant message). Events arrive over the SSE stream in
		// parallel; a supplement posted mid-turn additionally extends the turn
		// that is already running.
		err := srv.sendMessage(s.inner.ctx, sessionID, parts, s.agentName, s.model)
		if isSessionMissing(err) {
			slog.Warn("opencode server session: stored session no longer exists, starting a fresh one",
				"session_id", sessionID)
			s.forgetSession()
			fresh, _, cerr := s.ensureSession()
			if cerr != nil {
				done <- cerr
				return
			}
			err = srv.sendMessage(s.inner.ctx, fresh, parts, s.agentName, s.model)
		}
		done <- err
	}()

	select {
	case err := <-done:
		// The server answers the message request only once the turn is over, so a
		// successful return means this request is done. Only the last request of
		// the turn may end it.
		if s.finishTurnRequest(gen) {
			s.turnInFlight.Store(false)
			if err == nil {
				s.ensureTurnResult(gen)
			}
		}
		// The engine surfaces Send errors itself, so no extra event here —
		// mirroring the run transport.
		return err
	case <-time.After(150 * time.Millisecond):
		// Return promptly so the engine can keep processing incoming messages
		// (including a mid-turn /ps) while the turn runs. The request itself
		// stays open until the turn completes.
		go func() {
			err := <-done
			last := s.finishTurnRequest(gen)
			if err != nil {
				// A turn that was ended deliberately (stall abort, /stop) makes the
				// request fail afterwards; an error event then would only confuse.
				if s.inner.resultSent.Load() {
					slog.Debug("opencode server session: message request failed after the turn ended",
						"error", err)
					return
				}
				if last {
					s.turnInFlight.Store(false)
				}
				s.emitError(err)
				return
			}
			if !last {
				// A supplement of the same turn that finished before the original
				// request must not end the turn the sibling is still running.
				return
			}
			s.turnInFlight.Store(false)
			s.ensureTurnResult(gen)
		}()
		return nil
	case <-s.inner.ctx.Done():
		return s.inner.ctx.Err()
	}
}

// beginTurn starts a new turn generation and registers the caller's message
// request with it.
func (s *serverSession) beginTurn() int64 {
	s.turnMu.Lock()
	defer s.turnMu.Unlock()
	s.turnGen++
	s.turnRequests = 1
	return s.turnGen
}

// joinTurn registers a second, concurrent message request (a mid-turn /ps) with
// the turn that is already running, instead of starting a new generation: the
// supplement shares the turn's answer, so it must share its bookkeeping too.
func (s *serverSession) joinTurn() int64 {
	s.turnMu.Lock()
	defer s.turnMu.Unlock()
	s.turnRequests++
	return s.turnGen
}

// finishTurnRequest drops one in-flight message request and reports whether this
// was the last one of gen while gen is still the turn in progress — i.e. whether
// the caller may end the turn.
func (s *serverSession) finishTurnRequest(gen int64) bool {
	s.turnMu.Lock()
	defer s.turnMu.Unlock()
	if gen != s.turnGen {
		return false
	}
	if s.turnRequests > 0 {
		s.turnRequests--
	}
	return s.turnRequests == 0
}

// currentTurnGen returns the generation of the turn in progress.
func (s *serverSession) currentTurnGen() int64 {
	s.turnMu.Lock()
	defer s.turnMu.Unlock()
	return s.turnGen
}

// claimTurnEnd reports whether gen is the current turn and it has not been ended
// yet, marking it ended when so. The turn is thus closed exactly once even when
// several paths (idle event, returned request, stale safety net) race to do it.
func (s *serverSession) claimTurnEnd(gen int64) bool {
	s.turnMu.Lock()
	defer s.turnMu.Unlock()
	if gen != s.turnGen || s.turnFinished == gen {
		return false
	}
	s.turnFinished = gen
	return true
}

// server returns the server this session is currently talking to.
func (s *serverSession) server() *opencodeServer {
	s.srvMu.Lock()
	defer s.srvMu.Unlock()
	return s.srv
}

// liveServer returns the server this session is attached to, or nil when there
// is none or its process has exited. It holds no reference, so it is only good
// for work that can fail and retry (abort, permission replies, the event stream)
// — a reference for a turn comes from acquireTurnServer.
func (s *serverSession) liveServer() *opencodeServer {
	srv := s.server()
	if srv == nil || srv.isExited() {
		return nil
	}
	return srv
}

// acquireTurnServer returns a live server for the turn that is starting and
// holds a reference on it until the returned release function runs (or Close
// runs it, whichever comes first).
//
// The reference lasts for the turn rather than for the session: an idle
// conversation must not keep an `opencode serve` process alive, and a reaped
// process costs one restart for the next turn (OpenCode keeps the conversation
// in its own store, so a fresh server resumes it). OpenCode resolves the
// process environment once, at startup, which is why sessions with different
// environments never share a server (see serverKey).
func (s *serverSession) acquireTurnServer(ctx context.Context) (*opencodeServer, func(), error) {
	s.srvMu.Lock()
	defer s.srvMu.Unlock()

	if s.srv != nil && !s.srv.isExited() {
		if retained, err := retainServer(s.srv.key, s.srv); err == nil {
			return retained, s.leaseTurn(retained), nil
		}
	}

	stale := s.srv
	fresh, err := acquireOpencodeServer(ctx, s.serveCfg)
	if err != nil {
		return nil, nil, err
	}
	s.srv = fresh
	if stale != nil {
		releaseOpencodeServer(stale)
	}
	slog.Info("opencode server session: attached to a server", "url", fresh.baseURL, "dir", s.serveCfg.workDir)
	return fresh, s.leaseTurn(fresh), nil
}

// leaseTurn records one turn's reference on srv and returns the function that
// drops it exactly once, however many times it is called.
func (s *serverSession) leaseTurn(srv *opencodeServer) func() {
	var once sync.Once
	release := func() { once.Do(func() { releaseOpencodeServer(srv) }) }
	s.leaseMu.Lock()
	s.turnRelease = release
	s.leaseMu.Unlock()
	return release
}

// releaseCurrentTurn drops the reference of the turn that is running, if any.
// Close uses it to make sure a turn that is still in flight does not outlive the
// session's own teardown.
func (s *serverSession) releaseCurrentTurn() {
	s.leaseMu.Lock()
	release := s.turnRelease
	s.turnRelease = nil
	s.leaseMu.Unlock()
	if release != nil {
		release()
	}
}

// signalTurn wakes the event-stream reader after a turn attached (or replaced)
// the server.
func (s *serverSession) signalTurn() {
	select {
	case s.turnWake <- struct{}{}:
	default:
	}
}

// waitForTurn blocks until a turn starts (or the session is closing). Idle
// conversations stay here without polling and without starting a server.
func (s *serverSession) waitForTurn(ctx context.Context) {
	select {
	case <-s.turnWake:
	case <-ctx.Done():
	}
}

// markStreamOpen records that the event stream is attached, releasing any turn
// that is waiting for it.
func (s *serverSession) markStreamOpen() {
	s.streamMu.Lock()
	s.streamOpen = true
	if s.streamReady != nil {
		close(s.streamReady)
		s.streamReady = nil
	}
	s.streamMu.Unlock()
}

// markStreamClosed records that the event stream is no longer attached.
func (s *serverSession) markStreamClosed() {
	s.streamMu.Lock()
	s.streamOpen = false
	s.streamMu.Unlock()
}

// waitStreamReady waits until the event stream is attached, so a turn does not
// post its prompt before anything is listening. It gives up after d: an
// unreadable stream is reported by the reader itself, and blocking the turn on
// it would be worse than starting it late.
func (s *serverSession) waitStreamReady(ctx context.Context, d time.Duration) {
	s.streamMu.Lock()
	if s.streamOpen {
		s.streamMu.Unlock()
		return
	}
	if s.streamReady == nil {
		s.streamReady = make(chan struct{})
	}
	ready := s.streamReady
	s.streamMu.Unlock()

	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ready:
	case <-timer.C:
		slog.Warn("opencode server session: event stream not ready, starting the turn anyway")
	case <-ctx.Done():
	}
}

// flushFinalAnswer delivers the text the inner session buffered for the final
// step, then closes the turn. The inner session flushes on a reason="stop"
// step-finish, which this transport deliberately defers (compaction emits the
// same reason), so the flush happens here — once the session really is idle.
func (s *serverSession) flushFinalAnswer(gen int64) {
	if gen != s.currentTurnGen() {
		// A stale request (the safety net of a previous turn) must not end the
		// turn that is running now.
		return
	}
	if s.inner.resultSent.Load() {
		return
	}

	// The inner session writes straight to the event channel here, so the flush
	// must not overlap Close (which sets eventsClosed under the same lock before
	// closing the channel). Senders outside the SSE goroutine take this path.
	s.eventMu.Lock()
	if s.eventsClosed {
		s.eventMu.Unlock()
		return
	}
	// In server mode OpenCode's own auto-continue runs inside the server, so the
	// run transport's "wait for a continuation" deferral must not suppress the
	// answer.
	s.inner.expectingContinue.Store(false)

	part := map[string]any{"reason": "stop"}
	if v := s.finalStep.Load(); v != nil {
		if stored, ok := v.(map[string]any); ok && stored != nil {
			part = stored
		}
	}
	s.inner.handleStepFinish(map[string]any{"part": part})
	s.eventMu.Unlock()

	s.endTurn(gen) // no-op when the flush already delivered the result
}

// endTurn tells the engine the turn is over, exactly once, through the guarded
// send path (the run transport's helper writes to the channel directly, which
// would race Close).
func (s *serverSession) endTurn(gen int64) {
	if !s.claimTurnEnd(gen) {
		return
	}
	if !s.inner.resultSent.CompareAndSwap(false, true) {
		return
	}
	s.sendEvent(core.Event{Type: core.EventResult, SessionID: s.CurrentSessionID(), Done: true})
}

// sessionMissingMarker is how OpenCode reports a session id that no longer
// exists (e.g. after `opencode session delete`).
const sessionMissingMarker = "Session not found"

// isSessionMissing reports whether err is OpenCode's "this session is gone"
// response. The run transport recovers from it by clearing the stored id; the
// server transport must do the same, otherwise every later message in that
// conversation fails with 404.
func isSessionMissing(err error) bool {
	return err != nil && strings.Contains(err.Error(), sessionMissingMarker)
}

// forgetSession drops the cached agent session id so the next send creates a
// fresh session.
func (s *serverSession) forgetSession() {
	s.sessionMu.Lock()
	s.sessionID = ""
	// The replacement conversation gets its ruleset from createSession.
	s.permsApplied = false
	s.sessionMu.Unlock()
	s.inner.chatID.Store("")
}

// ensureTurnResult makes sure the engine is told the turn is over exactly once.
// The primary signal is the stream's session.idle event; this is the safety net
// for a turn whose idle event was missed (stream hiccup), using the fact that the
// message request only returns once the turn has finished. It waits briefly so
// the trailing text parts are not overtaken by the result.
func (s *serverSession) ensureTurnResult(gen int64) {
	for i := 0; i < 15; i++ {
		if gen != s.currentTurnGen() || s.inner.resultSent.Load() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	slog.Warn("opencode server session: no idle event for a finished turn, ending it explicitly")
	s.flushFinalAnswer(gen)
}

// ensureSession returns the OpenCode session id for this turn, creating the
// session on first use. created reports whether it had to be created, so the
// caller can announce the new id to the engine.
func (s *serverSession) ensureSession() (string, bool, error) {
	s.sessionMu.Lock()
	defer s.sessionMu.Unlock()
	if s.sessionID != "" {
		s.applyYoloPermissionsLocked()
		return s.sessionID, false, nil
	}
	srv := s.liveServer()
	if srv == nil {
		// Only reachable when the turn's server died between acquiring it and
		// creating the conversation; the next turn starts a fresh one.
		return "", false, errNoLiveServer
	}
	id, err := srv.createSession(s.inner.ctx, s.workDir, s.agentName, s.mode)
	if err != nil {
		return "", false, err
	}
	s.sessionID = id
	s.inner.chatID.Store(id)
	// createSession already sent the ruleset for yolo mode.
	s.permsApplied = s.mode == "yolo"
	return id, true, nil
}

// applyYoloPermissionsLocked pushes the allow-everything ruleset onto an
// already existing conversation. Only yolo mode does this: the run transport's
// --dangerously-skip-permissions is a per-prompt flag, but the server API has no
// such flag, so the equivalent is the session ruleset. Without it a resumed
// session keeps asking for approvals nobody can give over the bridge.
func (s *serverSession) applyYoloPermissionsLocked() {
	if s.mode != "yolo" || s.permsApplied || s.sessionID == "" {
		return
	}
	// One attempt per attached conversation: a failure must not add a request
	// to every turn. The permission.asked safety net below still covers it.
	s.permsApplied = true
	srv := s.liveServer()
	if srv == nil {
		s.permsApplied = false
		return
	}
	if err := srv.updateSessionPermissions(s.inner.ctx, s.sessionID, s.workDir, yoloPermissionRuleset()); err != nil {
		slog.Warn("opencode server session: could not apply the yolo permission ruleset; "+
			"a tool call that needs approval may wait for one nobody can give",
			"session", s.sessionID, "error", err)
	}
}

func (s *serverSession) emitError(err error) {
	if s.abortedTurn.Load() && isAbortEcho(err) {
		slog.Debug("opencode server session: ignoring the abort echo of our own stop", "error", err)
		return
	}
	slog.Error("opencode server session: error", "error", err)
	s.sendEvent(core.Event{Type: core.EventError, Error: err})
}

// sendEvent delivers one event to the engine unless the session is shutting
// down. Senders outside the SSE goroutine (Send failures, tool results) must go
// through here: a bare channel send would race the channel close in Close.
// The session id is stamped on the way out so the engine persists the agent
// session id as soon as anything happens in the turn — otherwise a mid-turn
// /stop would drop it and the next message would start a fresh conversation.
func (s *serverSession) sendEvent(evt core.Event) {
	if evt.SessionID == "" {
		evt.SessionID = s.CurrentSessionID()
	}
	s.eventMu.Lock()
	defer s.eventMu.Unlock()
	if s.eventsClosed {
		return
	}
	select {
	case s.inner.events <- evt:
	case <-s.inner.ctx.Done():
	}
}

// drainEvents empties the event buffer so a sender blocked on a full channel can
// make progress during shutdown.
func (s *serverSession) drainEvents() {
	for {
		select {
		case <-s.inner.events:
		default:
			return
		}
	}
}

// readEventStream consumes the server's SSE stream and forwards the parts that
// belong to this session. It reconnects for the lifetime of the session: a
// server that exits while a turn is running is replaced (see acquireTurnServer)
// and the stream is re-opened against the replacement, so a crash costs a
// restart rather than the conversation.
//
// Its own lifecycle is deliberately passive: it never starts a server. While the
// conversation is idle (the process was reaped after the last turn) it waits for
// the next turn to attach one instead of keeping a process alive just to listen.
func (s *serverSession) readEventStream(ctx context.Context) {
	defer s.wg.Done()

	for {
		if ctx.Err() != nil {
			return
		}

		body, err := s.streamEvents(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			if errors.Is(err, errNoLiveServer) {
				s.waitForTurn(ctx)
				continue
			}
			s.reportStreamFailure(err)
			select {
			case <-time.After(opencodeServerRetryDelay):
			case <-ctx.Done():
				return
			}
			continue
		}
		// The stream carries no history, so anything announced before this
		// connection was opened is unknown to us; rebuild the session state
		// (role map and, mid-turn, the parts of the running turn).
		s.resyncSession(ctx)
		s.markStreamOpen()

		scanner := bufio.NewScanner(body)
		scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
		var data strings.Builder
		for scanner.Scan() {
			line := scanner.Text()
			switch {
			case line == "":
				if data.Len() > 0 {
					s.handleServerEvent([]byte(data.String()))
					data.Reset()
				}
			case strings.HasPrefix(line, "data:"):
				if data.Len() > 0 {
					data.WriteByte('\n')
				}
				data.WriteString(strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
			}
		}
		_ = body.Close()
		s.markStreamClosed()

		if ctx.Err() != nil {
			return
		}
		streamErr := scanner.Err()
		slog.Warn("opencode server session: event stream ended, reconnecting", "error", streamErr)
		if streamErr != nil {
			s.reportStreamFailure(fmt.Errorf("event stream ended: %w", streamErr))
		} else {
			s.reportStreamFailure(errors.New("event stream ended"))
		}
		select {
		case <-time.After(opencodeServerRetryDelay):
		case <-ctx.Done():
			return
		}
	}
}

// streamEvents opens the event stream against the server this session is already
// attached to. It never starts one: doing so would resurrect the process of an
// idle conversation that was just reaped.
func (s *serverSession) streamEvents(ctx context.Context) (io.ReadCloser, error) {
	srv := s.liveServer()
	if srv == nil {
		return nil, errNoLiveServer
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return srv.events(ctx)
}

// reportStreamFailure logs that events stopped flowing for a moment. A reconnect
// is transparent — the reader re-attaches, the session is resynced and the turn
// picks up where it left off — so this must never reach the engine: an EventError
// would finalize the card and end the turn the reconnect is about to resume.
func (s *serverSession) reportStreamFailure(err error) {
	if !s.turnInFlight.Load() || !s.streamLossReported.CompareAndSwap(false, true) {
		slog.Warn("opencode server session: event stream unavailable", "error", err)
		return
	}
	slog.Error("opencode server session: event stream lost mid-turn, reconnecting", "error", err)
}

func (s *serverSession) handleServerEvent(payload []byte) {
	var evt struct {
		Type       string         `json:"type"`
		Properties map[string]any `json:"properties"`
	}
	if err := json.Unmarshal(payload, &evt); err != nil {
		slog.Debug("opencode server session: non-JSON event", "payload", truncate(string(payload), 200))
		return
	}

	// Session-scoped events are filtered; server-wide ones (heartbeats etc. carry
	// no sessionID) are ignored below by their type. Only this session's own
	// events count as activity for the stall watchdog — a heartbeat must not make
	// a hung turn look alive.
	if sid, _ := evt.Properties["sessionID"].(string); sid != "" {
		if !s.matchesSession(sid) {
			return
		}
		s.lastSessionEvent.Store(time.Now().UnixNano())
	}

	switch evt.Type {
	case "message.updated":
		info, _ := evt.Properties["info"].(map[string]any)
		if info == nil {
			return
		}
		if id, _ := info["id"].(string); id != "" {
			switch role, _ := info["role"].(string); role {
			case "assistant":
				s.msgMu.Lock()
				s.assistantMsgs[id] = struct{}{}
				if _, known := s.msgTurn[id]; !known {
					// Bind the message to the turn that is running now, so a later
					// resync replays it only while that same turn is in flight.
					s.msgTurn[id] = s.currentTurnGen()
				}
				s.msgMu.Unlock()
			case "user":
				// Remember the echo of the user's own prompt so its text part can
				// be dropped instead of showing up inside the reply.
				s.msgMu.Lock()
				s.userMsgs[id] = struct{}{}
				s.msgMu.Unlock()
			}
		}
	case "message.part.updated":
		part, _ := evt.Properties["part"].(map[string]any)
		if part == nil {
			return
		}
		s.dispatchPart(part)
	case "message.part.delta":
		// Deltas only feed live previews; parts carry the authoritative text at
		// completion, which is what the engine aggregates into the reply.
		slog.Debug("opencode server session: part delta",
			"field", evt.Properties["field"], "delta_len", len(fmt.Sprint(evt.Properties["delta"])))
	case "session.idle":
		s.turnInFlight.Store(false)
		s.flushFinalAnswer(s.currentTurnGen())
	case "session.status":
		if status, ok := evt.Properties["status"].(map[string]any); ok {
			if kind, _ := status["type"].(string); kind == "idle" {
				s.turnInFlight.Store(false)
				s.flushFinalAnswer(s.currentTurnGen())
			}
		}
	case "session.error":
		if s.abortedTurn.Load() && isAbortEcho(evt.Properties["error"]) {
			// We stopped this turn ourselves; the abort is reported back on the
			// stream. Relaying it would hand the user a raw error right after the
			// notice that already explained the stop.
			slog.Debug("opencode server session: ignoring the abort echo of our own stop")
			return
		}
		s.turnInFlight.Store(false)
		s.inner.handleError(map[string]any{"error": evt.Properties["error"]})
	case "permission.asked":
		s.handlePermissionAsked(evt.Properties)
	}
}

// handlePermissionAsked deals with OpenCode asking for approval on a tool call.
//
// Unanswered, the request parks the turn forever: no event is emitted, the
// session stays busy, and every later message just queues behind it. yolo mode
// therefore approves immediately (the run transport gets the same effect from
// --dangerously-skip-permissions); other modes hand the request to the engine,
// which renders an Allow/Deny prompt and answers through RespondPermission.
func (s *serverSession) handlePermissionAsked(props map[string]any) {
	requestID, _ := props["id"].(string)
	if requestID == "" {
		return
	}
	permission, _ := props["permission"].(string)
	patterns := stringList(props["patterns"])

	if s.mode == "yolo" {
		srv := s.liveServer()
		if srv == nil {
			// The request came from a process that is already gone (an idle reap
			// between the ask and this event): it cannot be answered any more.
			slog.Warn("opencode server session: auto-approve skipped, no live server",
				"request", requestID, "permission", permission)
			return
		}
		if err := srv.replyPermission(s.inner.ctx, requestID, "always"); err != nil {
			slog.Error("opencode server session: auto-approve failed; the turn will wait for an approval",
				"request", requestID, "permission", permission, "patterns", patterns, "error", err)
			return
		}
		slog.Info("opencode server session: auto-approved tool permission (yolo mode)",
			"request", requestID, "permission", permission, "patterns", patterns)
		return
	}

	slog.Info("opencode server session: tool permission requested",
		"request", requestID, "permission", permission, "patterns", patterns)
	s.sendEvent(core.Event{
		Type:         core.EventPermissionRequest,
		RequestID:    requestID,
		ToolName:     permission,
		ToolInput:    strings.Join(patterns, ", "),
		ToolInputRaw: props,
	})
}

// stringList reads a JSON string array without panicking on other shapes.
func stringList(raw any) []string {
	items, ok := raw.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// dispatchPart converts one OpenCode part into engine events.
func (s *serverSession) dispatchPart(part map[string]any) {
	partType, _ := part["type"].(string)
	s.markPartSeen(part)

	switch partType {
	case "text":
		if !s.isAssistantPart(part) {
			// The user's own prompt is echoed back as a text part; emitting it
			// would duplicate the request inside the reply.
			return
		}
		s.recordTextPart(part)
		s.inner.handleText(map[string]any{"part": part})
	case "reasoning":
		s.recordTextPart(part)
		s.inner.handleReasoning(map[string]any{"part": part})
	case "step-start":
		s.inner.handleStepStart(map[string]any{"part": part})
	case "step-finish":
		reason, _ := part["reason"].(string)
		if reason == "stop" {
			// A reason="stop" step-finish also arrives when OpenCode compacts the
			// conversation (observed via POST /session/{id}/compact), so it must not
			// end the turn here — the turn ends on session.idle. Keep the part so the
			// answer and the token totals can be flushed at the real turn end.
			s.finalStep.Store(part)
			slog.Debug("opencode server session: final step finished",
				"reason", reason, "tokens", part["tokens"])
			return
		}
		// Intermediate step: flush its narration as progress, exactly like the run
		// transport does. Buffered step text is only delivered by this call, so
		// skipping it would lose the answer entirely.
		s.inner.handleStepFinish(map[string]any{"part": part})
	case "tool":
		s.dispatchToolPart(part)
	}
}

// markPartSeen records that a part reached the engine, so a replay after a stream
// gap can skip it instead of emitting it a second time.
func (s *serverSession) markPartSeen(part map[string]any) {
	id, _ := part["id"].(string)
	if id == "" {
		return
	}
	s.msgMu.Lock()
	s.seenParts[id] = struct{}{}
	s.msgMu.Unlock()
}

// recordTextPart remembers how much of a text part has been emitted. The part's
// text is cumulative on the wire, so a replay can send only the tail that a stream
// gap swallowed.
func (s *serverSession) recordTextPart(part map[string]any) {
	id, _ := part["id"].(string)
	if id == "" {
		return
	}
	text, _ := part["text"].(string)
	s.msgMu.Lock()
	s.seenParts[id] = struct{}{}
	if len(text) > s.partText[id] {
		s.partText[id] = len(text)
	}
	s.msgMu.Unlock()
}

// partWithText returns a copy of part with its text replaced, so only a slice of a
// replayed part reaches the engine.
func partWithText(part map[string]any, text string) map[string]any {
	out := make(map[string]any, len(part))
	for k, v := range part {
		out[k] = v
	}
	out["text"] = text
	return out
}

// replayPart re-dispatches one authoritative part from the session's own message
// list after a stream gap. Text is cumulative on the wire, so only the tail the
// gap swallowed is emitted; everything else is emitted only when it never reached
// the engine (a repeated tool result would otherwise show up twice).
func (s *serverSession) replayPart(part map[string]any) {
	partType, _ := part["type"].(string)
	partID, _ := part["id"].(string)

	switch partType {
	case "text", "reasoning":
		if partType == "text" && !s.isAssistantPart(part) {
			return
		}
		if partID == "" {
			// Without an id there is no way to tell what was delivered already,
			// and a blind re-emit would duplicate the answer.
			slog.Debug("opencode server session: cannot replay an id-less part", "part_type", partType)
			return
		}
		text, _ := part["text"].(string)
		s.msgMu.Lock()
		delivered := s.partText[partID]
		s.seenParts[partID] = struct{}{}
		if len(text) > delivered {
			s.partText[partID] = len(text)
		}
		s.msgMu.Unlock()
		if len(text) <= delivered {
			return
		}
		if delivered > 0 {
			// The part grew after the gap: emit only what the stream did not.
			s.dispatchPart(partWithText(part, text[delivered:]))
			return
		}
		s.dispatchPart(part)
	case "step-finish":
		// Re-storing the final step is what lets the flushed result carry the
		// answer's token totals; it emits no event of its own.
		s.dispatchPart(part)
	default:
		if partID == "" {
			return
		}
		s.msgMu.Lock()
		_, seen := s.seenParts[partID]
		s.msgMu.Unlock()
		if seen {
			return
		}
		s.dispatchPart(part)
	}
}

// resyncSession rebuilds the state a stream gap would otherwise lose. The stream
// carries no history, so after every (re)connect the transport re-reads the
// session's messages: the role map is rebuilt from them, and — when a turn is in
// flight — the parts of the running turn are replayed.
//
// Without the replay a reconnect mid-turn loses the answer: the parts announced
// while the stream was down never arrive, and the turn then ends with an empty
// result. Replaying them is what makes the reconnect transparent, which is the
// whole point of the server transport.
func (s *serverSession) resyncSession(ctx context.Context) {
	sessionID := s.CurrentSessionID()
	if sessionID == "" {
		return
	}
	srv := s.server()
	if srv == nil {
		return
	}
	msgs, err := srv.listMessages(ctx, sessionID, s.workDir)
	if err != nil {
		slog.Debug("opencode server session: message resync failed", "session", sessionID, "error", err)
		return
	}

	// Only the turn that is running now may be replayed: an already delivered
	// answer must not be re-emitted into a newer turn.
	gen := s.currentTurnGen()
	replaying := s.turnInFlight.Load() && !s.inner.resultSent.Load()

	assistant, user := 0, 0
	s.msgMu.Lock()
	var replay []any
	for _, m := range msgs {
		info, _ := m["info"].(map[string]any)
		if info == nil {
			continue
		}
		id, _ := info["id"].(string)
		if id == "" {
			continue
		}
		switch role, _ := info["role"].(string); role {
		case "assistant":
			s.assistantMsgs[id] = struct{}{}
			assistant++
			if _, known := s.msgTurn[id]; !known {
				// First seen now: it belongs to the turn running now, or to no
				// turn at all when the session is idle (generation 0).
				seen := int64(0)
				if replaying {
					seen = gen
				}
				s.msgTurn[id] = seen
			}
			if replaying && s.msgTurn[id] == gen {
				if parts, ok := m["parts"].([]any); ok {
					replay = append(replay, parts...)
				}
			}
		case "user":
			s.userMsgs[id] = struct{}{}
			user++
		}
	}
	s.msgMu.Unlock()

	for _, raw := range replay {
		if part, ok := raw.(map[string]any); ok {
			s.replayPart(part)
		}
	}
	slog.Debug("opencode server session: session resynced",
		"session", sessionID, "assistant", assistant, "user", user, "replayed", len(replay))
}

func (s *serverSession) isAssistantPart(part map[string]any) bool {
	id, _ := part["messageID"].(string)
	if id == "" {
		return false
	}
	s.msgMu.Lock()
	_, isUser := s.userMsgs[id]
	_, isAssistant := s.assistantMsgs[id]
	s.msgMu.Unlock()
	if isUser {
		return false
	}
	if isAssistant {
		return true
	}
	// Unknown message: its `message.updated` was missed (a stream gap at the
	// start of a turn, before the roles were announced). Accept the part — the
	// run transport never filtered by role at all, so accepting is the safe
	// default: leaking the user's own prompt into the progress lane is cosmetic,
	// silently dropping the answer is not.
	slog.Debug("opencode server session: part from an unknown message, accepting as assistant",
		"message_id", id, "part_type", part["type"])
	return true
}

// dispatchToolPart emits a single tool-use event per call plus its result. The
// server sends one update per state change (pending → running → completed), so
// repeats are dropped to keep the progress card free of duplicates.
func (s *serverSession) dispatchToolPart(part map[string]any) {
	toolName, _ := part["tool"].(string)
	callID, _ := part["callID"].(string)
	if callID == "" {
		callID, _ = part["id"].(string)
	}
	state, _ := part["state"].(map[string]any)
	status := ""
	if state != nil {
		status, _ = state["status"].(string)
	}

	if status == "" || status == "pending" {
		return
	}

	s.msgMu.Lock()
	_, alreadyEmitted := s.emittedTools[callID]
	s.msgMu.Unlock()

	if !alreadyEmitted {
		s.msgMu.Lock()
		s.emittedTools[callID] = struct{}{}
		s.msgMu.Unlock()
		s.sendEvent(core.Event{Type: core.EventToolUse, ToolName: toolName, ToolInput: extractToolInput(state)})
	}

	switch status {
	case "completed":
		output, _ := state["output"].(string)
		s.sendEvent(core.Event{Type: core.EventToolResult, ToolName: toolName, Content: truncate(output, 500)})
	case "error":
		errMsg, _ := state["error"].(string)
		if errMsg == "" {
			return
		}
		slog.Info("opencode server session: tool rejected, surfacing error as text", "tool", toolName, "error", errMsg)
		s.sendEvent(core.Event{Type: core.EventText, Content: toolRejectionNotice(toolName, errMsg)})
	}
}

// isAbortEcho reports whether an error is OpenCode's report of a turn we aborted
// ourselves (its message always carries NaN/abort wording rather than a provider
// failure).
func isAbortEcho(payload any) bool {
	text := strings.ToLower(fmt.Sprint(payload))
	return strings.Contains(text, "messageabortederror") || strings.Contains(text, "aborted")
}

// stallWatchdog aborts a turn that stops producing events. The run transport
// kills a silent opencode process after the same delay; the server equivalent is
// an abort, which keeps the conversation usable for the next message.
func (s *serverSession) stallWatchdog(ctx context.Context, tick time.Duration) {
	ticker := time.NewTicker(tick)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if s.stallTimeout < 0 {
				// opencode_stall_timeout = "off": never abort a silent turn.
				continue
			}
			if !s.turnInFlight.Load() || s.inner.resultSent.Load() {
				continue
			}
			started := s.turnStartedAt.Load()
			if started == 0 {
				continue
			}
			last := s.lastSessionEvent.Load()
			if last == 0 {
				// No event at all yet: measure from the start of the turn, so a
				// turn that hangs before its first event is caught too.
				last = started
			}
			idle := time.Since(time.Unix(0, last))
			if idle < s.stallTimeout {
				continue
			}
			if !s.stallReported.CompareAndSwap(false, true) {
				continue
			}
			s.abortStalledTurn(idle)
		}
	}
}

// abortStalledTurn stops a silent turn and ends it with the same notice the run
// transport shows, so the conversation can continue.
func (s *serverSession) abortStalledTurn(idle time.Duration) {
	sessionID := s.CurrentSessionID()
	slog.Error("opencode server session: no events for a long time, aborting the stalled turn",
		"session", sessionID, "idle", idle.Round(time.Second), "timeout", s.stallTimeout)

	s.abortedTurn.Store(true)
	if srv := s.server(); srv != nil && sessionID != "" && !srv.isExited() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		if err := srv.abortSession(ctx, sessionID); err != nil {
			slog.Warn("opencode server session: abort of the stalled turn failed", "session", sessionID, "error", err)
		}
		cancel()
	}

	s.inner.expectingContinue.Store(false)
	s.endTurnWithNotice(serverStallNotice)
	s.turnInFlight.Store(false)
}

// endTurnWithNotice ends the turn with a user-visible reason, delivering the
// notice both as text and as the result content so it reaches the user no matter
// how the base delivers the final answer (with step buffering the answer rides
// on the result, without it on the text events).
func (s *serverSession) endTurnWithNotice(notice string) {
	if !s.inner.resultSent.CompareAndSwap(false, true) {
		return
	}
	sessionID := s.CurrentSessionID()
	evt := core.Event{Type: core.EventResult, SessionID: sessionID, Content: notice, Done: true}
	if reporter, ok := any(s.inner).(core.ContextUsageReporter); ok {
		if usage := reporter.GetContextUsage(); usage != nil {
			evt.InputTokens = usage.InputTokens
			evt.OutputTokens = usage.OutputTokens
			evt.CacheReadInputTokens = usage.CachedInputTokens
			evt.CacheCreationInputTokens = usage.CacheCreationInputTokens
		}
	}
	s.sendEvent(core.Event{Type: core.EventText, Content: notice, SessionID: sessionID})
	s.sendEvent(evt)
}

func (s *serverSession) matchesSession(sessionID string) bool {
	s.sessionMu.Lock()
	current := s.sessionID
	s.sessionMu.Unlock()
	// Before this session has an id, nothing on the stream can belong to it —
	// the server is shared by every conversation in the workspace, so accepting
	// events "while we do not know our id yet" leaks another user's turn into
	// this reply. (A part can only be produced after the message that created
	// the session, so no event of ours is ever dropped here.)
	return current != "" && current == sessionID
}

// GetContextUsage implements core.ContextUsageReporter so server-transport turns
// report real token counts too (reply footer, and auto-compress decisions instead
// of the engine's heuristic estimate). OpenCode's token totals are accumulated by
// the run session, which reports them when it has the capability; on bases
// without that the delegation simply yields nil.
func (s *serverSession) GetContextUsage() *core.ContextUsage {
	reporter, ok := any(s.inner).(core.ContextUsageReporter)
	if !ok {
		return nil
	}
	return reporter.GetContextUsage()
}

// RespondPermission answers a permission request surfaced as
// core.EventPermissionRequest (non-yolo modes). "once" mirrors Allow;
// everything else rejects, which reports the refusal back to the model instead
// of leaving the turn parked.
func (s *serverSession) RespondPermission(requestID string, result core.PermissionResult) error {
	if requestID == "" {
		return nil
	}
	reply := "reject"
	if result.Behavior == "allow" {
		reply = "once"
	}
	srv := s.liveServer()
	if srv == nil {
		// The turn that asked is over (and its process reaped): the request can no
		// longer be answered, and the model has already been told the turn ended.
		return fmt.Errorf("opencode server session: no live server to answer permission %s", requestID)
	}
	if err := srv.replyPermission(s.inner.ctx, requestID, reply); err != nil {
		return fmt.Errorf("opencode server session: reply to permission %s: %w", requestID, err)
	}
	return nil
}

func (s *serverSession) Events() <-chan core.Event { return s.inner.Events() }

func (s *serverSession) CurrentSessionID() string {
	s.sessionMu.Lock()
	defer s.sessionMu.Unlock()
	if s.sessionID != "" {
		return s.sessionID
	}
	return s.inner.CurrentSessionID()
}

func (s *serverSession) Alive() bool { return s.inner.Alive() }

// Close aborts a turn that is still running and detaches from the server. The
// engine calls this for /stop as well as for teardown, and may call it more than
// once for the same session, so it is idempotent. Aborting (rather than killing
// a process) keeps the stored session usable for the next resume.
func (s *serverSession) Close() error {
	var err error
	s.closeOnce.Do(func() {
		err = s.close()
	})
	return err
}

func (s *serverSession) close() error {
	if s.turnInFlight.Load() {
		s.abortedTurn.Store(true)
		sessionID := s.CurrentSessionID()
		if srv := s.server(); sessionID != "" && srv != nil && !srv.isExited() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			if err := srv.abortSession(ctx, sessionID); err != nil {
				slog.Warn("opencode server session: abort on close failed", "error", err)
			}
			cancel()
		}
	}

	s.sseCancel()

	// Wait for the event-stream goroutine before closing the channel it feeds.
	// It may be parked on a full event buffer, so drain while waiting.
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	deadline := time.After(opencodeServerStopTimeout)
waitLoop:
	for {
		select {
		case <-done:
			break waitLoop
		case <-time.After(50 * time.Millisecond):
			s.drainEvents()
		case <-deadline:
			slog.Warn("opencode server session: event stream close timed out")
			break waitLoop
		}
	}

	// Stop accepting further events, then let the inner session close the
	// channel. Senders outside the SSE goroutine (Send error reporting) are
	// serialized behind the same flag, so they cannot hit a closed channel.
	s.eventMu.Lock()
	s.eventsClosed = true
	s.eventMu.Unlock()

	err := s.inner.Close()

	// Drop the reference the running turn holds, if any. Close during a turn is
	// what /stop and the idle reaper do.
	s.releaseCurrentTurn()
	return err
}
