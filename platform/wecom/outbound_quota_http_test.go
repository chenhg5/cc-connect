package wecom

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

type quotaRoundTripper func(*http.Request) (*http.Response, error)

func (f quotaRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func quotaHTTPResponse(body string) *http.Response {
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func TestHTTPQuota_RefreshesTokenAfterLongWait(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		q := testQuota(t, &quotaRegistry{}, testLimits(1, 100), "token")
		takeQuota(t, q, quotaTarget{recipient: "user"}).finish(true)
		refreshes, sends := 0, 0
		p := &Platform{quota: q, agentID: "1", tokenCache: tokenCache{token: "old", expiresAt: time.Now().Add(30 * time.Second)}}
		p.apiClient = &http.Client{Transport: quotaRoundTripper(func(req *http.Request) (*http.Response, error) {
			if req.URL.Path == "/cgi-bin/gettoken" {
				refreshes++
				return quotaHTTPResponse(`{"access_token":"fresh","expires_in":7200}`), nil
			}
			sends++
			if got := req.URL.Query().Get("access_token"); got != "fresh" {
				t.Errorf("stale token after quota wait: %q", got)
			}
			return quotaHTTPResponse(`{"errcode":0}`), nil
		})}
		start := time.Now()
		if err := p.Send(context.Background(), replyContext{userID: "user"}, "hello"); err != nil {
			t.Fatal(err)
		}
		if refreshes != 1 || sends != 1 || time.Since(start) != time.Minute {
			t.Fatalf("refreshes=%d sends=%d waited=%v", refreshes, sends, time.Since(start))
		}
	})
}

func TestHTTPQuota_TokenFailureDoesNotConsumeMessagePermit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		q := testQuota(t, &quotaRegistry{}, testLimits(1, 1), "token")
		attempts := 0
		fail := errors.New("token unavailable")
		p := &Platform{quota: q, agentID: "1"}
		p.apiClient = &http.Client{Transport: quotaRoundTripper(func(req *http.Request) (*http.Response, error) {
			attempts++
			if attempts == 1 {
				return nil, fail
			}
			if req.URL.Path == "/cgi-bin/gettoken" {
				return quotaHTTPResponse(`{"access_token":"fresh","expires_in":7200}`), nil
			}
			return quotaHTTPResponse(`{"errcode":0}`), nil
		})}
		if err := p.Send(context.Background(), replyContext{userID: "user"}, "first"); !errors.Is(err, fail) {
			t.Fatal(err)
		}
		start := time.Now()
		if err := p.Send(context.Background(), replyContext{userID: "user"}, "second"); err != nil {
			t.Fatal(err)
		}
		if time.Since(start) != 0 {
			t.Fatal("unattempted message consumed quota")
		}
	})
}

func TestHTTPQuota_CancellationReachesTokenRequest(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		q := testQuota(t, &quotaRegistry{}, testLimits(1, 1), "token")
		p := &Platform{quota: q, agentID: "1"}
		started := make(chan struct{})
		p.apiClient = &http.Client{Transport: quotaRoundTripper(func(req *http.Request) (*http.Response, error) {
			close(started)
			<-req.Context().Done()
			return nil, req.Context().Err()
		})}
		done := make(chan error, 1)
		go func() { done <- p.Send(context.Background(), replyContext{userID: "user"}, "send") }()
		<-started
		if err := p.Stop(); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("request ignored stop: %v", err)
		}
	})
}
