package wecom

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/gorilla/websocket"
)

type capturedStreamFrame struct {
	Cmd     string         `json:"cmd"`
	Headers wsFrameHeaders `json:"headers"`
	Body    struct {
		MsgType string `json:"msgtype"`
		Stream  struct {
			ID      string `json:"id"`
			Content string `json:"content"`
			Finish  bool   `json:"finish"`
		} `json:"stream"`
	} `json:"body"`
}

// A real local WS exchange exercises request correlation and the production
// ACK dispatcher, rather than treating a successful socket write as delivery.
func streamTestPlatform(t *testing.T, errCode int) (*WSPlatform, func() []capturedStreamFrame) {
	t.Helper()
	var mu sync.Mutex
	var frames []capturedStreamFrame
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			var frame capturedStreamFrame
			if err := conn.ReadJSON(&frame); err != nil {
				return
			}
			mu.Lock()
			frames = append(frames, frame)
			mu.Unlock()
			if err := conn.WriteJSON(map[string]any{"headers": frame.Headers, "errcode": errCode, "errmsg": "test ack"}); err != nil {
				return
			}
		}
	}))
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	p := &WSPlatform{conn: conn}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var frame wsFrame
			if json.Unmarshal(data, &frame) == nil {
				p.handleFrame(frame)
			}
		}
	}()
	t.Cleanup(func() { conn.Close(); <-done; server.Close() })
	return p, func() []capturedStreamFrame {
		mu.Lock()
		defer mu.Unlock()
		return append([]capturedStreamFrame(nil), frames...)
	}
}

func TestWeComStream_DMAndGroupReplaceSameMessageAndFinish(t *testing.T) {
	for _, chatType := range []string{"single", "group"} {
		t.Run(chatType, func(t *testing.T) {
			p, frames := streamTestPlatform(t, 0)
			card, err := p.CreateStreamingCard(context.Background(), wsReplyContext{reqID: "callback-" + chatType, chatID: "chat", chatType: chatType})
			if err != nil {
				t.Fatal(err)
			}
			c := card.(*wsStreamingCard)
			c.interval = 0
			for _, text := range []string{"**你", "**你好**\n1. 中文"} {
				if err := c.Update(context.Background(), text); err != nil {
					t.Fatal(err)
				}
			}
			if err := c.Finalize(context.Background(), "**你好**\n1. 中文"); err != nil {
				t.Fatal(err)
			}
			if err := c.Close(context.Background()); err != nil {
				t.Fatal(err)
			}
			if err := c.Update(context.Background(), "must not send after finish"); err != nil {
				t.Fatal(err)
			}
			got := frames()
			if len(got) != 3 {
				t.Fatalf("frames=%d, want 2 updates and one final", len(got))
			}
			for i, f := range got {
				if f.Cmd != "aibot_respond_msg" || f.Headers.ReqID != "callback-"+chatType || f.Body.MsgType != "stream" || f.Body.Stream.ID != got[0].Body.Stream.ID || f.Body.Stream.ID == "" || f.Body.Stream.Finish != (i == 2) {
					t.Fatalf("invalid frame %d: %+v", i, f)
				}
			}
			if got[2].Body.Stream.Content != "**你好**\n1. 中文" {
				t.Fatal("final text changed")
			}
		})
	}
}

func TestWeComStream_ThrottleAndInterruptionClose(t *testing.T) {
	p, frames := streamTestPlatform(t, 0)
	card, _ := p.CreateStreamingCard(context.Background(), wsReplyContext{reqID: "callback"})
	c := card.(*wsStreamingCard)
	c.interval = time.Hour
	if err := c.Update(context.Background(), "partial"); err != nil {
		t.Fatal(err)
	}
	if err := c.Update(context.Background(), "coalesced"); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := frames()
	if len(got) != 2 || !got[1].Body.Stream.Finish || got[1].Body.Stream.Content != "partial" {
		t.Fatalf("frames=%+v", got)
	}
}

func TestWeComStream_RejectsACKAndProactiveContext(t *testing.T) {
	p, frames := streamTestPlatform(t, 40008)
	card, _ := p.CreateStreamingCard(context.Background(), wsReplyContext{reqID: "callback"})
	if err := card.Update(context.Background(), "hello"); err == nil || !card.Failed() {
		t.Fatal("rejected ACK must fail the stream")
	}
	if len(frames()) != 1 {
		t.Fatal("unexpected retry")
	}
	if proactive, err := p.CreateStreamingCard(context.Background(), wsReplyContext{chatID: "chat"}); err != nil || proactive != nil {
		t.Fatal("proactive message must use ordinary Send")
	}
	p.disableStreaming = true
	if disabled, err := p.CreateStreamingCard(context.Background(), wsReplyContext{reqID: "callback"}); err != nil || disabled != nil {
		t.Fatal("disabled stream created")
	}
}

func TestWeComStream_UTF8OverflowClosesBeforeFallback(t *testing.T) {
	p, frames := streamTestPlatform(t, 0)
	card, _ := p.CreateStreamingCard(context.Background(), wsReplyContext{reqID: "callback"})
	text := strings.Repeat("中文🙂", 4000)
	if err := card.Update(context.Background(), text); err != nil {
		t.Fatal(err)
	}
	if err := card.Finalize(context.Background(), text); err == nil {
		t.Fatal("oversize answer must request fallback")
	}
	got := frames()
	if len(got) != 2 || !got[1].Body.Stream.Finish {
		t.Fatalf("frames=%d", len(got))
	}
	for _, f := range got {
		if !utf8.ValidString(f.Body.Stream.Content) || len(f.Body.Stream.Content) > wsStreamMaxBytes {
			t.Fatal("invalid stream byte limit")
		}
	}
}

func TestWeComStream_SilentTurnSendsNothing(t *testing.T) {
	p, frames := streamTestPlatform(t, 0)
	card, _ := p.CreateStreamingCard(context.Background(), wsReplyContext{reqID: "callback"})
	if err := card.Finalize(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	if len(frames()) != 0 {
		t.Fatal("silent turn posted a message")
	}
}
