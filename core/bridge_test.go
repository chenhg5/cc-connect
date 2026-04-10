package core

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// helpers ------------------------------------------------------------------

func startTestBridge(t *testing.T, token string) (*BridgeServer, string) {
	t.Helper()
	bs := NewBridgeServer(0, token, "/bridge/ws", nil)

	mux := http.NewServeMux()
	mux.HandleFunc("/bridge/ws", bs.handleWS)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/bridge/ws"
	return bs, wsURL
}

func dialWS(t *testing.T, url string, headers http.Header) *websocket.Conn {
	t.Helper()
	conn, _, err := websocket.DefaultDialer.Dial(url, headers)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

func register(t *testing.T, conn *websocket.Conn, platform string, caps []string) {
	t.Helper()
	msg := map[string]any{
		"type":         "register",
		"platform":     platform,
		"capabilities": caps,
	}
	mustWriteJSON(t, conn, msg)
	var ack map[string]any
	mustReadJSON(t, conn, &ack)
	if ack["ok"] != true {
		t.Fatalf("register failed: %v", ack["error"])
	}
}

func registerWithMetadata(t *testing.T, conn *websocket.Conn, platform string, caps []string, metadata map[string]any) {
	t.Helper()
	msg := map[string]any{
		"type":         "register",
		"platform":     platform,
		"capabilities": caps,
		"metadata":     metadata,
	}
	mustWriteJSON(t, conn, msg)
	var ack map[string]any
	mustReadJSON(t, conn, &ack)
	if ack["ok"] != true {
		t.Fatalf("register failed: %v", ack["error"])
	}
}

func readMsg(t *testing.T, conn *websocket.Conn) map[string]any {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	var m map[string]any
	if err := conn.ReadJSON(&m); err != nil {
		t.Fatalf("read message: %v", err)
	}
	return m
}

func mustWriteJSON(t *testing.T, conn *websocket.Conn, v any) {
	t.Helper()
	if err := conn.WriteJSON(v); err != nil {
		t.Fatalf("write JSON: %v", err)
	}
}

func mustReadJSON(t *testing.T, conn *websocket.Conn, v any) {
	t.Helper()
	if err := conn.ReadJSON(v); err != nil {
		t.Fatalf("read JSON: %v", err)
	}
}

func mustDecodeJSON(t *testing.T, r io.Reader, v any) {
	t.Helper()
	if err := json.NewDecoder(r).Decode(v); err != nil {
		t.Fatalf("decode JSON: %v", err)
	}
}

func mustEncodeJSON(t *testing.T, w io.Writer, v any) {
	t.Helper()
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Fatalf("encode JSON: %v", err)
	}
}

func mustUnmarshalJSON(t *testing.T, data []byte, v any) {
	t.Helper()
	if err := json.Unmarshal(data, v); err != nil {
		t.Fatalf("unmarshal JSON: %v", err)
	}
}

// tests --------------------------------------------------------------------

func TestBridge_RegisterAndConnect(t *testing.T) {
	bs, wsURL := startTestBridge(t, "")

	conn := dialWS(t, wsURL, nil)
	register(t, conn, "test-chat", []string{"text", "buttons"})

	adapters := bs.ConnectedAdapters()
	if len(adapters) != 1 || adapters[0] != "test-chat" {
		t.Fatalf("expected [test-chat], got %v", adapters)
	}
}

func TestBridge_AuthRequired(t *testing.T) {
	_, wsURL := startTestBridge(t, "secret123")

	// No auth → should fail
	_, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err == nil {
		t.Fatal("expected connection to be rejected")
	}
	if resp != nil && resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}

	// With auth → should succeed
	headers := http.Header{}
	headers.Set("Authorization", "Bearer secret123")
	conn := dialWS(t, wsURL, headers)
	register(t, conn, "authed-chat", []string{"text"})
}

func TestBridge_AuthQueryParam(t *testing.T) {
	_, wsURL := startTestBridge(t, "qtoken")

	conn := dialWS(t, wsURL+"?token=qtoken", nil)
	register(t, conn, "qp-chat", []string{"text"})
}

func TestBridge_RegisterMissingPlatform(t *testing.T) {
	_, wsURL := startTestBridge(t, "")
	conn := dialWS(t, wsURL, nil)

	mustWriteJSON(t, conn, map[string]any{
		"type":         "register",
		"platform":     "",
		"capabilities": []string{"text"},
	})

	var ack map[string]any
	mustReadJSON(t, conn, &ack)
	if ack["ok"] == true {
		t.Fatal("expected registration to fail for empty platform")
	}
}

func TestBridge_MessageRouting(t *testing.T) {
	bs, wsURL := startTestBridge(t, "")

	var received *Message
	var receivedMu sync.Mutex

	bp := bs.NewPlatform("test-proj")

	e := NewEngine("test-proj", &stubAgent{}, []Platform{bp}, "", LangEnglish)
	bs.RegisterEngine("test-proj", e, bp)
	bp.handler = func(p Platform, msg *Message) {
		receivedMu.Lock()
		received = msg
		receivedMu.Unlock()
	}

	conn := dialWS(t, wsURL, nil)
	register(t, conn, "mychat", []string{"text"})

	imgData := base64.StdEncoding.EncodeToString([]byte("fakepng"))
	mustWriteJSON(t, conn, map[string]any{
		"type":        "message",
		"msg_id":      "m1",
		"session_key": "mychat:user1:user1",
		"user_id":     "user1",
		"user_name":   "Alice",
		"content":     "hello bridge",
		"reply_ctx":   "conv-1",
		"images":      []map[string]any{{"mime_type": "image/png", "data": imgData, "file_name": "test.png"}},
	})

	time.Sleep(100 * time.Millisecond)

	receivedMu.Lock()
	defer receivedMu.Unlock()
	if received == nil {
		t.Fatal("expected message to be received")
	}
	if received.Content != "hello bridge" {
		t.Fatalf("content = %q, want %q", received.Content, "hello bridge")
	}
	if received.Platform != "mychat" {
		t.Fatalf("platform = %q, want %q", received.Platform, "mychat")
	}
	if received.UserName != "Alice" {
		t.Fatalf("user_name = %q, want %q", received.UserName, "Alice")
	}
	if len(received.Images) != 1 {
		t.Fatalf("images count = %d, want 1", len(received.Images))
	}
	if received.Images[0].FileName != "test.png" {
		t.Fatalf("image filename = %q, want %q", received.Images[0].FileName, "test.png")
	}
}

func TestBridge_MessageReplyCtxCarriesProgressHints(t *testing.T) {
	bs, wsURL := startTestBridge(t, "")

	var got *bridgeReplyCtx

	bp := bs.NewPlatform("test-proj")
	e := NewEngine("test-proj", &stubAgent{}, []Platform{bp}, "", LangEnglish)
	bs.RegisterEngine("test-proj", e, bp)
	bp.handler = func(p Platform, msg *Message) {
		rc, ok := msg.ReplyCtx.(*bridgeReplyCtx)
		if !ok {
			t.Fatalf("reply ctx type = %T, want *bridgeReplyCtx", msg.ReplyCtx)
		}
		got = rc
	}

	conn := dialWS(t, wsURL, nil)
	registerWithMetadata(t, conn, "bridge", []string{"text", "card", "preview", "update_message"}, map[string]any{
		"adapter": "bot-gateway",
	})

	mustWriteJSON(t, conn, map[string]any{
		"type":        "message",
		"msg_id":      "m1",
		"session_key": "bridge:room-1:user-1",
		"user_id":     "user-1",
		"content":     "hello",
		"reply_ctx":   "ctx-1",
	})

	time.Sleep(100 * time.Millisecond)

	if got == nil {
		t.Fatal("expected reply ctx to be captured")
	}
	if got.progressStyleHint() != progressStyleCard {
		t.Fatalf("progressStyleHint() = %q, want %q", got.progressStyleHint(), progressStyleCard)
	}
	if !got.supportsProgressCardPayloadHint() {
		t.Fatal("supportsProgressCardPayloadHint() = false, want true")
	}
}

func TestBridge_ReplyRouting(t *testing.T) {
	bs, wsURL := startTestBridge(t, "")

	bp := bs.NewPlatform("test-proj")

	e := NewEngine("test-proj", &stubAgent{}, []Platform{bp}, "", LangEnglish)
	bs.RegisterEngine("test-proj", e, bp)
	bp.handler = func(p Platform, msg *Message) {
		if err := p.Reply(context.TODO(), msg.ReplyCtx, "pong"); err != nil {
			t.Fatalf("Reply: %v", err)
		}
	}

	conn := dialWS(t, wsURL, nil)
	register(t, conn, "rc", []string{"text"})

	mustWriteJSON(t, conn, map[string]any{
		"type":        "message",
		"msg_id":      "m1",
		"session_key": "rc:u1:u1",
		"user_id":     "u1",
		"content":     "ping",
		"reply_ctx":   "ctx-1",
	})

	reply := readMsg(t, conn)
	if reply["type"] != "reply" {
		t.Fatalf("type = %q, want reply", reply["type"])
	}
	if reply["content"] != "pong" {
		t.Fatalf("content = %q, want pong", reply["content"])
	}
	if reply["reply_ctx"] != "ctx-1" {
		t.Fatalf("reply_ctx = %q, want ctx-1", reply["reply_ctx"])
	}
}

func TestBridge_ReconstructReplyCtx_RequiresCapability(t *testing.T) {
	bs, wsURL := startTestBridge(t, "")
	bp := bs.NewPlatform("advisor-gemini")

	conn := dialWS(t, wsURL, nil)
	register(t, conn, "bridge", []string{"text"})

	_, err := bp.ReconstructReplyCtx("bridge:1491487450722341088:relay")
	if err == nil || !strings.Contains(err.Error(), "does not support reconstruct_reply") {
		t.Fatalf("ReconstructReplyCtx() error = %v, want reconstruct_reply capability error", err)
	}
}

func TestBridge_ReconstructReplyCtx_UsesStructuredPayload(t *testing.T) {
	bs, wsURL := startTestBridge(t, "")
	bp := bs.NewPlatform("advisor-gemini")

	conn := dialWS(t, wsURL, nil)
	register(t, conn, "bridge", []string{"text", "reconstruct_reply"})

	replyCtx, err := bp.ReconstructReplyCtx("bridge:1491487450722341088:relay")
	if err != nil {
		t.Fatalf("ReconstructReplyCtx() error = %v", err)
	}

	rc, ok := replyCtx.(*bridgeReplyCtx)
	if !ok {
		t.Fatalf("reply ctx type = %T, want *bridgeReplyCtx", replyCtx)
	}
	if rc.Platform != "bridge" {
		t.Fatalf("Platform = %q, want bridge", rc.Platform)
	}
	if rc.SessionKey != "bridge:1491487450722341088:relay" {
		t.Fatalf("SessionKey = %q, want relay session key", rc.SessionKey)
	}

	var payload bridgeReconstructReplyCtxPayload
	if err := json.Unmarshal([]byte(rc.ReplyCtx), &payload); err != nil {
		t.Fatalf("unmarshal reply_ctx: %v", err)
	}
	if payload.Kind != bridgeReconstructReplyCtxKind {
		t.Fatalf("kind = %q, want %q", payload.Kind, bridgeReconstructReplyCtxKind)
	}
	if payload.Version != 1 {
		t.Fatalf("version = %d, want 1", payload.Version)
	}
	if payload.SenderProject != "advisor-gemini" {
		t.Fatalf("sender_project = %q, want advisor-gemini", payload.SenderProject)
	}
	if payload.TransportChatID != "1491487450722341088" {
		t.Fatalf("transport_chat_id = %q, want 1491487450722341088", payload.TransportChatID)
	}
	if payload.TransportSessionKey != "bridge:1491487450722341088:relay" {
		t.Fatalf("transport_session_key = %q, want relay session key", payload.TransportSessionKey)
	}
}

func TestBridge_ReconstructReplyCtx_UsesAdapterProgressHints(t *testing.T) {
	bs, wsURL := startTestBridge(t, "")
	bp := bs.NewPlatform("test-proj")

	conn := dialWS(t, wsURL, nil)
	registerWithMetadata(t, conn, "bridge", []string{"text", "card", "preview", "update_message", "reconstruct_reply"}, map[string]any{
		"adapter": "bot-gateway",
	})

	replyCtx, err := bp.ReconstructReplyCtx("bridge:room-1:user-1")
	if err != nil {
		t.Fatalf("ReconstructReplyCtx() error = %v", err)
	}

	rc, ok := replyCtx.(*bridgeReplyCtx)
	if !ok {
		t.Fatalf("reply ctx type = %T, want *bridgeReplyCtx", replyCtx)
	}
	if rc.progressStyleHint() != progressStyleCard {
		t.Fatalf("progressStyleHint() = %q, want %q", rc.progressStyleHint(), progressStyleCard)
	}
	if !rc.supportsProgressCardPayloadHint() {
		t.Fatal("supportsProgressCardPayloadHint() = false, want true")
	}
}

func TestBridge_CardFallback(t *testing.T) {
	bs, wsURL := startTestBridge(t, "")

	bp := bs.NewPlatform("test-proj")

	e := NewEngine("test-proj", &stubAgent{}, []Platform{bp}, "", LangEnglish)
	bs.RegisterEngine("test-proj", e, bp)
	bp.handler = func(p Platform, msg *Message) {
		cs, ok := p.(CardSender)
		if !ok {
			t.Fatal("BridgePlatform should implement CardSender")
		}
		card := NewCard().Title("Test", "blue").Markdown("hello").Build()
		if err := cs.SendCard(context.TODO(), msg.ReplyCtx, card); err != nil {
			t.Fatalf("SendCard: %v", err)
		}
	}

	// Adapter declares NO card capability → should get text fallback
	conn := dialWS(t, wsURL, nil)
	register(t, conn, "nocards", []string{"text"})

	mustWriteJSON(t, conn, map[string]any{
		"type":        "message",
		"msg_id":      "m1",
		"session_key": "nocards:u1:u1",
		"user_id":     "u1",
		"content":     "hi",
		"reply_ctx":   "c1",
	})

	reply := readMsg(t, conn)
	if reply["type"] != "reply" {
		t.Fatalf("expected text fallback, got type=%q", reply["type"])
	}
	content, _ := reply["content"].(string)
	if !strings.Contains(content, "hello") {
		t.Fatalf("fallback should contain 'hello', got %q", content)
	}
}

func TestBridge_CardNative(t *testing.T) {
	bs, wsURL := startTestBridge(t, "")

	bp := bs.NewPlatform("test-proj")

	e := NewEngine("test-proj", &stubAgent{}, []Platform{bp}, "", LangEnglish)
	bs.RegisterEngine("test-proj", e, bp)
	bp.handler = func(p Platform, msg *Message) {
		cs := p.(CardSender)
		card := NewCard().Title("Test", "blue").Markdown("hello").Build()
		if err := cs.SendCard(context.TODO(), msg.ReplyCtx, card); err != nil {
			t.Fatalf("SendCard: %v", err)
		}
	}

	// Adapter declares card capability → should get card
	conn := dialWS(t, wsURL, nil)
	register(t, conn, "withcards", []string{"text", "card"})

	mustWriteJSON(t, conn, map[string]any{
		"type":        "message",
		"msg_id":      "m1",
		"session_key": "withcards:u1:u1",
		"user_id":     "u1",
		"content":     "hi",
		"reply_ctx":   "c1",
	})

	reply := readMsg(t, conn)
	if reply["type"] != "card" {
		t.Fatalf("expected card, got type=%q", reply["type"])
	}
	cardData, ok := reply["card"].(map[string]any)
	if !ok {
		t.Fatal("card field should be a map")
	}
	header, _ := cardData["header"].(map[string]any)
	if header["title"] != "Test" {
		t.Fatalf("card title = %q, want Test", header["title"])
	}
}

func TestBridge_Ping(t *testing.T) {
	_, wsURL := startTestBridge(t, "")
	conn := dialWS(t, wsURL, nil)
	register(t, conn, "pingtest", []string{"text"})

	mustWriteJSON(t, conn, map[string]any{"type": "ping", "ts": time.Now().UnixMilli()})
	pong := readMsg(t, conn)
	if pong["type"] != "pong" {
		t.Fatalf("expected pong, got %q", pong["type"])
	}
}

func TestBridge_AdapterReplace(t *testing.T) {
	bs, wsURL := startTestBridge(t, "")

	conn1 := dialWS(t, wsURL, nil)
	register(t, conn1, "replaceme", []string{"text"})

	if len(bs.ConnectedAdapters()) != 1 {
		t.Fatal("expected 1 adapter")
	}

	conn2 := dialWS(t, wsURL, nil)
	register(t, conn2, "replaceme", []string{"text", "card"})

	if len(bs.ConnectedAdapters()) != 1 {
		t.Fatal("expected still 1 adapter after replace")
	}

	a := bs.getAdapter("replaceme")
	if !a.capabilities["card"] {
		t.Fatal("replaced adapter should have card capability")
	}
}

func TestSerializeCard(t *testing.T) {
	card := NewCard().
		Title("Model", "blue").
		Markdown("Choose:").
		Buttons(PrimaryBtn("GPT-4", "cmd:/model switch gpt-4"), DefaultBtn("Claude", "cmd:/model switch claude")).
		Divider().
		Note("tip").
		Build()

	result := serializeCard(card)

	header, _ := result["header"].(map[string]string)
	if header["title"] != "Model" || header["color"] != "blue" {
		t.Fatalf("header = %v", header)
	}

	elements, _ := result["elements"].([]map[string]any)
	if len(elements) != 4 {
		t.Fatalf("elements count = %d, want 4", len(elements))
	}
	if elements[0]["type"] != "markdown" {
		t.Fatalf("first element type = %q", elements[0]["type"])
	}
	if elements[1]["type"] != "actions" {
		t.Fatalf("second element type = %q", elements[1]["type"])
	}
	if elements[2]["type"] != "divider" {
		t.Fatalf("third element type = %q", elements[2]["type"])
	}
	if elements[3]["type"] != "note" {
		t.Fatalf("fourth element type = %q", elements[3]["type"])
	}

	btns, _ := elements[1]["buttons"].([]map[string]any)
	if len(btns) != 2 {
		t.Fatalf("buttons count = %d", len(btns))
	}
	if btns[0]["text"] != "GPT-4" || btns[0]["value"] != "cmd:/model switch gpt-4" {
		t.Fatalf("button[0] = %v", btns[0])
	}

	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("json marshal: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("serialized card is empty")
	}
}

// ---------------------------------------------------------------------------
// Session Management REST API tests
// ---------------------------------------------------------------------------

// startTestBridgeWithREST creates a bridge server with both WS and REST endpoints.
func startTestBridgeWithREST(t *testing.T, token string) (*BridgeServer, string) {
	t.Helper()
	bs := NewBridgeServer(0, token, "/bridge/ws", nil)

	agent := &stubAgent{}
	sm := NewSessionManager("")
	engine := NewEngine("test-proj", agent, nil, "", LangEnglish)
	engine.sessions = sm

	bp := bs.NewPlatform("test-proj")
	bs.RegisterEngine("test-proj", engine, bp)

	mux := http.NewServeMux()
	mux.HandleFunc("/bridge/ws", bs.handleWS)
	mux.HandleFunc("/bridge/sessions", bs.authHTTP(bs.handleSessions))
	mux.HandleFunc("/bridge/sessions/", bs.authHTTP(bs.handleSessionRoutes))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	return bs, srv.URL
}

type bridgeAPIResponse struct {
	OK    bool            `json:"ok"`
	Data  json.RawMessage `json:"data,omitempty"`
	Error string          `json:"error,omitempty"`
}

func bridgeGet(t *testing.T, url, token string) bridgeAPIResponse {
	t.Helper()
	req, _ := http.NewRequest("GET", url, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	var r bridgeAPIResponse
	mustDecodeJSON(t, resp.Body, &r)
	return r
}

func bridgePost(t *testing.T, url, token string, body any) bridgeAPIResponse {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		mustEncodeJSON(t, &buf, body)
	}
	req, _ := http.NewRequest("POST", url, &buf)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer resp.Body.Close()
	var r bridgeAPIResponse
	mustDecodeJSON(t, resp.Body, &r)
	return r
}

func bridgeDel(t *testing.T, url, token string) bridgeAPIResponse {
	t.Helper()
	req, _ := http.NewRequest("DELETE", url, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE %s: %v", url, err)
	}
	defer resp.Body.Close()
	var r bridgeAPIResponse
	mustDecodeJSON(t, resp.Body, &r)
	return r
}

func TestBridge_SessionList(t *testing.T) {
	_, baseURL := startTestBridgeWithREST(t, "tok")

	// List sessions for a new key — should create a default session
	r := bridgeGet(t, baseURL+"/bridge/sessions?session_key=test:u1:u1&token=tok", "")
	if !r.OK {
		t.Logf("no sessions yet: %s", r.Error)
	}

	// Create a session first
	r = bridgePost(t, baseURL+"/bridge/sessions", "tok", map[string]string{
		"session_key": "test:u1:u1",
		"name":        "work",
	})
	if !r.OK {
		t.Fatalf("create session failed: %s", r.Error)
	}
	var created struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	mustUnmarshalJSON(t, r.Data, &created)
	if created.ID == "" {
		t.Fatal("expected session ID")
	}
	if created.Name != "work" {
		t.Fatalf("expected name 'work', got %q", created.Name)
	}

	// Now list — should have 1 session
	r = bridgeGet(t, baseURL+"/bridge/sessions?session_key=test:u1:u1", "tok")
	if !r.OK {
		t.Fatalf("list sessions failed: %s", r.Error)
	}
	var listData struct {
		Sessions []map[string]any `json:"sessions"`
	}
	mustUnmarshalJSON(t, r.Data, &listData)
	if len(listData.Sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(listData.Sessions))
	}
}

func TestBridge_SessionCreateAndDetail(t *testing.T) {
	_, baseURL := startTestBridgeWithREST(t, "tok")

	// Create
	r := bridgePost(t, baseURL+"/bridge/sessions", "tok", map[string]string{
		"session_key": "test:u1:u1",
		"name":        "dev",
	})
	if !r.OK {
		t.Fatalf("create failed: %s", r.Error)
	}
	var created struct {
		ID string `json:"id"`
	}
	mustUnmarshalJSON(t, r.Data, &created)

	// Get detail
	r = bridgeGet(t, baseURL+"/bridge/sessions/"+created.ID+"?session_key=test:u1:u1", "tok")
	if !r.OK {
		t.Fatalf("get detail failed: %s", r.Error)
	}
	var detail struct {
		ID      string           `json:"id"`
		Name    string           `json:"name"`
		History []map[string]any `json:"history"`
	}
	mustUnmarshalJSON(t, r.Data, &detail)
	if detail.ID != created.ID {
		t.Fatalf("expected id %q, got %q", created.ID, detail.ID)
	}
	if detail.Name != "dev" {
		t.Fatalf("expected name 'dev', got %q", detail.Name)
	}
}

func TestBridge_SessionDelete(t *testing.T) {
	_, baseURL := startTestBridgeWithREST(t, "tok")

	r := bridgePost(t, baseURL+"/bridge/sessions", "tok", map[string]string{
		"session_key": "test:u1:u1",
		"name":        "temp",
	})
	if !r.OK {
		t.Fatalf("create failed: %s", r.Error)
	}
	var created struct {
		ID string `json:"id"`
	}
	mustUnmarshalJSON(t, r.Data, &created)

	// Delete
	r = bridgeDel(t, baseURL+"/bridge/sessions/"+created.ID+"?session_key=test:u1:u1", "tok")
	if !r.OK {
		t.Fatalf("delete failed: %s", r.Error)
	}

	// Verify deleted
	r = bridgeGet(t, baseURL+"/bridge/sessions/"+created.ID+"?session_key=test:u1:u1", "tok")
	if r.OK {
		t.Fatal("expected 404 after deletion")
	}
}

func TestBridge_SessionSwitch(t *testing.T) {
	_, baseURL := startTestBridgeWithREST(t, "tok")

	// Create two sessions
	r := bridgePost(t, baseURL+"/bridge/sessions", "tok", map[string]string{
		"session_key": "test:u1:u1",
		"name":        "first",
	})
	if !r.OK {
		t.Fatalf("create first failed: %s", r.Error)
	}

	r = bridgePost(t, baseURL+"/bridge/sessions", "tok", map[string]string{
		"session_key": "test:u1:u1",
		"name":        "second",
	})
	if !r.OK {
		t.Fatalf("create second failed: %s", r.Error)
	}
	var second struct {
		ID string `json:"id"`
	}
	mustUnmarshalJSON(t, r.Data, &second)

	// Switch to second
	r = bridgePost(t, baseURL+"/bridge/sessions/switch", "tok", map[string]string{
		"session_key": "test:u1:u1",
		"target":      second.ID,
	})
	if !r.OK {
		t.Fatalf("switch failed: %s", r.Error)
	}
	var switched struct {
		ActiveSessionID string `json:"active_session_id"`
	}
	mustUnmarshalJSON(t, r.Data, &switched)
	if switched.ActiveSessionID != second.ID {
		t.Fatalf("expected active=%s, got %s", second.ID, switched.ActiveSessionID)
	}
}

func TestBridge_SessionAuthRequired(t *testing.T) {
	_, baseURL := startTestBridgeWithREST(t, "secret")

	r := bridgeGet(t, baseURL+"/bridge/sessions?session_key=test:u1:u1", "")
	if r.OK {
		t.Fatal("expected auth failure without token")
	}

	r = bridgeGet(t, baseURL+"/bridge/sessions?session_key=test:u1:u1", "secret")
	if !r.OK {
		t.Fatalf("expected success with token, got: %s", r.Error)
	}
}

func TestBridge_SessionMissingParams(t *testing.T) {
	_, baseURL := startTestBridgeWithREST(t, "tok")

	// Missing session_key
	r := bridgeGet(t, baseURL+"/bridge/sessions", "tok")
	if r.OK {
		t.Fatal("expected error without session_key")
	}

	// Missing session_key in POST
	r = bridgePost(t, baseURL+"/bridge/sessions", "tok", map[string]string{
		"name": "test",
	})
	if r.OK {
		t.Fatal("expected error without session_key in POST")
	}

	// Missing params in switch
	r = bridgePost(t, baseURL+"/bridge/sessions/switch", "tok", map[string]string{
		"session_key": "test:u1:u1",
	})
	if r.OK {
		t.Fatal("expected error without target in switch")
	}
}
