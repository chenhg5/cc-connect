package dsh

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Remote streams share a peer cookie and one owning socket. Each chat owns a
// separate peer so a stalled chat cannot block another chat's approvals.
type remoteTransport struct {
	conn     *websocket.Conn
	cookie   string
	ctx      context.Context
	cancel   context.CancelFunc
	mu       sync.Mutex
	writeMu  sync.Mutex
	streams  map[string]*remoteStream
	clientID string
}
type remoteStream struct {
	ctx      context.Context
	id       string
	messages chan remoteMessage
}
type remoteMessage struct {
	Type     string          `json:"type"`
	StreamID string          `json:"streamId"`
	Value    json.RawMessage `json:"value"`
	Error    *rpcError       `json:"error,omitempty"`
}

func connectionOptions(opts map[string]any) (string, string, error) {
	raw := optsString(opts, "base_url")
	token := optsString(opts, "token")
	if raw != "" {
		if !strings.Contains(raw, "://") {
			raw = "http://" + raw
		}
		u, err := url.Parse(raw)
		if err != nil {
			return "", "", fmt.Errorf("dsh: invalid base_url")
		}
		if token == "" {
			token = u.Query().Get("token")
		}
		u.RawQuery = ""
		u.Fragment = ""
		raw = u.String()
	}
	if token == "" {
		token = os.Getenv("DSH_TOKEN")
	}
	base, err := parseBaseURL(raw)
	return base, token, err
}
func (c *rpcClient) sessionClient() *rpcClient {
	if c.token == "" {
		return c
	}
	clone := newRPCClient(c.baseURL)
	clone.token = c.token
	clone.sessionOwned = true
	return clone
}
func (c *rpcClient) closeRemote() {
	c.remoteMu.Lock()
	r := c.remote
	c.remote = nil
	c.remoteMu.Unlock()
	if r != nil {
		r.cancel()
		_ = r.conn.Close()
	}
	c.httpClient.CloseIdleConnections()
}
func (c *rpcClient) connection(ctx context.Context) (*remoteTransport, error) {
	c.remoteMu.Lock()
	defer c.remoteMu.Unlock()
	if c.remote != nil && c.remote.ctx.Err() == nil {
		return c.remote, nil
	}
	connect, err := url.Parse(c.baseURL + "/connect")
	if err != nil {
		return nil, err
	}
	q := connect.Query()
	q.Set("token", c.token)
	interactions := "none"
	if c.sessionOwned {
		interactions = "answer"
	}
	q.Set("interactions", interactions)
	connect.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, connect.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("dsh: create authentication request")
	}
	authClient := *c.httpClient
	authClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := authClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("dsh: authenticate Remote API: %s", strings.ReplaceAll(strings.ReplaceAll(err.Error(), url.QueryEscape(c.token), "[redacted]"), c.token, "[redacted]"))
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		return nil, fmt.Errorf("dsh: Remote authentication returned HTTP %d; check the running server's launch token", resp.StatusCode)
	}
	cookies := resp.Cookies()
	if len(cookies) == 0 {
		return nil, fmt.Errorf("dsh: Remote authentication returned no peer cookie")
	}
	cookieReq := &http.Request{Header: http.Header{}}
	for _, cookie := range cookies {
		cookieReq.AddCookie(cookie)
	}
	socketURL, _ := url.Parse(c.baseURL + "/api/remote.mux")
	if socketURL.Scheme == "https" {
		socketURL.Scheme = "wss"
	} else {
		socketURL.Scheme = "ws"
	}
	conn, wsResp, err := websocket.DefaultDialer.DialContext(ctx, socketURL.String(), cookieReq.Header)
	if err != nil {
		if wsResp != nil {
			_ = wsResp.Body.Close()
		}
		return nil, fmt.Errorf("dsh: open Remote socket: %w", err)
	}
	lifetime, cancel := context.WithCancel(context.Background())
	r := &remoteTransport{conn: conn, cookie: cookieReq.Header.Get("Cookie"), ctx: lifetime, cancel: cancel, streams: map[string]*remoteStream{}}
	c.remote = r
	go r.read()
	return r, nil
}
func (r *remoteTransport) read() {
	defer func() {
		r.cancel()
		_ = r.conn.Close()
		r.mu.Lock()
		for _, s := range r.streams {
			close(s.messages)
		}
		r.streams = map[string]*remoteStream{}
		r.mu.Unlock()
	}()
	for {
		var msg remoteMessage
		if err := r.conn.ReadJSON(&msg); err != nil {
			return
		}
		r.mu.Lock()
		s := r.streams[msg.StreamID]
		r.mu.Unlock()
		if s == nil {
			continue
		}
		select {
		case s.messages <- msg:
		case <-s.ctx.Done():
		case <-r.ctx.Done():
			return
		}
	}
}
func (r *remoteTransport) write(value any) error {
	r.writeMu.Lock()
	defer r.writeMu.Unlock()
	if err := r.conn.SetWriteDeadline(time.Now().Add(10 * time.Second)); err != nil {
		return err
	}
	return r.conn.WriteJSON(value)
}
func (r *remoteTransport) open(ctx context.Context, endpoint string, args any) (*remoteStream, error) {
	s := &remoteStream{ctx: ctx, id: mintRPCID(), messages: make(chan remoteMessage, 1024)}
	r.mu.Lock()
	r.streams[s.id] = s
	r.mu.Unlock()
	if err := r.write(map[string]any{"type": "open", "streamId": s.id, "endpoint": endpoint, "payload": map[string]any{"args": args}}); err != nil {
		r.forget(s)
		return nil, err
	}
	return s, nil
}
func (r *remoteTransport) forget(s *remoteStream) {
	r.mu.Lock()
	delete(r.streams, s.id)
	r.mu.Unlock()
	_ = r.write(map[string]any{"type": "cancel", "streamId": s.id})
}
func (r *remoteTransport) next(ctx context.Context, s *remoteStream) (json.RawMessage, error) {
	select {
	case m, ok := <-s.messages:
		if !ok {
			return nil, fmt.Errorf("dsh: Remote stream disconnected")
		}
		if m.Type == "error" {
			if m.Error != nil {
				return nil, &rpcCallError{Code: m.Error.Code, Message: m.Error.Message}
			}
			return nil, fmt.Errorf("dsh: Remote stream failed")
		}
		if m.Type != "item" {
			return nil, fmt.Errorf("dsh: Remote stream ended unexpectedly")
		}
		return m.Value, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-r.ctx.Done():
		return nil, fmt.Errorf("dsh: Remote connection closed")
	}
}
func (c *rpcClient) remoteInvoke(ctx context.Context, endpoint string, args any, out any) error {
	r, err := c.connection(ctx)
	if err != nil {
		return err
	}
	return c.callHTTP(ctx, endpoint, map[string]any{"args": args}, out, r.cookie)
}
func decodeRemoteValue(value any, out any) error {
	if out == nil {
		return nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}
func (c *rpcClient) remoteCall(ctx context.Context, method string, payload any, out any) error {
	p, ok := payload.(map[string]any)
	if !ok {
		return fmt.Errorf("dsh: invalid Remote request for %s", method)
	}
	switch method {
	case "session.list":
		return c.remoteList(ctx, p, out)
	case "session.create":
		if id, _ := p["sessionId"].(string); id != "" {
			var list struct {
				Items []sessionSummary `json:"items"`
			}
			if err := c.remoteCall(ctx, "session.list", map[string]any{}, &list); err != nil {
				return err
			}
			cwd, _ := p["cwd"].(string)
			for _, s := range list.Items {
				if s.SessionID == id && filepath.Clean(s.Cwd) == filepath.Clean(cwd) {
					return decodeRemoteValue(map[string]any{"sessionId": id}, out)
				}
			}
			return fmt.Errorf("dsh: cannot resume session %q outside this workspace", id)
		}
	case "session.models":
		var catalog map[string]json.RawMessage
		if err := c.remoteInvoke(ctx, "session/modelCatalog", map[string]any{}, &catalog); err != nil {
			return err
		}
		var projections struct {
			Values struct {
				ModelSelection struct {
					Next json.RawMessage `json:"next"`
				} `json:"modelSelection"`
			} `json:"values"`
		}
		id, _ := p["sessionId"].(string)
		if id != "" {
			if err := c.remoteInvoke(ctx, "session/projections", map[string]any{"request": map[string]any{"sessionId": id}}, &projections); err != nil {
				return err
			}
		}
		catalog["current"] = catalog["default"]
		if len(projections.Values.ModelSelection.Next) > 0 && string(projections.Values.ModelSelection.Next) != "null" {
			catalog["current"] = projections.Values.ModelSelection.Next
		}
		return decodeRemoteValue(catalog, out)
	case "command.execute":
		var execution *struct {
			Result struct{ Kind, Text string } `json:"result"`
		}
		err := c.remoteInvoke(ctx, "commands/execute", map[string]any{"agentId": p["sessionId"], "line": p["line"], "submittedAttachments": []any{}}, &execution)
		if err != nil {
			return err
		}
		if execution != nil && execution.Result.Kind == "error" {
			return fmt.Errorf("dsh: command rejected: %s", execution.Result.Text)
		}
		return decodeRemoteValue(map[string]any{"matched": execution != nil}, out)
	}
	endpoint := strings.Replace(method, ".", "/", 1)
	return c.remoteInvoke(ctx, endpoint, map[string]any{"request": p}, out)
}
func (c *rpcClient) remoteList(ctx context.Context, p map[string]any, out any) error {
	var list struct {
		Items []sessionSummary `json:"items"`
	}
	if err := c.remoteInvoke(ctx, "session/list", map[string]any{"_request": p}, &list); err != nil {
		return err
	}
	var workspace struct {
		Type  string
		Value struct {
			ArchivedSessionIDs []string `json:"archivedSessionIds"`
		}
	}
	if err := c.remoteFirst(ctx, "workspace/follow", map[string]any{}, &workspace); err != nil {
		return err
	}
	if workspace.Type != "baseline" {
		return fmt.Errorf("dsh: missing workspace baseline")
	}
	archived := make(map[string]bool)
	for _, id := range workspace.Value.ArchivedSessionIDs {
		archived[id] = true
	}
	visible := list.Items[:0]
	for _, session := range list.Items {
		if !archived[session.SessionID] {
			visible = append(visible, session)
		}
	}
	list.Items = visible
	return decodeRemoteValue(list, out)
}

func (c *rpcClient) remoteRespond(ctx context.Context, id string, value any) error {
	p, ok := value.(map[string]any)
	if !ok {
		return fmt.Errorf("dsh: invalid Remote answer")
	}
	answer := p["answer"]
	if outcome, ok := p["outcome"]; ok {
		answer = outcome
	}
	return c.remoteEventResult(ctx, id, map[string]any{"kind": "result", "value": answer})
}
func (c *rpcClient) remoteEventResult(ctx context.Context, id string, outcome any) error {
	r, err := c.connection(ctx)
	if err != nil {
		return err
	}
	r.mu.Lock()
	clientID := r.clientID
	r.mu.Unlock()
	return c.remoteInvoke(ctx, "$events/result", map[string]any{"clientId": clientID, "eventId": id, "outcome": outcome}, nil)
}

type remoteHistoryRecord struct {
	Event sessionEvent `json:"event"`
}

type remoteFollowFrame struct {
	Type    string                `json:"type"`
	Cursor  int64                 `json:"cursor"`
	Records []remoteHistoryRecord `json:"records"`
	HasMore bool                  `json:"hasMore"`
	Event   json.RawMessage       `json:"event"`
	Frame   struct {
		Type  string       `json:"type"`
		Chunk *streamChunk `json:"chunk"`
	} `json:"frame"`
	AssistantStream *struct {
		ActiveAttempt *struct {
			Stream []compactStreamRecord `json:"stream"`
			Turn   int                   `json:"turn"`
		} `json:"activeAttempt"`
	} `json:"assistantStream"`
}
type compactStreamRecord struct {
	Type  string       `json:"type"`
	Texts []string     `json:"texts"`
	Chunk *streamChunk `json:"chunk"`
	Index int          `json:"index"`
	ID    string       `json:"id"`
	Name  string       `json:"name"`
	Args  []string     `json:"args"`
}

func (c *rpcClient) remoteMux(ctx context.Context, sid string) (<-chan muxFrame, int, error) {
	r, err := c.connection(ctx)
	if err != nil {
		return nil, 0, err
	}
	events, err := r.open(ctx, "$events", map[string]any{})
	if err != nil {
		return nil, 0, err
	}
	handshake, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	raw, err := r.next(handshake, events)
	if err != nil {
		return nil, 0, err
	}
	var ready struct{ Type, ClientID string }
	if json.Unmarshal(raw, &ready) != nil || ready.Type != "ready" || ready.ClientID == "" {
		return nil, 0, fmt.Errorf("dsh: missing Remote event readiness")
	}
	r.mu.Lock()
	r.clientID = ready.ClientID
	r.mu.Unlock()
	follow, err := r.open(ctx, "session/follow", map[string]any{"request": map[string]any{"address": map[string]any{"kind": "session", "sessionId": sid}, "assistantStream": true}})
	if err != nil {
		return nil, 0, err
	}
	raw, err = r.next(handshake, follow)
	if err != nil {
		return nil, 0, err
	}
	var snapshot remoteFollowFrame
	if json.Unmarshal(raw, &snapshot) != nil || snapshot.Type != "snapshot" {
		return nil, 0, fmt.Errorf("dsh: missing Remote session snapshot")
	}
	// Establish the native sequence before Send can reserve the next turn.
	// Snapshot replay runs asynchronously and can arrive after that submission.
	initialTurn := 0
	for _, record := range snapshot.Records {
		var data sessionEventData
		if json.Unmarshal(record.Event.Data, &data) == nil && data.Turn > initialTurn {
			initialTurn = data.Turn
		}
	}
	if snapshot.AssistantStream != nil && snapshot.AssistantStream.ActiveAttempt != nil && snapshot.AssistantStream.ActiveAttempt.Turn > initialTurn {
		initialTurn = snapshot.AssistantStream.ActiveAttempt.Turn
	}
	frames := make(chan muxFrame, 1024)
	go c.pumpRemote(ctx, sid, events, follow, snapshot, frames)
	return frames, initialTurn, nil
}
