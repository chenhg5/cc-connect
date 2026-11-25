package telegram

import (
	"testing"
	"time"
)

func TestTelegramConfiguredHTTPTimeoutForLargeUploads(t *testing.T) {
	p, err := New(map[string]any{"token": "test-token", "http_timeout_secs": int64(300)})
	if err != nil {
		t.Fatal(err)
	}
	if got := p.(*Platform).httpClient.Timeout; got != 300*time.Second {
		t.Fatalf("HTTP timeout = %v, want 300s for large uploads", got)
	}
}

func TestTelegramHTTPTimeoutDefaultsAndValidation(t *testing.T) {
	p, err := New(map[string]any{"token": "test-token"})
	if err != nil {
		t.Fatal(err)
	}
	if got := p.(*Platform).httpClient.Timeout; got != 90*time.Second {
		t.Fatalf("default HTTP timeout = %v, want 90s", got)
	}
	for _, value := range []any{int64(60), int64(0), -1, "300"} {
		if _, err := New(map[string]any{"token": "test-token", "http_timeout_secs": value}); err == nil {
			t.Errorf("http_timeout_secs=%v should be rejected", value)
		}
	}
}
