package core

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
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
	bs := NewBridgeServer(0, token, "/bridge/ws")

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
	if err := conn.WriteJSON(msg); err != nil {
		t.Fatalf("send register: %v", err)
	}
	var ack map[string]any
	if err := conn.ReadJSON(&ack); err != nil {
		t.Fatalf("read register_ack: %v", err)
	}
	if ack["ok"] != true {
		t.Fatalf("register failed: %v", ack["error"])
	}
}

func readMsg(t *testing.T, conn *websocket.Conn) map[string]any {
	t.Helper()
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var m map[string]any
	if err := conn.ReadJSON(&m); err != nil {
		t.Fatalf("read message: %v", err)
	}
	return m
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

	if err := conn.WriteJSON(map[string]any{
		"type":         "register",
		"platform":     "",
		"capabilities": []string{"text"},
	}); err != nil {
		t.Fatal(err)
	}

	var ack map[string]any
	conn.ReadJSON(&ack)
	if ack["ok"] == true {
		t.Fatal("expected registration to fail for empty platform")
	}
}

func TestBridge_MessageRouting(t *testing.T) {
	bs, wsURL := startTestBridge(t, "")

	var received *Message
	var receivedMu sync.Mutex

	bp := bs.NewPlatform("test-proj")
	bp.handler = func(p Platform, msg *Message) {
		receivedMu.Lock()
		received = msg
		receivedMu.Unlock()
	}

	e := NewEngine("test-proj", &stubAgent{}, []Platform{bp}, "", LangEnglish)
	bs.RegisterEngine("test-proj", e, bp)

	conn := dialWS(t, wsURL, nil)
	register(t, conn, "mychat", []string{"text"})

	imgData := base64.StdEncoding.EncodeToString([]byte("fakepng"))
	if err := conn.WriteJSON(map[string]any{
		"type":        "message",
		"msg_id":      "m1",
		"session_key": "mychat:user1:user1",
		"user_id":     "user1",
		"user_name":   "Alice",
		"content":     "hello bridge",
		"reply_ctx":   "conv-1",
		"images":      []map[string]any{{"mime_type": "image/png", "data": imgData, "file_name": "test.png"}},
	}); err != nil {
		t.Fatal(err)
	}

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

func TestBridge_ReplyRouting(t *testing.T) {
	bs, wsURL := startTestBridge(t, "")

	bp := bs.NewPlatform("test-proj")
	bp.handler = func(p Platform, msg *Message) {
		p.Reply(nil, msg.ReplyCtx, "pong")
	}

	e := NewEngine("test-proj", &stubAgent{}, []Platform{bp}, "", LangEnglish)
	bs.RegisterEngine("test-proj", e, bp)

	conn := dialWS(t, wsURL, nil)
	register(t, conn, "rc", []string{"text"})

	conn.WriteJSON(map[string]any{
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

func TestBridge_CardFallback(t *testing.T) {
	bs, wsURL := startTestBridge(t, "")

	bp := bs.NewPlatform("test-proj")
	bp.handler = func(p Platform, msg *Message) {
		cs, ok := p.(CardSender)
		if !ok {
			t.Fatal("BridgePlatform should implement CardSender")
		}
		card := NewCard().Title("Test", "blue").Markdown("hello").Build()
		cs.SendCard(nil, msg.ReplyCtx, card)
	}

	e := NewEngine("test-proj", &stubAgent{}, []Platform{bp}, "", LangEnglish)
	bs.RegisterEngine("test-proj", e, bp)

	// Adapter declares NO card capability → should get text fallback
	conn := dialWS(t, wsURL, nil)
	register(t, conn, "nocards", []string{"text"})

	conn.WriteJSON(map[string]any{
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
	bp.handler = func(p Platform, msg *Message) {
		cs := p.(CardSender)
		card := NewCard().Title("Test", "blue").Markdown("hello").Build()
		cs.SendCard(nil, msg.ReplyCtx, card)
	}

	e := NewEngine("test-proj", &stubAgent{}, []Platform{bp}, "", LangEnglish)
	bs.RegisterEngine("test-proj", e, bp)

	// Adapter declares card capability → should get card
	conn := dialWS(t, wsURL, nil)
	register(t, conn, "withcards", []string{"text", "card"})

	conn.WriteJSON(map[string]any{
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

	conn.WriteJSON(map[string]any{"type": "ping", "ts": time.Now().UnixMilli()})
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
		Buttons(PrimaryBtn("GPT-4", "cmd:/model gpt-4"), DefaultBtn("Claude", "cmd:/model claude")).
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
	if btns[0]["text"] != "GPT-4" || btns[0]["value"] != "cmd:/model gpt-4" {
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
	bs := NewBridgeServer(0, token, "/bridge/ws")

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
	json.NewDecoder(resp.Body).Decode(&r)
	return r
}

func bridgePost(t *testing.T, url, token string, body any) bridgeAPIResponse {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		json.NewEncoder(&buf).Encode(body)
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
	json.NewDecoder(resp.Body).Decode(&r)
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
	json.NewDecoder(resp.Body).Decode(&r)
	return r
}

func TestBridge_SessionList(t *testing.T) {
	_, baseURL := startTestBridgeWithREST(t, "tok")

	// List sessions for a new key — should create a default session
	r := bridgeGet(t, baseURL+"/bridge/sessions?session_key=test:u1:u1&token=tok", "")
	if !r.OK {
		// No sessions yet, that's fine — list returns empty
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
	json.Unmarshal(r.Data, &created)
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
	json.Unmarshal(r.Data, &listData)
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
	json.Unmarshal(r.Data, &created)

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
	json.Unmarshal(r.Data, &detail)
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
	json.Unmarshal(r.Data, &created)

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
	json.Unmarshal(r.Data, &second)

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
	json.Unmarshal(r.Data, &switched)
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
