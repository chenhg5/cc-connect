package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// --- Agent: mode cache & SetMode/GetMode ---------------------------

func TestAgent_PermissionModes_emptyBeforeFirstHandshake(t *testing.T) {
	a := &Agent{}
	if got := a.PermissionModes(); len(got) != 0 {
		t.Fatalf("want empty modes before first handshake, got %v", got)
	}
	if got := a.GetMode(); got != "" {
		t.Fatalf("want empty mode before handshake, got %q", got)
	}
}

func TestAgent_reportModes_populatesCache(t *testing.T) {
	a := &Agent{}
	a.reportModes(acpModesBlock{
		CurrentModeID: "plan",
		AvailableModes: []acpModeInfo{
			{ID: "normal", Name: "Code", Description: "Write and edit code"},
			{ID: "plan", Name: "Plan", Description: "Plan changes"},
			{ID: "bypass", Name: "Bypass Permissions"},
		},
	})

	modes := a.PermissionModes()
	if len(modes) != 3 {
		t.Fatalf("got %d modes, want 3", len(modes))
	}
	if modes[0].Key != "normal" || modes[0].Name != "Code" || modes[0].Desc != "Write and edit code" {
		t.Fatalf("modes[0] = %+v", modes[0])
	}
	if modes[0].NameZh != "Code" || modes[0].DescZh != "Write and edit code" {
		t.Fatalf("zh fallback missing on modes[0] = %+v", modes[0])
	}
	// Nobody has called SetMode, so GetMode falls back to the
	// server-reported currentModeId.
	if got := a.GetMode(); got != "plan" {
		t.Fatalf("GetMode = %q, want plan (fallback to server currentModeId when no explicit SetMode)", got)
	}
}

// Regression: after `/mode plan`, cc-connect's engine calls SetMode("plan")
// then reads back GetMode() to decide what to display and apply via
// SetLiveMode. The pending SetMode MUST win over the previously-cached
// currentModeId, otherwise /mode reports the wrong mode name and the
// live switch goes to the old mode.
func TestAgent_GetMode_pendingWinsOverCachedCurrent(t *testing.T) {
	a := &Agent{}
	// Simulate a first session handshake which reported current=normal.
	a.reportModes(acpModesBlock{
		CurrentModeID: "normal",
		AvailableModes: []acpModeInfo{
			{ID: "normal", Name: "Code"},
			{ID: "plan", Name: "Plan"},
		},
	})
	if got := a.GetMode(); got != "normal" {
		t.Fatalf("pre-SetMode GetMode = %q, want normal", got)
	}

	a.SetMode("plan")
	if got := a.GetMode(); got != "plan" {
		t.Fatalf("post-SetMode GetMode = %q, want plan (pending takes precedence)", got)
	}
}

func TestAgent_SetMode_normalisesAgainstCache(t *testing.T) {
	a := &Agent{}
	a.reportModes(acpModesBlock{
		CurrentModeID: "normal",
		AvailableModes: []acpModeInfo{
			{ID: "normal", Name: "Code"},
			{ID: "accept-edits", Name: "Accept Edits"},
		},
	})

	// Case-insensitive match on id
	a.SetMode("Normal")
	a.mu.RLock()
	if a.mode != "normal" {
		t.Fatalf("pending mode = %q, want normal", a.mode)
	}
	a.mu.RUnlock()

	// Case-insensitive match on display name → canonical id
	a.SetMode("accept edits")
	a.mu.RLock()
	gotPending := a.mode
	a.mu.RUnlock()
	if gotPending != "accept-edits" {
		t.Fatalf("pending mode = %q, want accept-edits (normalised via case-insensitive display-name match)", gotPending)
	}

	// Unknown input → stored as-is so a later StartSession can try it
	// (at which point session/set_mode will soft-fail loudly).
	a.SetMode("totally-unknown")
	a.mu.RLock()
	gotPending = a.mode
	a.mu.RUnlock()
	if gotPending != "totally-unknown" {
		t.Fatalf("pending mode = %q, want totally-unknown (passthrough)", gotPending)
	}
}

func TestAgent_GetMode_fallbackToPendingWhenNoSession(t *testing.T) {
	a := &Agent{mode: "plan"}
	if got := a.GetMode(); got != "plan" {
		t.Fatalf("GetMode = %q, want plan (pending, no handshake yet)", got)
	}
}

// --- session/list parsing ------------------------------------------

func TestConvertSessionList_cwdFilter(t *testing.T) {
	entries := []acpSessionListEntry{
		{SessionID: "a", Cwd: "/tmp/proj1", Title: "First", UpdatedAt: "2026-04-18T16:15:29+00:00"},
		{SessionID: "b", Cwd: "/tmp/proj2", Title: "Second", UpdatedAt: "2026-04-18T16:10:29+00:00"},
		// Entry without cwd passes through regardless of filter
		{SessionID: "c", Cwd: "", Title: "Third", UpdatedAt: "2026-04-18T16:05:29+00:00"},
	}

	got := convertSessionList(entries, "/tmp/proj1")
	if len(got) != 2 {
		t.Fatalf("got %d, want 2 (proj1 + cwd-less)", len(got))
	}
	if got[0].ID != "a" || got[0].Summary != "First" {
		t.Fatalf("entry[0] = %+v", got[0])
	}
	if got[1].ID != "c" {
		t.Fatalf("entry[1] = %+v, want passthrough of cwd-less entry", got[1])
	}
	if got[0].ModifiedAt.IsZero() {
		t.Fatalf("ModifiedAt not parsed: %+v", got[0])
	}
}

func TestConvertSessionList_noCwdFilter(t *testing.T) {
	entries := []acpSessionListEntry{
		{SessionID: "a", Cwd: "/tmp/proj1"},
		{SessionID: "b", Cwd: "/tmp/proj2"},
	}
	got := convertSessionList(entries, "")
	if len(got) != 2 {
		t.Fatalf("got %d, want 2 when no filter", len(got))
	}
}

func TestConvertSessionList_pathCleanAndCaseInsensitive(t *testing.T) {
	entries := []acpSessionListEntry{
		{SessionID: "a", Cwd: "/Users/Foo/Proj"},
		{SessionID: "b", Cwd: "/users/foo/proj"}, // case-insensitive match expected on case-insensitive FS
		{SessionID: "c", Cwd: "/Users/Foo/Proj/sub"},
	}
	// filter that includes trailing separator to verify Clean
	got := convertSessionList(entries, "/Users/Foo/Proj/")
	if len(got) != 2 {
		t.Fatalf("got %d, want 2 (case-insensitive + Clean match on first two)", len(got))
	}
}

// Verifies probeListSessions swallows -32601 (method not found) and
// surfaces other errors.
func TestProbeListSessions_softFailsOnMethodNotFound(t *testing.T) {
	rResp, wResp := io.Pipe()
	rReq, wReq := io.Pipe()

	tr := newTransport(rResp, wReq, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go tr.readLoop(ctx)

	// Mock server: respond -32601 for session/list.
	go func() {
		defer wResp.Close()
		sc := bufio.NewScanner(rReq)
		for sc.Scan() {
			var req map[string]any
			if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
				continue
			}
			id := req["id"]
			line := fmt.Sprintf(`{"jsonrpc":"2.0","id":%v,"error":{"code":-32601,"message":"method not found"}}`+"\n", id)
			_, _ = io.WriteString(wResp, line)
		}
	}()

	entries, err := probeListSessions(ctx, tr, "")
	if err != nil {
		t.Fatalf("want nil error on method-not-found, got %v", err)
	}
	if entries != nil {
		t.Fatalf("want nil entries on method-not-found, got %v", entries)
	}
}

func TestProbeListSessions_propagatesHardError(t *testing.T) {
	rResp, wResp := io.Pipe()
	rReq, wReq := io.Pipe()

	tr := newTransport(rResp, wReq, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go tr.readLoop(ctx)

	go func() {
		defer wResp.Close()
		sc := bufio.NewScanner(rReq)
		for sc.Scan() {
			var req map[string]any
			if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
				continue
			}
			id := req["id"]
			line := fmt.Sprintf(`{"jsonrpc":"2.0","id":%v,"error":{"code":-32000,"message":"boom"}}`+"\n", id)
			_, _ = io.WriteString(wResp, line)
		}
	}()

	entries, err := probeListSessions(ctx, tr, "")
	if err == nil {
		t.Fatalf("want error on hard failure, got entries=%v", entries)
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Fatalf("expected wrapped error to contain 'boom', got %v", err)
	}
}

func TestProbeListSessions_parsesSessions(t *testing.T) {
	rResp, wResp := io.Pipe()
	rReq, wReq := io.Pipe()

	tr := newTransport(rResp, wReq, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go tr.readLoop(ctx)

	go func() {
		defer wResp.Close()
		sc := bufio.NewScanner(rReq)
		for sc.Scan() {
			var req map[string]any
			if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
				continue
			}
			id := req["id"]
			line := fmt.Sprintf(`{"jsonrpc":"2.0","id":%v,"result":{"sessions":[{"sessionId":"s1","cwd":"/tmp","title":"hi","updatedAt":"2026-04-18T16:15:29+00:00"}]}}`+"\n", id)
			_, _ = io.WriteString(wResp, line)
		}
	}()

	entries, err := probeListSessions(ctx, tr, "/tmp")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].SessionID != "s1" || entries[0].Title != "hi" {
		t.Fatalf("unexpected entries: %+v", entries)
	}
}

// --- session: SetLiveMode + callbacks ------------------------------

// fakeCallbacks captures reportModes / reportListSupported invocations
// so tests can assert on them deterministically.
type fakeCallbacks struct {
	mu         sync.Mutex
	modes      []acpModesBlock
	listCalls  []bool
}

func (f *fakeCallbacks) reportModes(b acpModesBlock)       { f.mu.Lock(); f.modes = append(f.modes, b); f.mu.Unlock() }
func (f *fakeCallbacks) reportListSupported(supported bool) { f.mu.Lock(); f.listCalls = append(f.listCalls, supported); f.mu.Unlock() }
func (f *fakeCallbacks) lastModes() (acpModesBlock, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.modes) == 0 {
		return acpModesBlock{}, false
	}
	return f.modes[len(f.modes)-1], true
}

// newTestSession builds an acpSession with a pipe-backed transport
// (no real subprocess). The second return value is a writer the test
// uses to inject server-side RPC responses.
func newTestSession(t *testing.T, cb sessionCallbacks) (*acpSession, *io.PipeWriter, *io.PipeReader) {
	t.Helper()
	rResp, wResp := io.Pipe() // server → client
	rReq, wReq := io.Pipe()   // client → server

	s := &acpSession{
		workDir:       t.TempDir(),
		events:        make(chan core.Event, 32),
		permByID:      make(map[string]permState),
		toolInputByID: make(map[string]string),
		callbacks:     cb,
	}
	s.ctx, s.cancel = context.WithCancel(context.Background())
	s.alive.Store(true)
	s.acpSessID = "test-session-id"
	s.tr = newTransport(rResp, wReq, s.onNotification, s.onServerRequest)
	go s.tr.readLoop(s.ctx)

	t.Cleanup(func() {
		s.cancel()
		wResp.Close()
		rReq.Close()
	})
	return s, wResp, rReq
}

func TestSession_SetLiveMode_success(t *testing.T) {
	cb := &fakeCallbacks{}
	s, wResp, rReq := newTestSession(t, cb)

	// Pre-populate availableModes so SetLiveMode validates OK.
	s.absorbModes(&acpModesBlock{
		CurrentModeID: "normal",
		AvailableModes: []acpModeInfo{
			{ID: "normal", Name: "Code"},
			{ID: "plan", Name: "Plan"},
		},
	})

	// Mock server: read one request, verify it, respond success.
	done := make(chan struct{})
	go func() {
		defer close(done)
		sc := bufio.NewScanner(rReq)
		for sc.Scan() {
			var req struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
				Params struct {
					SessionID string `json:"sessionId"`
					ModeID    string `json:"modeId"`
				} `json:"params"`
			}
			if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
				continue
			}
			if req.Method != "session/set_mode" {
				continue
			}
			if req.Params.SessionID != "test-session-id" || req.Params.ModeID != "plan" {
				_, _ = fmt.Fprintf(wResp, `{"jsonrpc":"2.0","id":%s,"error":{"code":-32602,"message":"bad params"}}`+"\n", req.ID)
				return
			}
			_, _ = fmt.Fprintf(wResp, `{"jsonrpc":"2.0","id":%s,"result":{}}`+"\n", req.ID)
			return
		}
	}()

	if !s.SetLiveMode("plan") {
		t.Fatal("SetLiveMode returned false for valid mode")
	}
	<-done

	if got := s.CurrentMode(); got != "plan" {
		t.Fatalf("CurrentMode = %q, want plan", got)
	}

	// Callback should have been re-fired with currentModeId=plan.
	time.Sleep(10 * time.Millisecond) // small grace for goroutine
	last, ok := cb.lastModes()
	if !ok {
		t.Fatalf("expected callback invocation after successful set_mode")
	}
	if last.CurrentModeID != "plan" {
		t.Fatalf("callback currentModeId = %q, want plan", last.CurrentModeID)
	}
}

func TestSession_SetLiveMode_rejectsUnknownMode(t *testing.T) {
	cb := &fakeCallbacks{}
	s, wResp, rReq := newTestSession(t, cb)
	_ = wResp
	_ = rReq

	s.absorbModes(&acpModesBlock{
		CurrentModeID: "normal",
		AvailableModes: []acpModeInfo{
			{ID: "normal", Name: "Code"},
		},
	})

	if s.SetLiveMode("plan") {
		t.Fatal("SetLiveMode should refuse unknown mode without making RPC")
	}
	// currentMode unchanged.
	if got := s.CurrentMode(); got != "normal" {
		t.Fatalf("CurrentMode drifted: %q", got)
	}
}

func TestSession_SetLiveMode_caseInsensitive(t *testing.T) {
	cb := &fakeCallbacks{}
	s, wResp, rReq := newTestSession(t, cb)

	s.absorbModes(&acpModesBlock{
		AvailableModes: []acpModeInfo{
			{ID: "accept-edits", Name: "Accept Edits"},
		},
	})

	// Mock server: unconditionally OK.
	go func() {
		sc := bufio.NewScanner(rReq)
		for sc.Scan() {
			var env struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
				Params struct {
					ModeID string `json:"modeId"`
				} `json:"params"`
			}
			if err := json.Unmarshal(sc.Bytes(), &env); err != nil {
				continue
			}
			if env.Method != "session/set_mode" {
				continue
			}
			if env.Params.ModeID != "accept-edits" {
				// Test asserts canonicalisation happened before RPC.
				_, _ = fmt.Fprintf(wResp, `{"jsonrpc":"2.0","id":%s,"error":{"code":-32602,"message":"wrong id %q"}}`+"\n", env.ID, env.Params.ModeID)
				return
			}
			_, _ = fmt.Fprintf(wResp, `{"jsonrpc":"2.0","id":%s,"result":{}}`+"\n", env.ID)
			return
		}
	}()

	// User types "ACCEPT EDITS" with wrong case
	if !s.SetLiveMode("ACCEPT EDITS") {
		t.Fatal("SetLiveMode should accept case-variant of display name and canonicalise to id")
	}
}

func TestSession_absorbModes_reportsViaCallback(t *testing.T) {
	cb := &fakeCallbacks{}
	s, _, _ := newTestSession(t, cb)

	s.absorbModes(&acpModesBlock{
		CurrentModeID: "plan",
		AvailableModes: []acpModeInfo{
			{ID: "normal"},
			{ID: "plan"},
		},
	})

	got, ok := cb.lastModes()
	if !ok {
		t.Fatal("expected callback")
	}
	if got.CurrentModeID != "plan" || len(got.AvailableModes) != 2 {
		t.Fatalf("unexpected callback block: %+v", got)
	}
}

func TestSession_maybeAbsorbCurrentModeUpdate(t *testing.T) {
	cb := &fakeCallbacks{}
	s, _, _ := newTestSession(t, cb)
	s.absorbModes(&acpModesBlock{
		AvailableModes: []acpModeInfo{{ID: "normal"}, {ID: "plan"}},
	})

	// Simulate a server-sent current_mode_update notification
	params := json.RawMessage(`{
		"sessionId": "test-session-id",
		"update": {
			"sessionUpdate": "current_mode_update",
			"currentModeId": "plan"
		}
	}`)
	s.maybeAbsorbCurrentModeUpdate(params)

	if got := s.CurrentMode(); got != "plan" {
		t.Fatalf("currentMode = %q, want plan", got)
	}
	last, ok := cb.lastModes()
	if !ok || last.CurrentModeID != "plan" {
		t.Fatalf("callback should have been fired with currentModeId=plan, got %+v ok=%v", last, ok)
	}
}
