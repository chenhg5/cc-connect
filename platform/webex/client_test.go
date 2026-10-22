package webex

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestGetMessageUnauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	c := &httpClient{token: "t", hc: srv.Client(), baseURL: srv.URL}
	_, err := c.GetMessage(context.Background(), "abc")
	if !errors.Is(err, errUnauthorized) {
		t.Fatalf("expected errUnauthorized, got %v", err)
	}
}

func TestGetMessageRetriesOn429(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		if n == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"abc","roomId":"r","roomType":"direct"}`))
	}))
	defer srv.Close()

	c := &httpClient{token: "t", hc: srv.Client(), baseURL: srv.URL}
	m, err := c.GetMessage(context.Background(), "abc")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.ID != "abc" {
		t.Fatalf("message ID = %q", m.ID)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("expected 2 calls (1 retry), got %d", got)
	}
}
