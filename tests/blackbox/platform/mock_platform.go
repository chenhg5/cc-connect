// Package platform provides the MockPlatform used in blackbox tests.
//
// MockPlatform implements core.Platform without connecting to any real IM
// service. Tests inject messages via InjectMessage and verify cc-connect's
// output via WaitForReply / GetSentMessages.
package platform

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// Kinds of outbound message a SentMessage can record.
const (
	KindReply   = "reply"
	KindCard    = "card"
	KindButtons = "buttons"
	KindPreview = "preview"
	KindImage   = "image"
	KindFile    = "file"
)

// SentMessage captures a single outbound call from cc-connect to the platform.
//
// Records are immutable once stored: a preview that cc-connect later edits is
// replaced in the log by a new SentMessage with the updated Content, so a test
// holding an older pointer never races with the update.
type SentMessage struct {
	Kind     string
	Content  string
	Card     *core.Card // non-nil when SendCard/ReplyCard was called in-process
	CardJSON map[string]any
	Buttons  [][]core.ButtonOption
	ReplyCtx any
	At       time.Time

	// Preview fields: set for KindPreview. Handle identifies the preview,
	// Updates counts UpdateMessage calls and Deleted marks a removed preview.
	Handle  string
	Updates int
	Deleted bool
}

// Text returns the displayable text of the message (card fallback if needed).
func (m *SentMessage) Text() string {
	if m.Content != "" {
		return m.Content
	}
	if m.Card != nil {
		return m.Card.RenderText()
	}
	return ""
}

// ButtonValues returns the callback values of every button on the message,
// whether they came from SendWithButtons or from card action elements.
func (m *SentMessage) ButtonValues() []string {
	var out []string
	for _, row := range m.Buttons {
		for _, b := range row {
			out = append(out, b.Data)
		}
	}
	if m.Card != nil {
		for _, elem := range m.Card.Elements {
			switch e := elem.(type) {
			case core.CardActions:
				for _, b := range e.Buttons {
					out = append(out, b.Value)
				}
			case core.CardListItem:
				if e.BtnValue != "" {
					out = append(out, e.BtnValue)
				}
			}
		}
	}
	out = append(out, cardJSONButtonValues(m.CardJSON)...)
	return out
}

// MockReplyCtx is a simple reply context stored in injected messages.
type MockReplyCtx struct {
	Platform string
	ChatID   string
	UserID   string
}

// MockPlatform implements core.Platform for blackbox tests.
// Messages are injected via InjectMessage; outbound calls (Reply, Send,
// SendCard, etc.) are recorded and retrievable for assertions.
type MockPlatform struct {
	mu      sync.Mutex
	name    string
	handler core.MessageHandler
	sent    []*SentMessage
	notify  chan struct{}

	// self is the core.Platform value the engine was started with. It is the
	// MockPlatform itself for the legacy profile, or a capability wrapper (see
	// profile.go). Injected messages are delivered with self so the engine
	// sees exactly the capabilities of the chosen profile.
	self core.Platform

	navHandler core.CardNavigationHandler
	previewSeq int

	// remote, when set, sends injected messages to a cc-connect process over
	// the bridge instead of calling an in-process handler (see bridge.go).
	remote Remote
}

// New creates a named MockPlatform ready for use in tests.
func New(name string) *MockPlatform {
	return &MockPlatform{
		name:   name,
		notify: make(chan struct{}, 1),
	}
}

// ── core.Platform interface ──────────────────────────────────────────────────

func (p *MockPlatform) Name() string { return p.name }

func (p *MockPlatform) Start(handler core.MessageHandler) error {
	return p.startAs(p, handler)
}

func (p *MockPlatform) startAs(self core.Platform, handler core.MessageHandler) error {
	p.mu.Lock()
	p.self = self
	p.mu.Unlock()
	p.handler = handler
	return nil
}

func (p *MockPlatform) Stop() error { return nil }

func (p *MockPlatform) Reply(ctx context.Context, replyCtx any, content string) error {
	p.record(&SentMessage{Kind: KindReply, Content: content, ReplyCtx: replyCtx, At: time.Now()})
	return nil
}

func (p *MockPlatform) Send(ctx context.Context, replyCtx any, content string) error {
	return p.Reply(ctx, replyCtx, content)
}

// ── Optional platform interfaces ─────────────────────────────────────────────

func (p *MockPlatform) SendCard(ctx context.Context, replyCtx any, card *core.Card) error {
	p.record(&SentMessage{Kind: KindCard, Content: card.RenderText(), Card: card, ReplyCtx: replyCtx, At: time.Now()})
	return nil
}

func (p *MockPlatform) ReplyCard(ctx context.Context, replyCtx any, card *core.Card) error {
	return p.SendCard(ctx, replyCtx, card)
}

func (p *MockPlatform) SendWithButtons(ctx context.Context, replyCtx any, content string, buttons [][]core.ButtonOption) error {
	p.record(&SentMessage{Kind: KindButtons, Content: content, Buttons: buttons, ReplyCtx: replyCtx, At: time.Now()})
	return nil
}

func (p *MockPlatform) SendImage(ctx context.Context, replyCtx any, img core.ImageAttachment) error {
	p.record(&SentMessage{Kind: KindImage, Content: "[image]", ReplyCtx: replyCtx, At: time.Now()})
	return nil
}

func (p *MockPlatform) SendFile(ctx context.Context, replyCtx any, file core.FileAttachment) error {
	p.record(&SentMessage{Kind: KindFile, Content: fmt.Sprintf("[file: %s]", file.FileName), ReplyCtx: replyCtx, At: time.Now()})
	return nil
}

// ── Message injection ────────────────────────────────────────────────────────

// InjectMessage simulates a user sending a plain-text message to cc-connect.
// userID and chatID form the session key; each unique (userID, chatID) pair is
// an independent session.
func (p *MockPlatform) InjectMessage(userID, chatID, content string) {
	p.InjectMessageWithAttachments(userID, chatID, content, nil, nil)
}

// InjectMessageWithAttachments simulates a user sending a message with attachments.
func (p *MockPlatform) InjectMessageWithAttachments(
	userID, chatID, content string,
	images []core.ImageAttachment,
	files []core.FileAttachment,
) {
	if r := p.getRemote(); r != nil {
		if err := r.SendMessage(RemoteMessage{
			SessionKey: p.SessionKeyFor(userID, chatID),
			UserID:     userID,
			UserName:   "test-user-" + userID,
			Content:    content,
			Images:     images,
			Files:      files,
		}); err != nil {
			p.record(&SentMessage{Kind: KindReply, Content: "[blackbox: bridge send failed: " + err.Error() + "]", At: time.Now()})
		}
		return
	}
	msg := &core.Message{
		Platform:   p.name,
		SessionKey: p.SessionKeyFor(userID, chatID),
		MessageID:  fmt.Sprintf("msg_%d_%s", time.Now().UnixNano(), userID),
		UserID:     userID,
		UserName:   "test-user-" + userID,
		ChatName:   chatID,
		Content:    content,
		Images:     images,
		Files:      files,
		ReplyCtx:   &MockReplyCtx{Platform: p.name, ChatID: chatID, UserID: userID},
	}
	// Run in goroutine, matching how real platforms deliver messages.
	go p.handler(p.selfPlatform(), msg)
}

// ── Assertion helpers ────────────────────────────────────────────────────────

// WaitForTurnComplete waits until the message stream has been "stable" —
// no new messages — for at least idlePeriod, then returns all messages
// received since startIdx. This is the right way to wait for a full agent
// turn when cc-connect may send multiple messages per turn (thinking indicators,
// progress updates, then the final reply).
//
// A good idlePeriod for real agents is 3-5s; set timeout to your test deadline.
func (p *MockPlatform) WaitForTurnComplete(startIdx int, idlePeriod, timeout time.Duration) []*SentMessage {
	deadline := time.Now().Add(timeout)

	// We must first receive at least one message after startIdx.
	if !func() bool {
		for {
			p.mu.Lock()
			if len(p.sent) > startIdx {
				p.mu.Unlock()
				return true
			}
			p.mu.Unlock()
			remaining := time.Until(deadline)
			if remaining <= 0 {
				return false
			}
			select {
			case <-p.notify:
			case <-time.After(remaining):
			}
		}
	}() {
		return nil
	}

	// Now wait for stability: no new messages for idlePeriod.
	for {
		lastCount := p.MessageCount()
		wait := idlePeriod
		if time.Until(deadline) < wait {
			wait = time.Until(deadline)
		}
		if wait <= 0 {
			break
		}

		select {
		case <-p.notify:
			// New message arrived — reset stability window.
			continue
		case <-time.After(wait):
		}

		// Check if count changed during the wait (notify is non-blocking so
		// we might have missed a signal).
		if p.MessageCount() == lastCount {
			break // stable
		}
	}

	p.mu.Lock()
	out := append([]*SentMessage(nil), p.sent[startIdx:]...)
	p.mu.Unlock()
	return out
}

// WaitForReply waits until at least one new message is sent after startIdx,
// then returns it. Returns nil on timeout.
func (p *MockPlatform) WaitForReply(startIdx int, timeout time.Duration) *SentMessage {
	deadline := time.Now().Add(timeout)
	for {
		p.mu.Lock()
		if len(p.sent) > startIdx {
			msg := p.sent[startIdx]
			p.mu.Unlock()
			return msg
		}
		p.mu.Unlock()

		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil
		}
		// Block until a new message arrives or timeout.
		select {
		case <-p.notify:
		case <-time.After(remaining):
		}
	}
}

// WaitForN waits until at least n messages have been sent in total.
// Returns the current slice when satisfied, or times out.
func (p *MockPlatform) WaitForN(n int, timeout time.Duration) ([]*SentMessage, bool) {
	deadline := time.Now().Add(timeout)
	for {
		p.mu.Lock()
		if len(p.sent) >= n {
			out := append([]*SentMessage(nil), p.sent...)
			p.mu.Unlock()
			return out, true
		}
		p.mu.Unlock()

		remaining := time.Until(deadline)
		if remaining <= 0 {
			return p.GetSentMessages(), false
		}
		select {
		case <-p.notify:
		case <-time.After(remaining):
		}
	}
}

// WaitForMessageContaining waits until any message (starting from startIdx)
// contains the given substring (case-insensitive). Returns the matching message
// or nil on timeout.
func (p *MockPlatform) WaitForMessageContaining(startIdx int, substr string, timeout time.Duration) *SentMessage {
	deadline := time.Now().Add(timeout)
	for {
		p.mu.Lock()
		for i := startIdx; i < len(p.sent); i++ {
			if strings.Contains(strings.ToLower(p.sent[i].Text()), strings.ToLower(substr)) {
				msg := p.sent[i]
				p.mu.Unlock()
				return msg
			}
		}
		p.mu.Unlock()

		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil
		}
		select {
		case <-p.notify:
		case <-time.After(remaining):
		}
	}
}

// MessageCount returns the current number of sent messages.
func (p *MockPlatform) MessageCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.sent)
}

// GetSentMessages returns a snapshot of all sent messages.
func (p *MockPlatform) GetSentMessages() []*SentMessage {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]*SentMessage(nil), p.sent...)
}

// GetLastMessage returns the most recent sent message, or nil if none.
func (p *MockPlatform) GetLastMessage() *SentMessage {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.sent) == 0 {
		return nil
	}
	return p.sent[len(p.sent)-1]
}

// AllText returns all sent message texts joined by newline, useful for
// debugging failed assertions.
func (p *MockPlatform) AllText() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	parts := make([]string, len(p.sent))
	for i, m := range p.sent {
		parts[i] = m.Text()
	}
	return strings.Join(parts, "\n---\n")
}

// Reset clears all recorded messages. Use between sub-scenarios within one
// test where you need a clean slate.
func (p *MockPlatform) Reset() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sent = nil
}

// ── internal ─────────────────────────────────────────────────────────────────

func (p *MockPlatform) record(m *SentMessage) {
	p.mu.Lock()
	p.sent = append(p.sent, m)
	p.mu.Unlock()
	// Non-blocking notify: if a waiter is already queued, it will pick up the
	// message on its next iteration.
	select {
	case p.notify <- struct{}{}:
	default:
	}
}
