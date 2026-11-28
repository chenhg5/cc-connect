package piweb

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// fakeAcpServer is a minimal ACP server speaking newline-delimited JSON-RPC
// over a real Unix socket. It answers initialize/session/new/session/load
// and can push session/update notifications, exercising the same wire
// behavior as pi-web's aiti-channels ACP server.
type fakeAcpServer struct {
	listener net.Listener
	// onNotify, when set, observes every notification method the server receives.
	onNotify func(method string)

	mu      sync.Mutex
	conns   []net.Conn
	connSeq int // per-connection session id sequence (concurrent-session tests)
}

func startFakeAcpServer(t *testing.T) *fakeAcpServer {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "test.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen unix: %v", err)
	}
	s := &fakeAcpServer{listener: ln}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			s.mu.Lock()
			s.conns = append(s.conns, conn)
			s.mu.Unlock()
			go s.serve(conn)
		}
	}()
	t.Cleanup(func() { _ = ln.Close() })
	return s
}

func (s *fakeAcpServer) serve(conn net.Conn) {
	defer conn.Close()
	r := bufio.NewReader(conn)
	enc := json.NewEncoder(conn)
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			return
		}
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if json.Unmarshal(line, &req) != nil || req.Method == "" {
			continue
		}
		if req.ID == nil && s.onNotify != nil {
			s.onNotify(req.Method)
		}
		switch req.Method {
		case "initialize":
			_ = enc.Encode(map[string]any{
				"jsonrpc": "2.0", "id": req.ID,
				"result": map[string]any{
					"protocolVersion":   1,
					"agentCapabilities": map[string]any{"loadSession": true, "sessionCapabilities": map[string]any{}},
				},
			})
		case "session/load":
			// stale id (registry lost / session gone) → error; agent/acp should fall back to session/new and self-heal
			if strings.Contains(string(line), "acp-stale") {
				_ = enc.Encode(map[string]any{
					"jsonrpc": "2.0", "id": req.ID,
					"error": map[string]any{"code": -32000, "message": "unknown session: acp-stale"},
				})
				continue
			}
			_ = enc.Encode(map[string]any{
				"jsonrpc": "2.0", "id": req.ID,
				"result": map[string]any{"sessionId": "acp-test-1234"},
			})
		case "session/new":
			// assign a distinct sessionId per connection (the first connection keeps acp-test-1234 for compat with existing cases)
			s.mu.Lock()
			s.connSeq++
			sid := fmt.Sprintf("acp-test-%d", 1233+s.connSeq)
			s.mu.Unlock()
			_ = enc.Encode(map[string]any{
				"jsonrpc": "2.0", "id": req.ID,
				"result": map[string]any{"sessionId": sid},
			})
		default:
			_ = enc.Encode(map[string]any{
				"jsonrpc": "2.0", "id": req.ID,
				"error": map[string]any{"code": -32601, "message": "not implemented"},
			})
		}
	}
}

// pushUpdateTo sends a session/update carrying the given sessionId only over
// the connection whose session/new returned that id (targeted push).
func (s *fakeAcpServer) pushUpdateTo(t *testing.T, sid string, update map[string]any) {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"method":  "session/update",
		"params": map[string]any{
			"sessionId": sid,
			"update":    update,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := 0
	if strings.HasSuffix(sid, "1235") {
		idx = 1
	} else if strings.HasSuffix(sid, "1236") {
		idx = 2
	}
	if idx < len(s.conns) {
		if _, err := s.conns[idx].Write(append(b, '\n')); err != nil {
			t.Fatalf("push update: %v", err)
		}
	}
}

// pushUpdate sends a session/update notification over every served
// connection (there is exactly one in these tests).
func (s *fakeAcpServer) pushUpdate(t *testing.T, update map[string]any) {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"method":  "session/update",
		"params": map[string]any{
			"sessionId": "acp-test-1234",
			"update":    update,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.conns {
		if _, err := c.Write(append(b, '\n')); err != nil {
			t.Fatalf("push update: %v", err)
		}
	}
}

func TestNew_RequiresSocket(t *testing.T) {
	if _, err := New(map[string]any{}); err == nil {
		t.Fatal("New without socket should fail")
	}
	a, err := New(map[string]any{"socket": "/nonexistent.sock"})
	if err != nil {
		t.Fatal(err)
	}
	if a.Name() != "piweb" {
		t.Fatalf("Name = %q, want piweb", a.Name())
	}
}

func TestNew_DisplayNameDefault(t *testing.T) {
	a, _ := New(map[string]any{"socket": "/nonexistent.sock"})
	if got := a.(*Agent).CLIDisplayName(); got != "pi-web" {
		t.Fatalf("CLIDisplayName = %q, want pi-web", got)
	}
}

func TestWorkspaceAgentOptions(t *testing.T) {
	a, err := New(map[string]any{
		"socket":       "/tmp/pi.sock",
		"display_name": "pi-web 会话",
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshotter, ok := a.(core.WorkspaceAgentOptionSnapshotter)
	if !ok {
		t.Fatal("agent does not implement WorkspaceAgentOptionSnapshotter")
	}
	opts := snapshotter.WorkspaceAgentOptions()
	if got, _ := opts["socket"].(string); got != "/tmp/pi.sock" {
		t.Fatalf("socket = %q", got)
	}
	if got, _ := opts["display_name"].(string); got != "pi-web 会话" {
		t.Fatalf("display_name = %q", got)
	}
	if _, ok := opts["work_dir"]; ok {
		t.Fatal("work_dir must not be in the snapshot (engine owns it)")
	}
}

// TestWorkDirAndStop covers the WorkDirSwitcher surface and Stop no-op.
func TestWorkDirAndStop(t *testing.T) {
	a, _ := New(map[string]any{"socket": "/tmp/pi.sock", "work_dir": "/tmp/wd-a"})
	agent := a.(*Agent)
	if got := agent.GetWorkDir(); got != "/tmp/wd-a" {
		t.Fatalf("GetWorkDir = %q, want /tmp/wd-a", got)
	}
	agent.SetWorkDir("/tmp/wd-b")
	if got := agent.GetWorkDir(); got != "/tmp/wd-b" {
		t.Fatalf("GetWorkDir after SetWorkDir = %q", got)
	}
	if err := a.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

// TestStartSession_DialFail verifies the error mentions the socket path so
// the IM user can tell pi-web is not running.
func TestStartSession_DialFail(t *testing.T) {
	a, _ := New(map[string]any{"socket": filepath.Join(t.TempDir(), "missing.sock")})
	if _, err := a.StartSession(context.Background(), ""); err == nil {
		t.Fatal("StartSession on missing socket should fail")
	} else if !strings.Contains(err.Error(), "missing.sock") {
		t.Fatalf("error should mention socket path, got: %v", err)
	}
}

// TestStartSession_FullFlow dials a fake ACP server over a real Unix socket:
// the handshake reuses agent/acp (session id comes back), a pushed
// session/update flows out as a core.Event, and Close tears the conn down.
func TestStartSession_FullFlow(t *testing.T) {
	srv := startFakeAcpServer(t)

	a, err := New(map[string]any{"socket": srv.listener.Addr().String(), "work_dir": t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	sess, err := a.StartSession(context.Background(), "")
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	if got := sess.CurrentSessionID(); got != "acp-test-1234" {
		t.Fatalf("CurrentSessionID = %q, want acp-test-1234", got)
	}
	if !sess.Alive() {
		t.Fatal("session should be alive after handshake")
	}

	srv.pushUpdate(t, map[string]any{
		"sessionUpdate": "agent_message_chunk",
		"content":       map[string]any{"type": "text", "text": "hello from pi-web"},
	})
	select {
	case ev := <-sess.Events():
		if ev.Type != core.EventText {
			t.Fatalf("event type = %q, want %q", ev.Type, core.EventText)
		}
		if ev.Content != "hello from pi-web" {
			t.Fatalf("event content = %q", ev.Content)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for session/update to surface as core.Event")
	}

	if err := sess.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if sess.Alive() {
		t.Fatal("session should not be alive after Close")
	}
}

// TestStartSession_StaleIDSelfHeal: a persisted session id that the server no
// longer knows (registry lost) must not break StartSession — agent/acp warns
// and falls back to session/new, returning a fresh id.
func TestStartSession_StaleIDSelfHeal(t *testing.T) {
	srv := startFakeAcpServer(t)
	a, _ := New(map[string]any{"socket": srv.listener.Addr().String(), "work_dir": t.TempDir()})
	sess, err := a.StartSession(context.Background(), "acp-stale")
	if err != nil {
		t.Fatalf("StartSession with stale id should self-heal: %v", err)
	}
	defer sess.Close()
	if got := sess.CurrentSessionID(); got != "acp-test-1234" {
		t.Fatalf("CurrentSessionID = %q, want fresh acp-test-1234", got)
	}
	if !sess.Alive() {
		t.Fatal("session should be alive after self-heal")
	}
}

// TestStartSession_CtxCancelClosesConn: canceling the parent context without
// Close must end the session (the ctx watcher closes the conn, unblocking the
// read loop) — mirrors CommandContext auto-kill on the spawn path.
func TestStartSession_CtxCancelClosesConn(t *testing.T) {
	srv := startFakeAcpServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	a, _ := New(map[string]any{"socket": srv.listener.Addr().String(), "work_dir": t.TempDir()})
	sess, err := a.StartSession(ctx, "")
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	if !sess.Alive() {
		t.Fatal("session should start alive")
	}
	cancel()
	deadline := time.After(3 * time.Second)
	for sess.Alive() {
		select {
		case <-deadline:
			t.Fatal("session still alive 3s after ctx cancel")
		case <-time.After(10 * time.Millisecond):
		}
	}
	_ = sess.Close()
}

// TestConcurrentSessions_Isolation: two simultaneous sessions (two socket
// connections, two distinct pi sessions) must not cross-stream — each
// connection's updates surface only on its own session's event channel.
// This mirrors two IM chats being served in parallel by one piweb agent.
func TestConcurrentSessions_Isolation(t *testing.T) {
	srv := startFakeAcpServer(t)
	newAgent := func() (core.AgentSession, error) {
		a, _ := New(map[string]any{"socket": srv.listener.Addr().String(), "work_dir": t.TempDir()})
		return a.StartSession(context.Background(), "")
	}
	sessA, err := newAgent()
	if err != nil {
		t.Fatal(err)
	}
	defer sessA.Close()
	sessB, err := newAgent()
	if err != nil {
		t.Fatal(err)
	}
	defer sessB.Close()

	idA, idB := sessA.CurrentSessionID(), sessB.CurrentSessionID()
	if idA == "" || idB == "" || idA == idB {
		t.Fatalf("session ids must be distinct and non-empty: %q vs %q", idA, idB)
	}

	// 服务端向两个连接分别推送不同文本
	srv.pushUpdateTo(t, idA, map[string]any{
		"sessionUpdate": "agent_message_chunk",
		"content":       map[string]any{"type": "text", "text": "from-A"},
	})
	srv.pushUpdateTo(t, idB, map[string]any{
		"sessionUpdate": "agent_message_chunk",
		"content":       map[string]any{"type": "text", "text": "from-B"},
	})

	readText := func(sess core.AgentSession, tag string) string {
		t.Helper()
		deadline := time.After(3 * time.Second)
		var text string
		for {
			select {
			case ev := <-sess.Events():
				if ev.Type == core.EventText {
					text += ev.Content
					if text != "" {
						return text
					}
				}
			case <-deadline:
				t.Fatalf("[%s] timed out waiting for its own update", tag)
			}
		}
	}
	textA := readText(sessA, "A")
	textB := readText(sessB, "B")
	if textA != "from-A" {
		t.Fatalf("session A got %q, want from-A (cross-stream!)", textA)
	}
	if textB != "from-B" {
		t.Fatalf("session B got %q, want from-B (cross-stream!)", textB)
	}
}

// TestCancelTurn_Notification verifies /stop surfaces as a session/cancel
// notification on the socket (pi-web side aborts the run, session stays up).
func TestCancelTurn_Notification(t *testing.T) {
	srv := startFakeAcpServer(t)

	// Record notifications received by the server.
	var gotCancel chan string = make(chan string, 1)
	srv.onNotify = func(method string) {
		if method == "session/cancel" {
			gotCancel <- method
		}
	}

	a, _ := New(map[string]any{"socket": srv.listener.Addr().String(), "work_dir": t.TempDir()})
	sess, err := a.StartSession(context.Background(), "")
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	defer sess.Close()

	canceler, ok := sess.(interface{ CancelTurn() error })
	if !ok {
		t.Fatal("session does not implement CancelTurn")
	}
	if err := canceler.CancelTurn(); err != nil {
		t.Fatalf("CancelTurn: %v", err)
	}
	select {
	case <-gotCancel:
		// notification reached the server
	case <-time.After(3 * time.Second):
		t.Fatal("session/cancel notification not received by server")
	}
	// Session must survive a cancel (it's not a Close).
	if !sess.Alive() {
		t.Fatal("session should stay alive after CancelTurn")
	}
}

// TestStartSession_Resume passes a previous session id; agent/acp turns it
// into a session/load which the fake server answers with the same id.
func TestStartSession_Resume(t *testing.T) {
	srv := startFakeAcpServer(t)
	a, _ := New(map[string]any{"socket": srv.listener.Addr().String(), "work_dir": t.TempDir()})
	sess, err := a.StartSession(context.Background(), "acp-prev-session")
	if err != nil {
		t.Fatalf("StartSession resume: %v", err)
	}
	defer sess.Close()
	if got := sess.CurrentSessionID(); got != "acp-test-1234" {
		t.Fatalf("CurrentSessionID = %q, want acp-test-1234", got)
	}
}

// TestIntegration_PiWebSocket_Resume resumes a previously created session
// (PIWEB_SMOKE_RESUME=<acpSessionId>): session/load must return the same id,
// proving cc-connect's persisted session id round-trips through pi-web's
// aiti-channels registry (reattach).
func TestIntegration_PiWebSocket_Resume(t *testing.T) {
	sid := os.Getenv("PIWEB_SMOKE_RESUME")
	if sid == "" {
		t.Skip("PIWEB_SMOKE_RESUME not set; skipping live resume")
	}
	sock := os.Getenv("PIWEB_SMOKE_SOCKET")
	if sock == "" {
		t.Skip("PIWEB_SMOKE_SOCKET not set")
	}
	cwd := os.Getenv("PIWEB_SMOKE_CWD")
	if cwd == "" {
		cwd = t.TempDir()
	}
	a, _ := New(map[string]any{"socket": sock, "work_dir": cwd})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	sess, err := a.StartSession(ctx, sid)
	if err != nil {
		t.Fatalf("StartSession(resume): %v", err)
	}
	defer sess.Close()
	if got := sess.CurrentSessionID(); got != sid {
		t.Fatalf("resume returned %q, want original %q", got, sid)
	}
	t.Logf("resumed pi-web ACP session: %s", sid)

	// Optional: prove the reopened session is interactive, not just mounted.
	if os.Getenv("PIWEB_SMOKE_RESUME_PROMPT") != "" {
		if err := sess.Send("只回复两个字符：在", "resume-test", nil, nil); err != nil {
			t.Fatalf("Send on resumed session: %v", err)
		}
		var text strings.Builder
		deadline := time.After(60 * time.Second)
		for {
			select {
			case ev, ok := <-sess.Events():
				if !ok {
					if text.Len() > 0 {
						t.Logf("reply on resumed session: %s", text.String())
						return
					}
					t.Fatal("events closed before text")
				}
				switch ev.Type {
				case core.EventText:
					text.WriteString(ev.Content)
				case core.EventError:
					t.Fatalf("agent error: %v", ev.Error)
				case core.EventResult:
					if text.Len() == 0 {
						t.Fatal("resumed session replied empty")
					}
					t.Logf("reply on resumed session: %s", text.String())
					return
				}
			case <-deadline:
				t.Fatalf("timed out (got %q)", text.String())
			}
		}
	}
}

// TestIntegration_PiWebSocket runs a full prompt round-trip against a live
// pi-web ACP server (aiti-channels extension): dial → handshake → prompt →
// streamed text events. Skipped unless PIWEB_SMOKE_SOCKET is set to the
// socket path (e.g. ~/.pi/web/data/aiti-channels/config/acp/default.sock),
// so unit runs stay hermetic. Creates a real pi-web session.
func TestIntegration_PiWebSocket(t *testing.T) {
	sock := os.Getenv("PIWEB_SMOKE_SOCKET")
	if sock == "" {
		t.Skip("PIWEB_SMOKE_SOCKET not set; skipping live pi-web round-trip")
	}
	cwd := os.Getenv("PIWEB_SMOKE_CWD")
	if cwd == "" {
		cwd = t.TempDir()
	}
	a, err := New(map[string]any{"socket": sock, "work_dir": cwd})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	sess, err := a.StartSession(ctx, "")
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	defer sess.Close()
	t.Logf("pi-web ACP session established: %s", sess.CurrentSessionID())

	if err := sess.Send("只回复两个字符：pong", "integration-test", nil, nil); err != nil {
		t.Fatalf("Send: %v", err)
	}
	var text strings.Builder
	deadline := time.After(80 * time.Second)
	for {
		select {
		case ev, ok := <-sess.Events():
			if !ok {
				// channel closed after turn end (aggregated); fall through to done check
				if text.Len() > 0 {
					t.Logf("reply: %s", text.String())
					return
				}
				t.Fatal("events channel closed before any text")
			}
			switch ev.Type {
			case core.EventText:
				text.WriteString(ev.Content)
			case core.EventError:
				t.Fatalf("agent error: %v", ev.Error)
			case core.EventResult:
				if text.Len() == 0 {
					t.Fatal("turn finished with empty reply")
				}
				t.Logf("reply: %s", text.String())
				return
			}
		case <-deadline:
			t.Fatalf("timed out waiting for reply (got %q)", text.String())
		}
	}
}

func TestSkillDirs(t *testing.T) {
	dir := t.TempDir()
	a, err := New(map[string]any{"socket": "/tmp/s.sock", "work_dir": dir})
	if err != nil {
		t.Fatal(err)
	}
	dirs := a.(core.SkillProvider).SkillDirs()
	want := []string{
		filepath.Join(dir, ".pi", "skills"),
		filepath.Join(dir, ".agents", "skills"),
		filepath.Join(dir, ".pi", "agent", "skills"),
	}
	home, _ := os.UserHomeDir()
	if home != "" {
		want = append(want,
			filepath.Join(home, ".pi", "agent", "skills"),
			filepath.Join(home, ".agents", "skills"),
		)
	}
	if len(dirs) < len(want) {
		t.Fatalf("SkillDirs = %v, want at least %v", dirs, want)
	}
	for i, w := range want {
		if dirs[i] != w {
			t.Fatalf("SkillDirs[%d] = %q, want %q (full: %v)", i, dirs[i], w, dirs)
		}
	}
}
