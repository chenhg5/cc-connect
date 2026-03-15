package telegram

import (
	"strings"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"github.com/chenhg5/cc-connect/core"
)

// menuPageToKeyboard converts a core.MenuPage's Buttons into a Telegram InlineKeyboardMarkup.
func menuPageToKeyboard(page *core.MenuPage) tgbotapi.InlineKeyboardMarkup {
	var rows [][]tgbotapi.InlineKeyboardButton
	for _, row := range page.Buttons {
		var btns []tgbotapi.InlineKeyboardButton
		for _, b := range row {
			data := b.Data
			if len(data) > 64 {
				data = data[:64]
			}
			btns = append(btns, tgbotapi.NewInlineKeyboardButtonData(b.Text, data))
		}
		rows = append(rows, btns)
	}
	return tgbotapi.NewInlineKeyboardMarkup(rows...)
}

// menuMessageText formats a MenuPage's title and subtitle into a single HTML string.
func menuMessageText(page *core.MenuPage) string {
	if page.Subtitle == "" {
		return page.Title
	}
	return page.Title + "\n" + "<i>" + escapeHTML(page.Subtitle) + "</i>"
}

// escapeHTML escapes Telegram HTML special characters in s.
func escapeHTML(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}
