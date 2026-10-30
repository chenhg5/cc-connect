package slack

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"unicode/utf8"
)

type rawLogCapture struct {
	raw string
}

func (h *rawLogCapture) Enabled(context.Context, slog.Level) bool { return true }
func (h *rawLogCapture) WithAttrs([]slog.Attr) slog.Handler       { return h }
func (h *rawLogCapture) WithGroup(string) slog.Handler            { return h }
func (h *rawLogCapture) Handle(_ context.Context, r slog.Record) error {
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == "raw" {
			h.raw = a.Value.String()
		}
		return true
	})
	return nil
}

func TestRichContentLogTruncationMultibyte(t *testing.T) {
	const jsonPrefix = `{"attachments":null,"blocks":[{"text":{"text":"`
	text := strings.Repeat("a", richContentLogLimit-len(jsonPrefix)-1) + "界" + strings.Repeat("b", richContentLogLimit)
	encodedText, err := json.Marshal(text)
	if err != nil {
		t.Fatal(err)
	}
	event := `{"blocks":[{"type":"section","text":{"type":"plain_text","text":` + string(encodedText) + `}}]}`

	handler := &rawLogCapture{}
	previous := slog.Default()
	slog.SetDefault(slog.New(handler))
	t.Cleanup(func() { slog.SetDefault(previous) })
	extractRichContent(rawJSON(t, event), "1.1")

	if handler.raw == "" {
		t.Fatal("debug log raw attribute was not captured")
	}
	if !utf8.ValidString(handler.raw) {
		t.Errorf("rich content log truncation produced invalid UTF-8 at byte %d", richContentLogLimit)
	}
}
