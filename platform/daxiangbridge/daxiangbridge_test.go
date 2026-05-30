package daxiangbridge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
	"github.com/gorilla/websocket"
)

type previewAgent struct{}

func (a *previewAgent) Name() string { return "preview-agent" }
func (a *previewAgent) StartSession(_ context.Context, _ string) (core.AgentSession, error) {
	return newPreviewAgentSession(), nil
}
func (a *previewAgent) ListSessions(_ context.Context) ([]core.AgentSessionInfo, error) {
	return nil, nil
}
func (a *previewAgent) Stop() error { return nil }

type previewAgentSession struct {
	events chan core.Event
}

func newPreviewAgentSession() *previewAgentSession {
	return &previewAgentSession{events: make(chan core.Event, 4)}
}

func (s *previewAgentSession) Send(_ string, _ []core.ImageAttachment, _ []core.FileAttachment) error {
	s.events <- core.Event{Type: core.EventText, Content: "bridge-ok"}
	s.events <- core.Event{Type: core.EventResult, Content: "", Done: true}
	return nil
}
func (s *previewAgentSession) RespondPermission(_ string, _ core.PermissionResult) error { return nil }
func (s *previewAgentSession) Events() <-chan core.Event                                 { return s.events }
func (s *previewAgentSession) CurrentSessionID() string                                  { return "preview-session" }
func (s *previewAgentSession) Alive() bool                                               { return true }
func (s *previewAgentSession) Close() error                                              { return nil }

func waitForFrameType(t *testing.T, framesCh <-chan BridgeFrame, want map[string]bool, timeout time.Duration) BridgeFrame {
	t.Helper()
	deadline := time.After(timeout)
	var seen []string
	for {
		select {
		case frame := <-framesCh:
			seen = append(seen, frame.Type)
			if want[frame.Type] {
				return frame
			}
		case <-deadline:
			t.Fatalf("timeout waiting for frame, want=%v seen=%v", want, seen)
		}
	}
}

func payloadText(t *testing.T, frame BridgeFrame) string {
	t.Helper()
	var payload AgentReplyPayload
	if err := json.Unmarshal(frame.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	return payload.Text
}

func startTestServer(t *testing.T, afterRegister func(*websocket.Conn), framesCh chan<- BridgeFrame) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		defer conn.Close()

		_, raw, err := conn.ReadMessage()
		if err != nil {
			t.Errorf("read register: %v", err)
			return
		}
		var frame BridgeFrame
		if err := json.Unmarshal(raw, &frame); err != nil {
			t.Errorf("unmarshal register: %v", err)
			return
		}
		if frame.Type != FrameTypeClientRegister {
			t.Errorf("expected register, got %q", frame.Type)
			return
		}

		ack := BridgeFrame{Type: FrameTypeClientRegistered}
		b, _ := json.Marshal(ack)
		if err := conn.WriteMessage(websocket.TextMessage, b); err != nil {
			t.Errorf("write ack: %v", err)
			return
		}

		if afterRegister != nil {
			afterRegister(conn)
		}

		for {
			_, raw, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var outbound BridgeFrame
			if err := json.Unmarshal(raw, &outbound); err != nil {
				t.Errorf("unmarshal outbound: %v", err)
				return
			}
			framesCh <- outbound
		}
	}))
}

var upgrader = websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}

func TestPlatform_ReceivesMessage(t *testing.T) {
	received := make(chan *core.Message, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, _ := upgrader.Upgrade(w, r, nil)
		defer conn.Close()

		_, raw, _ := conn.ReadMessage()
		var frame BridgeFrame
		json.Unmarshal(raw, &frame)
		if frame.Type != FrameTypeClientRegister {
			t.Errorf("expected register, got %q", frame.Type)
		}

		ack := BridgeFrame{Type: FrameTypeClientRegistered}
		b, _ := json.Marshal(ack)
		conn.WriteMessage(websocket.TextMessage, b)

		payload := BridgeEventPayload{
			Platform: "daxiang", ChatType: "private",
			ConversationID: "conv_1", MessageID: "msg_1",
			FromUserID: "u_1", FromUserName: "张三", Text: "hello",
		}
		raw2, _ := json.Marshal(payload)
		event := BridgeFrame{
			Type: FrameTypeBridgeEventMessage, RequestID: "req_1",
			SessionID: "sess_1", Payload: raw2,
		}
		b2, _ := json.Marshal(event)
		conn.WriteMessage(websocket.TextMessage, b2)
		time.Sleep(200 * time.Millisecond)
	}))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	p, err := New(map[string]any{
		"ws_url":        wsURL,
		"client_id":     "test-client",
		"client_secret": "0123456789abcdef0123456789abcdef",
		"bot_id":        int64(123),
	})
	if err != nil {
		t.Fatal(err)
	}
	p.Start(func(_ core.Platform, msg *core.Message) {
		received <- msg
	})
	defer p.Stop()

	select {
	case msg := <-received:
		if msg.Content != "hello" {
			t.Errorf("content: got %q", msg.Content)
		}
		if msg.UserName != "张三" {
			t.Errorf("userName: got %q", msg.UserName)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for message")
	}
}

func TestPlatform_StreamPreviewFinalizeSendsTerminalFrame(t *testing.T) {
	framesCh := make(chan BridgeFrame, 16)
	msgSent := make(chan struct{}, 1)
	srv := startTestServer(t, func(conn *websocket.Conn) {
		payload := BridgeEventPayload{
			Platform:       "daxiang",
			ChatType:       "private",
			ConversationID: "conv_1",
			MessageID:      "msg_1",
			FromUserID:     "u_1",
			FromUserName:   "张三",
			Text:           "hello",
		}
		raw, _ := json.Marshal(payload)
		event := BridgeFrame{
			Type:      FrameTypeBridgeEventMessage,
			RequestID: "req_stream_1",
			SessionID: "sess_stream_1",
			Payload:   raw,
		}
		b, _ := json.Marshal(event)
		if err := conn.WriteMessage(websocket.TextMessage, b); err != nil {
			t.Errorf("write event: %v", err)
			return
		}
		msgSent <- struct{}{}
	}, framesCh)
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	platform, err := New(map[string]any{
		"ws_url":        wsURL,
		"client_id":     "test-client",
		"client_secret": "0123456789abcdef0123456789abcdef",
		"bot_id":        int64(123),
	})
	if err != nil {
		t.Fatal(err)
	}

	agent := &previewAgent{}
	engine := core.NewEngine("test", agent, []core.Platform{platform}, "", core.LangEnglish)
	engine.SetStreamPreviewCfg(core.StreamPreviewCfg{
		Enabled:       true,
		IntervalMs:    0,
		MinDeltaChars: 1,
		MaxChars:      500,
	})

	if err := engine.Start(); err != nil {
		t.Fatal(err)
	}
	defer engine.Stop()

	select {
	case <-msgSent:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for inbound message")
	}

	startFrame := waitForFrameType(t, framesCh, map[string]bool{FrameTypeAgentReplyStart: true}, 2*time.Second)
	if startFrame.RequestID != "req_stream_1" {
		t.Fatalf("start requestID = %q, want req_stream_1", startFrame.RequestID)
	}

	deltaFrame := waitForFrameType(t, framesCh, map[string]bool{FrameTypeAgentReplyDelta: true}, 2*time.Second)
	var deltaPayload AgentDeltaPayload
	if err := json.Unmarshal(deltaFrame.Payload, &deltaPayload); err != nil {
		t.Fatalf("unmarshal delta payload: %v", err)
	}
	if deltaPayload.Delta != "bridge-ok" {
		t.Fatalf("delta text = %q, want bridge-ok", deltaPayload.Delta)
	}

	terminalFrame := waitForFrameType(t, framesCh, map[string]bool{
		FrameTypeAgentReplyFinal: true,
		FrameTypeAgentReplyEnd:   true,
	}, 2*time.Second)
	if got := payloadText(t, terminalFrame); got != "bridge-ok" {
		t.Fatalf("terminal text = %q, want bridge-ok", got)
	}
}

func TestPlatform_UpdateMessageSendsDeltaWithoutTerminalFrame(t *testing.T) {
	framesCh := make(chan BridgeFrame, 16)
	srv := startTestServer(t, nil, framesCh)
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	platform, err := New(map[string]any{
		"ws_url":        wsURL,
		"client_id":     "test-client",
		"client_secret": "0123456789abcdef0123456789abcdef",
		"bot_id":        int64(123),
	})
	if err != nil {
		t.Fatal(err)
	}

	p := platform.(*Platform)
	if err := p.Start(func(_ core.Platform, _ *core.Message) {}); err != nil {
		t.Fatal(err)
	}
	defer p.Stop()

	time.Sleep(100 * time.Millisecond)
	replyCtx := replyContext{requestID: "req_stream_2", sessionID: "sess_stream_2"}
	handle, err := p.SendPreviewStart(context.Background(), replyCtx, "he")
	if err != nil {
		t.Fatalf("SendPreviewStart: %v", err)
	}
	if err := p.UpdateMessage(context.Background(), handle, "hello"); err != nil {
		t.Fatalf("UpdateMessage: %v", err)
	}

	_ = waitForFrameType(t, framesCh, map[string]bool{FrameTypeAgentReplyStart: true}, 2*time.Second)
	firstDelta := waitForFrameType(t, framesCh, map[string]bool{FrameTypeAgentReplyDelta: true}, 2*time.Second)
	var firstPayload AgentDeltaPayload
	if err := json.Unmarshal(firstDelta.Payload, &firstPayload); err != nil {
		t.Fatalf("unmarshal first delta payload: %v", err)
	}
	if firstPayload.Delta != "he" {
		t.Fatalf("first delta text = %q, want he", firstPayload.Delta)
	}

	secondDelta := waitForFrameType(t, framesCh, map[string]bool{FrameTypeAgentReplyDelta: true}, 2*time.Second)
	var secondPayload AgentDeltaPayload
	if err := json.Unmarshal(secondDelta.Payload, &secondPayload); err != nil {
		t.Fatalf("unmarshal second delta payload: %v", err)
	}
	if secondPayload.Delta != "hello" {
		t.Fatalf("second delta text = %q, want hello", secondPayload.Delta)
	}

	select {
	case frame := <-framesCh:
		if frame.Type == FrameTypeAgentReplyFinal || frame.Type == FrameTypeAgentReplyEnd {
			t.Fatalf("unexpected terminal frame %q", frame.Type)
		}
	case <-time.After(200 * time.Millisecond):
	}
}
