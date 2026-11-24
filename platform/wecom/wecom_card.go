package wecom

import (
	"crypto/rand"
	"fmt"
	"strings"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// buildWeComTemplateCard converts a core.Card into a WeCom template_card payload.
func buildWeComTemplateCard(card *core.Card) (map[string]any, error) {
	if card == nil {
		return nil, fmt.Errorf("wecom: nil card")
	}

	payload := make(map[string]any)

	// Main Title
	if card.Header != nil && card.Header.Title != "" {
		payload["main_title"] = map[string]string{
			"title": card.Header.Title,
		}
	}

	var subTexts []string
	var buttons []map[string]any
	var horizontalList []map[string]string

	for _, elem := range card.Elements {
		switch e := elem.(type) {
		case core.CardMarkdown:
			if strings.TrimSpace(e.Content) != "" {
				subTexts = append(subTexts, e.Content)
			}
		case core.CardNote:
			if strings.TrimSpace(e.Text) != "" {
				subTexts = append(subTexts, e.Text)
			}
		case core.CardListItem:
			item := map[string]string{}
			if e.Text != "" {
				item["keyname"] = e.Text
			}
			if e.BtnText != "" {
				item["value"] = e.BtnText
			}
			if len(item) > 0 {
				horizontalList = append(horizontalList, item)
			}
		case core.CardActions:
			for _, btn := range e.Buttons {
				style := 2 // default
				switch btn.Type {
				case "primary":
					style = 1
				case "danger":
					style = 3
				}
				buttons = append(buttons, map[string]any{
					"text":  btn.Text,
					"style": style,
					"key":   btn.Value,
				})
			}
		}
	}

	if len(subTexts) > 0 {
		payload["sub_title_text"] = strings.Join(subTexts, "\n\n")
	}

	if len(horizontalList) > 0 {
		payload["horizontal_content_list"] = horizontalList
	}

	// Always add card_action default
	payload["card_action"] = map[string]any{
		"type": 0,
	}

	if len(buttons) > 0 {
		payload["card_type"] = "button_interaction"
		payload["button_list"] = buttons
		// WeCom marks task_id required for button_interaction and rejects the
		// message without it (errcode non-zero). On the WebSocket ReplyCard path
		// the frame is fire-and-forget, so the permission prompt simply never
		// reaches the user.
		payload["task_id"] = newCardTaskID()
		// main_title.title is required for button_interaction as well. Cards
		// normally carry a header; this keeps a header-less button card
		// deliverable instead of dropping its prompt.
		if payload["main_title"] == nil {
			payload["main_title"] = map[string]string{"title": defaultButtonCardTitle}
		}
	} else {
		payload["card_type"] = "text_notice"
		// text_notice requires sub_title_text or main_title
		if payload["main_title"] == nil && payload["sub_title_text"] == nil {
			payload["sub_title_text"] = " "
		}
	}

	return payload, nil
}

// defaultButtonCardTitle is the main_title used for button cards that arrive
// without a header; WeCom requires the field for card_type=button_interaction.
const defaultButtonCardTitle = "CC-Connect"

// newCardTaskID builds a template_card.task_id. WeCom requires one per
// button_interaction card, allows only digits, letters and "_-@", caps it at
// 128 bytes, and requires each task id to be unique per app, so a timestamp is
// combined with random bytes.
func newCardTaskID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("cc_%d", time.Now().UnixNano())
	}
	return fmt.Sprintf("cc_%d_%x", time.Now().UnixNano(), b)
}

// parseWeComPermissionResponse converts WeCom button event_key (e.g. perm:allow, perm:deny)
// into standard permission response strings ("allow", "deny", "allow all", "deny all").
// Returns response text and whether it was a permission response.
func parseWeComPermissionResponse(eventKey string) (string, bool) {
	switch eventKey {
	case "perm:allow":
		return "allow", true
	case "perm:deny":
		return "deny", true
	case "perm:allow_all":
		return "allow all", true
	case "perm:deny_all":
		return "deny all", true
	}
	return eventKey, false
}
