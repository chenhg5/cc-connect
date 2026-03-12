package codex

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestFetchUsage_Success(t *testing.T) {
	agent := &Agent{}
	client := &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if got := req.URL.String(); got != "https://chatgpt.com/backend-api/wham/usage" {
				t.Fatalf("request URL = %q", got)
			}
			if got := req.Header.Get("Authorization"); got != "Bearer token-123" {
				t.Fatalf("authorization = %q", got)
			}
			if got := req.Header.Get("ChatGPT-Account-Id"); got != "acct-123" {
				t.Fatalf("account id header = %q", got)
			}
			body := `{
			  "user_id": "user-1",
			  "account_id": "acct-123",
			  "email": "dev@example.com",
			  "plan_type": "team",
			  "rate_limit": {
			    "allowed": true,
			    "limit_reached": false,
			    "primary_window": {
			      "used_percent": 23,
			      "limit_window_seconds": 18000,
			      "reset_after_seconds": 6665,
			      "reset_at": 1773292109
			    },
			    "secondary_window": {
			      "used_percent": 42,
			      "limit_window_seconds": 604800,
			      "reset_after_seconds": 512698,
			      "reset_at": 1773798142
			    }
			  },
			  "code_review_rate_limit": {
			    "allowed": true,
			    "limit_reached": false,
			    "primary_window": {
			      "used_percent": 0,
			      "limit_window_seconds": 604800,
			      "reset_after_seconds": 604800,
			      "reset_at": 1773890244
			    },
			    "secondary_window": null
			  },
			  "credits": {
			    "has_credits": false,
			    "unlimited": false,
			    "balance": null
			  }
			}`
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(body)),
			}, nil
		}),
	}

	report, err := agent.fetchUsage(context.Background(), client, codexOAuthTokens{
		AccessToken: "token-123",
		AccountID:   "acct-123",
	})
	if err != nil {
		t.Fatalf("fetchUsage returned error: %v", err)
	}
	if report.Provider != "codex" {
		t.Fatalf("provider = %q, want codex", report.Provider)
	}
	if report.Plan != "team" {
		t.Fatalf("plan = %q, want team", report.Plan)
	}
	if len(report.Buckets) != 2 {
		t.Fatalf("buckets = %d, want 2", len(report.Buckets))
	}
	if got := report.Buckets[0].Windows[0].UsedPercent; got != 23 {
		t.Fatalf("primary used percent = %d, want 23", got)
	}
	if got := report.Buckets[0].Windows[1].UsedPercent; got != 42 {
		t.Fatalf("secondary used percent = %d, want 42", got)
	}
}

func TestFetchUsage_HTTPError(t *testing.T) {
	agent := &Agent{}
	client := &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusUnauthorized,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"error":"unauthorized"}`)),
			}, nil
		}),
	}

	_, err := agent.fetchUsage(context.Background(), client, codexOAuthTokens{
		AccessToken: "token-123",
		AccountID:   "acct-123",
	})
	if err == nil || !strings.Contains(err.Error(), "status 401") {
		t.Fatalf("err = %v, want status 401", err)
	}
}

func TestReadOAuthTokens_MissingFields(t *testing.T) {
	agent := &Agent{}
	_, err := agent.readOAuthTokens(func(string) ([]byte, error) {
		return []byte(`{"tokens":{"access_token":"token-only"}}`), nil
	})
	if err == nil || !strings.Contains(err.Error(), "account_id") {
		t.Fatalf("err = %v, want missing account_id", err)
	}
}

func TestReadOAuthTokens_InvalidJSON(t *testing.T) {
	agent := &Agent{}
	_, err := agent.readOAuthTokens(func(string) ([]byte, error) {
		return []byte(`{`), nil
	})
	if err == nil || !strings.Contains(err.Error(), "parse") {
		t.Fatalf("err = %v, want parse error", err)
	}
}
