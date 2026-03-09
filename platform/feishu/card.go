package feishu

import (
	"encoding/json"

	"github.com/chenhg5/cc-connect/core"
)

func plainText(content string) map[string]any {
	return map[string]any{"tag": "plain_text", "content": content}
}

// renderCardMap converts a core.Card into the Feishu Interactive Card map
// using the v1 format. Used both for message API (via renderCard) and
// callback responses (CardActionTriggerResponse).
func renderCardMap(card *core.Card) map[string]any {
	result := map[string]any{
		"config": map[string]any{
			"wide_screen_mode": true,
		},
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
			action := map[string]any{
				"tag":   "button",
				"text":  plainText(btn.Text),
				"type":  btnType,
				"value": map[string]string{"action": btn.Value},
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
							"value": map[string]string{"action": e.BtnValue},
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

// renderCard converts a core.Card into the Feishu Interactive Card JSON string.
func renderCard(card *core.Card) string {
	b, _ := json.Marshal(renderCardMap(card))
	return string(b)
}
