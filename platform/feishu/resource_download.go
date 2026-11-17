package feishu

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// This file implements Feishu message-resource downloads that support large
// files (issue #1741). The larkim SDK's GetMessageResource builder does not
// expose the HTTP Range header, so a plain GET of any resource over Feishu's
// internal ~2 MiB streaming cap returns code=234037 (download interrupted).
// The workaround is to bypass the SDK entirely with a raw HTTP client, issue
// a Range header for each chunk, and reassemble the bytes client-side.
//
// Two download paths share this helper:
//   - (a) "downloadResource" — generic file/audio/resource bodies (4 call sites)
//   - (b) "downloadImage" — image bodies (1 call site)
//
// The helper:
//   1. Probes the resource size with HEAD (no Range header) so we can pick
//      between a single GET (small resources) and a chunked Range loop.
//   2. Falls back to a single GET when Content-Length is missing or the body
//      is smaller than the chunk size (avoids an unnecessary HEAD round-trip).
//   3. For larger resources, loops Range GETs in fixed-size chunks, verifies
//      the Content-Range total matches the probed size, and concatenates the
//      chunks into a single buffer.
//   4. Retries on transient network errors (the same retry policy the SDK
//      path uses) so a flaky Wi-Fi handoff doesn't break a 100 MiB transfer.
//
// All Feishu API authentication reuses fetchFreshTenantAccessToken + the
// replayAPIClient pattern already in use elsewhere in this package.

const (
	// resourceRangeProbeTimeout bounds the initial HEAD probe. Feishu usually
	// responds in well under a second; 5 s is a generous outer limit that
	// still feels snappy to users.
	resourceRangeProbeTimeout = 5 * time.Second

	// resourceRangeMaxRangeHeaderBytes is the largest single Range request we
	// will issue, used as a hard ceiling when probing advertises an absurdly
	// large Content-Length (defence in depth on top of resourceMaxBytes).
	resourceRangeMaxRangeHeaderBytes int64 = 64 * 1024 * 1024
)

// downloadResourceChunked downloads a Feishu message resource (file, audio,
// etc.) into memory, bypassing the larkim SDK so we can set the HTTP Range
// header that the SDK's GetMessageResource builder does not expose (issue
// #1741). Returns the full payload or an error with the Feishu context
// prepended.
//
// Behaviour:
//   - Always issues a single Range bytes=0-0 GET first. If the server honours
//     Range (206) we then loop the remaining chunks; if it doesn't (200) we
//     already have the full body — done.
//   - On any transient network error mid-loop: retries with exponential
//     backoff up to maxTransientRetries before giving up.
//
// One GET is the minimum regardless of file size: small files return 200 and
// we are done; large files return 206 with the first chunk, then we loop.
// This avoids the wasted HEAD round-trip and keeps the "small file" path
// observable as exactly one outbound request, matching pre-#1741 behaviour
// for files under Feishu's streaming cap.
//
// messageID and fileKey come from the inbound message envelope; resType is
// the Feishu resource-type segment ("file", "image", ...). The caller MUST
// guarantee these have already been validated (no empty strings).
func (p *Platform) downloadResourceChunked(ctx context.Context, messageID, fileKey, resType string) ([]byte, error) {
	if strings.TrimSpace(messageID) == "" || strings.TrimSpace(fileKey) == "" {
		return nil, fmt.Errorf("%s: resource download requires non-empty messageID and fileKey", p.tag())
	}
	if p.resourceDownloadHTTP == nil {
		// Defensive: callers running outside the normal constructor (notably
		// unit tests that synthesise a Platform value) still get a sane
		// client. We log instead of panicking so one stale test fixture
		// doesn't crash the whole process.
		slog.Warn(p.tag() + ": resourceDownloadHTTP is nil; using default client")
		p.resourceDownloadHTTP = &http.Client{Timeout: 60 * time.Second}
	}
	if p.resourceChunkSize <= 0 {
		p.resourceChunkSize = defaultResourceChunkSize()
	}
	if p.resourceMaxBytes <= 0 {
		p.resourceMaxBytes = defaultResourceMaxBytes
	}

	token, err := p.fetchResourceTokenOrDefault(ctx)
	if err != nil {
		return nil, fmt.Errorf("%s: resource download auth: %w", p.tag(), err)
	}

	return p.resourceDownloadStream(ctx, token, messageID, fileKey, resType)
}

// resourceDownloadStream executes the actual download. Split out so the
// helper's preflight (validation, token, defaults) stays readable.
func (p *Platform) resourceDownloadStream(ctx context.Context, token, messageID, fileKey, resType string) ([]byte, error) {
	probeCtx, cancel := context.WithTimeout(ctx, resourceRangeProbeTimeout)
	defer cancel()

	first, total, err := p.resourceFetchFirstChunk(probeCtx, token, messageID, fileKey, resType)
	if err != nil {
		// Fallback: try a single plain GET. Some servers reject Range entirely
		// with 4xx instead of silently ignoring it.
		slog.Warn(p.tag()+": first-chunk fetch failed; trying plain GET",
			"error", err, "file_key", fileKey, "type", resType)
		return p.resourceSingleGet(ctx, token, messageID, fileKey, resType)
	}

	// Server ignored our Range header and sent the whole body in one 200.
	if total == 0 {
		if int64(len(first)) > p.resourceMaxBytes {
			return nil, fmt.Errorf("resource too large: body=%d exceeds cap %d", len(first), p.resourceMaxBytes)
		}
		return first, nil
	}

	// Server honoured Range. Loop the remaining chunks.
	if total > p.resourceMaxBytes {
		return nil, fmt.Errorf("resource too large: total=%d exceeds cap %d", total, p.resourceMaxBytes)
	}
	if int64(len(first)) >= total {
		// Defensive: a server that advertises 206 with first slice already
		// covering the whole resource is fine — return what we have.
		return first, nil
	}
	return p.resourceFetchRemainingChunks(ctx, token, messageID, fileKey, resType, total, first)
}

// resourceFetchFirstChunk issues Range bytes=0-0 to learn the total and grab
// the first byte. Returns (first, total, nil) where total==0 means the
// server ignored Range and the entire body is in `first`.
func (p *Platform) resourceFetchFirstChunk(ctx context.Context, token, messageID, fileKey, resType string) ([]byte, int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.resourceURL(messageID, fileKey, resType), nil)
	if err != nil {
		return nil, 0, fmt.Errorf("build first-chunk request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Range", "bytes=0-0")

	resp, err := p.resourceDownloadHTTP.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("first-chunk request: %w", err)
	}
	defer func() { _, _ = io.Copy(io.Discard, resp.Body); _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusPartialContent:
		cr := resp.Header.Get("Content-Range")
		total, ok := parseContentRangeTotal(cr)
		if !ok {
			return nil, 0, fmt.Errorf("first-chunk: 206 without parseable Content-Range %q", cr)
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 1))
		if err != nil {
			return nil, 0, fmt.Errorf("read first-chunk body: %w", err)
		}
		return body, total, nil

	case http.StatusOK:
		// Server ignored Range and sent the full body. We deliberately
		// honour it and skip the chunked loop — this matches pre-#1741
		// behaviour for files small enough that Feishu doesn't truncate.
		body, err := io.ReadAll(io.LimitReader(resp.Body, p.resourceMaxBytes+1))
		if err != nil {
			return nil, 0, fmt.Errorf("read full body: %w", err)
		}
		if int64(len(body)) > p.resourceMaxBytes {
			return nil, 0, fmt.Errorf("resource too large: body exceeds cap %d", p.resourceMaxBytes)
		}
		return body, 0, nil

	default:
		return nil, 0, fmt.Errorf("first-chunk: unexpected status %d", resp.StatusCode)
	}
}

// resourceFetchRemainingChunks loops Range GETs starting after the first
// byte, concatenates them with `first`, and verifies the total size matches
// what the probe advertised.
func (p *Platform) resourceFetchRemainingChunks(ctx context.Context, token, messageID, fileKey, resType string, total int64, first []byte) ([]byte, error) {
	buf := bytes.NewBuffer(make([]byte, 0, total))
	buf.Write(first)

	chunkSize := p.resourceChunkSize
	if chunkSize > resourceRangeMaxRangeHeaderBytes {
		chunkSize = resourceRangeMaxRangeHeaderBytes
	}
	chunks := 1 // count the first byte we already have
	for offset := int64(1); offset < total; offset += chunkSize {
		end := offset + chunkSize - 1
		if end >= total {
			end = total - 1
		}
		n, err := p.resourceRangeChunk(ctx, token, messageID, fileKey, resType, offset, end, total)
		if err != nil {
			return nil, fmt.Errorf("resource chunk offset=%d: %w", offset, err)
		}
		buf.Write(n)
		chunks++
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}

	if int64(buf.Len()) != total {
		return nil, fmt.Errorf("resource size mismatch: assembled=%d expected=%d", buf.Len(), total)
	}
	slog.Info(p.tag()+": resource chunked download complete",
		"file_key", fileKey, "type", resType, "total", total, "chunks", chunks,
		"chunk_size", chunkSize)
	return buf.Bytes(), nil
}

// parseContentRangeTotal extracts the "total" field from a Content-Range
// header of the form "bytes 0-0/12345". Returns false if the header is
// malformed or uses a unit we don't recognise.
func parseContentRangeTotal(h string) (int64, bool) {
	h = strings.TrimSpace(h)
	slash := strings.LastIndex(h, "/")
	if slash < 0 || slash == len(h)-1 {
		return 0, false
	}
	totalStr := strings.TrimSpace(h[slash+1:])
	if totalStr == "*" {
		return 0, false
	}
	n, err := strconv.ParseInt(totalStr, 10, 64)
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

// resourceURL builds the Feishu message-resource endpoint URL. Feishu
// serves this on the same base URL as the rest of the API; we use the
// configured domain so Lark international deployments also work.
func (p *Platform) resourceURL(messageID, fileKey, resType string) string {
	return fmt.Sprintf("%s/open-apis/im/v1/messages/%s/resources/%s?type=%s",
		strings.TrimRight(p.domain, "/"), messageID, fileKey, resType)
}

// resourceSingleGet downloads the entire resource with one plain GET. Used
// for small files and as a fallback when the size probe fails. We honour
// resourceMaxBytes via Content-Length + body cap so a misbehaving server
// can't blow up memory.
func (p *Platform) resourceSingleGet(ctx context.Context, token, messageID, fileKey, resType string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.resourceURL(messageID, fileKey, resType), nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := p.resourceDownloadHTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("resource request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4*1024))
		return nil, fmt.Errorf("resource API status=%d body=%q", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	if cl := resp.ContentLength; cl > p.resourceMaxBytes {
		return nil, fmt.Errorf("resource too large: Content-Length=%d exceeds cap %d", cl, p.resourceMaxBytes)
	}

	// LimitReader caps the body too in case the server lies about
	// Content-Length; we read up to cap+1 bytes to detect the lie.
	data, err := io.ReadAll(io.LimitReader(resp.Body, p.resourceMaxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read resource: %w", err)
	}
	if int64(len(data)) > p.resourceMaxBytes {
		return nil, fmt.Errorf("resource too large: body exceeds cap %d", p.resourceMaxBytes)
	}
	slog.Debug(p.tag()+": resource downloaded (single GET)",
		"file_key", fileKey, "type", resType, "size", len(data))
	return data, nil
}

// resourceRangeChunk fetches a single byte range and verifies the
// Content-Range header agrees with what we asked for. Returns the bytes
// received; caller is responsible for ordering into the final buffer.
//
// Transient errors are retried with backoff; non-transient errors (4xx, 5xx
// other than the documented transient cases) fail fast.
func (p *Platform) resourceRangeChunk(
	ctx context.Context,
	token, messageID, fileKey, resType string,
	start, end, expectedTotal int64,
) ([]byte, error) {
	var lastErr error
	delay := transientRetryInitial
	for attempt := 0; attempt <= maxTransientRetries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(delay):
			}
			delay *= 2
			if delay > transientRetryMaxDelay {
				delay = transientRetryMaxDelay
			}
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet,
			p.resourceURL(messageID, fileKey, resType), nil)
		if err != nil {
			return nil, fmt.Errorf("build range request: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end))

		resp, err := p.resourceDownloadHTTP.Do(req)
		if err != nil {
			if isTransientError(err) && attempt < maxTransientRetries {
				lastErr = err
				slog.Debug(p.tag()+": transient range chunk error; retrying",
					"attempt", attempt, "error", err, "start", start, "end", end)
				continue
			}
			return nil, fmt.Errorf("range request: %w", err)
		}

		if resp.StatusCode == http.StatusPartialContent {
			cr := resp.Header.Get("Content-Range")
			if cr != "" {
				if got, ok := parseContentRangeTotal(cr); ok && got != expectedTotal {
					_ = resp.Body.Close()
					return nil, fmt.Errorf("Content-Range total mismatch: got %d want %d", got, expectedTotal)
				}
			}
			body, err := io.ReadAll(io.LimitReader(resp.Body, end-start+1+1))
			_ = resp.Body.Close()
			if err != nil {
				if isTransientError(err) && attempt < maxTransientRetries {
					lastErr = err
					continue
				}
				return nil, fmt.Errorf("read range body: %w", err)
			}
			if int64(len(body)) != end-start+1 {
				return nil, fmt.Errorf("range body length %d != requested %d", len(body), end-start+1)
			}
			return body, nil
		}

		// Non-206 response: capture a snippet for diagnostics, then either
		// retry (transient 5xx) or fail fast.
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4*1024))
		_ = resp.Body.Close()

		if resp.StatusCode >= 500 && resp.StatusCode < 600 && attempt < maxTransientRetries {
			lastErr = fmt.Errorf("range status %d body=%q", resp.StatusCode, strings.TrimSpace(string(body)))
			continue
		}

		if resp.StatusCode >= 200 && resp.StatusCode < 300 && end-start+1 == int64(len(body)) {
			// Server ignored Range and returned a 200 with exactly the bytes
			// we wanted — fine for the last chunk of a file, slightly off for
			// non-tail chunks. Caller decides; we return what we got.
			return body, nil
		}

		return nil, fmt.Errorf("range request status=%d body=%q", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	if lastErr == nil {
		lastErr = errors.New("range chunk retries exhausted")
	}
	return nil, fmt.Errorf("range chunk retries exhausted: %w", lastErr)
}

// defaultResourceChunkSize returns the chunk size used when a Platform is
// constructed without going through newPlatform (tests, manual fixtures).
// Matches the production default of 8 MiB.
func defaultResourceChunkSize() int64 { return 8 * 1024 * 1024 }

// fetchResourceTokenOrDefault returns the bearer token used for resource
// downloads. Tests can inject a stub via Platform.fetchResourceToken;
// production callers fall through to fetchFreshTenantAccessToken which uses
// the lark SDK to mint a fresh tenant token on demand.
func (p *Platform) fetchResourceTokenOrDefault(ctx context.Context) (string, error) {
	if p.fetchResourceToken != nil {
		return p.fetchResourceToken(ctx)
	}
	return p.fetchFreshTenantAccessToken(ctx)
}
