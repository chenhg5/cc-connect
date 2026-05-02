package wecom

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
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

func TestHandleMsgCallback_SingleChat_ChatIDFallback(t *testing.T) {
	p := &WSPlatform{
		allowFrom: "*",
	}

	captured := make(chan *core.Message, 1)
	p.handler = func(_ core.Platform, msg *core.Message) {
		captured <- msg
	}

	body := wsMsgCallbackBody{
		MsgID:    "msg_001",
		ChatID:   "", // single chat: no chatID from server
		ChatType: "single",
		MsgType:  "text",
	}
	body.From.UserID = "zhangsan"
	body.Text.Content = "hello"
	body.CreateTime = time.Now().Unix()

	bodyBytes, _ := json.Marshal(body)
	frame := wsFrame{
		Cmd:     "aibot_msg_callback",
		Headers: wsFrameHeaders{ReqID: "req_123"},
		Body:    bodyBytes,
	}

	p.handleMsgCallback(frame)

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
	p := &WSPlatform{
		allowFrom: "*",
	}

	captured := make(chan *core.Message, 1)
	p.handler = func(_ core.Platform, msg *core.Message) {
		captured <- msg
	}

	body := wsMsgCallbackBody{
		MsgID:    "msg_002",
		ChatID:   "group_chat_id_123",
		ChatType: "group",
		MsgType:  "text",
	}
	body.From.UserID = "zhangsan"
	body.Text.Content = "hi group"
	body.CreateTime = time.Now().Unix()

	bodyBytes, _ := json.Marshal(body)
	frame := wsFrame{
		Cmd:     "aibot_msg_callback",
		Headers: wsFrameHeaders{ReqID: "req_456"},
		Body:    bodyBytes,
	}

	p.handleMsgCallback(frame)

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

func TestHandleMsgCallback_StripsBotMention(t *testing.T) {
	p := &WSPlatform{
		allowFrom: "*",
		botID:     "robot01",
	}

	captured := make(chan *core.Message, 1)
	p.handler = func(_ core.Platform, msg *core.Message) {
		captured <- msg
	}

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

	bodyBytes, _ := json.Marshal(body)
	frame := wsFrame{
		Cmd:     "aibot_msg_callback",
		Headers: wsFrameHeaders{ReqID: "req_m"},
		Body:    bodyBytes,
	}

	p.handleMsgCallback(frame)

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

// pendingAcks now stores chan *wsFrame (not chan error) so callers that need
// to read the ack body (e.g. uploadMedia parsing upload_id / media_id) can.
// These tests exercise the channel mechanics directly.

func TestWriteAndWaitAck_SuccessfulAck(t *testing.T) {
	p := &WSPlatform{}

	reqID := "send_1"
	ch := make(chan *wsFrame, 1)
	p.pendingAcks.Store(reqID, ch)

	go func() {
		time.Sleep(10 * time.Millisecond)
		if v, ok := p.pendingAcks.LoadAndDelete(reqID); ok {
			ec := 0
			v.(chan *wsFrame) <- &wsFrame{ErrCode: &ec, ErrMsg: "ok"}
		}
	}()

	ctx := context.Background()
	select {
	case f := <-ch:
		if f == nil {
			t.Fatal("expected non-nil frame on success")
		}
		if f.ErrCode == nil || *f.ErrCode != 0 {
			t.Fatalf("expected errcode=0, got frame=%+v", f)
		}
	case <-ctx.Done():
		t.Fatal("context cancelled unexpectedly")
	case <-time.After(1 * time.Second):
		t.Fatal("timed out waiting for ack")
	}
}

func TestWriteAndWaitAck_AckWithError(t *testing.T) {
	p := &WSPlatform{}

	reqID := "send_2"
	ch := make(chan *wsFrame, 1)
	p.pendingAcks.Store(reqID, ch)

	go func() {
		time.Sleep(10 * time.Millisecond)
		if v, ok := p.pendingAcks.LoadAndDelete(reqID); ok {
			ec := 40001
			v.(chan *wsFrame) <- &wsFrame{ErrCode: &ec, ErrMsg: "invalid token"}
		}
	}()

	select {
	case f := <-ch:
		if f == nil || f.ErrCode == nil || *f.ErrCode == 0 {
			t.Fatalf("expected error frame, got %+v", f)
		}
		if f.ErrMsg != "invalid token" {
			t.Fatalf("unexpected errmsg: %q", f.ErrMsg)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("timed out waiting for ack")
	}
}

func TestWriteAndWaitAck_Timeout(t *testing.T) {
	p := &WSPlatform{}

	reqID := "send_timeout"
	ch := make(chan *wsFrame, 1)
	p.pendingAcks.Store(reqID, ch)

	start := time.Now()
	select {
	case <-ch:
		t.Fatal("should not receive from channel without ack")
	case <-time.After(100 * time.Millisecond):
		// Expected
	}
	if elapsed := time.Since(start); elapsed > 1*time.Second {
		t.Fatalf("timeout took too long: %v", elapsed)
	}
	p.pendingAcks.Delete(reqID)
}

func TestWriteAndWaitAck_ContextCancelled(t *testing.T) {
	p := &WSPlatform{}

	reqID := "send_cancel"
	ch := make(chan *wsFrame, 1)
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
		// Expected
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
	ch := make(chan *wsFrame, 1)
	p.pendingAcks.Store(reqID, ch)

	errCode := 0
	frame := wsFrame{
		Cmd:     "",
		Headers: wsFrameHeaders{ReqID: reqID},
		ErrCode: &errCode,
		ErrMsg:  "ok",
	}

	p.handleFrame(frame)

	select {
	case f := <-ch:
		if f == nil || f.ErrCode == nil || *f.ErrCode != 0 {
			t.Fatalf("expected ok frame, got %+v", f)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("ack not dispatched")
	}
}

func TestHandleFrame_AckDispatch_WithError(t *testing.T) {
	p := &WSPlatform{}

	reqID := "aibot_send_msg_2"
	ch := make(chan *wsFrame, 1)
	p.pendingAcks.Store(reqID, ch)

	errCode := 40001
	frame := wsFrame{
		Cmd:     "",
		Headers: wsFrameHeaders{ReqID: reqID},
		ErrCode: &errCode,
		ErrMsg:  "invalid token",
	}

	p.handleFrame(frame)

	select {
	case f := <-ch:
		if f == nil || f.ErrCode == nil || *f.ErrCode == 0 {
			t.Fatalf("expected error frame, got %+v", f)
		}
		if f.ErrMsg != "invalid token" {
			t.Fatalf("expected errmsg 'invalid token', got %q", f.ErrMsg)
		}
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
	// Format is "<prefix>_<unix_ms>_<seq>_<rand_hex>" per the WeCom SDK
	// pattern: bare per-process sequence is NOT enough — after a restart the
	// IDs would collide with stream IDs the server already committed and the
	// WeChat Work client silently drops the frames.
	p := &WSPlatform{}
	id1 := p.generateReqID("ping")
	if !strings.HasPrefix(id1, "ping_") {
		t.Fatalf("expected prefix 'ping_', got %s", id1)
	}
	id2 := p.generateReqID("ping")
	if id1 == id2 {
		t.Fatalf("expected unique ids, got duplicate %s", id1)
	}
	parts := strings.SplitN(id1, "_", 4)
	if len(parts) != 4 {
		t.Fatalf("expected 4 parts (prefix_ms_seq_rand), got %d in %q", len(parts), id1)
	}
	if parts[0] != "ping" {
		t.Fatalf("expected prefix 'ping', got %q in %s", parts[0], id1)
	}
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

// ---------------------------------------------------------------------------
// Streaming preview interface contract: SendPreviewStart / UpdateMessage /
// FinalizeStream / KeepPreviewOnFinish (cmd: aibot_respond_msg, msgtype: stream)
// ---------------------------------------------------------------------------

func TestSendPreviewStart_RejectsWrongCtxType(t *testing.T) {
	p := &WSPlatform{}
	_, err := p.SendPreviewStart(context.Background(), "not-a-wsReplyContext", "hi")
	if err == nil {
		t.Fatal("expected error for wrong reply context type")
	}
}

func TestSendPreviewStart_RejectsEmptyReqID(t *testing.T) {
	// Without reqID we cannot anchor an aibot_respond_msg stream to the
	// original user message; caller is expected to fall back to Send().
	p := &WSPlatform{}
	rctx := wsReplyContext{chatID: "c1", userID: "u1"} // reqID intentionally empty
	_, err := p.SendPreviewStart(context.Background(), rctx, "hi")
	if err == nil {
		t.Fatal("expected error when reqID is empty")
	}
}

func TestUpdateMessage_RejectsWrongHandleType(t *testing.T) {
	p := &WSPlatform{}
	err := p.UpdateMessage(context.Background(), "not-a-handle", "hi")
	if err == nil {
		t.Fatal("expected error for wrong handle type")
	}
}

func TestFinalizeStream_RejectsWrongHandleType(t *testing.T) {
	p := &WSPlatform{}
	err := p.FinalizeStream(context.Background(), 42, "final")
	if err == nil {
		t.Fatal("expected error for wrong handle type")
	}
}

func TestKeepPreviewOnFinish_True(t *testing.T) {
	// The stream preview message IS the final delivered message — finish()
	// must call FinalizeStream rather than delete the preview.
	p := &WSPlatform{}
	if !p.KeepPreviewOnFinish() {
		t.Fatal("KeepPreviewOnFinish must be true so finish() reaches FinalizeStream")
	}
}

// Compile-time assertions: confirm WSPlatform satisfies all preview-related
// optional interfaces. If any is dropped accidentally these will fail to build.
var (
	_ core.Platform                = (*WSPlatform)(nil)
	_ core.MessageUpdater          = (*WSPlatform)(nil)
	_ core.PreviewStarter          = (*WSPlatform)(nil)
	_ core.StreamFinalizer         = (*WSPlatform)(nil)
	_ core.PreviewFinishPreference = (*WSPlatform)(nil)
	_ core.TypingIndicator         = (*WSPlatform)(nil)
	_ core.ImageSender             = (*WSPlatform)(nil)
	_ core.FileSender              = (*WSPlatform)(nil)
)

// ---------------------------------------------------------------------------
// chunkBytes — binary-safe slicing for media upload
// ---------------------------------------------------------------------------

func TestChunkBytes_ExactBoundary(t *testing.T) {
	in := bytes.Repeat([]byte{0xAB}, 256)
	chunks := chunkBytes(in, 256)
	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk at exact boundary, got %d", len(chunks))
	}
	if !bytes.Equal(chunks[0], in) {
		t.Fatalf("chunk content mismatch")
	}
}

func TestChunkBytes_Splits(t *testing.T) {
	in := bytes.Repeat([]byte{0xCD}, 600)
	chunks := chunkBytes(in, 256)
	if len(chunks) != 3 {
		t.Fatalf("expected 3 chunks (256+256+88), got %d", len(chunks))
	}
	if len(chunks[0]) != 256 || len(chunks[1]) != 256 || len(chunks[2]) != 88 {
		t.Fatalf("unexpected chunk sizes: %d %d %d", len(chunks[0]), len(chunks[1]), len(chunks[2]))
	}
	// Reassemble must equal input — must NOT be UTF-8 aware (binary-safe).
	var reassembled []byte
	for _, c := range chunks {
		reassembled = append(reassembled, c...)
	}
	if !bytes.Equal(reassembled, in) {
		t.Fatalf("reassembled bytes differ from input")
	}
}

func TestChunkBytes_Empty(t *testing.T) {
	chunks := chunkBytes(nil, 256)
	if len(chunks) != 0 {
		t.Fatalf("expected 0 chunks for empty input, got %d", len(chunks))
	}
}

// ---------------------------------------------------------------------------
// SendImage / SendFile — context type validation (full upload requires a
// live ws connection and is exercised by integration testing).
// ---------------------------------------------------------------------------

func TestSendImage_RejectsWrongCtxType(t *testing.T) {
	p := &WSPlatform{}
	err := p.SendImage(context.Background(), "not-a-wsReplyContext", core.ImageAttachment{Data: []byte("x")})
	if err == nil {
		t.Fatal("expected error for wrong reply context type")
	}
}

func TestSendFile_RejectsWrongCtxType(t *testing.T) {
	p := &WSPlatform{}
	err := p.SendFile(context.Background(), 42, core.FileAttachment{Data: []byte("x")})
	if err == nil {
		t.Fatal("expected error for wrong reply context type")
	}
}

func TestImageExtFromMime(t *testing.T) {
	cases := []struct{ mime, want string }{
		{"image/png", ".png"},
		{"IMAGE/PNG", ".png"},
		{"image/jpeg", ".jpg"},
		{"image/jpg", ".jpg"},
		{"image/gif", ".gif"},
		{"image/webp", ".webp"},
		{"", ".jpg"},
		{"application/octet-stream", ".jpg"},
	}
	for _, c := range cases {
		if got := imageExtFromMime(c.mime); got != c.want {
			t.Errorf("imageExtFromMime(%q) = %q, want %q", c.mime, got, c.want)
		}
	}
}

// ---------------------------------------------------------------------------
// TypingIndicator: placeholder stream + hand-off to SendPreviewStart
// ---------------------------------------------------------------------------

func TestStartTyping_RejectsWrongCtxType(t *testing.T) {
	p := &WSPlatform{}
	stop := p.StartTyping(context.Background(), "not-a-wsReplyContext")
	if stop == nil {
		t.Fatal("StartTyping must always return a non-nil stop func")
	}
	stop() // no-op stop should not panic
}

func TestStartTyping_NoOpWhenReqIDMissing(t *testing.T) {
	// Without reqID we can't anchor an aibot_respond_msg stream — should
	// silently no-op rather than fail the user turn.
	p := &WSPlatform{}
	stop := p.StartTyping(context.Background(), wsReplyContext{chatID: "c1", userID: "u1"})
	if stop == nil {
		t.Fatal("StartTyping must always return a non-nil stop func")
	}
	stop()
	count := 0
	p.streamStates.Range(func(_, _ any) bool { count++; return true })
	if count != 0 {
		t.Fatalf("expected no typing handles registered, got %d", count)
	}
}
