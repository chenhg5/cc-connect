package core

import (
	"fmt"
	"strings"
	"testing"
)

func TestCronExprToHumanRussian(t *testing.T) {
	cases := map[string]string{
		"* * * * *":             "Каждую минуту",
		"*/1 * * * *":           "Каждую минуту",
		"*/2 * * * *":           "Каждые 2 минуты",
		"*/5 * * * *":           "Каждые 5 минут",
		"*/11 * * * *":          "Каждые 11 минут",
		"*/21 * * * *":          "Каждую 21 минуту",
		"*/22 * * * *":          "Каждые 22 минуты",
		"0 */1 * * *":           "Каждый час в 0 мин, начиная с 00:00",
		"5 */2 * * *":           "Каждые 2 часа в 5 мин, начиная с 00:05",
		"0 */5 * * *":           "Каждые 5 часов в 0 мин, начиная с 00:00",
		"0 */11 * * *":          "Каждые 11 часов в 0 мин, начиная с 00:00",
		"0 */21 * * *":          "Каждый 21 час в 0 мин, начиная с 00:00",
		"* */2 * * *":           "Каждую минуту в часы с шагом 2, начиная с 00:00",
		"*/5 */2 * * *":         "Каждые 5 минут в часы с шагом 2, начиная с 00:00",
		"0 9 * * *":             "Ежедневно в 09:00",
		"*/5 9 * * *":           "Ежедневно каждые 5 минут с 09:00 до 09:59",
		"* 9 * * *":             "Ежедневно каждую минуту с 09:00 до 09:59",
		"15 * * * *":            "Каждый час в 15 мин",
		"0 9 * * 1":             "По понедельникам, в 09:00",
		"*/2 * * * 3":           "По средам, каждые 2 минуты",
		"15 * * * 5":            "По пятницам, каждый час в 15 мин",
		"0 9 1 * *":             "Ежемесячно, 1-го числа, в 09:00",
		"0 9 1 1 *":             "Ежегодно, 1 января, в 09:00",
		"0 9 * 2 *":             "В течение февраля, в 09:00",
		"0 9 * 2 6":             "По субботам, в течение февраля, в 09:00",
		"TZ=UTC 0 9 * * *":      "Ежедневно в 09:00",
		"CRON_TZ=UTC 0 9 * * *": "Ежедневно в 09:00",
	}
	for expr, want := range cases {
		if got := CronExprToHuman(expr, LangRussian); got != want {
			t.Errorf("%q: got %q, want %q", expr, got, want)
		}
	}
	// Never silently omit a field, misdescribe DOM/DOW's OR, or turn an
	// invalid schedule into a plausible-looking Russian time.
	for _, expr := range []string{"invalid", "@daily", "0 9 1 * 1", "0 9 * 1,6 *", "0 9 * * 1-5", "0 9 * JAN MON", "0 25 * * *", "60 9 * * *", "*/0 * * * *", "*/5junk * * * *", "CRON_TZ=Invalid/Zone 0 9 * * *"} {
		if got := CronExprToHuman(expr, LangRussian); got != expr {
			t.Errorf("fallback for %q = %q", expr, got)
		}
	}
}

func TestRussianCronCalendarNames(t *testing.T) {
	weekdays := []string{"воскресеньям", "понедельникам", "вторникам", "средам", "четвергам", "пятницам", "субботам"}
	for day, name := range weekdays {
		if got := CronExprToHuman(fmt.Sprintf("0 9 * * %d", day), LangRussian); got != "По "+name+", в 09:00" {
			t.Error(got)
		}
	}
	months := []string{"января", "февраля", "марта", "апреля", "мая", "июня", "июля", "августа", "сентября", "октября", "ноября", "декабря"}
	for month, name := range months {
		if got := CronExprToHuman(fmt.Sprintf("0 9 1 %d *", month+1), LangRussian); !strings.Contains(got, "1 "+name) {
			t.Error(got)
		}
	}
}
