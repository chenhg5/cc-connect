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
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var wire map[string]any
		if err := json.NewDecoder(r.Body).Decode(&wire); err != nil {
			t.Error(err)
			return
		}
		attempt := calls.Add(1)
		if wire["type"] != "input_injected" || wire["reply_ctx"] != "supplement" || wire["target_message_id"] != "original" || wire["session_key"] != "session" {
			t.Errorf("incorrect receipt: %+v", wire)
		}
		if attempt <= 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	tp := newPollTransport(srv.URL, "", "cloud_web", "project", "", "", 1000)
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
	if err := q.db.Close(); err != nil {
		t.Fatal(err)
	}
	q, err = openInputReceipts(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := q.db.Close(); err != nil {
			t.Error(err)
		}
	}()
	p.inputReceipts = q
	if err := p.flushInputReceipts(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := calls.Load(); got != 4 {
		t.Fatalf("attempts=%d, want 4 after restart", got)
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
		if err := json.NewDecoder(r.Body).Decode(&wire); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		wires <- wire
		if _, err := w.Write([]byte(`{"ok":true}`)); err != nil {
			t.Error(err)
		}
	}))
	defer srv.Close()
	tp := newPollTransport(srv.URL, "", "cloud_web", "project", "", "", 1000)
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
	stale := replyContext{SessionKey: "session", ReplyCtx: "stale"}
	p.ReportInputQueued(context.Background(), stale)
	p.ReportDropped(context.Background(), stale, "stale")
	if err := p.flushInputReceipts(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case wire := <-wires:
		if wire["type"] != "input_dropped" || wire["reply_ctx"] != "stale" || wire["reason"] != "stale" {
			t.Fatalf("incorrect stale receipt: %+v", wire)
		}
	default:
		t.Fatal("stale drop did not resolve the pending marker before restart")
	}
	if err := p.flushInputReceipts(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(wires) != 0 {
		t.Fatal("acknowledged stale receipt was sent again")
	}

	p.ReportInputQueued(context.Background(), replyContext{SessionKey: "session", ReplyCtx: "started"})
	p.ReportInputStarted(context.Background(), replyContext{SessionKey: "session", ReplyCtx: "started"})
	if err := q.db.Close(); err != nil {
		t.Fatal(err)
	}
	q, err = openInputReceipts(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := q.db.Close(); err != nil {
			t.Error(err)
		}
	}()
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
	tp := newPollTransport(srv.URL, "", "cloud_web", "project", "", "", 1000)
	tp.setCaps(capabilitySet([]string{"input_receipts_v1"}))
	q, err := openInputReceipts(filepath.Join(t.TempDir(), "receipts.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := q.db.Close(); err != nil {
			t.Error(err)
		}
	}()
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

func TestInjectedWithoutTargetRetiresPendingMarker(t *testing.T) {
	ctx := context.Background()
	tp := newPollTransport("", "", "cloud_web", "project", "", "", 1000)
	tp.setCaps(capabilitySet([]string{"input_receipts_v1"}))
	path := filepath.Join(t.TempDir(), "receipts.sqlite")
	q, err := openInputReceipts(path)
	if err != nil {
		t.Fatal(err)
	}
	p := &Platform{tp: tp, inputReceipts: q}
	rc := replyContext{SessionKey: "session", ReplyCtx: "supplement"}
	other := replyContext{SessionKey: "session", ReplyCtx: "still-queued"}
	p.ReportInputQueued(ctx, rc)
	p.ReportInputQueued(ctx, other)
	var pending int
	if err := q.db.QueryRow("SELECT pending FROM receipts WHERE session=? AND message=?", rc.SessionKey, rc.ReplyCtx).Scan(&pending); err != nil || pending != 1 {
		t.Fatalf("missing pending fixture: pending=%d err=%v", pending, err)
	}
	p.ReportInjected(ctx, rc, "")
	if err := q.db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := openInputReceipts(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := reopened.db.Close(); err != nil {
			t.Error(err)
		}
	}()
	var count int
	if err := reopened.db.QueryRow("SELECT COUNT(*) FROM receipts WHERE session=? AND message=?", rc.SessionKey, rc.ReplyCtx).Scan(&count); err != nil || count != 0 {
		t.Fatalf("success with no target retained a receipt across restart: count=%d err=%v", count, err)
	}
	if err := reopened.db.QueryRow("SELECT pending FROM receipts WHERE session=? AND message=?", other.SessionKey, other.ReplyCtx).Scan(&pending); err != nil || pending != 0 {
		t.Fatalf("unrelated pending input was not recoverable: pending=%d err=%v", pending, err)
	}
}
