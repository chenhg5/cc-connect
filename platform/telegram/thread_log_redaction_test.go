package telegram

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestInvalidThreadLogOmitsRawSessionSegment(t *testing.T) {
	const secret = "TELEGRAM-THREAD-SENTINEL"
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	p := &Platform{shareSessionInChannel: true}
	_, err := p.ReconstructReplyCtx("telegram:123:" + secret)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(logs.String(), secret) {
		t.Fatal("Telegram Warn log exposed raw session segment")
	}
}
