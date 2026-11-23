package cloudweb

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestInjectedInputReceiptNegotiatesAndRetriesTheSameTarget(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var wire map[string]any
		if err := json.NewDecoder(r.Body).Decode(&wire); err != nil {
			t.Error(err)
			return
		}
		calls++
		if wire["type"] != "input_injected" || wire["reply_ctx"] != "supplement" || wire["target_message_id"] != "original" || wire["session_key"] != "session" {
			t.Errorf("incorrect receipt: %+v", wire)
		}
		if calls == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	tp := newPollTransport(srv.URL, "token", "cloud_web", "project", "", "", 1000)
	p := &Platform{tp: tp}
	rc := replyContext{SessionKey: "session", ReplyCtx: "supplement"}
	p.ReportInjected(context.Background(), rc, "original")
	if calls != 0 {
		t.Fatal("unnegotiated receipt")
	}
	tp.setCaps(capabilitySet([]string{"input_injected_v1"}))
	p.ReportInjected(context.Background(), rc, "original")
	if calls != 2 {
		t.Fatalf("attempts=%d, want 2", calls)
	}
}
