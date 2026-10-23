package dsh

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
	"github.com/gorilla/websocket"
)

type remoteFixture struct {
	mu                    sync.Mutex
	server                *httptest.Server
	sessions              map[string]string
	turns                 map[string]int
	peers                 map[string]*remoteFixturePeer
	replies               []map[string]any
	replayApprovalSession string
	blockCommand          bool
	commandStarted        chan struct{}
}
type remoteFixturePeer struct {
	mu      sync.Mutex
	conn    *websocket.Conn
	streams map[string]string
}

func (p *remoteFixturePeer) send(id string, value any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.conn != nil {
		_ = p.conn.WriteJSON(map[string]any{"type": "item", "streamId": id, "value": value})
	}
}
func newRemoteFixture(t *testing.T) *remoteFixture {
	t.Helper()
	f := &remoteFixture{sessions: map[string]string{}, turns: map[string]int{}, peers: map[string]*remoteFixturePeer{}}
	f.server = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(func() {
		f.mu.Lock()
		for _, p := range f.peers {
			p.mu.Lock()
			if p.conn != nil {
				_ = p.conn.Close()
			}
			p.mu.Unlock()
		}
		f.mu.Unlock()
		f.server.Close()
	})
	return f
}
func (f *remoteFixture) handle(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/connect" {
		if r.URL.Query().Get("token") != "fixture-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		f.mu.Lock()
		id := fmt.Sprint(len(f.peers) + 1)
		f.peers[id] = &remoteFixturePeer{streams: map[string]string{}}
		f.mu.Unlock()
		http.SetCookie(w, &http.Cookie{Name: "dsh-api", Value: id, Path: "/"})
		w.Header().Set("Location", "/api/remote.mux")
		w.WriteHeader(http.StatusFound)
		return
	}
	cookie, err := r.Cookie("dsh-api")
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	f.mu.Lock()
	peer := f.peers[cookie.Value]
	f.mu.Unlock()
	if peer == nil {
		http.Error(w, "expired peer", http.StatusUnauthorized)
		return
	}
	if r.URL.Path == "/api/remote.mux" {
		conn, err := (&websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		peer.mu.Lock()
		peer.conn = conn
		peer.mu.Unlock()
		defer func() { _ = conn.Close() }()
		for {
			var req struct {
				Type, StreamID, Endpoint string
				Payload                  struct {
					Args struct {
						Request struct {
							Address struct {
								SessionID string `json:"sessionId"`
							} `json:"address"`
						} `json:"request"`
					} `json:"args"`
				} `json:"payload"`
			}
			if conn.ReadJSON(&req) != nil {
				return
			}
			if req.Type == "cancel" {
				peer.mu.Lock()
				delete(peer.streams, req.StreamID)
				peer.mu.Unlock()
				continue
			}
			if req.Type != "open" {
				continue
			}
			peer.mu.Lock()
			peer.streams[req.StreamID] = req.Payload.Args.Request.Address.SessionID
			peer.mu.Unlock()
			switch req.Endpoint {
			case "$events":
				peer.send(req.StreamID, map[string]any{"type": "ready", "clientId": cookie.Value})
				if f.replayApprovalSession != "" {
					peer.send(req.StreamID, map[string]any{"type": "waterfall", "event": "approval/request", "eventId": "replayed", "agentId": f.replayApprovalSession, "request": map[string]any{"toolName": "bash", "reason": "pending before disconnect"}})
				}
			case "workspace/follow":
				peer.send(req.StreamID, map[string]any{"type": "baseline", "value": map[string]any{"archivedSessionIds": []string{}}})
			case "session/follow":
				f.mu.Lock()
				turn := f.turns[req.Payload.Args.Request.Address.SessionID]
				f.mu.Unlock()
				records := []any{}
				if turn > 0 {
					records = append(records, map[string]any{"event": evTurnEnd(turn, "completed")})
				}
				peer.send(req.StreamID, map[string]any{"type": "snapshot", "cursor": 0, "records": records, "projections": map[string]any{"values": map[string]any{}}})
			}
		}
	}
	var req struct {
		RPCID   string `json:"rpcId"`
		Method  string `json:"method"`
		Payload struct {
			Args map[string]json.RawMessage `json:"args"`
		} `json:"payload"`
	}
	if json.NewDecoder(r.Body).Decode(&req) != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	method := strings.TrimPrefix(r.URL.Path, "/api/")
	if req.Method != method {
		http.Error(w, "method mismatch", http.StatusBadRequest)
		return
	}
	var request struct {
		SessionID, Cwd string
		Content        []struct{ Text string }
	}
	_ = json.Unmarshal(req.Payload.Args["request"], &request)
	var value any
	switch method {
	case "session/list":
		f.mu.Lock()
		items := []any{}
		for id, cwd := range f.sessions {
			items = append(items, map[string]any{"sessionId": id, "cwd": cwd, "updatedAt": 1})
		}
		f.mu.Unlock()
		value = map[string]any{"items": items}
	case "session/create":
		f.mu.Lock()
		id := fmt.Sprintf("remote-%d", len(f.sessions)+1)
		f.sessions[id] = request.Cwd
		f.mu.Unlock()
		value = map[string]any{"sessionId": id}
	case "session/modelCatalog":
		value = map[string]any{"default": map[string]any{"provider": "fixture", "model": "one"}, "groups": []any{map[string]any{"id": "fixture", "name": "Fixture", "models": []any{map[string]any{"id": "one"}, map[string]any{"id": "two"}}}}}
	case "session/projections":
		value = map[string]any{"kind": "sequenced", "asOfSeq": 0, "values": map[string]any{}}
	case "session/selectModel", "session/cancel":
		value = map[string]any{"accepted": true}
	case "session/prompt":
		f.mu.Lock()
		f.turns[request.SessionID]++
		turn := f.turns[request.SessionID]
		f.mu.Unlock()
		text := fmt.Sprintf("reply-%d", turn)
		peer.mu.Lock()
		streams := map[string]string{}
		for id, sid := range peer.streams {
			streams[id] = sid
		}
		peer.mu.Unlock()
		for id, sid := range streams {
			if sid != request.SessionID {
				continue
			}
			sendEvent := func(kind string, data any) {
				peer.send(id, map[string]any{"type": "event", "event": map[string]any{"type": kind, "seq": turn * 10, "data": data}})
			}
			sendEvent("turn/start", map[string]any{"turn": turn})
			peer.send(id, map[string]any{"type": "assistant-stream", "frame": map[string]any{"type": "chunk", "chunk": map[string]any{"type": "text-delta", "text": text}}})
			sendEvent("assistant/message", map[string]any{"turn": turn, "step": 1, "message": map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}}, "usage": map[string]any{"inputTokens": 12, "outputTokens": 3}})
			sendEvent("turn/end", map[string]any{"turn": turn, "reason": map[string]any{"kind": "completed"}})
		}
		value = map[string]any{"accepted": true}
	case "commands/list":
		value = []any{map[string]any{"name": "compact"}}
	case "commands/execute":
		if f.blockCommand {
			close(f.commandStarted)
			<-r.Context().Done()
			return
		}
		value = map[string]any{"result": map[string]any{"kind": "success", "text": "command done"}}
	case "$events/result":
		f.mu.Lock()
		v := map[string]any{}
		for k, b := range req.Payload.Args {
			_ = json.Unmarshal(b, new(any))
			var x any
			_ = json.Unmarshal(b, &x)
			v[k] = x
		}
		f.replies = append(f.replies, v)
		f.mu.Unlock()
	default:
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	writeServerValue(w, req.RPCID, value)
}
func TestRemote_AuthenticatedMultiTurnResume(t *testing.T) {
	f := newRemoteFixture(t)
	dir := t.TempDir()
	a, err := New(map[string]any{"base_url": f.server.URL, "token": "fixture-token", "work_dir": dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Stop() })
	s, err := a.StartSession(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	id := s.CurrentSessionID()
	for turn := 1; turn <= 2; turn++ {
		if err := s.Send("hello", fmt.Sprint(turn), nil, nil); err != nil {
			t.Fatal(err)
		}
		assertRemoteReply(t, s, fmt.Sprintf("reply-%d", turn))
	}
	_ = s.Close()
	s, err = a.StartSession(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if s.CurrentSessionID() != id {
		t.Fatal("resume forked the conversation")
	}
	if err := s.Send("resume", "3", nil, nil); err != nil {
		t.Fatal(err)
	}
	assertRemoteReply(t, s, "reply-3")
	sessions, err := a.ListSessions(context.Background())
	if err != nil || len(sessions) != 1 {
		t.Fatalf("sessions=%v err=%v", sessions, err)
	}
}
func assertRemoteReply(t *testing.T, s core.AgentSession, want string) {
	t.Helper()
	var text strings.Builder
	timeout := time.After(3 * time.Second)
	for {
		select {
		case e, ok := <-s.Events():
			if !ok {
				t.Fatal("stream closed")
			}
			switch e.Type {
			case core.EventText:
				text.WriteString(e.Content)
			case core.EventError:
				t.Fatalf("event error: %v", e.Error)
			case core.EventResult:
				if text.String() != want || e.InputTokens != 12 {
					t.Fatalf("reply=%q result=%+v", text.String(), e)
				}
				return
			}
		case <-timeout:
			t.Fatal("turn did not complete")
		}
	}
}

func TestRemote_RejectsForeignWorkspaceResume(t *testing.T) {
	f := newRemoteFixture(t)
	a, err := New(map[string]any{"base_url": f.server.URL, "token": "fixture-token", "work_dir": t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Stop() })
	s, err := a.StartSession(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	a.(core.WorkDirSwitcher).SetWorkDir(t.TempDir())
	foreign, err := a.StartSession(context.Background(), s.CurrentSessionID())
	if foreign != nil {
		_ = foreign.Close()
	}
	if err == nil {
		t.Fatal("resumed another workspace's conversation")
	}
}

func TestConnectionOptions_RemovesLaunchSecretFromURL(t *testing.T) {
	base, token, err := connectionOptions(map[string]any{"base_url": "http://localhost:3080/?token=private-token"})
	if err != nil || base != "http://localhost:3080" || token != "private-token" {
		t.Fatalf("connection options: base=%q err=%v", base, err)
	}
	for _, raw := range []string{"ftp://localhost:3080", "http://user:password@localhost:3080"} {
		if _, _, err := connectionOptions(map[string]any{"base_url": raw}); err == nil {
			t.Fatal("accepted unsupported or credential-bearing URL")
		}
	}
}

func TestFoldHistory_ReplacementsAndEmptyEdits(t *testing.T) {
	var records []remoteHistoryRecord
	raw := `[
 {"event":{"type":"user/message","seq":1,"time":1000,"surfaceOp":"append","data":{"content":[{"type":"text","text":"old prompt"}]}}},
 {"event":{"type":"assistant/message","seq":2,"time":2000,"surfaceOp":"append","data":{"message":{"content":[{"type":"text","text":"old answer"}]}}}},
 {"event":{"type":"user/message","seq":3,"time":3000,"surfaceOp":{"op":"replace","startSeq":1,"endSeq":1},"data":{"content":[{"type":"text","text":"edited prompt"}]}}},
 {"event":{"type":"assistant/message","seq":4,"time":4000,"surfaceOp":{"op":"replace","startSeq":2,"endSeq":2},"data":{"message":{"content":[]}}}},
 {"event":{"type":"assistant/message","seq":5,"time":5000,"surfaceOp":"append","data":{"message":{"content":[{"type":"reasoning","text":"hidden"},{"type":"text","text":"new answer"}]}}}}
 ]`
	if err := json.Unmarshal([]byte(raw), &records); err != nil {
		t.Fatal(err)
	}
	history := foldHistory(records, 10)
	if len(history) != 2 || history[0].Content != "edited prompt" || history[0].Timestamp.UnixMilli() != 3000 || history[1].Content != "new answer" {
		t.Fatalf("stale surface in history: %+v", history)
	}
	tail := foldHistory(records, 1)
	if len(tail) != 1 || tail[0].Content != "new answer" {
		t.Fatalf("history limit: %+v", tail)
	}
}

func TestRemote_ApprovalIsRoutedAndAcknowledgedByItsOwnPeer(t *testing.T) {
	f := newRemoteFixture(t)
	a, err := New(map[string]any{"base_url": f.server.URL, "token": "fixture-token", "work_dir": t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Stop() })
	one, err := a.StartSession(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = one.Close() })
	two, err := a.StartSession(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = two.Close() })
	f.mu.Lock()
	peers := []*remoteFixturePeer{}
	for _, peer := range f.peers {
		peers = append(peers, peer)
	}
	f.mu.Unlock()
	for _, peer := range peers {
		peer.mu.Lock()
		ids := []string{}
		for id, sid := range peer.streams {
			if sid == "" {
				ids = append(ids, id)
			}
		}
		peer.mu.Unlock()
		for _, id := range ids {
			peer.send(id, map[string]any{"type": "waterfall", "event": "approval/request", "eventId": "approval", "agentId": one.CurrentSessionID(), "request": map[string]any{"toolName": "write", "reason": "fixture approval"}})
		}
	}
	select {
	case event := <-one.Events():
		if event.Type != core.EventPermissionRequest || event.RequestID != "approval" {
			t.Fatalf("approval: %+v", event)
		}
		if err := one.RespondPermission(event.RequestID, core.PermissionResult{Behavior: "allow"}); err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("approval was not delivered")
	}
	waitFor(t, time.Second, func() bool { f.mu.Lock(); defer f.mu.Unlock(); return len(f.replies) == 1 }, "one session-owned grant")
	f.mu.Lock()
	defer f.mu.Unlock()
	grants, delegated := 0, 0
	for _, reply := range f.replies {
		outcome := reply["outcome"].(map[string]any)
		switch outcome["kind"] {
		case "next":
			delegated++
		case "result":
			if outcome["value"] != "allowed-once" {
				t.Fatalf("wrong decision: %+v", reply)
			}
			grants++
		}
	}
	if grants != 1 || delegated != 0 {
		t.Fatalf("grants=%d delegated=%d", grants, delegated)
	}
	select {
	case event := <-two.Events():
		t.Fatalf("approval leaked to another topic: %+v", event)
	default:
	}
}

func TestRemote_StopCancelsNativeCommandAndKeepsSession(t *testing.T) {
	f := newRemoteFixture(t)
	f.blockCommand = true
	f.commandStarted = make(chan struct{})
	a, err := New(map[string]any{"base_url": f.server.URL, "token": "fixture-token", "work_dir": t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Stop() })
	s, err := a.StartSession(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Send("/compact", "command", nil, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case <-f.commandStarted:
	case <-time.After(time.Second):
		t.Fatal("native command was not invoked")
	}
	if err := s.(core.AgentSessionCanceller).CancelTurn(); err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-s.Events():
		if event.Type != core.EventResult || !event.Done || event.Error != nil {
			t.Fatalf("cancelled command: %+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("command cancellation stalled")
	}
	if err := s.Send("after stop", "next", nil, nil); err != nil {
		t.Fatal(err)
	}
	assertRemoteReply(t, s, "reply-1")
}

type failingAuthTransport struct{}

func (failingAuthTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return nil, fmt.Errorf("fixture failure for %s", req.URL)
}
func TestRemote_AuthenticationErrorRedactsEncodedToken(t *testing.T) {
	c := newRPCClient("http://localhost:3080")
	c.token = "private+token/?="
	c.httpClient.Transport = failingAuthTransport{}
	_, err := c.connection(context.Background())
	if err == nil || strings.Contains(err.Error(), c.token) || strings.Contains(err.Error(), url.QueryEscape(c.token)) || !strings.Contains(err.Error(), "[redacted]") {
		t.Fatalf("authentication error exposed a token: %v", err)
	}
}
func TestRemote_RestoreAndReconcilePartialAttempt(t *testing.T) {
	var snapshot remoteFollowFrame
	raw := `{"type":"snapshot","records":[{"event":{"type":"assistant/message","data":{"message":{"content":[{"type":"text","text":"historical answer"}]}}}}],"assistantStream":{"activeAttempt":{"turn":2,"stream":[{"type":"text-chunks","texts":["partial"]},{"type":"reasoning-chunks","texts":["thinking"]},{"type":"tool-call-chunks","index":2,"id":"call","name":"write","args":["{}"]}]}}}`
	if err := json.Unmarshal([]byte(raw), &snapshot); err != nil {
		t.Fatal(err)
	}
	frames := make(chan muxFrame, 16)
	p := remoteSessionPump{ctx: context.Background(), sessionID: "session", frames: frames}
	if !p.restore(snapshot) {
		t.Fatal("snapshot restore failed")
	}
	final := json.RawMessage(`{"type":"event","event":{"type":"assistant/message","data":{"message":{"content":[{"type":"text","text":"partial rest"}]}}}}`)
	if !p.follow(final) {
		t.Fatal("durable reconciliation failed")
	}
	close(frames)
	var text, thinking strings.Builder
	tools := 0
	for frame := range frames {
		var event sessionEvent
		_ = json.Unmarshal(frame.Event, &event)
		var data sessionEventData
		_ = json.Unmarshal(event.Data, &data)
		if data.Chunk == nil {
			continue
		}
		switch data.Chunk.Type {
		case "text-delta":
			text.WriteString(data.Chunk.Text)
		case "reasoning-delta":
			thinking.WriteString(data.Chunk.Text)
		case "tool-call-delta":
			if data.Chunk.Name == "write" {
				tools++
			}
		}
	}
	if text.String() != "partial rest" || thinking.String() != "thinking" || tools != 1 {
		t.Fatalf("snapshot output: text=%q thinking=%q tools=%d", text.String(), thinking.String(), tools)
	}
}

func TestFoldHistory_ReplacementUsesSurfaceOrderAndHiddenEndpoints(t *testing.T) {
	cases := []string{
		`[{"event":{"seq":1,"type":"user/message","data":{"content":[{"type":"text","text":"old"}]}}},{"event":{"seq":2,"type":"assistant/message","data":{"message":{"content":[{"type":"text","text":"answer"}]}}}},{"event":{"seq":3,"type":"user/message","surfaceOp":{"op":"replace","startSeq":1,"endSeq":1},"data":{"content":[{"type":"text","text":"edited"}]}}},{"event":{"seq":4,"type":"user/message","surfaceOp":{"op":"replace","startSeq":3,"endSeq":2},"data":{"content":[{"type":"text","text":"summary"}]}}}]`,
		`[{"event":{"seq":1,"type":"system/message","data":{"message":{"content":[{"type":"text","text":"hidden"}]}}}},{"event":{"seq":2,"type":"user/message","data":{"content":[{"type":"text","text":"old"}]}}},{"event":{"seq":3,"type":"assistant/message","data":{"message":{"content":[{"type":"text","text":"answer"}]}}}},{"event":{"seq":4,"type":"user/message","surfaceOp":{"op":"replace","startSeq":1,"endSeq":3},"data":{"content":[{"type":"text","text":"summary"}]}}}]`,
	}
	for i, raw := range cases {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			var records []remoteHistoryRecord
			if err := json.Unmarshal([]byte(raw), &records); err != nil {
				t.Fatal(err)
			}
			history := foldHistory(records, 10)
			if len(history) != 1 || history[0].Content != "summary" || history[0].Role != "user" {
				t.Fatalf("replacement retained shadowed messages: %+v", history)
			}
		})
	}
}
