package cloudweb

import "github.com/chenhg5/cc-connect/core"

func serializeCard(c *core.Card) map[string]any {
	result := make(map[string]any)
	if c.Header != nil {
		result["header"] = map[string]string{
			"title": c.Header.Title,
			"color": c.Header.Color,
		}
	}
	var elements []map[string]any
	for _, elem := range c.Elements {
		switch e := elem.(type) {
		case core.CardMarkdown:
			elements = append(elements, map[string]any{"type": "markdown", "content": e.Content})
		case core.CardDivider:
			elements = append(elements, map[string]any{"type": "divider"})
		case core.CardActions:
			var btns []map[string]any
			for _, b := range e.Buttons {
				btns = append(btns, map[string]any{
					"text": b.Text, "btn_type": b.Type, "value": b.Value,
				})
			}
			elements = append(elements, map[string]any{
				"type": "actions", "buttons": btns, "layout": string(e.Layout),
			})
		case core.CardNote:
			m := map[string]any{"type": "note", "text": e.Text}
			if e.Tag != "" {
				m["tag"] = e.Tag
			}
			elements = append(elements, m)
		case core.CardListItem:
			elements = append(elements, map[string]any{
				"type": "list_item", "text": e.Text,
				"btn_text": e.BtnText, "btn_type": e.BtnType, "btn_value": e.BtnValue,
			})
		case core.CardSelect:
			var opts []map[string]string
			for _, o := range e.Options {
				opts = append(opts, map[string]string{"text": o.Text, "value": o.Value})
			}
			elements = append(elements, map[string]any{
				"type": "select", "placeholder": e.Placeholder,
				"options": opts, "init_value": e.InitValue,
			})
		}
	}
	result["elements"] = elements
	return result
}
