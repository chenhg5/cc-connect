package telegram

import (
	"testing"

	"github.com/go-telegram/bot/models"
)

func TestPermissionCallbackDisplayLabelUsesActualButtonLanguage(t *testing.T) {
	markup := &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
		{{Text: "Разрешить", CallbackData: "perm:allow"}, {Text: "Отклонить", CallbackData: "perm:deny"}},
		{{Text: "Авторазрешение", CallbackData: "perm:allow_all"}},
	}}
	for _, tc := range []struct{ value, fallback, want string }{
		{"perm:allow", "✅ Allowed", "✅ Разрешить"},
		{"perm:deny", "❌ Denied", "❌ Отклонить"},
		{"perm:allow_all", "✅ Allow All", "✅ Авторазрешение"},
		{"perm:unknown", "fallback", "fallback"},
	} {
		if got := permissionCallbackDisplayLabel(tc.value, tc.fallback, markup); got != tc.want {
			t.Errorf("%q: got %q, want %q", tc.value, got, tc.want)
		}
	}
	if got := permissionCallbackDisplayLabel("perm:allow", "✅ Allowed", nil); got != "✅ Allowed" {
		t.Fatalf("missing reply markup: got %q", got)
	}
}
