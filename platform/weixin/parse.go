package weixin

import (
	"fmt"
	"strings"
)

func isMediaItemType(t int) bool {
	switch t {
	case messageItemImage, messageItemVoice, messageItemFile, messageItemVideo:
		return true
	default:
		return false
	}
}

// bodyFromItemList extracts user-visible text from Weixin item_list (text, quotes, voice ASR).
func bodyFromItemList(items []messageItem) string {
	if len(items) == 0 {
		return ""
	}
	for _, item := range items {
		switch item.Type {
		case messageItemText:
			if item.TextItem == nil {
				continue
			}
			text := strings.TrimSpace(item.TextItem.Text)
			ref := item.RefMsg
			if ref == nil {
				if text == "" {
					// Skip placeholder text items with no body and no quoted ref
					// so the loop can still find a later voice transcription
					// or text item. Returning "" here drops a voice item that
					// followed an empty text item — dispatchInbound treats the
					// message as empty + media-less and silently discards it.
					continue
				}
				return text
			}
			if ref.MessageItem != nil && isMediaItemType(ref.MessageItem.Type) {
				return text
			}
			var parts []string
			if ref.Title != "" {
				parts = append(parts, ref.Title)
			}
			if ref.MessageItem != nil {
				refBody := bodyFromItemList([]messageItem{*ref.MessageItem})
				if refBody != "" {
					parts = append(parts, refBody)
				}
			}
			if len(parts) == 0 {
				if text == "" {
					continue
				}
				return text
			}
			return fmt.Sprintf("[引用: %s]\n%s", strings.Join(parts, " | "), text)
		case messageItemVoice:
			if item.VoiceItem != nil && strings.TrimSpace(item.VoiceItem.Text) != "" {
				return strings.TrimSpace(item.VoiceItem.Text)
			}
		}
	}
	return ""
}
