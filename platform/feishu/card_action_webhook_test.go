package feishu

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestAppCardActionWebhook(t *testing.T) {
	var calls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var v cardActionPayload
		if r.Method != "POST" || json.NewDecoder(r.Body).Decode(&v) != nil || v.Action != "app:save" || v.UserID != "ou_allowed" {
			t.Error("unexpected callback payload")
		}
		_, _ = w.Write([]byte(`{"toast":{"type":"success","content":"saved"}}`))
	}))
	defer s.Close()
	p, _ := newAllowFromTestPlatform(t, "ou_allowed")
	p.cardActionWebhook = s.URL
	if _, err := p.onCardAction(cardActionEvent("ou_other", "app:save")); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 0 {
		t.Fatal("unauthorized callback reached handler")
	}
	res, err := p.onCardAction(cardActionEvent("ou_allowed", "app:save"))
	if err != nil || res == nil || res.Toast == nil || calls.Load() != 1 {
		t.Fatalf("unexpected callback result: %v %v", res, err)
	}
	for _, u := range []any{"https://example.com/x", "http://example.com/x", "http://user@127.0.0.1/x", true} {
		if _, err := parseCardActionWebhook(u); err == nil {
			t.Errorf("accepted %v", u)
		}
	}
	if _, err := parseCardActionWebhook(s.URL); err != nil {
		t.Fatal(err)
	}
}
