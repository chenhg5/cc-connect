package opencode

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
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
	opencodeServerIdleTTL      = 60 * time.Second
	opencodeServerStopTimeout  = 5 * time.Second
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
	extraEnv  []string
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

// serverKey identifies a server by the binary, its extra args and the
// workspace directory it serves.
func serverKey(cfg opencodeServeConfig) string {
	return cfg.cmd + "\x00" + strings.Join(cfg.extraArgs, "\x00") + "\x00" + cfg.workDir
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
		opencodeServersMu.Lock()
		if cur, ok := opencodeServers[key]; ok && cur == srv {
			delete(opencodeServers, key)
		}
		opencodeServersMu.Unlock()
		return nil, errServerGone
	}
	srv.refs++
	if srv.idleTimer != nil {
		srv.idleTimer.Stop()
		srv.idleTimer = nil
	}
	return srv, nil
}

// errServerGone signals that a server exited while being acquired.
var errServerGone = errors.New("opencode server: process exited during acquire")

// releaseOpencodeServer drops a reference; the process is kept alive for a
// short grace period so a follow-up turn in the same workspace does not pay the
// startup cost again, then stopped.
func releaseOpencodeServer(srv *opencodeServer) {
	if srv == nil {
		return
	}
	srv.mu.Lock()
	if srv.refs > 0 {
		srv.refs--
	}
	remaining := srv.refs
	if remaining == 0 && !srv.stopped {
		if srv.idleTimer != nil {
			srv.idleTimer.Stop()
		}
		srv.idleTimer = time.AfterFunc(opencodeServerIdleTTL, func() {
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
	_ = cmd.Process.Signal(os.Interrupt)
	if srv.waitDone == nil {
		return
	}
	select {
	case <-srv.waitDone:
	case <-time.After(opencodeServerStopTimeout):
		if err := cmd.Process.Kill(); err != nil {
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
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		return nil, err
	}

	srv := &opencodeServer{
		baseURL:  baseURL,
		password: password,
		key:      serverKey(cfg),
		cmd:      cmd,
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
				ch <- result{url: strings.TrimSuffix(m[1], "/")}
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

	resp, err := http.DefaultClient.Do(req)
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
// messages.
func (srv *opencodeServer) diagnostics() string {
	if srv.logTail == nil {
		return ""
	}
	return truncate(strings.TrimSpace(srv.logTail.String()), 300)
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
		body["permission"] = []map[string]any{{"permission": "*", "pattern": "*", "action": "allow"}}
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

// events opens the server's event stream (SSE).
func (srv *opencodeServer) events(ctx context.Context) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.baseURL+"/event", nil)
	if err != nil {
		return nil, fmt.Errorf("opencode server: build event request: %w", err)
	}
	req.Header.Set("Accept", "text/event-stream")
	req.SetBasicAuth(opencodeServerUser, srv.password)

	resp, err := http.DefaultClient.Do(req)
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

	workDir   string
	model     string
	agentName string
	mode      string

	sessionMu sync.Mutex
	sessionID string

	sendMu       sync.Mutex
	turnInFlight atomic.Bool

	msgMu         sync.Mutex
	assistantMsgs map[string]struct{}
	emittedTools  map[string]struct{}

	sseCancel context.CancelFunc
	wg        sync.WaitGroup
	closeOnce sync.Once
	// streamLossReported bounds stream-loss reporting to once per turn.
	streamLossReported atomic.Bool
	// finalStep holds the last reason="stop" step-finish part, used to flush the
	// buffered answer (and its token totals) when the session goes idle.
	finalStep atomic.Value

	// eventMu guards eventsClosed, which stops the transport from sending on the
	// engine's event channel once Close has started tearing the session down
	// (the channel itself is closed by the inner session).
	eventMu      sync.Mutex
	eventsClosed bool
}

// newServerSession starts (or reuses) an `opencode serve` instance for the
// workspace and attaches a session to it.
func newServerSession(ctx context.Context, serveCfg opencodeServeConfig, model, mode, agentName, resumeID string) (*serverSession, error) {
	srv, err := acquireOpencodeServer(ctx, serveCfg)
	if err != nil {
		return nil, err
	}
	s, err := newServerSessionOn(ctx, srv, serveCfg, model, mode, agentName, resumeID)
	if err != nil {
		releaseOpencodeServer(srv)
		return nil, err
	}
	return s, nil
}

// newServerSessionOn attaches to an already running server. Split out from
// newServerSession so tests can drive the session against a stub server.
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
		emittedTools:  map[string]struct{}{},
		sseCancel:     cancel,
	}

	s.wg.Add(1)
	go s.readEventStream(sessionCtx)
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

	srv, err := s.ensureServer(s.inner.ctx)
	if err != nil {
		s.emitError(err)
		return err
	}

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

	// A new prompt starts (or supplements) a turn: allow exactly one EventResult
	// for it, matching the run transport.
	s.inner.resultSent.Store(false)
	s.inner.expectingContinue.Store(false)
	s.turnInFlight.Store(true)
	// Each turn gets its own budget for one stream-loss report, so an outage that
	// happened while the session was idle cannot swallow the report for a turn.
	s.streamLossReported.Store(false)
	s.finalStep = atomic.Value{}

	s.sendMu.Lock()
	defer s.sendMu.Unlock()

	done := make(chan error, 1)
	go func() {
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
		// successful return means nothing is in flight any more.
		s.turnInFlight.Store(false)
		if err == nil {
			s.ensureTurnResult()
		}
		if err != nil {
			// The engine surfaces Send errors itself, so no extra event here —
			// mirroring the run transport.
			return err
		}
		return nil
	case <-time.After(150 * time.Millisecond):
		// Return promptly so the engine can keep processing incoming messages
		// (including a mid-turn /ps) while the turn runs. The request itself
		// stays open until the turn completes.
		go func() {
			err := <-done
			s.turnInFlight.Store(false)
			if err != nil {
				s.emitError(err)
				return
			}
			s.ensureTurnResult()
		}()
		return nil
	case <-s.inner.ctx.Done():
		return s.inner.ctx.Err()
	}
}

// server returns the server this session is currently talking to.
func (s *serverSession) server() *opencodeServer {
	s.srvMu.Lock()
	defer s.srvMu.Unlock()
	return s.srv
}

// ensureServer returns a live server, replacing one whose process has exited.
// OpenCode keeps session state in its own store, so a fresh server resumes the
// same conversation — a crashed server therefore costs one restart, not the
// conversation.
func (s *serverSession) ensureServer(ctx context.Context) (*opencodeServer, error) {
	if cur := s.server(); cur != nil && !cur.isExited() {
		return cur, nil
	}

	s.srvMu.Lock()
	defer s.srvMu.Unlock()
	if s.srv != nil && !s.srv.isExited() {
		return s.srv, nil
	}
	stale := s.srv
	fresh, err := acquireOpencodeServer(ctx, s.serveCfg)
	if err != nil {
		return nil, err
	}
	s.srv = fresh
	if stale != nil {
		releaseOpencodeServer(stale)
	}
	slog.Info("opencode server session: reattached to a fresh server", "url", fresh.baseURL, "dir", s.serveCfg.workDir)
	return fresh, nil
}

// flushFinalAnswer delivers the text the inner session buffered for the final
// step, then closes the turn. The inner session flushes on a reason="stop"
// step-finish, which this transport deliberately defers (compaction emits the
// same reason), so the flush happens here — once the session really is idle.
func (s *serverSession) flushFinalAnswer() {
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

	s.endTurn() // no-op when the flush already delivered the result
}

// endTurn tells the engine the turn is over, exactly once, through the guarded
// send path (the run transport's helper writes to the channel directly, which
// would race Close).
func (s *serverSession) endTurn() {
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
	s.sessionMu.Unlock()
	s.inner.chatID.Store("")
}

// ensureTurnResult makes sure the engine is told the turn is over exactly once.
// The primary signal is the stream's session.idle event; this is the safety net
// for a turn whose idle event was missed (stream hiccup), using the fact that the
// message request only returns once the turn has finished. It waits briefly so
// the trailing text parts are not overtaken by the result.
func (s *serverSession) ensureTurnResult() {
	for i := 0; i < 15; i++ {
		if s.inner.resultSent.Load() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	slog.Warn("opencode server session: no idle event for a finished turn, ending it explicitly")
	s.flushFinalAnswer()
}

// ensureSession returns the OpenCode session id for this turn, creating the
// session on first use. created reports whether it had to be created, so the
// caller can announce the new id to the engine.
func (s *serverSession) ensureSession() (string, bool, error) {
	s.sessionMu.Lock()
	defer s.sessionMu.Unlock()
	if s.sessionID != "" {
		return s.sessionID, false, nil
	}
	id, err := s.srv.createSession(s.inner.ctx, s.workDir, s.agentName, s.mode)
	if err != nil {
		return "", false, err
	}
	s.sessionID = id
	s.inner.chatID.Store(id)
	return id, true, nil
}

func (s *serverSession) emitError(err error) {
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
// server that exits mid-turn is replaced (see ensureServer) and the stream is
// re-opened against the replacement, so a crash costs a restart rather than the
// conversation.
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
			s.reportStreamFailure(err)
			select {
			case <-time.After(opencodeServerRetryDelay):
			case <-ctx.Done():
				return
			}
			continue
		}

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

// streamEvents opens the event stream against the current server, re-acquiring
// one when the previous process exited.
func (s *serverSession) streamEvents(ctx context.Context) (io.ReadCloser, error) {
	srv, err := s.ensureServer(ctx)
	if err != nil {
		return nil, err
	}
	return srv.events(ctx)
}

// reportStreamFailure tells the user once per turn that events stopped flowing
// while a turn was running; reconnects afterwards stay silent so a flapping
// server cannot spam the chat.
func (s *serverSession) reportStreamFailure(err error) {
	if !s.turnInFlight.Load() || !s.streamLossReported.CompareAndSwap(false, true) {
		slog.Warn("opencode server session: event stream unavailable", "error", err)
		return
	}
	slog.Error("opencode server session: event stream lost mid-turn", "error", err)
	s.sendEvent(core.Event{Type: core.EventError, Error: fmt.Errorf("opencode event stream lost: %w", err)})
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
	// no sessionID) are ignored below by their type.
	if sid, _ := evt.Properties["sessionID"].(string); sid != "" && !s.matchesSession(sid) {
		return
	}

	switch evt.Type {
	case "message.updated":
		info, _ := evt.Properties["info"].(map[string]any)
		if info == nil {
			return
		}
		if role, _ := info["role"].(string); role == "assistant" {
			if id, _ := info["id"].(string); id != "" {
				s.msgMu.Lock()
				s.assistantMsgs[id] = struct{}{}
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
		s.flushFinalAnswer()
	case "session.status":
		if status, ok := evt.Properties["status"].(map[string]any); ok {
			if kind, _ := status["type"].(string); kind == "idle" {
				s.turnInFlight.Store(false)
				s.flushFinalAnswer()
			}
		}
	case "session.error":
		s.turnInFlight.Store(false)
		s.inner.handleError(map[string]any{"error": evt.Properties["error"]})
	}
}

// dispatchPart converts one OpenCode part into engine events.
func (s *serverSession) dispatchPart(part map[string]any) {
	partType, _ := part["type"].(string)

	switch partType {
	case "text":
		if !s.isAssistantPart(part) {
			// The user's own prompt is echoed back as a text part; emitting it
			// would duplicate the request inside the reply.
			return
		}
		s.inner.handleText(map[string]any{"part": part})
	case "reasoning":
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

func (s *serverSession) isAssistantPart(part map[string]any) bool {
	id, _ := part["messageID"].(string)
	if id == "" {
		return false
	}
	s.msgMu.Lock()
	defer s.msgMu.Unlock()
	_, ok := s.assistantMsgs[id]
	return ok
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
		s.sendEvent(core.Event{Type: core.EventText, Content: errMsg})
	}
}

func (s *serverSession) matchesSession(sessionID string) bool {
	s.sessionMu.Lock()
	current := s.sessionID
	s.sessionMu.Unlock()
	return current == "" || current == sessionID
}

func (s *serverSession) RespondPermission(_ string, _ core.PermissionResult) error { return nil }

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

	if srv := s.server(); srv != nil {
		releaseOpencodeServer(srv)
	}
	return err
}
