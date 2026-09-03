package dsh

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// mockDshServer emulates the subset of the dsh web server's RPC API that the
// cc-connect adapter uses: unary POST /api/<method>, POST /api/respond, and
// the GET /api/events.mux SSE stream.
type mockDshServer struct {
	mu sync.Mutex

	sessions map[string]*mockSession // sessionId → session
	nextID   int

	// queue of frames sent to the mux stream
	frames []mockFrame
	// subscribers receive every frame broadcast
	subs map[chan mockFrame]struct{}

	// hooks for tests to observe RPC traffic
	created  []string // sessionIds created in order
	prompts  []string // prompt texts received in order (first text part)
	cancels  int
	responds []mockRespondCall

	// command.execute lines and session.selectModel payloads (for mode/effort tests)
	commands           []string
	failCommandExecute bool
	settingsUpdates    []struct {
		NS    string         `json:"ns"`
		Patch map[string]any `json:"patch"`
	}
	selectModelCalls []struct {
		Provider        string `json:"provider"`
		Model           string `json:"model"`
		ReasoningEffort string `json:"reasoningEffort"`
	}

	// failNext prompts the server to return an RPC error for the next N calls
	failNext map[string]int

	createdCh chan struct{} // signaled on every session.create (for sync)
}

type mockSession struct {
	id      string
	cwd     string
	prompts int
}

type mockFrame struct {
	rpcID string
	body  map[string]any
}

type mockRespondCall struct {
	rpcID string
	value map[string]any
}

func newMockDshServer() *mockDshServer {
	m := &mockDshServer{
		sessions:  make(map[string]*mockSession),
		frames:    nil,
		subs:      make(map[chan mockFrame]struct{}),
		failNext:  make(map[string]int),
		createdCh: make(chan struct{}, 64),
	}
	return m
}

// close closes the server and all subscriber channels.
func (m *mockDshServer) close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for ch := range m.subs {
		close(ch)
	}
	m.subs = make(map[chan mockFrame]struct{})
}

// broadcast pushes a frame to every mux subscriber.
func (m *mockDshServer) broadcast(rpcID string, body map[string]any) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for ch := range m.subs {
		select {
		case ch <- mockFrame{rpcID: rpcID, body: body}:
		default:
		}
	}
}

// addSession creates a mock session and broadcasts its subscribed frame.
func (m *mockDshServer) addSession(cwd string) string {
	m.mu.Lock()
	m.nextID++
	id := fmt.Sprintf("session-mock-%d", m.nextID)
	m.sessions[id] = &mockSession{id: id, cwd: cwd}
	m.created = append(m.created, id)
	select {
	case m.createdCh <- struct{}{}:
	default:
	}
	m.mu.Unlock()
	return id
}

// waitCreated blocks until at least one session.create happened.
func (m *mockDshServer) waitCreated(timeout time.Duration) bool {
	select {
	case <-m.createdCh:
		return true
	case <-time.After(timeout):
		return false
	}
}

// Locked accessors for test assertions.
func (m *mockDshServer) createdCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.created)
}

func (m *mockDshServer) cancelCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cancels
}

func (m *mockDshServer) promptCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.prompts)
}

func (m *mockDshServer) promptAt(i int) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.prompts[i]
}

func (m *mockDshServer) respondCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.responds)
}

func (m *mockDshServer) respondAt(i int) mockRespondCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.responds[i]
}

func (m *mockDshServer) commandAt(i int) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.commands[i]
}

func (m *mockDshServer) commandCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.commands)
}

func (m *mockDshServer) settingsUpdateCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.settingsUpdates)
}

func (m *mockDshServer) settingsUpdateAt(i int) (ns string, patch map[string]any) {
	m.mu.Lock()
	defer m.mu.Unlock()
	u := m.settingsUpdates[i]
	return u.NS, u.Patch
}

func (m *mockDshServer) selectModelCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.selectModelCalls)
}

func (m *mockDshServer) selectModelAt(i int) (provider, model, effort string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c := m.selectModelCalls[i]
	return c.Provider, c.Model, c.ReasoningEffort
}

// handler is the HTTP handler for the mock server.
func (m *mockDshServer) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/events.mux", m.handleMux)
	mux.HandleFunc("/api/respond", m.handleRespond)
	mux.HandleFunc("/api/", m.handleRPC)
	return mux
}

func (m *mockDshServer) handleRPC(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		http.Error(w, "content type must be application/json", http.StatusUnsupportedMediaType)
		return
	}
	var req struct {
		Type    string          `json:"type"`
		RPCID   string          `json:"rpcId"`
		Method  string          `json:"method"`
		Payload json.RawMessage `json:"payload"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "body is not JSON", http.StatusBadRequest)
		return
	}
	if req.Type != "client-request" {
		http.Error(w, "not a client-request", http.StatusBadRequest)
		return
	}

	m.mu.Lock()
	if m.failNext[req.Method] > 0 {
		m.failNext[req.Method]--
		m.mu.Unlock()
		writeServerError(w, req.RPCID, "session-not-found", "mock: forced failure")
		return
	}
	m.mu.Unlock()

	var value any
	var errMsg string
	switch req.Method {
	case "session.create":
		var p struct {
			Cwd         string `json:"cwd"`
			SessionID   string `json:"sessionId"`
			AgentPreset string `json:"agentPreset"`
		}
		_ = json.Unmarshal(req.Payload, &p)
		if p.SessionID != "" {
			m.mu.Lock()
			existing, ok := m.sessions[p.SessionID]
			if ok && existing.cwd != p.Cwd {
				m.mu.Unlock()
				writeServerError(w, req.RPCID, "session-conflict", "session id already used with a different cwd")
				return
			}
			m.mu.Unlock()
			value = map[string]any{"sessionId": p.SessionID}
		} else {
			value = map[string]any{"sessionId": m.addSession(p.Cwd)}
		}
	case "session.prompt":
		var p struct {
			SessionID string `json:"sessionId"`
			Content   []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		}
		_ = json.Unmarshal(req.Payload, &p)
		m.mu.Lock()
		s, ok := m.sessions[p.SessionID]
		if ok {
			s.prompts++
		}
		m.mu.Unlock()
		if !ok {
			errMsg = "session-not-found"
			break
		}
		var texts []string
		for _, c := range p.Content {
			if c.Type == "text" {
				texts = append(texts, c.Text)
			}
		}
		m.mu.Lock()
		m.prompts = append(m.prompts, strings.Join(texts, "\n"))
		m.mu.Unlock()
		value = map[string]any{"accepted": true}
	case "session.list":
		m.mu.Lock()
		var items []map[string]any
		for _, s := range m.sessions {
			items = append(items, map[string]any{
				"sessionId": s.id,
				"updatedAt": time.Now().UnixMilli(),
				"running":   false,
				"blank":     s.prompts == 0,
				"cwd":       s.cwd,
			})
		}
		m.mu.Unlock()
		value = map[string]any{"items": items}
	case "session.cancel":
		m.mu.Lock()
		m.cancels++
		m.mu.Unlock()
		value = map[string]any{"accepted": true}
	case "session.models":
		value = map[string]any{
			"current":  map[string]any{"provider": "mock", "model": "mock-1"},
			"routable": true,
			"groups": []map[string]any{{
				"id":   "mock",
				"name": "Mock Provider",
				"models": []map[string]any{
					{
						"id": "mock-1", "name": "Mock One",
						"reasoning": map[string]any{
							"efforts": []map[string]any{
								{"id": "low", "name": "Low"},
								{"id": "high", "name": "High"},
							},
							"defaultEffort": "low",
						},
					},
					{"id": "mock-2", "name": "Mock Two"},
				},
			}},
		}
	case "session.selectModel":
		m.mu.Lock()
		var p struct {
			Provider        string `json:"provider"`
			Model           string `json:"model"`
			ReasoningEffort string `json:"reasoningEffort"`
		}
		_ = json.Unmarshal(req.Payload, &p)
		m.selectModelCalls = append(m.selectModelCalls, p)
		m.mu.Unlock()
		value = map[string]any{"selected": map[string]any{"provider": "mock", "model": "mock-1"}}
	case "command.execute":
		m.mu.Lock()
		if m.failCommandExecute {
			m.mu.Unlock()
			writeServerError(w, req.RPCID, "internal", "command registry is absent")
			return
		}
		var p struct {
			SessionID string `json:"sessionId"`
			Line      string `json:"line"`
		}
		_ = json.Unmarshal(req.Payload, &p)
		m.commands = append(m.commands, p.Line)
		m.mu.Unlock()
		value = map[string]any{"matched": true, "commandId": "cmd-1"}
	case "settings.update":
		m.mu.Lock()
		var p struct {
			NS    string         `json:"ns"`
			Patch map[string]any `json:"patch"`
		}
		_ = json.Unmarshal(req.Payload, &p)
		m.settingsUpdates = append(m.settingsUpdates, p)
		m.mu.Unlock()
		value = map[string]any{"ns": "permission", "writable": true}
	case "session.history":
		value = map[string]any{"events": []any{}, "hasMore": false}
	default:
		errMsg = "unknown method " + req.Method
	}

	if errMsg != "" {
		writeServerError(w, req.RPCID, "internal", errMsg)
		return
	}
	writeServerValue(w, req.RPCID, value)
}

func (m *mockDshServer) handleRespond(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Type   string `json:"type"`
		RPCID  string `json:"rpcId"`
		Result struct {
			OK    bool            `json:"ok"`
			Value json.RawMessage `json:"value"`
		} `json:"result"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	var value map[string]any
	_ = json.Unmarshal(req.Result.Value, &value)
	m.mu.Lock()
	m.responds = append(m.responds, mockRespondCall{rpcID: req.RPCID, value: value})
	m.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprint(w, `{"accepted":true}`)
}

// handleMux serves the aggregated event stream over WebSocket (matching the
// real dsh webserver, which requires an HTTP upgrade for /api/events.mux).
// It replays m.frames and then broadcasts live frames until the client
// disconnects.
func (m *mockDshServer) handleMux(w http.ResponseWriter, r *http.Request) {
	upgrader := websocket.Upgrader{
		CheckOrigin: func(*http.Request) bool { return true },
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	ch := make(chan mockFrame, 4096)
	m.mu.Lock()
	m.subs[ch] = struct{}{}
	replay := append([]mockFrame(nil), m.frames...)
	m.mu.Unlock()

	defer func() {
		m.mu.Lock()
		delete(m.subs, ch)
		m.mu.Unlock()
	}()

	send := func(f mockFrame) bool {
		payload, err := json.Marshal(f.body)
		if err != nil {
			return false
		}
		env, _ := json.Marshal(map[string]any{
			"type":    "server-request",
			"rpcId":   f.rpcID,
			"method":  "events.mux",
			"payload": json.RawMessage(payload),
		})
		return conn.WriteMessage(websocket.TextMessage, env) == nil
	}

	for _, f := range replay {
		if !send(f) {
			return
		}
	}

	// Detect client disconnect so the handler can exit.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()

	for {
		select {
		case <-done:
			return
		case f, ok := <-ch:
			if !ok {
				return
			}
			if !send(f) {
				return
			}
		}
	}
}

func writeServerValue(w http.ResponseWriter, rpcID string, value any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"type":  "server-response",
		"rpcId": rpcID,
		"result": map[string]any{
			"ok":    true,
			"value": value,
		},
	})
}

func writeServerError(w http.ResponseWriter, rpcID, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"type":  "server-response",
		"rpcId": rpcID,
		"result": map[string]any{
			"ok": false,
			"error": map[string]any{
				"code":    code,
				"message": message,
				"details": map[string]any{},
			},
		},
	})
}

// sse helpers -----------------------------------------------------------

// frameEvent builds a session/event mux frame body.
func frameEvent(sessionID string, ev map[string]any) map[string]any {
	return map[string]any{
		"type":      "session/event",
		"sessionId": sessionID,
		"event":     ev,
	}
}

// ev builds one SessionEvent with the given type/data.
func ev(typ string, data map[string]any) map[string]any {
	return map[string]any{"type": typ, "seq": 1, "time": 1, "data": data}
}

// turnStart/end helpers.
func evTurnStart(turn int) map[string]any {
	return ev("turn/start", map[string]any{"turn": turn})
}

func evTurnEnd(turn int, kind string) map[string]any {
	reason := map[string]any{"kind": kind}
	if kind == "error" {
		reason["error"] = map[string]any{"message": "mock failure", "code": "UNKNOWN"}
	}
	return ev("turn/end", map[string]any{"turn": turn, "reason": reason})
}

func evTextDelta(turn, step int, text string) map[string]any {
	return ev("assistant/chunk", map[string]any{
		"turn":  turn,
		"step":  step,
		"chunk": map[string]any{"type": "text-delta", "index": 0, "text": text},
	})
}

func evReasoningDelta(turn, step int, text string) map[string]any {
	return ev("assistant/chunk", map[string]any{
		"turn":  turn,
		"step":  step,
		"chunk": map[string]any{"type": "reasoning-delta", "index": 0, "text": text},
	})
}

func evToolCall(turn, step int, callID, name, args string) map[string]any {
	return ev("tool/call", map[string]any{
		"turn": turn, "step": step, "callId": callID, "name": name, "arguments": args,
	})
}

func evToolResult(turn, step int, callID string, isError bool, text string) map[string]any {
	return ev("tool/result", map[string]any{
		"turn": turn, "step": step, "callId": callID,
		"message": map[string]any{
			"role": "user",
			"content": []map[string]any{{
				"type":       "tool-result",
				"toolCallId": callID,
				"isError":    isError,
				"content":    []map[string]any{{"type": "text", "text": text}},
			}},
		},
	})
}

// startMockServer spins up a mock dsh server and returns it plus its URL.
func startMockServer(m *mockDshServer) *httptest.Server {
	return httptest.NewServer(m.handler())
}
