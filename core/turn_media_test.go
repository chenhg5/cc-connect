package core

import (
	"strings"
	"testing"
	"time"
)

func TestProcessInteractiveEvents_DeliversTurnImages(t *testing.T) {
	p := &stubMediaPlatform{stubPlatformEngine: stubPlatformEngine{n: "weixin"}}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	sessionKey := "test:user1"
	session := e.sessions.GetOrCreateActive(sessionKey)
	agentSession := newControllableSession("s1")
	state := &interactiveState{
		agentSession: agentSession,
		platform:     p,
		replyCtx:     "ctx-1",
	}
	e.interactiveStates[sessionKey] = state

	img := ImageAttachment{MimeType: "image/png", Data: []byte("png-bytes"), FileName: "chart.png"}
	agentSession.events <- Event{
		Type:    EventResult,
		Content: "here is the chart",
		Done:    true,
		Images:  []ImageAttachment{img},
	}
	e.processInteractiveEvents(state, session, e.sessions, sessionKey, "m1", time.Now(), nil, nil, state.replyCtx, 0)

	sent := strings.Join(p.getSent(), "\n")
	if !strings.Contains(sent, "here is the chart") {
		t.Fatalf("sent text = %q, want the reply", sent)
	}
	if strings.Contains(sent, "(empty response)") {
		t.Fatalf("sent text = %q, did not expect empty-response placeholder", sent)
	}
	if len(p.images) != 1 || string(p.images[0].Data) != "png-bytes" || p.images[0].FileName != "chart.png" {
		t.Fatalf("images = %#v", p.images)
	}
}

func TestProcessInteractiveEvents_ImageOnlySkipsEmptyPlaceholder(t *testing.T) {
	p := &stubMediaPlatform{stubPlatformEngine: stubPlatformEngine{n: "feishu"}}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	sessionKey := "test:user1"
	session := e.sessions.GetOrCreateActive(sessionKey)
	agentSession := newControllableSession("s1")
	state := &interactiveState{
		agentSession: agentSession,
		platform:     p,
		replyCtx:     "ctx-1",
	}
	e.interactiveStates[sessionKey] = state

	agentSession.events <- Event{
		Type:   EventResult,
		Done:   true,
		Images: []ImageAttachment{{MimeType: "image/jpeg", Data: []byte("jpg"), FileName: "photo.jpg"}},
	}
	e.processInteractiveEvents(state, session, e.sessions, sessionKey, "m1", time.Now(), nil, nil, state.replyCtx, 0)

	sent := strings.Join(p.getSent(), "\n")
	if strings.Contains(sent, "empty response") || strings.Contains(sent, "空响应") {
		t.Fatalf("image-only reply sent placeholder text: %q", sent)
	}
	if len(p.images) != 1 || p.images[0].FileName != "photo.jpg" {
		t.Fatalf("images = %#v", p.images)
	}
}

func TestProcessInteractiveEvents_TurnMediaRespectsAttachmentSend(t *testing.T) {
	p := &stubMediaPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	e.SetAttachmentSendEnabled(false)
	sessionKey := "test:user1"
	session := e.sessions.GetOrCreateActive(sessionKey)
	agentSession := newControllableSession("s1")
	state := &interactiveState{
		agentSession: agentSession,
		platform:     p,
		replyCtx:     "ctx-1",
	}
	e.interactiveStates[sessionKey] = state

	agentSession.events <- Event{
		Type:    EventResult,
		Content: "text stays",
		Done:    true,
		Images:  []ImageAttachment{{Data: []byte("x"), FileName: "a.png"}},
		Files:   []FileAttachment{{Data: []byte("y"), FileName: "a.txt"}},
	}
	e.processInteractiveEvents(state, session, e.sessions, sessionKey, "m1", time.Now(), nil, nil, state.replyCtx, 0)

	if sent := strings.Join(p.getSent(), "\n"); !strings.Contains(sent, "text stays") {
		t.Fatalf("sent text = %q", sent)
	}
	if len(p.images) != 0 || len(p.files) != 0 {
		t.Fatalf("attachments leaked: images=%#v files=%#v", p.images, p.files)
	}
}

func TestProcessInteractiveEvents_TurnMediaSkipsUnsupportedPlatform(t *testing.T) {
	p := &stubPlatformEngine{n: "text-only"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	sessionKey := "test:user1"
	session := e.sessions.GetOrCreateActive(sessionKey)
	agentSession := newControllableSession("s1")
	state := &interactiveState{
		agentSession: agentSession,
		platform:     p,
		replyCtx:     "ctx-1",
	}
	e.interactiveStates[sessionKey] = state

	agentSession.events <- Event{
		Type:    EventResult,
		Content: "words only",
		Done:    true,
		Images:  []ImageAttachment{{Data: []byte("x"), FileName: "a.png"}},
	}
	e.processInteractiveEvents(state, session, e.sessions, sessionKey, "m1", time.Now(), nil, nil, state.replyCtx, 0)
	if sent := strings.Join(p.getSent(), "\n"); !strings.Contains(sent, "words only") {
		t.Fatalf("sent text = %q", sent)
	}
}
