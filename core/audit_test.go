package core

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type captureAuditSink struct {
	mu      sync.Mutex
	records []AuditRecord
}

func (s *captureAuditSink) Name() string { return "capture" }

func (s *captureAuditSink) Write(_ context.Context, record *AuditRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records = append(s.records, cloneAuditRecord(*record))
	return nil
}

func (s *captureAuditSink) Close() error { return nil }

func (s *captureAuditSink) recordsSnapshot() []AuditRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]AuditRecord, len(s.records))
	for i, record := range s.records {
		out[i] = cloneAuditRecord(record)
	}
	return out
}

type closedEventsSession struct {
	ch chan Event
}

func newClosedEventsSession() *closedEventsSession {
	ch := make(chan Event)
	close(ch)
	return &closedEventsSession{ch: ch}
}

func (s *closedEventsSession) Send(_ string, _ []ImageAttachment, _ []FileAttachment) error {
	return nil
}
func (s *closedEventsSession) RespondPermission(_ string, _ PermissionResult) error { return nil }
func (s *closedEventsSession) Events() <-chan Event                                 { return s.ch }
func (s *closedEventsSession) CurrentSessionID() string                             { return "closed-session" }
func (s *closedEventsSession) Alive() bool                                          { return true }
func (s *closedEventsSession) Close() error                                         { return nil }

type fixedSessionAgent struct {
	session AgentSession
}

func (a *fixedSessionAgent) Name() string { return "stub" }
func (a *fixedSessionAgent) StartSession(_ context.Context, _ string) (AgentSession, error) {
	return a.session, nil
}
func (a *fixedSessionAgent) ListSessions(_ context.Context) ([]AgentSessionInfo, error) {
	return nil, nil
}
func (a *fixedSessionAgent) Stop() error { return nil }

type auditDeliveryPlatform struct {
	stubPlatformEngine
	replyMeta    AuditReplyMetadata
	sendReceipt  *SendReceipt
	replyReceipt *SendReceipt
	sendErr      error
	replyErr     error
}

func (p *auditDeliveryPlatform) AuditReplyMetadata(_ any) AuditReplyMetadata {
	meta := p.replyMeta
	meta.Extra = CloneAuditExtra(meta.Extra)
	return meta
}

func (p *auditDeliveryPlatform) SendWithReceipt(_ context.Context, _ any, content string) (*SendReceipt, error) {
	p.mu.Lock()
	p.sent = append(p.sent, content)
	p.mu.Unlock()
	if p.sendErr != nil {
		return p.sendReceipt, p.sendErr
	}
	if p.sendReceipt == nil {
		return nil, nil
	}
	receipt := *p.sendReceipt
	receipt.Extra = CloneAuditExtra(p.sendReceipt.Extra)
	return &receipt, nil
}

func (p *auditDeliveryPlatform) ReplyWithReceipt(_ context.Context, _ any, content string) (*SendReceipt, error) {
	p.mu.Lock()
	p.sent = append(p.sent, content)
	p.mu.Unlock()
	if p.replyErr != nil {
		return p.replyReceipt, p.replyErr
	}
	if p.replyReceipt == nil {
		return nil, nil
	}
	receipt := *p.replyReceipt
	receipt.Extra = CloneAuditExtra(p.replyReceipt.Extra)
	return &receipt, nil
}

type auditPreviewPlatform struct {
	stubPlatformEngine
	previewStarted []string
	previewUpdated []string
}

func (p *auditPreviewPlatform) SendPreviewStart(_ context.Context, _ any, content string) (any, error) {
	p.previewStarted = append(p.previewStarted, content)
	return "preview-1", nil
}

func (p *auditPreviewPlatform) UpdateMessage(_ context.Context, _ any, content string) error {
	p.previewUpdated = append(p.previewUpdated, content)
	return nil
}

func (p *auditPreviewPlatform) KeepPreviewOnFinish() bool { return true }

func (p *auditPreviewPlatform) PreviewReceipt(previewHandle any) *SendReceipt {
	id, _ := previewHandle.(string)
	return &SendReceipt{MessageID: id}
}

func (p *auditPreviewPlatform) AuditReplyMetadata(_ any) AuditReplyMetadata {
	return AuditReplyMetadata{
		SessionKey:       "preview-session",
		ReplyToMessageID: "m-preview",
	}
}

func TestEngineHandleMessageAuditsInboundReceived(t *testing.T) {
	sink := &captureAuditSink{}
	auditor := NewAuditor(0, sink)
	p := &stubPlatformEngine{n: "feishu"}
	e := NewEngine("proj", &fixedSessionAgent{session: newClosedEventsSession()}, []Platform{p}, "", LangEnglish)
	e.SetAuditor(auditor)

	e.handleMessage(p, &Message{
		SessionKey:      "feishu:chat:user",
		Platform:        "feishu",
		MessageID:       "m1",
		ParentMessageID: "p1",
		RootMessageID:   "r1",
		ThreadID:        "r1",
		UserID:          "u1",
		UserName:        "Alice",
		ChatName:        "Chat",
		ChannelKey:      "chat",
		Content:         "hello",
		ExtraContent:    "[quoted]",
		AuditExtra:      map[string]any{"chat_id": "oc_123"},
		ReplyCtx:        "ctx",
	})

	records := sink.recordsSnapshot()
	if len(records) == 0 {
		t.Fatal("expected at least one audit record")
	}
	got := records[0]
	if got.Kind != AuditKindInboundReceived {
		t.Fatalf("kind = %q, want %q", got.Kind, AuditKindInboundReceived)
	}
	if got.ContentOriginal != "hello" {
		t.Fatalf("ContentOriginal = %q, want %q", got.ContentOriginal, "hello")
	}
	if got.ExtraContent != "[quoted]" {
		t.Fatalf("ExtraContent = %q, want %q", got.ExtraContent, "[quoted]")
	}
	if got.ContentToAgent != "[quoted]\nhello" {
		t.Fatalf("ContentToAgent = %q, want %q", got.ContentToAgent, "[quoted]\nhello")
	}
	if got.ParentMessageID != "p1" || got.RootMessageID != "r1" || got.ThreadID != "r1" {
		t.Fatalf("unexpected message linkage: parent=%q root=%q thread=%q", got.ParentMessageID, got.RootMessageID, got.ThreadID)
	}
	if got.Extra["chat_id"] != "oc_123" {
		t.Fatalf("chat_id extra = %#v, want %q", got.Extra["chat_id"], "oc_123")
	}
}

func TestReplyWithErrorAuditsOutboundSentWithReceipt(t *testing.T) {
	sink := &captureAuditSink{}
	p := &auditDeliveryPlatform{
		stubPlatformEngine: stubPlatformEngine{n: "feishu"},
		replyMeta: AuditReplyMetadata{
			SessionKey:       "feishu:chat:user",
			ChannelKey:       "chat",
			ReplyToMessageID: "m-in",
			RootMessageID:    "root-1",
			ThreadID:         "root-1",
			Extra:            map[string]any{"chat_id": "oc_123"},
		},
		replyReceipt: &SendReceipt{
			MessageID: "m-out",
		},
	}
	e := NewEngine("proj", &stubAgent{}, []Platform{p}, "", LangEnglish)
	e.SetAuditor(NewAuditor(0, sink))

	if err := e.replyWithError(p, "ctx", "hello back"); err != nil {
		t.Fatalf("replyWithError() error = %v", err)
	}

	records := sink.recordsSnapshot()
	if len(records) != 1 {
		t.Fatalf("record count = %d, want 1", len(records))
	}
	got := records[0]
	if got.Kind != AuditKindOutboundSent {
		t.Fatalf("kind = %q, want %q", got.Kind, AuditKindOutboundSent)
	}
	if got.SessionKey != "feishu:chat:user" {
		t.Fatalf("SessionKey = %q, want %q", got.SessionKey, "feishu:chat:user")
	}
	if got.ReplyToMessageID != "m-in" || got.OutboundMessageID != "m-out" {
		t.Fatalf("unexpected outbound linkage: reply_to=%q outbound=%q", got.ReplyToMessageID, got.OutboundMessageID)
	}
	if got.ContentSent != "hello back" {
		t.Fatalf("ContentSent = %q, want %q", got.ContentSent, "hello back")
	}
	if got.Extra["delivery_method"] != "reply" {
		t.Fatalf("delivery_method = %#v, want %q", got.Extra["delivery_method"], "reply")
	}
}

func TestSendAlreadyRenderedWithErrorAuditsOutboundFailure(t *testing.T) {
	sink := &captureAuditSink{}
	p := &auditDeliveryPlatform{
		stubPlatformEngine: stubPlatformEngine{n: "feishu"},
		replyMeta: AuditReplyMetadata{
			SessionKey:       "feishu:chat:user",
			ReplyToMessageID: "m-in",
		},
		sendErr: errors.New("boom"),
	}
	e := NewEngine("proj", &stubAgent{}, []Platform{p}, "", LangEnglish)
	e.SetAuditor(NewAuditor(0, sink))

	err := e.sendAlreadyRenderedWithError(p, "ctx", "cannot deliver")
	if err == nil {
		t.Fatal("expected sendAlreadyRenderedWithError() to fail")
	}

	records := sink.recordsSnapshot()
	if len(records) != 1 {
		t.Fatalf("record count = %d, want 1", len(records))
	}
	got := records[0]
	if got.Kind != AuditKindOutboundFailed {
		t.Fatalf("kind = %q, want %q", got.Kind, AuditKindOutboundFailed)
	}
	if got.Error != "boom" {
		t.Fatalf("Error = %q, want %q", got.Error, "boom")
	}
}

func TestProcessInteractiveEventsAuditsAgentResultAndPreviewFinalize(t *testing.T) {
	sink := &captureAuditSink{}
	p := &auditPreviewPlatform{stubPlatformEngine: stubPlatformEngine{n: "feishu"}}
	e := NewEngine("proj", &stubAgent{}, []Platform{p}, "", LangEnglish)
	e.SetAuditor(NewAuditor(0, sink))

	sessionKey := "preview-session"
	session := e.sessions.GetOrCreateActive(sessionKey)
	agentSession := &resultAgentSession{events: make(chan Event, 2)}
	agentSession.events <- Event{Type: EventText, Content: "hello "}
	agentSession.events <- Event{Type: EventResult, Content: "hello world", Done: true}

	state := &interactiveState{
		agentSession: agentSession,
		platform:     p,
		replyCtx:     "ctx",
		agent:        e.agent,
	}

	e.processInteractiveEvents(state, session, e.sessions, sessionKey, "m-preview", time.Now(), nil, nil, "ctx")

	records := sink.recordsSnapshot()
	if len(records) < 2 {
		t.Fatalf("record count = %d, want at least 2", len(records))
	}

	var agentRecord, outboundRecord *AuditRecord
	for i := range records {
		record := records[i]
		switch record.Kind {
		case AuditKindAgentResult:
			agentRecord = &record
		case AuditKindOutboundSent:
			if record.OutboundMessageID == "preview-1" {
				outboundRecord = &record
			}
		}
	}
	if agentRecord == nil {
		t.Fatal("expected an agent_result audit record")
	}
	if agentRecord.AgentOutput != "hello world" {
		t.Fatalf("AgentOutput = %q, want %q", agentRecord.AgentOutput, "hello world")
	}
	if outboundRecord == nil {
		t.Fatal("expected an outbound_sent record for the preview handle")
	}
	if outboundRecord.Extra["delivery_method"] != "preview_update" {
		t.Fatalf("delivery_method = %#v, want %q", outboundRecord.Extra["delivery_method"], "preview_update")
	}
}
