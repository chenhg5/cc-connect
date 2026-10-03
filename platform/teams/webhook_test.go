package teams

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func testPlatform(s *testSigner) *Platform {
	return &Platform{
		cfg:         config{appID: "app-123"},
		validator:   validatorWith(s),
		dispatchSem: make(chan struct{}, maxConcurrentDispatch),
	}
}

func TestHandleActivity_ValidTokenAccepted(t *testing.T) {
	s := newTestSigner(t)
	p := testPlatform(s)

	req := httptest.NewRequest(http.MethodPost, "/api/messages", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+s.sign(t, baseClaims()))
	rec := httptest.NewRecorder()

	p.handleActivity(rec, req)

	// The turn is dispatched asynchronously; the webhook acks 202 immediately.
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", rec.Code)
	}
}

func TestHandleActivity_ShedsWhenSaturated(t *testing.T) {
	s := newTestSigner(t)
	p := testPlatform(s)
	// Saturate the dispatch pool so the next activity has no slot.
	for i := 0; i < maxConcurrentDispatch; i++ {
		p.dispatchSem <- struct{}{}
	}

	req := httptest.NewRequest(http.MethodPost, "/api/messages", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+s.sign(t, baseClaims()))
	rec := httptest.NewRecorder()

	p.handleActivity(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 when the dispatch pool is saturated", rec.Code)
	}
}

func TestHandleActivity_MissingAuthRejected(t *testing.T) {
	s := newTestSigner(t)
	p := testPlatform(s)

	req := httptest.NewRequest(http.MethodPost, "/api/messages", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()

	p.handleActivity(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestHandleActivity_BadTokenRejected(t *testing.T) {
	s := newTestSigner(t)
	other := newTestSigner(t)
	p := testPlatform(s)

	req := httptest.NewRequest(http.MethodPost, "/api/messages", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+other.sign(t, baseClaims()))
	rec := httptest.NewRecorder()

	p.handleActivity(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestHandleActivity_NonPostRejected(t *testing.T) {
	s := newTestSigner(t)
	p := testPlatform(s)

	req := httptest.NewRequest(http.MethodGet, "/api/messages", nil)
	rec := httptest.NewRecorder()

	p.handleActivity(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func TestHandleActivity_BodyReadError(t *testing.T) {
	s := newTestSigner(t)
	p := testPlatform(s)

	req := httptest.NewRequest(http.MethodPost, "/api/messages", errReader{})
	req.Header.Set("Authorization", "Bearer "+s.sign(t, baseClaims()))
	rec := httptest.NewRecorder()

	p.handleActivity(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 on body read error", rec.Code)
	}
}

func TestBearerToken(t *testing.T) {
	cases := map[string]string{
		"Bearer abc": "abc",
		"bearer abc": "abc",
		"abc":        "",
		"":           "",
		"Bearer ":    "",
		"Basic xyz":  "",
	}
	for header, want := range cases {
		req := httptest.NewRequest(http.MethodPost, "/", nil)
		if header != "" {
			req.Header.Set("Authorization", header)
		}
		if got := bearerToken(req); got != want {
			t.Errorf("bearerToken(%q) = %q, want %q", header, got, want)
		}
	}
}
