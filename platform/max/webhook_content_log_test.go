package max

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWebhookParseFailureOmitsRequestContent(t *testing.T) {
	const marker = "SENSITIVE_SENTINEL"
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	p := &Platform{}
	req := httptest.NewRequest(http.MethodPost, "/webhook", strings.NewReader(`{"text":"`+marker+`",`))
	rec := httptest.NewRecorder()
	p.webhookHandler(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if strings.Contains(logs.String(), marker) {
		t.Fatal("MAX parse log exposed request content")
	}
}
