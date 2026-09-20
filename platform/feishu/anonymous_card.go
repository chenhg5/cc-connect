package feishu

import (
	"encoding/json"
	"strings"

	"github.com/chenhg5/cc-connect/core"
)

// BuildAnonymousRichCard keeps activity metadata out of the outgoing payload,
// including collapsed panels and notification summaries. The answer element
// uses the same ID as the existing CardKit streaming transport.
func (p *interactivePlatform) BuildAnonymousRichCard(status core.CardStatus, toolCount int, markdown string, streaming bool, lang core.Language) string {
	i18n := core.NewI18n(lang)
	title, color := i18n.T(core.MsgStarting), "blue"
	active := status == core.CardStatusThinking || status == core.CardStatusWorking
	switch status {
	case core.CardStatusDone:
		title, color = i18n.T(core.MsgAnonymousProgressDone), "green"
	case core.CardStatusError:
		title, color = i18n.T(core.MsgAnonymousProgressError), "red"
	}
	var elements []map[string]any
	if active {
		elements = append(elements, map[string]any{
			"tag": "div",
			"text": map[string]any{
				"tag": "plain_text", "content": i18n.Tf(core.MsgAnonymousProgressTools, max(0, toolCount)),
				"text_size": "notation", "text_color": "grey",
			},
		})
	}
	body := sanitizeCardMarkdownForCard(markdown)
	if strings.TrimSpace(body) == "" {
		body = " " // keep the streaming element addressable before the answer
		if !active {
			body = title
		}
	}
	elements = append(elements, map[string]any{
		"tag": "markdown", "element_id": richCardMainTextElementID, "content": body,
	})
	card := map[string]any{
		"schema": "2.0",
		"config": map[string]any{
			"streaming_mode": streaming && active, "update_multi": true,
			"enable_forward_interaction": true,
			"summary":                    map[string]any{"content": title},
		},
		"header": map[string]any{
			"template": color, "title": map[string]any{"tag": "plain_text", "content": title},
		},
		"body": map[string]any{"elements": elements},
	}
	// All values above are JSON primitives, so marshaling cannot fail.
	b, _ := json.Marshal(card)
	return string(b)
}
