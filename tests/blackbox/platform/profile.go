package platform

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// Profile names a capability set. cc-connect decides how to render a turn
// (plain text, inline buttons, cards, streaming previews) by asking which
// optional interfaces a platform implements, so the same scenario can behave
// differently per profile. Real platforms fall into a handful of these
// profiles; testing the profiles covers the platform×agent interaction logic
// in core without one test per real platform.
type Profile string

const (
	// ProfileLegacy is the historical MockPlatform: cards, inline buttons,
	// images and files, but no streaming preview. It is the default so
	// existing blackbox tests keep their behaviour.
	ProfileLegacy Profile = "legacy"
	// ProfileText only implements core.Platform (Reply/Send): buttons and
	// cards degrade to text. Matches platforms such as LINE.
	ProfileText Profile = "text"
	// ProfileButtons adds inline buttons. Matches platforms such as QQ Bot.
	ProfileButtons Profile = "buttons"
	// ProfileStreaming adds preview start/update/delete without buttons or
	// cards. Matches platforms such as Slack and Matrix.
	ProfileStreaming Profile = "streaming"
	// ProfileCard adds cards with navigation plus inline buttons.
	ProfileCard Profile = "card"
	// ProfileFull adds every capability: cards, buttons, streaming preview,
	// images and files. Matches Feishu/Telegram-class platforms.
	ProfileFull Profile = "full"
)

// Profiles lists every profile, for table-driven tests.
var Profiles = []Profile{ProfileLegacy, ProfileText, ProfileButtons, ProfileStreaming, ProfileCard, ProfileFull}

// ParseProfile maps a name (case-insensitive, empty = legacy) to a Profile.
func ParseProfile(name string) (Profile, error) {
	switch p := Profile(strings.ToLower(strings.TrimSpace(name))); p {
	case "":
		return ProfileLegacy, nil
	case ProfileLegacy, ProfileText, ProfileButtons, ProfileStreaming, ProfileCard, ProfileFull:
		return p, nil
	default:
		return "", fmt.Errorf("unknown blackbox profile %q (want one of %v)", name, Profiles)
	}
}

// BridgeCapabilities returns the bridge protocol capability list that makes a
// cc-connect process treat a bridge adapter like this profile.
func (pr Profile) BridgeCapabilities() []string {
	switch pr {
	case ProfileText:
		return []string{"text"}
	case ProfileButtons:
		return []string{"text", "buttons"}
	case ProfileStreaming:
		return []string{"text", "update_message", "preview", "delete_message"}
	case ProfileCard:
		return []string{"text", "card", "buttons"}
	case ProfileFull:
		return []string{"text", "card", "buttons", "update_message", "preview", "delete_message", "image", "file", "typing"}
	default: // legacy
		return []string{"text", "card", "buttons", "image", "file"}
	}
}

// Platform returns the core.Platform value to hand to core.NewEngine for this
// profile. It exposes only the optional interfaces of the profile, while every
// call is still recorded on p.
func (p *MockPlatform) Platform(pr Profile) core.Platform {
	base := textPlatform{m: p}
	switch pr {
	case ProfileText:
		return &base
	case ProfileButtons:
		return &buttonsPlatform{textPlatform: base}
	case ProfileStreaming:
		return &streamingPlatform{textPlatform: base}
	case ProfileCard:
		return &cardPlatform{buttonsPlatform: buttonsPlatform{textPlatform: base}}
	case ProfileFull:
		return &fullPlatform{cardPlatform: cardPlatform{buttonsPlatform: buttonsPlatform{textPlatform: base}}}
	default:
		return p
	}
}

// ── Capability wrappers ──────────────────────────────────────────────────────
//
// Each wrapper is a distinct type so that core's type assertions
// (p.(core.CardSender), p.(core.PreviewStarter), ...) see exactly the methods
// of its profile. Wrappers hold the MockPlatform in an unexported field, so
// MockPlatform's own optional methods are not promoted.

type textPlatform struct{ m *MockPlatform }

func (w *textPlatform) Name() string                      { return w.m.Name() }
func (w *textPlatform) Start(h core.MessageHandler) error { return w.m.startAs(w, h) }
func (w *textPlatform) Stop() error                       { return w.m.Stop() }
func (w *textPlatform) Reply(ctx context.Context, rctx any, content string) error {
	return w.m.Reply(ctx, rctx, content)
}
func (w *textPlatform) Send(ctx context.Context, rctx any, content string) error {
	return w.m.Send(ctx, rctx, content)
}

type buttonsPlatform struct{ textPlatform }

func (w *buttonsPlatform) Start(h core.MessageHandler) error { return w.m.startAs(w, h) }
func (w *buttonsPlatform) SendWithButtons(ctx context.Context, rctx any, content string, buttons [][]core.ButtonOption) error {
	return w.m.SendWithButtons(ctx, rctx, content, buttons)
}

type streamingPlatform struct{ textPlatform }

func (w *streamingPlatform) Start(h core.MessageHandler) error { return w.m.startAs(w, h) }
func (w *streamingPlatform) SendPreviewStart(ctx context.Context, rctx any, content string) (any, error) {
	return w.m.sendPreviewStart(rctx, content), nil
}
func (w *streamingPlatform) UpdateMessage(ctx context.Context, handle any, content string) error {
	return w.m.updatePreview(handle, content)
}
func (w *streamingPlatform) DeletePreviewMessage(ctx context.Context, handle any) error {
	return w.m.deletePreview(handle)
}

type cardPlatform struct{ buttonsPlatform }

func (w *cardPlatform) Start(h core.MessageHandler) error { return w.m.startAs(w, h) }
func (w *cardPlatform) SendCard(ctx context.Context, rctx any, card *core.Card) error {
	return w.m.SendCard(ctx, rctx, card)
}
func (w *cardPlatform) ReplyCard(ctx context.Context, rctx any, card *core.Card) error {
	return w.m.ReplyCard(ctx, rctx, card)
}
func (w *cardPlatform) SetCardNavigationHandler(h core.CardNavigationHandler) {
	w.m.SetCardNavigationHandler(h)
}

type fullPlatform struct{ cardPlatform }

func (w *fullPlatform) Start(h core.MessageHandler) error { return w.m.startAs(w, h) }
func (w *fullPlatform) SendPreviewStart(ctx context.Context, rctx any, content string) (any, error) {
	return w.m.sendPreviewStart(rctx, content), nil
}
func (w *fullPlatform) UpdateMessage(ctx context.Context, handle any, content string) error {
	return w.m.updatePreview(handle, content)
}
func (w *fullPlatform) DeletePreviewMessage(ctx context.Context, handle any) error {
	return w.m.deletePreview(handle)
}
func (w *fullPlatform) SendImage(ctx context.Context, rctx any, img core.ImageAttachment) error {
	return w.m.SendImage(ctx, rctx, img)
}
func (w *fullPlatform) SendFile(ctx context.Context, rctx any, file core.FileAttachment) error {
	return w.m.SendFile(ctx, rctx, file)
}

// SetCardNavigationHandler records the engine's handler for nav:/act: clicks.
// It is reachable through the card and full profiles only.
func (p *MockPlatform) SetCardNavigationHandler(h core.CardNavigationHandler) {
	p.mu.Lock()
	p.navHandler = h
	p.mu.Unlock()
}

// ── Streaming preview ────────────────────────────────────────────────────────

type previewHandle struct{ id string }

func (p *MockPlatform) sendPreviewStart(rctx any, content string) any {
	p.mu.Lock()
	p.previewSeq++
	id := fmt.Sprintf("preview-%d", p.previewSeq)
	p.mu.Unlock()
	p.record(&SentMessage{Kind: KindPreview, Content: content, ReplyCtx: rctx, Handle: id, At: time.Now()})
	return &previewHandle{id: id}
}

func (p *MockPlatform) updatePreview(handle any, content string) error {
	h, ok := handle.(*previewHandle)
	if !ok {
		return fmt.Errorf("blackbox mock: invalid preview handle %T", handle)
	}
	return p.UpdatePreview(h.id, content)
}

func (p *MockPlatform) deletePreview(handle any) error {
	h, ok := handle.(*previewHandle)
	if !ok {
		return fmt.Errorf("blackbox mock: invalid preview handle %T", handle)
	}
	return p.DeletePreview(h.id)
}

// UpdatePreview replaces the content of the preview with the given handle.
// The record is replaced, not mutated, so readers never race with it.
func (p *MockPlatform) UpdatePreview(handleID, content string) error {
	return p.replacePreview(handleID, func(m *SentMessage) {
		m.Content = content
		m.Updates++
	})
}

// DeletePreview marks the preview with the given handle as deleted.
func (p *MockPlatform) DeletePreview(handleID string) error {
	return p.replacePreview(handleID, func(m *SentMessage) { m.Deleted = true })
}

func (p *MockPlatform) replacePreview(handleID string, change func(*SentMessage)) error {
	p.mu.Lock()
	idx := -1
	for i := len(p.sent) - 1; i >= 0; i-- {
		if p.sent[i].Kind == KindPreview && p.sent[i].Handle == handleID {
			idx = i
			break
		}
	}
	if idx < 0 {
		p.mu.Unlock()
		return fmt.Errorf("blackbox mock: unknown preview %q", handleID)
	}
	updated := *p.sent[idx]
	change(&updated)
	p.sent[idx] = &updated
	p.mu.Unlock()
	p.signal()
	return nil
}

// ── Button clicks ────────────────────────────────────────────────────────────

// InjectCardAction simulates userID clicking a button whose callback value is
// action, in chatID. It follows the same contract as the bridge adapter
// (core/bridge.go handleCardAction):
//
//   - perm:allow / perm:deny / perm:allow_all → a permission response
//   - askq:… → forwarded as a message with the raw value
//   - cmd:/x → forwarded as the message "/x"
//   - nav:… / act:… → the engine's card navigation handler; the card it
//     returns is recorded (as a card, or as text without card support)
func (p *MockPlatform) InjectCardAction(userID, chatID, action string) error {
	sessionKey := p.SessionKeyFor(userID, chatID)
	if r := p.getRemote(); r != nil {
		return r.SendCardAction(sessionKey, action)
	}

	rctx := &MockReplyCtx{Platform: p.name, ChatID: chatID, UserID: userID}
	msg := &core.Message{
		Platform:   p.name,
		SessionKey: sessionKey,
		MessageID:  fmt.Sprintf("click_%d_%s", time.Now().UnixNano(), userID),
		UserID:     userID,
		UserName:   "test-user-" + userID,
		ChatName:   chatID,
		ReplyCtx:   rctx,
	}
	switch {
	case strings.HasPrefix(action, "perm:"):
		switch action {
		case "perm:allow":
			msg.Content = "allow"
		case "perm:deny":
			msg.Content = "deny"
		case "perm:allow_all":
			msg.Content = "allow all"
		default:
			return fmt.Errorf("blackbox mock: unknown permission action %q", action)
		}
		msg.IsPermissionResponse = true
	case strings.HasPrefix(action, "askq:"):
		msg.Content = action
	case strings.HasPrefix(action, "cmd:"):
		msg.Content = strings.TrimPrefix(action, "cmd:")
	default:
		p.mu.Lock()
		nav := p.navHandler
		p.mu.Unlock()
		if nav == nil {
			return fmt.Errorf("blackbox mock: no card navigation handler for %q (profile without cards?)", action)
		}
		card := nav(action, sessionKey)
		if card == nil {
			return nil
		}
		p.record(&SentMessage{Kind: KindCard, Content: card.RenderText(), Card: card, ReplyCtx: rctx, At: time.Now()})
		return nil
	}
	go p.handler(p.selfPlatform(), msg)
	return nil
}

// ── helpers ──────────────────────────────────────────────────────────────────

// SessionKeyFor returns the session key cc-connect uses for userID in chatID.
func (p *MockPlatform) SessionKeyFor(userID, chatID string) string {
	return fmt.Sprintf("%s:%s:%s", p.name, chatID, userID)
}

func (p *MockPlatform) selfPlatform() core.Platform {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.self != nil {
		return p.self
	}
	return p
}

func (p *MockPlatform) getRemote() Remote {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.remote
}

func (p *MockPlatform) signal() {
	select {
	case p.notify <- struct{}{}:
	default:
	}
}
