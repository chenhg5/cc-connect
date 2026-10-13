package feishu

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// A target belongs to one click on one message, never the latest message in a
// session. Navigating again retires it so a slow page cannot replace a new one.
type cardUpdateTarget struct {
	messageID string
	command   string
	created   time.Time
	active    atomic.Bool
	mu        sync.Mutex
	lastCard  *core.Card
}

func (p *Platform) SetCardLanguageProvider(provider func() core.Language) {
	p.cardLanguage = provider
}

func (p *Platform) cardI18n() *core.I18n {
	if p.cardLanguage != nil {
		return core.NewI18n(p.cardLanguage())
	}
	return core.NewI18n(core.LangEnglish)
}

func (p *Platform) beginCardUpdate(messageID, command string) *cardUpdateTarget {
	if messageID == "" {
		return nil
	}
	p.cardUpdateMu.Lock()
	defer p.cardUpdateMu.Unlock()
	if p.cardUpdates == nil {
		p.cardUpdates = make(map[string]*cardUpdateTarget)
	}
	for id, previous := range p.cardUpdates {
		if id == messageID || time.Since(previous.created) > cardFormTTL {
			previous.active.Store(false)
			delete(p.cardUpdates, id)
		}
	}
	if len(p.cardUpdates) >= 256 {
		var oldest *cardUpdateTarget
		for _, target := range p.cardUpdates {
			if oldest == nil || target.created.Before(oldest.created) {
				oldest = target
			}
		}
		oldest.active.Store(false)
		delete(p.cardUpdates, oldest.messageID)
	}
	target := &cardUpdateTarget{messageID: messageID, command: command, created: time.Now()}
	target.active.Store(true)
	p.cardUpdates[messageID] = target
	return target
}

func (p *Platform) cardWithReturn(card *core.Card, command string) *core.Card {
	copy := *card
	copy.Reusable = true
	if card.Form != nil {
		return &copy // the form renderer supplies its own non-submitting Back button
	}
	for _, row := range card.CollectButtons() {
		for _, button := range row {
			if strings.HasPrefix(button.Data, "nav:/help") || button.Text == p.cardI18n().T(core.MsgCardBack) {
				return &copy
			}
		}
	}
	copy.Elements = append(append([]core.CardElement(nil), card.Elements...), core.CardDivider{}, core.CardActions{Buttons: []core.CardButton{
		core.DefaultBtn(p.cardI18n().T(core.MsgCardBack), core.CardReturnAction(command)),
	}})
	return &copy
}

func renderPanelCardMap(card *core.Card, sessionKey string) map[string]any {
	copy := *card
	copy.Reusable = true
	return renderCardMap(&copy, sessionKey)
}

func (p *interactivePlatform) refreshCardTarget(ctx context.Context, target *cardUpdateTarget, sessionKey string, card *core.Card) error {
	target.mu.Lock()
	defer target.mu.Unlock()
	if !target.active.Load() {
		return nil
	}
	copy := *card
	copy.Reusable = true
	if err := p.patchCardMessage(ctx, target.messageID, renderCard(&copy, sessionKey)); err != nil {
		return err
	}
	target.lastCard = &copy
	return nil
}

// Command replies reached from a card stay on that card. Normal chat messages
// have no target and continue through the original text/reply path.
func (p *Platform) replyCardText(ctx context.Context, rc replyContext, content string) (bool, error) {
	if rc.cardUpdate == nil {
		return false, nil
	}
	if !rc.cardUpdate.active.Load() {
		return true, nil // a newer click owns this message; suppress late replies
	}
	updater, ok := p.self.(*interactivePlatform)
	if !ok {
		return false, nil
	}
	card := core.NewCard().Title("cc-connect", "blue").Markdown(content).Build()
	rc.cardUpdate.mu.Lock()
	if last := rc.cardUpdate.lastCard; last != nil && last.Form != nil {
		// A retry form can be followed by an error from the command handler.
		copy := *last
		copy.Elements = append([]core.CardElement{core.CardMarkdown{Content: content}}, last.Elements...)
		card = &copy
	}
	rc.cardUpdate.mu.Unlock()
	card = p.cardWithReturn(card, rc.cardUpdate.command)
	if err := updater.refreshCardTarget(ctx, rc.cardUpdate, rc.sessionKey, card); err != nil {
		slog.Warn(p.tag()+": card reply update failed; sending message", "error", err)
		return false, nil
	}
	return true, nil
}
