package media_pipeline

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

type sendRecord struct {
	prompt string
	images []core.ImageAttachment
	files  []core.FileAttachment
}

type recordingAgent struct {
	session *recordingSession
}

func newRecordingAgent() *recordingAgent {
	return &recordingAgent{session: newRecordingSession()}
}

func (a *recordingAgent) Name() string { return "recording-agent" }

func (a *recordingAgent) StartSession(_ context.Context, sessionID string) (core.AgentSession, error) {
	a.session.setID(sessionID)
	return a.session, nil
}

func (a *recordingAgent) ListSessions(_ context.Context) ([]core.AgentSessionInfo, error) {
	return nil, nil
}

func (a *recordingAgent) Stop() error {
	return a.session.Close()
}

type recordingSession struct {
	mu         sync.Mutex
	id         string
	alive      bool
	records    []sendRecord
	events     chan core.Event
	blockFirst bool
	blocked    bool
}

func newRecordingSession() *recordingSession {
	return &recordingSession{alive: true, events: make(chan core.Event, 16)}
}

func (s *recordingSession) setID(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.id = id
}

func (s *recordingSession) blockFirstResult() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.blockFirst = true
}

func (s *recordingSession) Send(prompt string, images []core.ImageAttachment, files []core.FileAttachment) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.alive {
		return errors.New("session closed")
	}
	rec := sendRecord{
		prompt: prompt,
		images: append([]core.ImageAttachment(nil), images...),
		files:  append([]core.FileAttachment(nil), files...),
	}
	s.records = append(s.records, rec)
	if !(s.blockFirst && len(s.records) == 1) {
		s.events <- core.Event{Type: core.EventResult, Content: "media ok", Done: true}
	} else {
		s.blocked = true
	}
	return nil
}

func (s *recordingSession) Events() <-chan core.Event {
	return s.events
}

func (s *recordingSession) RespondPermission(string, core.PermissionResult) error {
	return nil
}

func (s *recordingSession) CurrentSessionID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.id
}

func (s *recordingSession) Alive() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.alive
}

func (s *recordingSession) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.alive {
		return nil
	}
	s.alive = false
	close(s.events)
	return nil
}

func (s *recordingSession) releaseFirstResult(content string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.blocked {
		return
	}
	s.events <- core.Event{Type: core.EventResult, Content: content, Done: true}
	s.blocked = false
}

func (s *recordingSession) waitRecords(t *testing.T, n int) []sendRecord {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		if len(s.records) >= n {
			out := append([]sendRecord(nil), s.records...)
			s.mu.Unlock()
			return out
		}
		s.mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t.Fatalf("timeout waiting for %d Send calls, got %d: %#v", n, len(s.records), s.records)
	return nil
}

type mediaPlatform struct {
	mu       sync.Mutex
	texts    []string
	images   []core.ImageAttachment
	files    []core.FileAttachment
	replyCtx []any
}

func (p *mediaPlatform) Name() string { return "media" }
func (p *mediaPlatform) Start(core.MessageHandler) error {
	return nil
}
func (p *mediaPlatform) Stop() error { return nil }
func (p *mediaPlatform) Reply(_ context.Context, replyCtx any, content string) error {
	return p.Send(context.Background(), replyCtx, content)
}
func (p *mediaPlatform) Send(_ context.Context, replyCtx any, content string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.texts = append(p.texts, content)
	p.replyCtx = append(p.replyCtx, replyCtx)
	return nil
}
func (p *mediaPlatform) SendImage(_ context.Context, replyCtx any, img core.ImageAttachment) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.images = append(p.images, img)
	p.replyCtx = append(p.replyCtx, replyCtx)
	return nil
}
func (p *mediaPlatform) SendFile(_ context.Context, replyCtx any, file core.FileAttachment) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.files = append(p.files, file)
	p.replyCtx = append(p.replyCtx, replyCtx)
	return nil
}

func (p *mediaPlatform) snapshot() (texts []string, images []core.ImageAttachment, files []core.FileAttachment, replyCtx []any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.texts...),
		append([]core.ImageAttachment(nil), p.images...),
		append([]core.FileAttachment(nil), p.files...),
		append([]any(nil), p.replyCtx...)
}

func (p *mediaPlatform) waitTextContaining(t *testing.T, substr string) string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		texts, _, _, _ := p.snapshot()
		for _, text := range texts {
			if strings.Contains(strings.ToLower(text), strings.ToLower(substr)) {
				return text
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	texts, _, _, _ := p.snapshot()
	t.Fatalf("timeout waiting for text containing %q, got %#v", substr, texts)
	return ""
}

func newMediaEngine(t *testing.T) (*core.Engine, *recordingAgent, *mediaPlatform) {
	t.Helper()
	agent := newRecordingAgent()
	platform := &mediaPlatform{}
	engine := core.NewEngine("release-media", agent, []core.Platform{platform}, t.TempDir()+"/sessions.json", core.LangEnglish)
	t.Cleanup(func() {
		engine.Stop()
		_ = agent.Stop()
	})
	return engine, agent, platform
}

func mediaMessage(content string) *core.Message {
	return &core.Message{
		SessionKey: "media:chat-1:user-1",
		Platform:   "media",
		UserID:     "user-1",
		UserName:   "tester",
		Content:    content,
		ReplyCtx:   "reply-ctx-1",
	}
}

func TestInboundImagesAndFilesReachAgentThroughEngine(t *testing.T) {
	engine, agent, platform := newMediaEngine(t)
	msg := mediaMessage("analyze these attachments")
	msg.Images = []core.ImageAttachment{{MimeType: "image/png", FileName: "screenshot.png", Data: []byte("png-bytes")}}
	msg.Files = []core.FileAttachment{{MimeType: "application/pdf", FileName: "spec.pdf", Data: []byte("%PDF")}}

	engine.ReceiveMessage(platform, msg)

	records := agent.session.waitRecords(t, 1)
	if !strings.Contains(records[0].prompt, "analyze these attachments") {
		t.Fatalf("prompt = %q, want user content", records[0].prompt)
	}
	if len(records[0].images) != 1 || records[0].images[0].FileName != "screenshot.png" || string(records[0].images[0].Data) != "png-bytes" {
		t.Fatalf("images not preserved: %#v", records[0].images)
	}
	if len(records[0].files) != 1 || records[0].files[0].FileName != "spec.pdf" || string(records[0].files[0].Data) != "%PDF" {
		t.Fatalf("files not preserved: %#v", records[0].files)
	}
	platform.waitTextContaining(t, "media ok")
}

func TestAttachmentOnlyMessageReachesAgent(t *testing.T) {
	engine, agent, platform := newMediaEngine(t)
	msg := mediaMessage("")
	msg.Images = []core.ImageAttachment{{MimeType: "image/jpeg", FileName: "photo.jpg", Data: []byte("jpeg-bytes")}}

	engine.ReceiveMessage(platform, msg)

	records := agent.session.waitRecords(t, 1)
	if len(records[0].images) != 1 || records[0].images[0].MimeType != "image/jpeg" {
		t.Fatalf("attachment-only image not delivered: %#v", records[0].images)
	}
	platform.waitTextContaining(t, "media ok")
}

func TestQueuedMessagePreservesFiles(t *testing.T) {
	engine, agent, platform := newMediaEngine(t)
	agent.session.blockFirstResult()

	first := mediaMessage("start long task")
	engine.ReceiveMessage(platform, first)
	agent.session.waitRecords(t, 1)

	queued := mediaMessage("please also inspect this file")
	queued.MessageID = "queued-msg"
	queued.Files = []core.FileAttachment{{MimeType: "text/plain", FileName: "queued.txt", Data: []byte("queued-file")}}
	engine.ReceiveMessage(platform, queued)

	platform.waitTextContaining(t, "process after")
	agent.session.releaseFirstResult("first done")

	records := agent.session.waitRecords(t, 2)
	if !strings.Contains(records[1].prompt, "please also inspect this file") {
		t.Fatalf("queued prompt = %q", records[1].prompt)
	}
	if len(records[1].files) != 1 || records[1].files[0].FileName != "queued.txt" || string(records[1].files[0].Data) != "queued-file" {
		t.Fatalf("queued file not preserved: %#v", records[1].files)
	}
}

func TestSendToSessionWithAttachmentsDeliversTextImagesAndFiles(t *testing.T) {
	engine, agent, platform := newMediaEngine(t)
	msg := mediaMessage("establish active session")
	engine.ReceiveMessage(platform, msg)
	agent.session.waitRecords(t, 1)
	platform.waitTextContaining(t, "media ok")

	err := engine.SendToSessionWithAttachments(
		msg.SessionKey,
		"delivery ready",
		[]core.ImageAttachment{{MimeType: "image/png", FileName: "chart.png", Data: []byte("chart")}},
		[]core.FileAttachment{{MimeType: "text/plain", FileName: "report.txt", Data: []byte("report")}},
	)
	if err != nil {
		t.Fatalf("SendToSessionWithAttachments() error = %v", err)
	}

	texts, images, files, replyCtx := platform.snapshot()
	if !containsText(texts, "delivery ready") {
		t.Fatalf("texts = %#v, want delivery message", texts)
	}
	if len(images) != 1 || images[0].FileName != "chart.png" || string(images[0].Data) != "chart" {
		t.Fatalf("images = %#v", images)
	}
	if len(files) != 1 || files[0].FileName != "report.txt" || string(files[0].Data) != "report" {
		t.Fatalf("files = %#v", files)
	}
	for _, ctx := range replyCtx {
		if ctx != "reply-ctx-1" {
			t.Fatalf("reply context = %#v, want original reply context", replyCtx)
		}
	}
}

func TestSendToSessionWithAttachmentsRespectsDisabledAttachmentSend(t *testing.T) {
	engine, agent, platform := newMediaEngine(t)
	msg := mediaMessage("establish active session")
	engine.ReceiveMessage(platform, msg)
	agent.session.waitRecords(t, 1)
	platform.waitTextContaining(t, "media ok")

	engine.SetAttachmentSendEnabled(false)
	err := engine.SendToSessionWithAttachments(
		msg.SessionKey,
		"should not send",
		nil,
		[]core.FileAttachment{{MimeType: "text/plain", FileName: "blocked.txt", Data: []byte("blocked")}},
	)
	if !errors.Is(err, core.ErrAttachmentSendDisabled) {
		t.Fatalf("err = %v, want ErrAttachmentSendDisabled", err)
	}
	texts, images, files, _ := platform.snapshot()
	if containsText(texts, "should not send") || len(images) != 0 || len(files) != 0 {
		t.Fatalf("disabled attachment send leaked output: texts=%#v images=%#v files=%#v", texts, images, files)
	}
}

func TestSendToSessionWithAttachmentsRequiresSessionWhenMultipleSessionsHaveAttachments(t *testing.T) {
	engine, agent, platform := newMediaEngine(t)
	first := mediaMessage("first")
	first.SessionKey = "media:chat-1:user-1"
	second := mediaMessage("second")
	second.SessionKey = "media:chat-1:user-2"
	second.ReplyCtx = "reply-ctx-2"

	engine.ReceiveMessage(platform, first)
	engine.ReceiveMessage(platform, second)
	agent.session.waitRecords(t, 2)
	platform.waitTextContaining(t, "media ok")

	err := engine.SendToSessionWithAttachments(
		"",
		"ambiguous",
		[]core.ImageAttachment{{MimeType: "image/png", FileName: "ambiguous.png", Data: []byte("img")}},
		nil,
	)
	if err == nil || !strings.Contains(err.Error(), "multiple active sessions") {
		t.Fatalf("err = %v, want multiple active sessions error", err)
	}
	texts, images, files, _ := platform.snapshot()
	if containsText(texts, "ambiguous") || len(images) != 0 || len(files) != 0 {
		t.Fatalf("ambiguous attachment send leaked output: texts=%#v images=%#v files=%#v", texts, images, files)
	}
}

func containsText(texts []string, want string) bool {
	for _, text := range texts {
		if strings.Contains(text, want) {
			return true
		}
	}
	return false
}
