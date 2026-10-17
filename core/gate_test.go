package core

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// maomao fork: a sub that is not called only listens (action=context). When it is called next,
// the images of those context messages must reach the agent as images, not as a text note
// (2026-10-17: 鲸鱼娘 was @-ed after two photos and said she could not see them).
func gateCtxImgServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req gateRequest
		json.NewDecoder(r.Body).Decode(&req)
		act := "context"
		if req.Mentioned {
			act = "reply"
		}
		json.NewEncoder(w).Encode(gateResponse{Action: act})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestGateContextImagesReachCalledTurn(t *testing.T) {
	e := newTestEngine()
	e.SetGate(gateCtxImgServer(t).URL, 2000, "reply")
	s := &Session{}
	img := ImageAttachment{MimeType: "image/png", Data: []byte("p1"), FileName: "a.png"}

	a := &Message{SessionKey: "discord:c", UserName: "zuiho", Images: []ImageAttachment{img}}
	e.gate.released[a] = true
	if !e.applyGate(nil, a, s, "k") {
		t.Fatal("image-only context message should be buffered")
	}
	b := &Message{SessionKey: "discord:c", UserName: "zuiho", Content: "鲸鱼你看看", Mentioned: true}
	e.gate.released[b] = true
	if e.applyGate(nil, b, s, "k") {
		t.Fatal("mentioned message should start a turn")
	}
	if len(b.Images) != 1 || string(b.Images[0].Data) != "p1" {
		t.Fatalf("called turn images = %d, want the context image", len(b.Images))
	}
	if !strings.Contains(b.Content, "1 张") {
		t.Fatalf("content does not say which message carried images:\n%s", b.Content)
	}
	// Delivered once: the next turn does not carry it again.
	c := &Message{SessionKey: "discord:c", UserName: "zuiho", Content: "还有呢", Mentioned: true}
	e.gate.released[c] = true
	e.applyGate(nil, c, s, "k")
	if len(c.Images) != 0 {
		t.Fatalf("next turn re-sent %d images", len(c.Images))
	}
}

type imgCaptureSession struct {
	resultAgentSession
	mu     sync.Mutex
	images []ImageAttachment
}

func (s *imgCaptureSession) Send(_ string, _ string, images []ImageAttachment, _ []FileAttachment) error {
	s.mu.Lock()
	s.images = append(s.images, images...)
	s.mu.Unlock()
	return nil
}

// Called while busy: the context images go into the running turn together with the message.
func TestGateContextImagesInjectedMidTurn(t *testing.T) {
	e := newTestEngine()
	e.SetGate(gateCtxImgServer(t).URL, 2000, "reply")
	e.gate.inject = true
	as := &imgCaptureSession{}
	e.interactiveMu.Lock()
	e.interactiveStates["k"] = &interactiveState{agentSession: as}
	e.interactiveMu.Unlock()
	s := &Session{}
	s.TryLock()
	p := e.platforms[0]

	a := &Message{SessionKey: "discord:c", UserName: "zuiho", Images: []ImageAttachment{{Data: []byte("p1")}, {Data: []byte("p2")}}}
	if !e.applyGate(p, a, s, "k") {
		t.Fatal("context message should be buffered")
	}
	own := ImageAttachment{Data: []byte("own")}
	b := &Message{SessionKey: "discord:c", UserName: "zuiho", Content: "看图", Mentioned: true, Images: []ImageAttachment{own}}
	if !e.applyGate(p, b, s, "k") {
		t.Fatal("busy mentioned message should be injected")
	}
	as.mu.Lock()
	defer as.mu.Unlock()
	if len(as.images) != 3 || string(as.images[0].Data) != "p1" || string(as.images[2].Data) != "own" {
		t.Fatalf("injected images = %d", len(as.images))
	}
}
