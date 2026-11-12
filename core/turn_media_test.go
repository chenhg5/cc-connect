package core

import (
	"context"
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

	sent := strings.Join(p.getSent(), "\n")
	if !strings.Contains(sent, "text stays") {
		t.Fatalf("sent text = %q", sent)
	}
	if !strings.Contains(sent, "a.png") || !strings.Contains(sent, "a.txt") {
		t.Fatalf("sent text = %q, want a visible fallback for the dropped attachments", sent)
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
	sent := strings.Join(p.getSent(), "\n")
	if !strings.Contains(sent, "words only") || !strings.Contains(sent, "a.png") {
		t.Fatalf("sent text = %q, want the reply and a visible attachment fallback", sent)
	}
}

func TestProcessInteractiveEvents_ImageOnlyOnTextPlatformIsVisible(t *testing.T) {
	p := &stubPlatformEngine{n: "text-only"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	sessionKey := "test:user-image"
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
		Images: []ImageAttachment{{Data: []byte("png"), FileName: "photo.png"}},
		Files:  []FileAttachment{{Data: []byte("pdf"), FileName: "notes.pdf"}},
	}
	e.processInteractiveEvents(state, session, e.sessions, sessionKey, "m1", time.Now(), nil, nil, state.replyCtx, 0)

	sent := strings.Join(p.getSent(), "\n")
	if strings.TrimSpace(sent) == "" || strings.Contains(sent, "(empty response)") {
		t.Fatalf("sent text = %q, want a visible fallback instead of silence", sent)
	}
	if !strings.Contains(sent, "photo.png") || !strings.Contains(sent, "notes.pdf") {
		t.Fatalf("sent text = %q, want both attachment names", sent)
	}
}

type imageOnlyPlatform struct {
	stubPlatformEngine
	images []ImageAttachment
}

func (p *imageOnlyPlatform) SendImage(_ context.Context, _ any, img ImageAttachment) error {
	p.images = append(p.images, img)
	return nil
}

func TestProcessInteractiveEvents_FileOnlyWithoutFileSenderIsVisible(t *testing.T) {
	p := &imageOnlyPlatform{stubPlatformEngine: stubPlatformEngine{n: "images-only"}}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	sessionKey := "test:user-file"
	session := e.sessions.GetOrCreateActive(sessionKey)
	agentSession := newControllableSession("s1")
	state := &interactiveState{
		agentSession: agentSession,
		platform:     p,
		replyCtx:     "ctx-1",
	}
	e.interactiveStates[sessionKey] = state

	agentSession.events <- Event{
		Type:  EventResult,
		Done:  true,
		Files: []FileAttachment{{Data: []byte("bin"), FileName: "report.pdf"}},
	}
	e.processInteractiveEvents(state, session, e.sessions, sessionKey, "m1", time.Now(), nil, nil, state.replyCtx, 0)

	sent := strings.Join(p.getSent(), "\n")
	if !strings.Contains(sent, "report.pdf") {
		t.Fatalf("sent text = %q, want the file name", sent)
	}
	if len(p.images) != 0 {
		t.Fatalf("images = %#v, file must not be sent as an image", p.images)
	}
}
