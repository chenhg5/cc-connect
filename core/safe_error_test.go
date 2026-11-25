package core

import (
	"errors"
	"net/url"
	"strings"
	"testing"
)

func TestSafeErrorRedactsURLAndKeepsNetworkDiagnosis(t *testing.T) {
	const sentinel = "SENTINEL_URL_SECRET"
	for _, cause := range []string{"no such host", "connection refused"} {
		err := &url.Error{Op: "Get", URL: "https://example.com/private?token=" + sentinel, Err: errors.New(cause)}
		got := SafeError(err)
		if strings.Contains(got, sentinel) || strings.Contains(got, "/private") || !strings.Contains(got, "https://example.com") || !strings.Contains(got, cause) {
			t.Fatalf("unsafe or unhelpful error: %q", got)
		}
	}
}

func TestSafeErrorFiltersOtherErrors(t *testing.T) {
	const sentinel = "SENTINEL_OTHER_SECRET"
	if got := SafeError(errors.New("connection refused")); got != "connection refused" {
		t.Fatalf("safe error = %q", got)
	}
	if got := SafeError(errors.New("credential="+sentinel), sentinel); strings.Contains(got, sentinel) {
		t.Fatalf("credential leaked: %q", got)
	}
	urlErr := &url.Error{Op: "Get", URL: "https://" + sentinel + ".example/private", Err: errors.New("connection refused")}
	if got := SafeError(urlErr, sentinel); strings.Contains(got, sentinel) || !strings.Contains(got, "connection refused") {
		t.Fatalf("known secret in URL host leaked or diagnosis lost: %q", got)
	}
	if got := SafeError(nil); got != "" {
		t.Fatalf("nil error = %q", got)
	}
}
