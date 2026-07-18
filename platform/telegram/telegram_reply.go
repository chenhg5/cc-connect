package telegram

import (
	"fmt"
	"strings"

	"github.com/go-telegram/bot/models"
)

// enrichReplyContent extracts the quoted/original message from a Telegram reply
// and formats it so the AI agent can see the context of what the user is replying to.
// Returns the enriched content string, or empty string if this is not a reply.
//
// Prefer msg.Quote (Bot API text_quote / 划词引用) when present; otherwise fall
// back to the full reply_to_message text/caption and media markers.
func enrichReplyContent(msg *models.Message) string {
	if msg.ReplyToMessage == nil {
		return ""
	}

	original := msg.ReplyToMessage
	var parts []string

	// Prefer partial quote selection over the full original body.
	if msg.Quote != nil && strings.TrimSpace(msg.Quote.Text) != "" {
		parts = append(parts, msg.Quote.Text)
	} else if original.Text != "" {
		parts = append(parts, original.Text)
	} else if original.Caption != "" {
		parts = append(parts, original.Caption)
	}

	if original.Location != nil {
		parts = append(parts, fmt.Sprintf("[Location] Latitude: %.6f, Longitude: %.6f",
			original.Location.Latitude, original.Location.Longitude))
	}

	if len(original.Photo) > 0 {
		parts = append(parts, "[Image]")
	}

	if original.Document != nil {
		parts = append(parts, fmt.Sprintf("[File: %s]", original.Document.FileName))
	}

	if original.Voice != nil {
		parts = append(parts, "[Voice Message]")
	}

	if original.Audio != nil {
		parts = append(parts, fmt.Sprintf("[Audio: %s]", original.Audio.FileName))
	}

	if original.Sticker != nil {
		parts = append(parts, "[Sticker]")
	}

	if len(parts) == 0 {
		return ""
	}

	// Identify who wrote the original message
	fromName := "Unknown"
	if original.From != nil {
		fromName = original.From.FirstName
		if original.From.LastName != "" {
			fromName += " " + original.From.LastName
		}
		if original.From.Username != "" {
			fromName = "@" + original.From.Username
		}
	}

	return fmt.Sprintf("[Reply to %s]: %s", fromName, strings.Join(parts, " "))
}
