package feishu

import (
	"context"
	"log/slog"
	"time"

	"github.com/chenhg5/cc-connect/core"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
)

var _ core.CardTaskActionHandlerSetter = (*interactivePlatform)(nil)

// SetCardTaskActionHandler installs a separate business-action boundary. Task
// actions must retain the authenticated operator, unlike navigation callbacks.
func (p *interactivePlatform) SetCardTaskActionHandler(h core.CardTaskActionHandler) {
	p.cardTaskActionHandler = h
}

func (p *Platform) onTaskCardAction(event *callback.CardActionTriggerEvent, action string) (*callback.CardActionTriggerResponse, error) {
	lang, _ := event.Event.Action.Value["lang"].(string)
	if lang == "" {
		lang = string(core.LangChinese)
	}
	i18n := core.NewI18n(core.NormalizeLanguageString(lang))
	expired := func() (*callback.CardActionTriggerResponse, error) {
		return &callback.CardActionTriggerResponse{Toast: &callback.Toast{
			Type: "error", Content: i18n.T(core.MsgSteerExpired),
		}}, nil
	}
	operator, callbackContext := event.Event.Operator, event.Event.Context
	if operator == nil || operator.OpenID == "" ||
		callbackContext == nil || callbackContext.OpenChatID == "" || callbackContext.OpenMessageID == "" {
		return expired()
	}
	if !core.AllowList(p.allowFrom, operator.OpenID) || !core.AllowList(p.allowChat, callbackContext.OpenChatID) {
		// A sibling project sharing this app may own the authenticated operator.
		return nil, nil
	}
	if p.cardTaskActionHandler == nil {
		return expired()
	}
	sessionKey := p.sessionKeyFromCardAction(callbackContext.OpenChatID, operator.OpenID, event.Event.Action.Value)
	rctx := replyContext{messageID: callbackContext.OpenMessageID, chatID: callbackContext.OpenChatID, sessionKey: sessionKey}
	request := core.CardTaskAction{
		Action: action, SessionKey: sessionKey, UserID: operator.OpenID,
		ChatID: callbackContext.OpenChatID, MessageID: callbackContext.OpenMessageID, ReplyCtx: rctx,
	}
	// Keep processing alive beyond Feishu's callback deadline; an RPC may already
	// have accepted the steer and must resolve before the queue can be released.
	done := make(chan core.CardTaskActionResult, 1)
	go func() { done <- p.cardTaskActionHandler(context.Background(), request) }()
	timer := time.NewTimer(cardNavTimeout)
	defer timer.Stop()
	select {
	case result := <-done:
		return taskActionResponse(result, sessionKey), nil
	case <-timer.C:
		go func() {
			result := <-done
			if result.Card != nil {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				err := p.patchCardMessage(ctx, request.MessageID, renderCard(result.Card, sessionKey))
				cancel()
				if err == nil {
					return
				}
				slog.Warn(p.tag()+": task action card update failed", "err", err)
			}
			if result.Toast != "" {
				// A failed card update may have exhausted its deadline. Give the
				// final status a separate delivery attempt in the original topic.
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				if err := p.Reply(ctx, rctx, result.Toast); err != nil {
					slog.Warn(p.tag()+": task action reply failed", "err", err)
				}
			}
		}()
		return &callback.CardActionTriggerResponse{Toast: &callback.Toast{
			Type: "info", Content: i18n.T(core.MsgSteerSubmitting),
		}}, nil
	}
}

func taskActionResponse(result core.CardTaskActionResult, sessionKey string) *callback.CardActionTriggerResponse {
	response := &callback.CardActionTriggerResponse{}
	if result.Card != nil {
		response.Card = &callback.Card{Type: "raw", Data: renderCardMap(result.Card, sessionKey)}
	}
	if result.Toast != "" {
		kind := result.ToastType
		if kind != "success" && kind != "error" {
			kind = "info"
		}
		response.Toast = &callback.Toast{Type: kind, Content: result.Toast}
	}
	return response
}
