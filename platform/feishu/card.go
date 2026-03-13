package feishu

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/chenhg5/cc-connect/core"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

func plainText(content string) map[string]any {
	return map[string]any{"tag": "plain_text", "content": content}
}

// ReplyCard sends a structured card as a reply to the original message.
func (p *interactivePlatform) ReplyCard(ctx context.Context, rctx any, card *core.Card) error {
	rc, ok := rctx.(replyContext)
	if !ok {
		return fmt.Errorf("%s: invalid reply context type %T", p.tag(), rctx)
	}

	cardJSON := renderCard(card, rc.sessionKey)
	resp, err := p.client.Im.Message.Reply(ctx, larkim.NewReplyMessageReqBuilder().
		MessageId(rc.messageID).
		Body(p.buildReplyMessageReqBody(rc, larkim.MsgTypeInteractive, cardJSON)).
		Build())
	if err != nil {
		return fmt.Errorf("%s: reply card api call: %w", p.tag(), err)
	}
	if !resp.Success() {
		return fmt.Errorf("%s: reply card failed code=%d msg=%s", p.tag(), resp.Code, resp.Msg)
	}
	return nil
}

// SendCard sends a structured card as a new message to the chat.
func (p *interactivePlatform) SendCard(ctx context.Context, rctx any, card *core.Card) error {
	rc, ok := rctx.(replyContext)
	if !ok {
		return fmt.Errorf("%s: invalid reply context type %T", p.tag(), rctx)
	}
	if rc.chatID == "" {
		return fmt.Errorf("%s: chatID is empty, cannot send card", p.tag())
	}

	if p.shouldReplyInThread(rc) {
		return p.ReplyCard(ctx, rctx, card)
	}

	cardJSON := renderCard(card, rc.sessionKey)
	resp, err := p.client.Im.Message.Create(ctx, larkim.NewCreateMessageReqBuilder().
		ReceiveIdType(larkim.ReceiveIdTypeChatId).
		Body(larkim.NewCreateMessageReqBodyBuilder().
			ReceiveId(rc.chatID).
			MsgType(larkim.MsgTypeInteractive).
			Content(cardJSON).
			Build()).
		Build())
	if err != nil {
		return fmt.Errorf("%s: send card api call: %w", p.tag(), err)
	}
	if !resp.Success() {
		return fmt.Errorf("%s: send card failed code=%d msg=%s", p.tag(), resp.Code, resp.Msg)
	}
	return nil
}

// renderCardMap converts a core.Card into the Feishu Interactive Card map
// using the v1 format. Used both for message API (via renderCard) and
// callback responses (CardActionTriggerResponse).
func renderCardMap(card *core.Card, sessionKey string) map[string]any {
	result := map[string]any{
		"config": map[string]any{
			"wide_screen_mode": true,
		},
	}
	if card == nil {
		return result
	}

	if card.Header != nil && card.Header.Title != "" {
		color := card.Header.Color
		if color == "" {
			color = "blue"
		}
		result["header"] = map[string]any{
			"title":    plainText(card.Header.Title),
			"template": color,
		}
	}
	if transformed, ok := renderDeleteModeCheckerCard(card, result); ok {
		return transformed
	}

	var elements []map[string]any
	for _, elem := range card.Elements {
		switch e := elem.(type) {
		case core.CardMarkdown:
			elements = append(elements, map[string]any{
				"tag":     "markdown",
				"content": e.Content,
			})
		case core.CardDivider:
			elements = append(elements, map[string]any{
				"tag": "hr",
			})
		case core.CardActions:
			var actions []map[string]any
			for _, btn := range e.Buttons {
				btnType := btn.Type
				if btnType == "" {
					btnType = "default"
				}
				valMap := map[string]string{"action": btn.Value}
				if sessionKey != "" {
					valMap["session_key"] = sessionKey
				}
				for k, v := range btn.Extra {
					valMap[k] = v
				}
				action := map[string]any{
					"tag":   "button",
					"text":  plainText(btn.Text),
					"type":  btnType,
					"value": valMap,
				}
				if e.Layout == core.CardActionLayoutEqualColumns {
					action["width"] = "fill"
				}
				actions = append(actions, action)
			}
			if len(actions) > 0 {
				if e.Layout == core.CardActionLayoutEqualColumns {
					columns := make([]map[string]any, 0, len(actions))
					for _, action := range actions {
						columns = append(columns, map[string]any{
							"tag":              "column",
							"width":            "weighted",
							"weight":           1,
							"vertical_align":   "center",
							"horizontal_align": "center",
							"elements":         []map[string]any{action},
						})
					}
					columnSet := map[string]any{
						"tag":     "column_set",
						"columns": columns,
					}
					if len(actions) == 2 {
						columnSet["flex_mode"] = "bisect"
					}
					elements = append(elements, columnSet)
				} else {
					elements = append(elements, map[string]any{
						"tag":     "action",
						"actions": actions,
					})
				}
			}
		case core.CardListItem:
			btnType := e.BtnType
			if btnType == "" {
				btnType = "default"
			}
			valMap := map[string]string{"action": e.BtnValue}
			if sessionKey != "" {
				valMap["session_key"] = sessionKey
			}
			for k, v := range e.Extra {
				valMap[k] = v
			}
			elements = append(elements, map[string]any{
				"tag":       "column_set",
				"flex_mode": "none",
				"columns": []map[string]any{
					{
						"tag":            "column",
						"width":          "weighted",
						"weight":         5,
						"vertical_align": "center",
						"elements": []map[string]any{
							{
								"tag":     "markdown",
								"content": e.Text,
							},
						},
					},
					{
						"tag":            "column",
						"width":          "auto",
						"vertical_align": "center",
						"elements": []map[string]any{
							{
								"tag":   "button",
								"text":  plainText(e.BtnText),
								"type":  btnType,
								"value": valMap,
							},
						},
					},
				},
			})
		case core.CardSelect:
			var options []map[string]any
			for _, opt := range e.Options {
				options = append(options, map[string]any{
					"text":  plainText(opt.Text),
					"value": opt.Value,
				})
			}
			selectElem := map[string]any{
				"tag":         "select_static",
				"placeholder": plainText(e.Placeholder),
				"options":     options,
			}
			if sessionKey != "" {
				selectElem["value"] = map[string]string{"session_key": sessionKey}
			}
			if e.InitValue != "" {
				selectElem["initial_option"] = e.InitValue
			}
			elements = append(elements, map[string]any{
				"tag":     "action",
				"actions": []map[string]any{selectElem},
			})
		case core.CardNote:
			elements = append(elements, map[string]any{
				"tag":      "note",
				"elements": []map[string]any{plainText(e.Text)},
			})
		}
	}

	if len(elements) == 0 {
		elements = []map[string]any{{"tag": "markdown", "content": " "}}
	}

	result["elements"] = elements
	return result
}

type deleteModeCheckerRow struct {
	id      string
	text    string
	checked bool
}

func renderDeleteModeCheckerCard(card *core.Card, base map[string]any) (map[string]any, bool) {
	if card == nil {
		return nil, false
	}

	rows := make([]deleteModeCheckerRow, 0)
	formRowElements := make([]map[string]any, 0)
	notes := make([]core.CardNote, 0)
	navRows := make([]core.CardActions, 0)
	submitText := ""
	cancelText := ""

	for _, elem := range card.Elements {
		switch e := elem.(type) {
		case core.CardListItem:
			id, selectable, ok := parseDeleteModeListItemAction(e.BtnValue)
			if !ok {
				return nil, false
			}
			text := normalizeDeleteModeCheckerText(e.Text)
			if !selectable {
				formRowElements = append(formRowElements, map[string]any{
					"tag":     "markdown",
					"content": "▶ " + text,
				})
				continue
			}
			row := deleteModeCheckerRow{
				id:      id,
				text:    text,
				checked: strings.Contains(e.Text, "☑"),
			}
			rows = append(rows, row)
			formRowElements = append(formRowElements, map[string]any{
				"tag":     "checker",
				"name":    deleteModeCheckerName(row.id),
				"checked": row.checked,
				"text": map[string]any{
					"tag":     "lark_md",
					"content": row.text,
				},
			})
		case core.CardNote:
			notes = append(notes, e)
		case core.CardActions:
			remaining := make([]core.CardButton, 0, len(e.Buttons))
			for _, btn := range e.Buttons {
				switch btn.Value {
				case "act:/delete-mode confirm":
					submitText = btn.Text
				case "act:/delete-mode cancel":
					cancelText = btn.Text
				default:
					remaining = append(remaining, btn)
				}
			}
			if len(remaining) > 0 {
				navRows = append(navRows, core.CardActions{Buttons: remaining, Layout: e.Layout})
			}
		case core.CardMarkdown, core.CardDivider, core.CardSelect:
			return nil, false
		}
	}

	if len(formRowElements) == 0 || submitText == "" {
		return nil, false
	}

	elements := make([]map[string]any, 0, len(notes)+1+len(navRows))
	for _, n := range notes {
		if n.Text == "" {
			continue
		}
		if n.Tag == "delete-mode-selected-count" {
			continue
		}
		elements = append(elements, map[string]any{
			"tag":      "note",
			"elements": []map[string]any{plainText(n.Text)},
		})
	}
	formElements := append([]map[string]any{}, formRowElements...)

	buttonColumns := []map[string]any{
		{
			"tag":            "column",
			"width":          "auto",
			"vertical_align": "center",
			"elements": []map[string]any{
				{
					"tag":              "button",
					"text":             plainText(submitText),
					"type":             "danger",
					"name":             "delete_mode_submit",
					"form_action_type": "submit",
					"value":            map[string]string{"action": "act:/delete-mode form-submit"},
				},
			},
		},
	}
	if cancelText != "" {
		buttonColumns = append(buttonColumns, map[string]any{
			"tag":            "column",
			"width":          "auto",
			"vertical_align": "center",
			"elements": []map[string]any{
				{
					"tag":   "button",
					"text":  plainText(cancelText),
					"type":  "default",
					"name":  "delete_mode_cancel",
					"value": map[string]string{"action": "act:/delete-mode cancel"},
				},
			},
		})
	}
	formElements = append(formElements, map[string]any{
		"tag":              "column_set",
		"horizontal_align": "left",
		"columns":          buttonColumns,
	})

	elements = append(elements, map[string]any{
		"tag":      "form",
		"name":     "delete_mode_form",
		"elements": formElements,
	})

	for _, row := range navRows {
		actions := make([]map[string]any, 0, len(row.Buttons))
		for _, btn := range row.Buttons {
			btnType := btn.Type
			if btnType == "" {
				btnType = "default"
			}
			valMap := map[string]string{"action": btn.Value}
			for k, v := range btn.Extra {
				valMap[k] = v
			}
			action := map[string]any{
				"tag":   "button",
				"text":  plainText(btn.Text),
				"type":  btnType,
				"value": valMap,
			}
			if row.Layout == core.CardActionLayoutEqualColumns {
				action["width"] = "fill"
			}
			actions = append(actions, action)
		}
		if len(actions) > 0 {
			elements = append(elements, map[string]any{
				"tag":     "action",
				"actions": actions,
			})
		}
	}

	base["elements"] = elements
	return base, true
}

func normalizeDeleteModeCheckerText(text string) string {
	trimmed := strings.TrimSpace(text)
	for _, prefix := range []string{"☑ ▶", "◻ ▶", "▶", "☑", "◻"} {
		if strings.HasPrefix(trimmed, prefix) {
			trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, prefix))
			break
		}
	}
	return trimmed
}

func parseDeleteModeListItemAction(action string) (id string, selectable bool, ok bool) {
	const (
		togglePrefix = "act:/delete-mode toggle "
		noopPrefix   = "act:/delete-mode noop "
	)
	switch {
	case strings.HasPrefix(action, togglePrefix):
		id = strings.TrimSpace(strings.TrimPrefix(action, togglePrefix))
		return id, true, id != ""
	case strings.HasPrefix(action, noopPrefix):
		id = strings.TrimSpace(strings.TrimPrefix(action, noopPrefix))
		return id, false, id != ""
	default:
		return "", false, false
	}
}

// renderCard converts a core.Card into the Feishu Interactive Card JSON string.
func renderCard(card *core.Card, sessionKey string) string {
	b, err := json.Marshal(renderCardMap(card, sessionKey))
	if err != nil {
		slog.Error("feishu: renderCard marshal failed", "error", err)
		return `{"config":{"wide_screen_mode":true},"elements":[]}`
	}
	return string(b)
}
