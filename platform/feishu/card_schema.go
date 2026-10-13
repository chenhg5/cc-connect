package feishu

import "strings"

// Upgrade the freshly rendered legacy layout, preserving columns, forms and
// callback metadata. Only reusable panels take this path. See Feishu's schema
// migration notes: /document/feishu-cards/card-json-v2-breaking-changes-release-notes.
func upgradePanelCard(card map[string]any) map[string]any {
	card["schema"] = "2.0"
	config := card["config"].(map[string]any)
	delete(config, "wide_screen_mode")
	config["width_mode"] = "default"
	config["update_multi"] = true
	config["enable_forward"] = false
	elements, _ := card["elements"].([]map[string]any)
	card["body"] = map[string]any{"elements": upgradePanelElements(elements), "vertical_spacing": "12px"}
	delete(card, "elements")
	return card
}

func upgradePanelElements(elements []map[string]any) []map[string]any {
	for i, element := range elements {
		switch element["tag"] {
		case "action":
			// 2.0 removed action rows. Auto-width columns preserve their layout.
			actions, _ := element["actions"].([]map[string]any)
			columns := make([]map[string]any, 0, len(actions))
			for _, action := range upgradePanelElements(actions) {
				columns = append(columns, map[string]any{"tag": "column", "width": "auto", "elements": []map[string]any{action}})
			}
			elements[i] = map[string]any{"tag": "column_set", "flex_mode": "flow", "columns": columns}
		case "note":
			var content []string
			texts, _ := element["elements"].([]map[string]any)
			for _, text := range texts {
				if value, ok := text["content"].(string); ok {
					content = append(content, value)
				}
			}
			elements[i] = map[string]any{"tag": "markdown", "content": strings.Join(content, "\n"), "text_size": "notation"}
		case "button", "select_static":
			if value, ok := element["value"]; ok {
				element["behaviors"] = []map[string]any{{"type": "callback", "value": value}}
				delete(element, "value")
			}
		default:
			for _, key := range []string{"elements", "columns"} {
				if children, ok := element[key].([]map[string]any); ok {
					element[key] = upgradePanelElements(children)
				}
			}
		}
	}
	return elements
}
