package feishu

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestParsePostFailureOmitsOriginalContent(t *testing.T) {
	const marker = "SENSITIVE_SENTINEL"
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	p := &Platform{}
	if p.parsePostBody(`{"text":"`+marker+`",`) != nil {
		t.Fatal("malformed post unexpectedly parsed")
	}
	if strings.Contains(logs.String(), marker) {
		t.Fatal("Feishu post parse log exposed original content")
	}
}
