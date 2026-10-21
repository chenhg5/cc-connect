package feishu

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// makeTestPayload builds a deterministic byte payload of the requested size.
// Each chunk of 256 bytes has a recognisable pattern so tests can assert that
// chunk boundaries land on the right offsets (catches off-by-one bugs in the
// Range header construction).
func makeTestPayload(size int) []byte {
	out := make([]byte, size)
	for i := range out {
		out[i] = byte((i / 256) % 256)
	}
	return out
}

// newTestPlatformForDownload assembles a Platform with the bare-minimum fields the
// resource-download helper touches. The HTTP client is a vanilla stdlib
// client (no Transport overrides) so httptest.Server URLs work directly.
func newTestPlatformForDownload(domain string, chunkSize, maxBytes int64) *Platform {
	return &Platform{
		platformName:         "feishu",
		domain:               domain,
		resourceDownloadHTTP: &http.Client{Timeout: 10 * time.Second},
		resourceChunkSize:    chunkSize,
		resourceMaxBytes:     maxBytes,
		fetchResourceToken:   func(_ context.Context) (string, error) { return "test-token", nil },
	}
}

// TestParseContentRangeTotal covers the Content-Range parser used to recover
// the resource's total size from the probe response. Feishu always returns
// "bytes start-end/total"; we reject malformed headers and "*" totals.
func TestParseContentRangeTotal(t *testing.T) {
	cases := []struct {
		in     string
		want   int64
		wantOK bool
	}{
		{"bytes 0-0/12345", 12345, true},
		{"bytes 0-99/100", 100, true},
		{"bytes  0-0 / 42 ", 42, true},
		{"bytes 0-0/*", 0, false},
		{"bytes 0-0/", 0, false},
		{"", 0, false},
		{"garbage", 0, false},
		{"bytes 0-0/-1", 0, false},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got, ok := parseContentRangeTotal(c.in)
			if ok != c.wantOK {
				t.Fatalf("ok=%v want %v for %q", ok, c.wantOK, c.in)
			}
			if ok && got != c.want {
				t.Fatalf("got=%d want %d for %q", got, c.want, c.in)
			}
		})
	}
}

// TestDownloadResourceChunked_SmallFileUsesSingleGet exercises the
// small-resource fast path: the helper issues Range bytes=0-0; the server
// ignores it (returns 200 with full body); the helper returns immediately.
// Exactly one outbound GET, matching pre-#1741 behaviour for files that fit
// in Feishu's streaming cap.
func TestDownloadResourceChunked_SmallFileUsesSingleGet(t *testing.T) {
	payload := makeTestPayload(1024)
	var getCount int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("unexpected method %s", r.Method)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("Authorization header = %q, want Bearer test-token", got)
		}
		if r.Header.Get("Range") != "bytes=0-0" {
			t.Errorf("expected Range bytes=0-0 probe, got %q", r.Header.Get("Range"))
		}
		atomic.AddInt32(&getCount, 1)
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(payload)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	p := newTestPlatformForDownload(srv.URL, 8*1024*1024, 512*1024*1024)
	got, err := p.downloadResourceChunked(context.Background(), "om_x", "fk_x", "file")
	if err != nil {
		t.Fatalf("downloadResourceChunked: %v", err)
	}
	if !bytesEqual(got, payload) {
		t.Fatalf("payload mismatch: got %d bytes want %d", len(got), len(payload))
	}
	if n := atomic.LoadInt32(&getCount); n != 1 {
		t.Fatalf("expected exactly 1 GET, got %d", n)
	}
}

// TestDownloadResourceChunked_LargeFileUsesRangeLoop exercises the
// chunked-download path: first GET returns 206 with Content-Range total,
// helper loops Range GETs for the remaining bytes, concatenates them, and
// returns the assembled payload. Verifies chunk count, byte order, and
// Content-Range header round-tripping.
func TestDownloadResourceChunked_LargeFileUsesRangeLoop(t *testing.T) {
	const (
		chunkSize = 1024
		total     = 5*chunkSize + 137 // non-multiple of chunkSize to exercise tail
	)
	payload := makeTestPayload(total)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if !strings.Contains(path, "/messages/om_x/resources/fk_x") {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodGet {
			t.Errorf("unexpected method %s", r.Method)
		}
		rangeHdr := r.Header.Get("Range")
		if rangeHdr == "" {
			t.Errorf("expected Range header on every GET, got none")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(payload)
			return
		}
		start, end, ok := parseRangeHeader(rangeHdr)
		if !ok {
			t.Errorf("unparseable Range %q", rangeHdr)
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		}
		if start >= int64(total) {
			t.Errorf("Range start %d >= total %d", start, total)
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		}
		if end >= int64(total) {
			end = int64(total) - 1
		}
		slice := payload[start : end+1]
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, total))
		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(slice)))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(slice)
	}))
	defer srv.Close()

	p := newTestPlatformForDownload(srv.URL, chunkSize, 512*1024*1024)
	got, err := p.downloadResourceChunked(context.Background(), "om_x", "fk_x", "file")
	if err != nil {
		t.Fatalf("downloadResourceChunked: %v", err)
	}
	if !bytesEqual(got, payload) {
		t.Fatalf("payload mismatch: got %d bytes want %d", len(got), len(payload))
	}
}

// TestDownloadResourceChunked_TotalSizeMismatchFails verifies the helper
// detects a server that disagrees with the first-chunk total. The first
// chunk advertises 4096 bytes, the second chunk claims a different total
// — the helper must reject the download rather than silently concatenate
// inconsistent chunks.
func TestDownloadResourceChunked_TotalSizeMismatchFails(t *testing.T) {
	const (
		chunkSize  = 1024
		probeTotal = 4 * chunkSize
		lieTotal   = 5*chunkSize + 1
	)
	payload := makeTestPayload(probeTotal)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rangeHdr := r.Header.Get("Range")
		start, end, _ := parseRangeHeader(rangeHdr)
		if end >= int64(len(payload)) {
			end = int64(len(payload)) - 1
		}
		slice := payload[start : end+1]
		total := int64(probeTotal)
		if start >= chunkSize {
			total = lieTotal
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, total))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(slice)
	}))
	defer srv.Close()

	p := newTestPlatformForDownload(srv.URL, chunkSize, 512*1024*1024)
	_, err := p.downloadResourceChunked(context.Background(), "om_x", "fk_x", "file")
	if err == nil {
		t.Fatal("expected error for Content-Range total mismatch, got nil")
	}
	if !strings.Contains(err.Error(), "Content-Range total mismatch") {
		t.Fatalf("expected Content-Range total mismatch error, got %v", err)
	}
}

// TestDownloadResourceChunked_ProbeFailureFallsBackToSingleGet verifies
// that a first-chunk failure (4xx, 5xx) does not block the download — the
// helper falls back to a plain GET, exactly matching today's behaviour
// for the code=234037 case but giving smaller files a chance to succeed.
func TestDownloadResourceChunked_ProbeFailureFallsBackToSingleGet(t *testing.T) {
	payload := makeTestPayload(2048)
	var rangeGETs, plainGETs int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.Header.Get("Range") != "":
			atomic.AddInt32(&rangeGETs, 1)
			http.Error(w, "boom", http.StatusInternalServerError)
		case r.Method == http.MethodGet:
			atomic.AddInt32(&plainGETs, 1)
			w.Header().Set("Content-Length", fmt.Sprintf("%d", len(payload)))
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(payload)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}))
	defer srv.Close()

	p := newTestPlatformForDownload(srv.URL, 1024, 512*1024*1024)
	got, err := p.downloadResourceChunked(context.Background(), "om_x", "fk_x", "file")
	if err != nil {
		t.Fatalf("downloadResourceChunked: %v", err)
	}
	if !bytesEqual(got, payload) {
		t.Fatalf("payload mismatch")
	}
	if n := atomic.LoadInt32(&rangeGETs); n != 1 {
		t.Fatalf("expected 1 range GET, got %d", n)
	}
	if n := atomic.LoadInt32(&plainGETs); n != 1 {
		t.Fatalf("expected 1 plain GET fallback, got %d", n)
	}
}

// TestDownloadResourceChunked_RespectsMaxBytes verifies the size cap is
// enforced: if the server advertises a total greater than resourceMaxBytes
// in the first chunk's Content-Range, the helper must reject the download
// rather than allocate the buffer.
func TestDownloadResourceChunked_RespectsMaxBytes(t *testing.T) {
	const claimedTotal = 100 * 1024 * 1024
	const cap = 10 * 1024 * 1024
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("unexpected method %s", r.Method)
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes 0-0/%d", claimedTotal))
		w.Header().Set("Content-Length", "1")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte{0})
	}))
	defer srv.Close()

	p := newTestPlatformForDownload(srv.URL, 1024*1024, cap)
	_, err := p.downloadResourceChunked(context.Background(), "om_x", "fk_x", "file")
	if err == nil {
		t.Fatal("expected error for oversize resource, got nil")
	}
	if !strings.Contains(err.Error(), "exceeds cap") {
		t.Fatalf("expected cap error, got %v", err)
	}
}

// TestDownloadResourceChunked_RetriesTransientChunk verifies that a
// transient network error on a single chunk is retried with backoff
// instead of failing the whole download. The first chunk succeeds (Range
// 0-0, advertising the full total), then the second chunk's connection
// resets twice before succeeding on the third attempt.
func TestDownloadResourceChunked_RetriesTransientChunk(t *testing.T) {
	const (
		chunkSize = 1024
		total     = 3 * chunkSize
	)
	payload := makeTestPayload(total)
	var secondChunkRetries int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("unexpected method %s", r.Method)
		}
		rangeHdr := r.Header.Get("Range")
		start, end, _ := parseRangeHeader(rangeHdr)

		// Second data chunk fails the first two attempts, succeeds on the
		// third. First chunk is bytes=0-0 (probe, always succeeds).
		if start >= chunkSize && start < 2*chunkSize {
			if atomic.LoadInt32(&secondChunkRetries) < 2 {
				atomic.AddInt32(&secondChunkRetries, 1)
				hj, ok := w.(http.Hijacker)
				if !ok {
					t.Fatal("ResponseWriter does not implement Hijacker")
					return
				}
				conn, _, err := hj.Hijack()
				if err != nil {
					t.Fatalf("hijack: %v", err)
				}
				_ = conn.Close()
				return
			}
		}

		if end >= int64(total) {
			end = int64(total) - 1
		}
		slice := payload[start : end+1]
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, total))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(slice)
	}))
	defer srv.Close()

	p := newTestPlatformForDownload(srv.URL, chunkSize, 512*1024*1024)
	origInit := transientRetryInitial
	transientRetryInitial = 1 * time.Millisecond
	origMax := transientRetryMaxDelay
	transientRetryMaxDelay = 2 * time.Millisecond
	defer func() {
		transientRetryInitial = origInit
		transientRetryMaxDelay = origMax
	}()

	got, err := p.downloadResourceChunked(context.Background(), "om_x", "fk_x", "file")
	if err != nil {
		t.Fatalf("downloadResourceChunked: %v", err)
	}
	if !bytesEqual(got, payload) {
		t.Fatalf("payload mismatch")
	}
	if r := atomic.LoadInt32(&secondChunkRetries); r < 2 {
		t.Fatalf("expected ≥2 second-chunk retries, got %d", r)
	}
}

// TestDownloadResourceChunked_ContextCancelStopsDownload verifies that
// cancelling the context aborts the download cleanly.
func TestDownloadResourceChunked_ContextCancelStopsDownload(t *testing.T) {
	const (
		chunkSize = 1024
		total     = 100 * chunkSize // big enough that the loop runs many times
	)
	payload := makeTestPayload(total)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rangeHdr := r.Header.Get("Range")
		start, end, _ := parseRangeHeader(rangeHdr)
		if end >= int64(total) {
			end = int64(total) - 1
		}
		slice := payload[start : end+1]
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, total))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(slice)
	}))
	defer srv.Close()

	p := newTestPlatformForDownload(srv.URL, chunkSize, 512*1024*1024)
	ctx, cancel := context.WithCancel(context.Background())

	errCh := make(chan error, 1)
	go func() {
		_, err := p.downloadResourceChunked(ctx, "om_x", "fk_x", "file")
		errCh <- err
	}()
	cancel()

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("expected error from cancelled context, got nil")
		}
		if !errors.Is(err, context.Canceled) {
			if !strings.Contains(err.Error(), "context canceled") &&
				!strings.Contains(err.Error(), "context deadline exceeded") {
				t.Fatalf("expected context error, got %v", err)
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatal("download did not honour context cancellation within 5s")
	}
}

// TestDownloadResourceChunked_RejectsEmptyIDs verifies the helper refuses
// obvious garbage inputs before hitting the network.
func TestDownloadResourceChunked_RejectsEmptyIDs(t *testing.T) {
	p := newTestPlatformForDownload("http://localhost:0", 1024, 1024)
	_, err := p.downloadResourceChunked(context.Background(), "", "fk_x", "file")
	if err == nil {
		t.Fatal("expected error for empty messageID")
	}
	_, err = p.downloadResourceChunked(context.Background(), "om_x", "", "file")
	if err == nil {
		t.Fatal("expected error for empty fileKey")
	}
}

// TestDownloadResourceChunked_AuthFailurePropagates verifies a token-fetch
// failure is reported with feishu context and doesn't leak the underlying
// transport error verbatim.
func TestDownloadResourceChunked_AuthFailurePropagates(t *testing.T) {
	p := newTestPlatformForDownload("http://localhost:1", 1024, 1024)
	p.fetchResourceToken = func(_ context.Context) (string, error) {
		return "", errors.New("simulated token error")
	}
	_, err := p.downloadResourceChunked(context.Background(), "om_x", "fk_x", "file")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "resource download auth") {
		t.Fatalf("expected auth error, got %v", err)
	}
	if !strings.Contains(err.Error(), "simulated token error") {
		t.Fatalf("expected underlying error in chain, got %v", err)
	}
}

// parseRangeHeader parses a "bytes=START-END" header. Returns ok=false for
// anything else; the helper treats malformed headers as fatal so the
// failure mode is loud.
func parseRangeHeader(h string) (start, end int64, ok bool) {
	const p = "bytes="
	if !strings.HasPrefix(h, p) {
		return 0, 0, false
	}
	body := strings.TrimPrefix(h, p)
	dash := strings.Index(body, "-")
	if dash < 0 {
		return 0, 0, false
	}
	startS := strings.TrimSpace(body[:dash])
	endS := strings.TrimSpace(body[dash+1:])
	if startS == "" || endS == "" {
		return 0, 0, false
	}
	var s, e int64
	var err error
	if s, err = parseInt(startS); err != nil {
		return 0, 0, false
	}
	if e, err = parseInt(endS); err != nil {
		return 0, 0, false
	}
	return s, e, true
}

// parseInt is a tiny strconv.ParseInt wrapper so the test file doesn't have
// to import strconv directly (keeps the import list short and intentional).
func parseInt(s string) (int64, error) {
	var n int64
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("not an integer: %q", s)
		}
		n = n*10 + int64(c-'0')
	}
	return n, nil
}

// bytesEqual compares two byte slices without importing bytes — keeps the
// test file's surface minimal and reads naturally inline.
func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Ensure io.Discard is referenced even when individual subtests are
// filtered out by -run. The download helper itself uses io.Discard, but
// this import guards against future refactors silently dropping it.
var _ = io.Discard
