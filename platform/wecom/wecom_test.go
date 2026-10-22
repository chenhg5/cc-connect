package wecom

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestWeComAPIURL_DefaultBase(t *testing.T) {
	p := &Platform{}
	got := p.wecomAPIURL("/cgi-bin/gettoken", url.Values{
		"corpid":     []string{"ww-test"},
		"corpsecret": []string{"sec-test"},
	})
	want := "https://qyapi.weixin.qq.com/cgi-bin/gettoken?corpid=ww-test&corpsecret=sec-test"
	if got != want {
		t.Fatalf("url = %q, want %q", got, want)
	}
}

func TestWeComAPIURL_CustomBase(t *testing.T) {
	p := &Platform{apiBaseURL: "https://wecom.internal.example.com/"}
	got := p.wecomAPIURL("/cgi-bin/message/send", url.Values{
		"access_token": []string{"tok"},
	})
	want := "https://wecom.internal.example.com/cgi-bin/message/send?access_token=tok"
	if got != want {
		t.Fatalf("url = %q, want %q", got, want)
	}
}

func TestNew_DefaultAPIBaseURL(t *testing.T) {
	pf, err := New(map[string]any{
		"corp_id":          "ww_test",
		"corp_secret":      "sec_test",
		"agent_id":         "1000002",
		"callback_token":   "cb_token",
		"callback_aes_key": "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
	})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}

	p, ok := pf.(*Platform)
	if !ok {
		t.Fatalf("platform type = %T, want *wecom.Platform", pf)
	}
	if p.apiBaseURL != defaultAPIBaseURL {
		t.Fatalf("apiBaseURL = %q, want %q", p.apiBaseURL, defaultAPIBaseURL)
	}
}

func TestNew_CustomAPIBaseURL_TrimTrailingSlash(t *testing.T) {
	pf, err := New(map[string]any{
		"corp_id":          "ww_test",
		"corp_secret":      "sec_test",
		"agent_id":         "1000002",
		"callback_token":   "cb_token",
		"callback_aes_key": "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		"api_base_url":     "https://wecom.internal.example.com/",
	})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}

	p, ok := pf.(*Platform)
	if !ok {
		t.Fatalf("platform type = %T, want *wecom.Platform", pf)
	}
	if p.apiBaseURL != "https://wecom.internal.example.com" {
		t.Fatalf("apiBaseURL = %q, want %q", p.apiBaseURL, "https://wecom.internal.example.com")
	}
}

// fakeWeComTokenRT serves a single canned /cgi-bin/gettoken response so
// getAccessToken's cache arithmetic can be exercised without network.
type fakeWeComTokenRT struct {
	body string
}

func (f *fakeWeComTokenRT) RoundTrip(req *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(f.body)),
		Header:     make(http.Header),
		Request:    req,
	}, nil
}

func TestGetAccessToken_ZeroExpiresIn_FallsBackToDefault(t *testing.T) {
	p := &Platform{
		corpID:     "ww_test",
		corpSecret: "sec_test",
		apiBaseURL: defaultAPIBaseURL,
		apiClient: &http.Client{
			Transport: &fakeWeComTokenRT{body: `{"errcode":0,"errmsg":"ok","access_token":"tok-zero","expires_in":0}`},
		},
	}

	before := time.Now()
	tok, err := p.getAccessToken()
	if err != nil {
		t.Fatalf("getAccessToken() error = %v", err)
	}
	if tok != "tok-zero" {
		t.Fatalf("token = %q, want %q", tok, "tok-zero")
	}

	// Without the fallback, expiresAt = time.Now() - 60s, so the cached
	// token would be stale on the very next call and every getAccessToken
	// invocation would re-fetch from /cgi-bin/gettoken.
	window := p.tokenCache.expiresAt.Sub(before)
	if window < time.Hour {
		t.Errorf("tokenCache window for expires_in=0 = %v, want >= 1h (zero should fall back, not cache for -60s)", window)
	}
}

func TestGetAccessToken_NormalExpiresIn_AppliesBuffer(t *testing.T) {
	p := &Platform{
		corpID:     "ww_test",
		corpSecret: "sec_test",
		apiBaseURL: defaultAPIBaseURL,
		apiClient: &http.Client{
			Transport: &fakeWeComTokenRT{body: `{"errcode":0,"errmsg":"ok","access_token":"tok-7200","expires_in":7200}`},
		},
	}

	before := time.Now()
	if _, err := p.getAccessToken(); err != nil {
		t.Fatalf("getAccessToken() error = %v", err)
	}
	// 7200 - 60 buffer = 7140s = 119 min. Allow a wide tolerance for elapsed time.
	window := p.tokenCache.expiresAt.Sub(before)
	if window < 110*time.Minute || window > 120*time.Minute {
		t.Errorf("tokenCache window for expires_in=7200 = %v, want ~7140s (110-120min)", window)
	}
}

