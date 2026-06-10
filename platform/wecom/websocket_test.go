package wecom

import (
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
	"github.com/gorilla/websocket"
)

// ---------------------------------------------------------------------------
// splitByBytes
// ---------------------------------------------------------------------------

func TestSplitByBytes_ShortString(t *testing.T) {
	parts := splitByBytes("hello", 100)
	if len(parts) != 1 || parts[0] != "hello" {
		t.Fatalf("expected single chunk, got %v", parts)
	}
}

func TestSplitByBytes_ExactBoundary(t *testing.T) {
	s := "abcdef"
	parts := splitByBytes(s, 6)
	if len(parts) != 1 || parts[0] != s {
		t.Fatalf("expected single chunk at exact boundary, got %v", parts)
	}
}

func TestSplitByBytes_SplitASCII(t *testing.T) {
	s := "abcdef"
	parts := splitByBytes(s, 4)
	if len(parts) != 2 {
		t.Fatalf("expected 2 chunks, got %d: %v", len(parts), parts)
	}
	if parts[0] != "abcd" || parts[1] != "ef" {
		t.Fatalf("unexpected chunks: %v", parts)
	}
}

func TestSplitByBytes_UTF8NeverSplitsMidRune(t *testing.T) {
	// "你好世界" = 4 runes × 3 bytes = 12 bytes
	s := "你好世界"
	parts := splitByBytes(s, 5) // 5 < 6, so only one 3-byte rune fits? Actually 3 fits, 4 doesn't → first chunk = "你" (3 bytes)
	// With maxBytes=5: first iteration end=5, s[5] is a continuation byte → back off to 3 → "你", next end=5 but only 9 left, s[5] continuation → 6 → "好世" wait...
	// Let's just verify no chunk contains a partial rune.
	reassembled := ""
	for _, p := range parts {
		reassembled += p
		// Each chunk must be valid UTF-8 (no partial rune)
		for i := 0; i < len(p); i++ {
			if p[i]>>6 == 0b10 && (i == 0 || p[i-1] < 0x80) {
				t.Fatalf("chunk contains orphaned continuation byte: %q", p)
			}
		}
	}
	if reassembled != s {
		t.Fatalf("reassembled %q != original %q", reassembled, s)
	}
}

func TestSplitByBytes_EmptyString(t *testing.T) {
	parts := splitByBytes("", 100)
	if len(parts) != 1 || parts[0] != "" {
		t.Fatalf("expected single empty chunk, got %v", parts)
	}
}

func TestSplitByBytes_ReassemblesLargeContent(t *testing.T) {
	var s string
	for i := 0; i < 500; i++ {
		s += fmt.Sprintf("line %d: 这是一段中文\n", i)
	}
	parts := splitByBytes(s, 2000)
	reassembled := ""
	for _, p := range parts {
		if len(p) > 2000 {
			t.Fatalf("chunk exceeds maxBytes: %d", len(p))
		}
		reassembled += p
	}
	if reassembled != s {
		t.Fatalf("reassembled content does not match original (len %d vs %d)", len(reassembled), len(s))
	}
}

// ---------------------------------------------------------------------------
// handleMsgCallback — chatID fallback to userID for single chats
// ---------------------------------------------------------------------------

func newCapturedWSPlatform() (*WSPlatform, <-chan *core.Message) {
	p := &WSPlatform{allowFrom: "*"}
	captured := make(chan *core.Message, 1)
	p.handler = func(_ core.Platform, msg *core.Message) {
		captured <- msg
	}
	return p, captured
}

func wsCallbackFrame(t *testing.T, reqID string, body wsMsgCallbackBody) wsFrame {
	t.Helper()

	bodyBytes, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal callback body: %v", err)
	}
	return wsFrame{
		Cmd:     "aibot_msg_callback",
		Headers: wsFrameHeaders{ReqID: reqID},
		Body:    bodyBytes,
	}
}

func TestHandleMsgCallback_SingleChat_ChatIDFallback(t *testing.T) {
	p, captured := newCapturedWSPlatform()

	body := wsMsgCallbackBody{
		MsgID:    "msg_001",
		ChatID:   "", // single chat: no chatID from server
		ChatType: "single",
		MsgType:  "text",
	}
	body.From.UserID = "zhangsan"
	body.Text.Content = "hello"
	body.CreateTime = time.Now().Unix()

	p.handleMsgCallback(wsCallbackFrame(t, "req_123", body))

	select {
	case msg := <-captured:
		if msg.SessionKey != "wecom:zhangsan:zhangsan" {
			t.Fatalf("expected sessionKey 'wecom:zhangsan:zhangsan', got %q", msg.SessionKey)
		}
		rc := msg.ReplyCtx.(wsReplyContext)
		if rc.chatID != "zhangsan" {
			t.Fatalf("expected chatID to fall back to userID 'zhangsan', got %q", rc.chatID)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("handler not called")
	}
}

func TestHandleMsgCallback_GroupChat_ChatIDPreserved(t *testing.T) {
	p, captured := newCapturedWSPlatform()

	body := wsMsgCallbackBody{
		MsgID:    "msg_002",
		ChatID:   "group_chat_id_123",
		ChatType: "group",
		MsgType:  "text",
	}
	body.From.UserID = "zhangsan"
	body.Text.Content = "hi group"
	body.CreateTime = time.Now().Unix()

	p.handleMsgCallback(wsCallbackFrame(t, "req_456", body))

	select {
	case msg := <-captured:
		if msg.SessionKey != "wecom:group_chat_id_123:zhangsan" {
			t.Fatalf("expected sessionKey 'wecom:group_chat_id_123:zhangsan', got %q", msg.SessionKey)
		}
		rc := msg.ReplyCtx.(wsReplyContext)
		if rc.chatID != "group_chat_id_123" {
			t.Fatalf("expected chatID 'group_chat_id_123', got %q", rc.chatID)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("handler not called")
	}
}

func TestHandleMsgCallback_RepliesToUnauthorizedSender(t *testing.T) {
	serverDone := make(chan error, 1)
	upgrader := websocket.Upgrader{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			serverDone <- err
			return
		}
		defer func() {
			_ = conn.Close()
		}()
		var frame wsFrame
		if err := conn.ReadJSON(&frame); err != nil {
			serverDone <- err
			return
		}
		var body struct {
			Stream struct {
				Content string `json:"content"`
			} `json:"stream"`
		}
		if err := json.Unmarshal(frame.Body, &body); err != nil {
			serverDone <- err
			return
		}
		if frame.Cmd != "aibot_respond_msg" {
			serverDone <- fmt.Errorf("cmd = %q, want aibot_respond_msg", frame.Cmd)
			return
		}
		if got := body.Stream.Content; got != core.UnauthorizedAccessMessage {
			serverDone <- fmt.Errorf("content = %q, want %q", got, core.UnauthorizedAccessMessage)
			return
		}
		serverDone <- nil
	}))
	defer server.Close()

	wsURL := "ws" + server.URL[len("http"):]
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial test websocket: %v", err)
	}
	defer func() {
		_ = conn.Close()
	}()

	p := &WSPlatform{
		allowFrom: "allowed-user",
		conn:      conn,
		handler: func(core.Platform, *core.Message) {
			t.Fatal("handler should not run for unauthorized sender")
		},
	}

	body := wsMsgCallbackBody{
		MsgID:    "msg_unauthorized",
		ChatID:   "chat_group",
		ChatType: "group",
		MsgType:  "text",
	}
	body.From.UserID = "blocked-user"
	body.Text.Content = "@bot hello"
	body.CreateTime = time.Now().Unix()
	p.handleMsgCallback(wsCallbackFrame(t, "req_unauthorized", body))

	select {
	case err := <-serverDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for unauthorized reply frame")
	}
}

func TestHandleMsgCallback_StripsBotMention(t *testing.T) {
	p, captured := newCapturedWSPlatform()
	p.botID = "robot01"

	body := wsMsgCallbackBody{
		MsgID:    "msg_mention",
		ChatID:   "grp1",
		ChatType: "group",
		MsgType:  "text",
		AibotID:  "robot01",
	}
	body.From.UserID = "u1"
	body.Text.Content = "允许 @Robot01"
	body.CreateTime = time.Now().Unix()

	p.handleMsgCallback(wsCallbackFrame(t, "req_m", body))

	select {
	case msg := <-captured:
		if msg.Content != "允许" {
			t.Fatalf("expected stripped content %q, got %q", "允许", msg.Content)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("handler not called")
	}
}

// ---------------------------------------------------------------------------
// ReconstructReplyCtx
// ---------------------------------------------------------------------------

func TestReconstructReplyCtx_Valid(t *testing.T) {
	p := &WSPlatform{}
	rctx, err := p.ReconstructReplyCtx("wecom:chatid123:user456")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	rc := rctx.(wsReplyContext)
	if rc.chatID != "chatid123" || rc.userID != "user456" {
		t.Fatalf("unexpected context: %+v", rc)
	}
}

func TestReconstructReplyCtx_InvalidPrefix(t *testing.T) {
	p := &WSPlatform{}
	_, err := p.ReconstructReplyCtx("slack:chatid123:user456")
	if err == nil {
		t.Fatal("expected error for invalid prefix")
	}
}

func TestReconstructReplyCtx_TooFewParts(t *testing.T) {
	p := &WSPlatform{}
	_, err := p.ReconstructReplyCtx("wecom:only")
	if err == nil {
		t.Fatal("expected error for too few parts")
	}
}

// ---------------------------------------------------------------------------
// writeAndWaitAck
// ---------------------------------------------------------------------------

func TestWriteAndWaitAck_SuccessfulAck(t *testing.T) {
	p := &WSPlatform{}

	reqID := "send_1"
	ch := make(chan wsAckResult, 1)
	p.pendingAcks.Store(reqID, ch)

	// Simulate receiving ack in another goroutine
	go func() {
		time.Sleep(10 * time.Millisecond)
		p.dispatchAck(reqID, wsAckResult{})
	}()

	assertAckResult(t, ch, func(result wsAckResult) {
		if result.err != nil {
			t.Fatalf("expected nil ack error, got %v", result.err)
		}
	})
}

func TestWriteAndWaitAck_AckWithError(t *testing.T) {
	p := &WSPlatform{}

	reqID := "send_2"
	ch := make(chan wsAckResult, 1)
	p.pendingAcks.Store(reqID, ch)

	ackErr := fmt.Errorf("wecom-ws: ack error: errcode=40001 errmsg=invalid token")
	go func() {
		time.Sleep(10 * time.Millisecond)
		p.dispatchAck(reqID, wsAckResult{err: ackErr})
	}()

	assertAckResult(t, ch, func(result wsAckResult) {
		if result.err == nil {
			t.Fatal("expected ack error, got nil")
		}
		if result.err.Error() != ackErr.Error() {
			t.Fatalf("unexpected error: %v", result.err)
		}
	})
}

func TestWriteAndWaitAck_Timeout(t *testing.T) {
	p := &WSPlatform{}

	reqID := "send_timeout"
	ch := make(chan wsAckResult, 1)
	p.pendingAcks.Store(reqID, ch)

	// Nobody sends ack → should timeout
	start := time.Now()
	select {
	case <-ch:
		t.Fatal("should not receive from channel without ack")
	case <-time.After(100 * time.Millisecond):
		// Expected: timed out without blocking forever
	}
	elapsed := time.Since(start)
	if elapsed > 1*time.Second {
		t.Fatalf("timeout took too long: %v", elapsed)
	}

	// Clean up
	p.pendingAcks.Delete(reqID)
}

func TestWriteAndWaitAck_ContextCancelled(t *testing.T) {
	p := &WSPlatform{}

	reqID := "send_cancel"
	ch := make(chan wsAckResult, 1)
	p.pendingAcks.Store(reqID, ch)

	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	select {
	case <-ch:
		t.Fatal("should not receive ack")
	case <-ctx.Done():
		// Expected: context cancelled
	case <-time.After(1 * time.Second):
		t.Fatal("timed out")
	}

	p.pendingAcks.Delete(reqID)
}

// ---------------------------------------------------------------------------
// handleFrame — ACK dispatch
// ---------------------------------------------------------------------------

func TestHandleFrame_AckDispatch(t *testing.T) {
	p := &WSPlatform{}

	reqID := "aibot_send_msg_1"
	ch := make(chan wsAckResult, 1)
	p.pendingAcks.Store(reqID, ch)

	errCode := 0
	frame := wsFrame{
		Cmd:     "",
		Headers: wsFrameHeaders{ReqID: reqID},
		ErrCode: &errCode,
		ErrMsg:  "ok",
	}

	p.handleFrame(frame)

	assertAckResult(t, ch, func(result wsAckResult) {
		if result.err != nil {
			t.Fatalf("expected nil error for successful ack, got %v", result.err)
		}
	})
}

func TestHandleFrame_AckDispatch_WithError(t *testing.T) {
	p := &WSPlatform{}

	reqID := "aibot_send_msg_2"
	ch := make(chan wsAckResult, 1)
	p.pendingAcks.Store(reqID, ch)

	errCode := 40001
	frame := wsFrame{
		Cmd:     "",
		Headers: wsFrameHeaders{ReqID: reqID},
		ErrCode: &errCode,
		ErrMsg:  "invalid token",
	}

	p.handleFrame(frame)

	assertAckResult(t, ch, func(result wsAckResult) {
		if result.err == nil {
			t.Fatal("expected error for failed ack, got nil")
		}
	})
}

func assertAckResult(t *testing.T, ch <-chan wsAckResult, check func(wsAckResult)) {
	t.Helper()

	select {
	case result := <-ch:
		check(result)
	case <-time.After(100 * time.Millisecond):
		t.Fatal("ack not dispatched")
	}
}

func TestHandleFrame_PingAck_ResetsMissedPong(t *testing.T) {
	p := &WSPlatform{}
	p.missedPong.Store(2)

	frame := wsFrame{
		Cmd:     "",
		Headers: wsFrameHeaders{ReqID: "ping_1"},
	}

	p.handleFrame(frame)

	if p.missedPong.Load() != 0 {
		t.Fatalf("expected missedPong to be reset to 0, got %d", p.missedPong.Load())
	}
}

// ---------------------------------------------------------------------------
// generateReqID
// ---------------------------------------------------------------------------

func TestGenerateReqID_Monotonic(t *testing.T) {
	p := &WSPlatform{}

	ids := make(map[string]bool)
	for i := 0; i < 100; i++ {
		id := p.generateReqID("test")
		if ids[id] {
			t.Fatalf("duplicate req_id: %s", id)
		}
		ids[id] = true
	}
}

func TestGenerateReqID_Format(t *testing.T) {
	p := &WSPlatform{}
	id := p.generateReqID("ping")
	if id != "ping_1" {
		t.Fatalf("expected ping_1, got %s", id)
	}
	id2 := p.generateReqID("aibot_send_msg")
	if id2 != "aibot_send_msg_2" {
		t.Fatalf("expected aibot_send_msg_2, got %s", id2)
	}
}

// ---------------------------------------------------------------------------
// SendImage
// ---------------------------------------------------------------------------

func TestWSPlatformSendImage_UploadsAndSendsMedia(t *testing.T) {
	imageData := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 1, 2, 3}
	serverDone := make(chan error, 1)
	upgrader := websocket.Upgrader{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			serverDone <- err
			return
		}
		defer conn.Close()
		serverDone <- assertWeComWSSendImageFrames(conn, imageData)
	}))
	defer server.Close()

	wsURL := "ws" + server.URL[len("http"):]
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial test websocket: %v", err)
	}
	defer conn.Close()

	p := &WSPlatform{conn: conn}
	go func() {
		for {
			var frame wsFrame
			if err := conn.ReadJSON(&frame); err != nil {
				return
			}
			p.handleFrame(frame)
		}
	}()

	err = p.SendImage(context.Background(), wsReplyContext{chatID: "chat1", userID: "u1"}, core.ImageAttachment{
		MimeType: "image/png",
		Data:     imageData,
		FileName: "chart.png",
	})
	if err != nil {
		t.Fatalf("SendImage returned error: %v", err)
	}

	select {
	case err := <-serverDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("server did not observe all expected frames")
	}
}

func assertWeComWSSendImageFrames(conn *websocket.Conn, imageData []byte) error {
	var initFrame struct {
		Cmd     string         `json:"cmd"`
		Headers wsFrameHeaders `json:"headers"`
		Body    struct {
			Type        string `json:"type"`
			Filename    string `json:"filename"`
			TotalSize   int    `json:"total_size"`
			TotalChunks int    `json:"total_chunks"`
			MD5         string `json:"md5"`
		} `json:"body"`
	}
	if err := conn.ReadJSON(&initFrame); err != nil {
		return fmt.Errorf("read init frame: %w", err)
	}
	sum := md5.Sum(imageData)
	if initFrame.Cmd != "aibot_upload_media_init" ||
		initFrame.Body.Type != "image" ||
		initFrame.Body.Filename != "chart.png" ||
		initFrame.Body.TotalSize != len(imageData) ||
		initFrame.Body.TotalChunks != 1 ||
		initFrame.Body.MD5 != hex.EncodeToString(sum[:]) {
		return fmt.Errorf("unexpected init frame: %#v", initFrame)
	}
	if err := conn.WriteJSON(map[string]any{
		"headers": initFrame.Headers,
		"errcode": 0,
		"errmsg":  "ok",
		"body":    map[string]string{"upload_id": "upload-1"},
	}); err != nil {
		return fmt.Errorf("write init ack: %w", err)
	}

	var chunkFrame struct {
		Cmd     string         `json:"cmd"`
		Headers wsFrameHeaders `json:"headers"`
		Body    struct {
			UploadID   string `json:"upload_id"`
			ChunkIndex int    `json:"chunk_index"`
			Base64Data string `json:"base64_data"`
		} `json:"body"`
	}
	if err := conn.ReadJSON(&chunkFrame); err != nil {
		return fmt.Errorf("read chunk frame: %w", err)
	}
	if chunkFrame.Cmd != "aibot_upload_media_chunk" ||
		chunkFrame.Body.UploadID != "upload-1" ||
		chunkFrame.Body.ChunkIndex != 0 ||
		chunkFrame.Body.Base64Data != base64.StdEncoding.EncodeToString(imageData) {
		return fmt.Errorf("unexpected chunk frame: %#v", chunkFrame)
	}
	if err := conn.WriteJSON(map[string]any{
		"headers": chunkFrame.Headers,
		"errcode": 0,
		"errmsg":  "ok",
	}); err != nil {
		return fmt.Errorf("write chunk ack: %w", err)
	}

	var finishFrame struct {
		Cmd     string         `json:"cmd"`
		Headers wsFrameHeaders `json:"headers"`
		Body    struct {
			UploadID string `json:"upload_id"`
		} `json:"body"`
	}
	if err := conn.ReadJSON(&finishFrame); err != nil {
		return fmt.Errorf("read finish frame: %w", err)
	}
	if finishFrame.Cmd != "aibot_upload_media_finish" || finishFrame.Body.UploadID != "upload-1" {
		return fmt.Errorf("unexpected finish frame: %#v", finishFrame)
	}
	if err := conn.WriteJSON(map[string]any{
		"headers": finishFrame.Headers,
		"errcode": 0,
		"errmsg":  "ok",
		"body":    map[string]string{"media_id": "media-1"},
	}); err != nil {
		return fmt.Errorf("write finish ack: %w", err)
	}

	var sendFrame struct {
		Cmd     string         `json:"cmd"`
		Headers wsFrameHeaders `json:"headers"`
		Body    struct {
			ChatID  string `json:"chatid"`
			MsgType string `json:"msgtype"`
			Image   struct {
				MediaID string `json:"media_id"`
			} `json:"image"`
		} `json:"body"`
	}
	if err := conn.ReadJSON(&sendFrame); err != nil {
		return fmt.Errorf("read send frame: %w", err)
	}
	if sendFrame.Cmd != "aibot_send_msg" ||
		sendFrame.Body.ChatID != "chat1" ||
		sendFrame.Body.MsgType != "image" ||
		sendFrame.Body.Image.MediaID != "media-1" {
		return fmt.Errorf("unexpected send frame: %#v", sendFrame)
	}
	if err := conn.WriteJSON(map[string]any{
		"headers": sendFrame.Headers,
		"errcode": 0,
		"errmsg":  "ok",
	}); err != nil {
		return fmt.Errorf("write send ack: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// SendFile
// ---------------------------------------------------------------------------

func TestWSPlatformSendFile_UploadsAndSendsMedia(t *testing.T) {
	fileData := []byte("<html><body>hello</body></html>")
	serverDone := make(chan error, 1)
	upgrader := websocket.Upgrader{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			serverDone <- err
			return
		}
		defer func() { _ = conn.Close() }()
		serverDone <- assertWeComWSSendFileFrames(conn, fileData)
	}))
	defer server.Close()

	wsURL := "ws" + server.URL[len("http"):]
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial test websocket: %v", err)
	}
	defer func() { _ = conn.Close() }()

	p := &WSPlatform{conn: conn}
	go func() {
		for {
			var frame wsFrame
			if err := conn.ReadJSON(&frame); err != nil {
				return
			}
			p.handleFrame(frame)
		}
	}()

	err = p.SendFile(context.Background(), wsReplyContext{chatID: "chat1", userID: "u1"}, core.FileAttachment{
		MimeType: "text/html",
		Data:     fileData,
		FileName: "hello.html",
	})
	if err != nil {
		t.Fatalf("SendFile returned error: %v", err)
	}

	select {
	case err := <-serverDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("server did not observe all expected frames")
	}
}

func assertWeComWSSendFileFrames(conn *websocket.Conn, fileData []byte) error {
	var initFrame struct {
		Cmd     string         `json:"cmd"`
		Headers wsFrameHeaders `json:"headers"`
		Body    struct {
			Type        string `json:"type"`
			Filename    string `json:"filename"`
			TotalSize   int    `json:"total_size"`
			TotalChunks int    `json:"total_chunks"`
			MD5         string `json:"md5"`
		} `json:"body"`
	}
	if err := conn.ReadJSON(&initFrame); err != nil {
		return fmt.Errorf("read init frame: %w", err)
	}
	sum := md5.Sum(fileData)
	if initFrame.Cmd != "aibot_upload_media_init" ||
		initFrame.Body.Type != "file" ||
		initFrame.Body.Filename != "hello.html" ||
		initFrame.Body.TotalSize != len(fileData) ||
		initFrame.Body.TotalChunks != 1 ||
		initFrame.Body.MD5 != hex.EncodeToString(sum[:]) {
		return fmt.Errorf("unexpected init frame: %#v", initFrame)
	}
	if err := conn.WriteJSON(map[string]any{
		"headers": initFrame.Headers,
		"errcode": 0,
		"errmsg":  "ok",
		"body":    map[string]string{"upload_id": "upload-f1"},
	}); err != nil {
		return fmt.Errorf("write init ack: %w", err)
	}

	var chunkFrame struct {
		Cmd     string         `json:"cmd"`
		Headers wsFrameHeaders `json:"headers"`
		Body    struct {
			UploadID   string `json:"upload_id"`
			ChunkIndex int    `json:"chunk_index"`
			Base64Data string `json:"base64_data"`
		} `json:"body"`
	}
	if err := conn.ReadJSON(&chunkFrame); err != nil {
		return fmt.Errorf("read chunk frame: %w", err)
	}
	if chunkFrame.Cmd != "aibot_upload_media_chunk" ||
		chunkFrame.Body.UploadID != "upload-f1" ||
		chunkFrame.Body.ChunkIndex != 0 ||
		chunkFrame.Body.Base64Data != base64.StdEncoding.EncodeToString(fileData) {
		return fmt.Errorf("unexpected chunk frame: %#v", chunkFrame)
	}
	if err := conn.WriteJSON(map[string]any{
		"headers": chunkFrame.Headers,
		"errcode": 0,
		"errmsg":  "ok",
	}); err != nil {
		return fmt.Errorf("write chunk ack: %w", err)
	}

	var finishFrame struct {
		Cmd     string         `json:"cmd"`
		Headers wsFrameHeaders `json:"headers"`
		Body    struct {
			UploadID string `json:"upload_id"`
		} `json:"body"`
	}
	if err := conn.ReadJSON(&finishFrame); err != nil {
		return fmt.Errorf("read finish frame: %w", err)
	}
	if finishFrame.Cmd != "aibot_upload_media_finish" || finishFrame.Body.UploadID != "upload-f1" {
		return fmt.Errorf("unexpected finish frame: %#v", finishFrame)
	}
	if err := conn.WriteJSON(map[string]any{
		"headers": finishFrame.Headers,
		"errcode": 0,
		"errmsg":  "ok",
		"body":    map[string]string{"media_id": "media-f1"},
	}); err != nil {
		return fmt.Errorf("write finish ack: %w", err)
	}

	var sendFrame struct {
		Cmd     string         `json:"cmd"`
		Headers wsFrameHeaders `json:"headers"`
		Body    struct {
			ChatID  string `json:"chatid"`
			MsgType string `json:"msgtype"`
			File    struct {
				MediaID string `json:"media_id"`
			} `json:"file"`
		} `json:"body"`
	}
	if err := conn.ReadJSON(&sendFrame); err != nil {
		return fmt.Errorf("read send frame: %w", err)
	}
	if sendFrame.Cmd != "aibot_send_msg" ||
		sendFrame.Body.ChatID != "chat1" ||
		sendFrame.Body.MsgType != "file" ||
		sendFrame.Body.File.MediaID != "media-f1" {
		return fmt.Errorf("unexpected send frame: %#v", sendFrame)
	}
	if err := conn.WriteJSON(map[string]any{
		"headers": sendFrame.Headers,
		"errcode": 0,
		"errmsg":  "ok",
	}); err != nil {
		return fmt.Errorf("write send ack: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// generateReqID — concurrency safety
// ---------------------------------------------------------------------------

func TestGenerateReqID_ConcurrentSafety(t *testing.T) {
	p := &WSPlatform{}

	var wg sync.WaitGroup
	ids := sync.Map{}
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id := p.generateReqID("concurrent")
			if _, loaded := ids.LoadOrStore(id, true); loaded {
				t.Errorf("duplicate req_id: %s", id)
			}
		}()
	}
	wg.Wait()
}

// ---------------------------------------------------------------------------
// newWebSocket
// ---------------------------------------------------------------------------

func TestNewWebSocket_MissingCredentials(t *testing.T) {
	tests := []struct {
		name string
		opts map[string]any
	}{
		{"empty opts", map[string]any{}},
		{"missing bot_secret", map[string]any{"bot_id": "aib123"}},
		{"missing bot_id", map[string]any{"bot_secret": "secret"}},
		{"both empty strings", map[string]any{"bot_id": "", "bot_secret": ""}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := newWebSocket(tt.opts)
			if err == nil {
				t.Fatal("expected error for missing credentials")
			}
		})
	}
}

func TestNewWebSocket_ValidConfig(t *testing.T) {
	p, err := newWebSocket(map[string]any{
		"bot_id":     "aibTest",
		"bot_secret": "secretXYZ",
		"allow_from": "user1,user2",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ws := p.(*WSPlatform)
	if ws.botID != "aibTest" || ws.secret != "secretXYZ" || ws.allowFrom != "user1,user2" {
		t.Fatalf("unexpected config: botID=%s secret=%s allowFrom=%s", ws.botID, ws.secret, ws.allowFrom)
	}
}
