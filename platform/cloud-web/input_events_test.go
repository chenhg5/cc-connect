package cloudweb

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
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
		if calls <= 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	tp := newPollTransport(srv.URL, "token", "cloud_web", "project", "", "", 1000)
	path := filepath.Join(t.TempDir(), "receipts.sqlite")
	q, err := openInputReceipts(path)
	if err != nil {
		t.Fatal(err)
	}
	p := &Platform{tp: tp, inputReceipts: q}
	rc := replyContext{SessionKey: "session", ReplyCtx: "supplement"}
	p.ReportInjected(context.Background(), rc, "original")
	var count int
	if err := q.db.QueryRow("SELECT count(*) FROM receipts").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("persisted unnegotiated receipt")
	}
	tp.setCaps(capabilitySet([]string{"input_receipts_v1"}))
	p.ReportInjected(context.Background(), rc, "original")
	for i := 0; i < 3; i++ {
		if p.flushInputReceipts(context.Background()) == nil {
			t.Fatal("expected transient error")
		}
	}
	q.db.Close()
	q, err = openInputReceipts(path)
	if err != nil {
		t.Fatal(err)
	}
	defer q.db.Close()
	p.inputReceipts = q
	if err := p.flushInputReceipts(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls != 4 {
		t.Fatalf("attempts=%d, want 4 after restart", calls)
	}
	if err := q.db.QueryRow("SELECT count(*) FROM receipts").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("acknowledged receipt retained")
	}
}

func TestQueuedInputMarkersRecoverUncertainWithoutReinjecting(t *testing.T) {
	wires := make(chan map[string]any, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var wire map[string]any
		json.NewDecoder(r.Body).Decode(&wire)
		wires <- wire
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	tp := newPollTransport(srv.URL, "token", "cloud_web", "project", "", "", 1000)
	tp.setCaps(capabilitySet([]string{"input_receipts_v1"}))
	path := filepath.Join(t.TempDir(), "receipts.sqlite")
	q, err := openInputReceipts(path)
	if err != nil {
		t.Fatal(err)
	}
	p := &Platform{tp: tp, inputReceipts: q}
	rc := replyContext{SessionKey: "session", ReplyCtx: "queued"}
	p.ReportInputQueued(context.Background(), rc)
	if err := p.flushInputReceipts(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(wires) != 0 {
		t.Fatal("live worker queue was prematurely terminated")
	}
	p.ReportInputQueued(context.Background(), replyContext{SessionKey: "session", ReplyCtx: "started"})
	p.ReportInputStarted(context.Background(), replyContext{SessionKey: "session", ReplyCtx: "started"})
	q.db.Close()
	q, err = openInputReceipts(path)
	if err != nil {
		t.Fatal(err)
	}
	defer q.db.Close()
	p.inputReceipts = q
	if err := p.flushInputReceipts(context.Background()); err != nil {
		t.Fatal(err)
	}
	wire := <-wires
	if wire["type"] != "input_uncertain" || wire["reply_ctx"] != "queued" || len(wires) != 0 {
		t.Fatalf("incorrect restart receipt: %+v", wire)
	}
}

func TestInputReceiptBackgroundRetriesAfterOutage(t *testing.T) {
	var calls atomic.Int32
	delivered := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) <= 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
		select {
		case delivered <- struct{}{}:
		default:
		}
	}))
	defer srv.Close()
	tp := newPollTransport(srv.URL, "token", "cloud_web", "project", "", "", 1000)
	tp.setCaps(capabilitySet([]string{"input_receipts_v1"}))
	q, err := openInputReceipts(filepath.Join(t.TempDir(), "receipts.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer q.db.Close()
	p := &Platform{tp: tp, inputReceipts: q}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.ReportInjected(ctx, replyContext{SessionKey: "session", ReplyCtx: "supplement"}, "root")
	go p.runInputReceipts(ctx)
	select {
	case <-delivered:
	case <-time.After(6 * time.Second):
		t.Fatal("receipt was not retried after three failures")
	}
	cancel()
	<-q.done
	if calls.Load() < 4 {
		t.Fatalf("attempts=%d, want recovery beyond three failures", calls.Load())
	}
}
