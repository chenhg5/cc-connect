package core

import (
	"context"
	"testing"
	"time"
)

type sharedCompatHistoryAgent struct {
	*sharedSettingsTestAgent
	history    []HistoryEntry
	historyErr error
}

func TestSharedHistoryUsesDaemonEvenWhenLocalHistoryExists(t *testing.T) {
	env, a, _ := newSharedSettingsEnv(t)
	env.e.agent = &sharedCompatHistoryAgent{sharedSettingsTestAgent: a, history: []HistoryEntry{{Role: "user", Content: "SERVER CLI QUESTION"}, {Role: "assistant", Content: "SERVER CLI ANSWER"}}}
	env.send("/history 10")
	env.await("SERVER CLI QUESTION")
	env.await("SERVER CLI ANSWER")
}

func TestHistoryTimestampUsesLocalTimeAndMarksMissing(t *testing.T) {
	e := &Engine{i18n: NewI18n(LangEnglish)}
	timestamp := time.Date(2026, 10, 2, 12, 34, 56, 0, time.FixedZone("source", 7*60*60+13))
	if got, want := e.historyTimestamp(timestamp), timestamp.Local().Format("15:04:05"); got != want {
		t.Fatalf("got %q want local %q", got, want)
	}
	if got := e.historyTimestamp(time.Time{}); got != e.i18n.T(MsgHistoryTimeUnknown) {
		t.Fatalf("missing timestamp=%q", got)
	}
}

func (a *sharedCompatHistoryAgent) GetSessionHistory(context.Context, string, int) ([]HistoryEntry, error) {
	return a.history, a.historyErr
}
