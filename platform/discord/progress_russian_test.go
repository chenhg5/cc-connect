package discord

import "testing"

func TestRussianProgressLabels(t *testing.T) {
	checks := map[string]string{
		"running":          discordProgressRunningText("ru"),
		"completed":        discordProgressCompletedText("ru"),
		"failed":           discordProgressFailedText("ru"),
		"completed footer": discordProgressCompletedFooter("ru"),
		"failed footer":    discordProgressFailedFooter("ru"),
		"latest":           discordProgressLatestOnlyText("ru"),
		"no output":        discordProgressNoOutputText("ru"),
		"tool":             discordProgressToolLabel("ru"),
		"ok":               discordProgressOKText("ru"),
	}
	expected := map[string]string{
		"running": "Обработка", "completed": "Завершено", "failed": "Ошибка",
		"completed footer": "Эта карточка больше не обновляется. Полный ответ — в следующем сообщении.",
		"failed footer":    "Обновление карточки остановлено из-за ошибки. Подробности — в следующем сообщении.",
		"latest":           "Показаны только последние обновления.", "no output": "Нет вывода",
		"tool": "Инструмент", "ok": "успешно",
	}
	for name, got := range checks {
		if got != expected[name] {
			t.Errorf("%s: got %q, want %q", name, got, expected[name])
		}
	}
	if got := normalizeDiscordProgressLang("ru-RU"); got != "ru" {
		t.Errorf("ru-RU normalized to %q", got)
	}
}
