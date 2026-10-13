package feishu

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/chenhg5/cc-connect/core"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
)

const cardFormSubmitPrefix = "cc_form_submit_"
const cardFormDraftPrefix = "cc_form_draft_"
const cardFormTTL = 30 * time.Minute

type pendingCardForm struct {
	message core.Message
	form    core.CardForm
	expires time.Time
	used    bool
}

func (p *Platform) rememberCardChatType(rc replyContext) {
	if rc.chatID != "" && rc.senderType == "user" && (rc.chatType == "p2p" || rc.chatType == "group") {
		p.cardChatTypes.Store(rc.chatID, rc.chatType)
	}
}

func (p *Platform) rememberForm(msg *core.Message, form core.CardForm) (string, error) {
	rc, ok := msg.ReplyCtx.(replyContext)
	if !ok || rc.chatID == "" || msg.UserID == "" || form.Command == "" {
		return "", fmt.Errorf("invalid form reply context")
	}
	p.formMu.Lock()
	defer p.formMu.Unlock()
	if p.forms == nil {
		p.forms = map[string]*pendingCardForm{}
	}
	for id, old := range p.forms {
		if time.Now().After(old.expires) || (old.message.UserID == msg.UserID && old.message.SessionKey == msg.SessionKey && old.form.Command == form.Command) {
			delete(p.forms, id)
		}
	}
	if len(p.forms) >= 256 {
		oldest := ""
		for id, f := range p.forms {
			if oldest == "" || f.expires.Before(p.forms[oldest].expires) {
				oldest = id
			}
		}
		delete(p.forms, oldest)
	}
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", fmt.Errorf("create form reference: %w", err)
	}
	id := hex.EncodeToString(token[:])
	copy := *msg
	copy.FormValues = nil
	copy.OnAccepted = nil
	form.ID = id
	p.forms[id] = &pendingCardForm{message: copy, form: form, expires: time.Now().Add(cardFormTTL)}
	return id, nil
}

// ReplyForm retains a server-side association with the original user, chat and
// command. The callback contains an opaque reference, never trusted identity data.
func (p *interactivePlatform) ReplyForm(ctx context.Context, msg *core.Message, card *core.Card) error {
	if card == nil || card.Form == nil {
		return fmt.Errorf("missing card form")
	}
	form := *card.Form
	id, err := p.rememberForm(msg, form)
	if err != nil {
		return err
	}
	form.ID = id
	copy := *card
	copy.Form = &form
	if err := p.ReplyCard(ctx, msg.ReplyCtx, &copy); err != nil {
		p.formMu.Lock()
		delete(p.forms, id)
		p.formMu.Unlock()
		return err
	}
	return nil
}

func formToast(typ, text string) *callback.CardActionTriggerResponse {
	return &callback.CardActionTriggerResponse{Toast: &callback.Toast{Type: typ, Content: text}}
}

func (p *Platform) submitCardForm(event *callback.CardActionTriggerEvent, userID, chatID, messageID string) (*callback.CardActionTriggerResponse, error) {
	id := strings.TrimPrefix(event.Event.Action.Name, cardFormSubmitPrefix)
	editing := strings.HasPrefix(event.Event.Action.Name, cardFormDraftPrefix)
	actionID := ""
	if editing {
		id, actionID, _ = strings.Cut(strings.TrimPrefix(event.Event.Action.Name, cardFormDraftPrefix), "_")
	}
	p.formMu.Lock()
	record := p.forms[id]
	language := core.LangEnglish
	if record != nil {
		language = record.form.Language
	}
	i18n := core.NewI18n(language)
	if record == nil || record.used || time.Now().After(record.expires) || record.message.UserID != userID {
		p.formMu.Unlock()
		return formToast("error", i18n.T(core.MsgFormExpired)), nil
	}
	rc, ok := record.message.ReplyCtx.(replyContext)
	if !ok || chatID != rc.chatID {
		p.formMu.Unlock()
		return formToast("error", i18n.T(core.MsgFormExpired)), nil
	}
	command := record.form.Command
	var values map[string][]string
	var err error
	if editing {
		command = formDraftCommand(&record.form, actionID)
		if command == "" {
			p.formMu.Unlock()
			return formToast("error", i18n.T(core.MsgFormExpired)), nil
		}
		values, err = record.form.ValidateDraft(event.Event.Action.FormValue)
	} else {
		values, err = record.form.ValidateValues(event.Event.Action.FormValue)
	}
	if err != nil {
		p.formMu.Unlock()
		return formToast("error", err.Error()), nil
	}
	record.used = true // atomically consume before dispatch, including double clicks
	msg := record.message
	msg.Content, msg.FormValues = command, values
	rc.messageID = messageID
	rc.cardUpdate = p.beginCardUpdate(messageID, command)
	msg.ReplyCtx, msg.MessageID = rc, messageID
	p.formMu.Unlock()
	go p.dispatchCoreMessage(&msg) // regular owner/admin/disabled-command checks still run
	if editing {
		return nil, nil
	}
	return formToast("info", i18n.T(core.MsgFormSubmitted)), nil
}

func formDraftCommand(form *core.CardForm, actionID string) string {
	for _, field := range form.Fields {
		for _, action := range field.Actions {
			if action.ID == actionID {
				return action.Command
			}
		}
	}
	return ""
}

func (p *Platform) invalidateNavigatedForm(event *callback.CardActionTriggerEvent, userID, chatID string) {
	id, _ := event.Event.Action.Value["form_ref"].(string)
	if id == "" {
		return
	}
	p.formMu.Lock()
	defer p.formMu.Unlock()
	if form := p.forms[id]; form != nil && form.message.UserID == userID {
		if rc, ok := form.message.ReplyCtx.(replyContext); ok && rc.chatID == chatID {
			delete(p.forms, id)
		}
	}
}

// Forms share schema 2.0 with every menu/result in the reusable panel.
func renderFormCard(card *core.Card, sessionKey string) map[string]any {
	form := card.Form
	var body []map[string]any
	for _, elem := range card.Elements {
		switch e := elem.(type) {
		case core.CardMarkdown:
			body = append(body, map[string]any{"tag": "markdown", "content": e.Content})
		case core.CardActions:
			// Navigation/auth actions stay outside the form so empty required
			// fields cannot prevent authorization or retrying pending work.
			for _, button := range e.Buttons {
				kind := button.Type
				if kind == "" {
					kind = "default"
				}
				value := map[string]string{"action": button.Value, "form_ref": form.ID, "session_key": sessionKey, "reuse_card": "true"}
				body = append(body, map[string]any{"tag": "button", "text": plainText(button.Text), "type": kind, "behaviors": []map[string]any{{"type": "callback", "value": value}}})
			}
		case core.CardDivider:
			body = append(body, map[string]any{"tag": "hr"})
		case core.CardNote:
			body = append(body, map[string]any{"tag": "markdown", "content": e.Text, "text_size": "notation"})
		}
	}
	var elements []map[string]any
	for _, field := range form.Fields {
		if field.Kind == "display" {
			elements = append(elements, map[string]any{"tag": "markdown", "content": field.Label})
			elements = append(elements, renderFormDraftActions(form, field.Actions)...)
			continue
		}
		component := map[string]any{"name": field.Name, "required": field.Required && !form.HasDraftActions(), "width": "fill"}
		if field.Placeholder != "" {
			component["placeholder"] = plainText(field.Placeholder)
		}
		if field.Kind == "select" || field.Kind == "multiselect" {
			elements = append(elements, map[string]any{"tag": "markdown", "content": field.Label})
			component["tag"] = "select_static"
			var options []map[string]any
			for _, option := range field.Options {
				options = append(options, map[string]any{"text": plainText(option.Text), "value": option.Value})
			}
			component["options"] = options
			if field.Kind == "multiselect" {
				component["tag"] = "multi_select_static"
				if len(field.Defaults) > 0 {
					component["selected_values"] = field.Defaults
				}
			} else if len(field.Defaults) > 0 {
				component["initial_option"] = field.Defaults[0]
			}
		} else {
			component["tag"], component["label"] = "input", plainText(field.Label)
			component["max_length"] = 1000
			if len(field.Defaults) > 0 {
				component["default_value"] = field.Defaults[0]
			}
			if field.Kind == "multiline" {
				component["input_type"], component["rows"] = "multiline_text", 3
			}
		}
		elements = append(elements, component)
		elements = append(elements, renderFormDraftActions(form, field.Actions)...)
	}
	elements = append(elements, map[string]any{"tag": "button", "name": cardFormSubmitPrefix + form.ID,
		"text": plainText(form.SubmitText), "type": "primary", "form_action_type": "submit", "width": "fill"})
	body = append(body, map[string]any{"tag": "form", "name": "creation_form", "elements": elements, "vertical_spacing": "12px"})
	if form.BackAction != "" {
		// Outside the form: Back must work even when required inputs are empty.
		body = append(body, map[string]any{"tag": "hr"}, map[string]any{
			"tag": "button", "text": plainText(core.NewI18n(form.Language).T(core.MsgCardBack)), "type": "default",
			"behaviors": []map[string]any{{"type": "callback", "value": map[string]string{"action": form.BackAction, "form_ref": form.ID, "session_key": sessionKey, "reuse_card": "true"}}},
		})
	}
	result := map[string]any{"schema": "2.0", "config": map[string]any{"update_multi": true, "width_mode": "default", "enable_forward": false},
		"body": map[string]any{"elements": body, "vertical_spacing": "12px"}}
	if card.Header != nil {
		result["header"] = map[string]any{"title": plainText(card.Header.Title), "template": card.Header.Color}
	}
	return result
}

func renderFormDraftActions(form *core.CardForm, actions []core.CardFormAction) []map[string]any {
	var out []map[string]any
	for start := 0; start < len(actions); start += 3 {
		var columns []map[string]any
		for _, action := range actions[start:min(start+3, len(actions))] {
			button := map[string]any{"tag": "button", "name": cardFormDraftPrefix + form.ID + "_" + action.ID, "width": "fill", "text": plainText(action.Text), "type": "default", "form_action_type": "submit"}
			columns = append(columns, map[string]any{"tag": "column", "width": "weighted", "weight": 1, "elements": []map[string]any{button}})
		}
		out = append(out, map[string]any{"tag": "column_set", "flex_mode": "none", "columns": columns})
	}
	return out
}

var _ core.FormCardSender = (*interactivePlatform)(nil)
