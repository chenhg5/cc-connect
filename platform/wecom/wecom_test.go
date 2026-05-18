package wecom

import (
	"crypto/sha1"
	"encoding/hex"
	"net/url"
	"sort"
	"strings"
	"testing"
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

// wecomSignature is a local re-implementation of the signature scheme so
// the test exercises verifySignature against expected outputs without
// depending on the production helper itself.
func wecomSignature(token, timestamp, nonce, encrypt string) string {
	parts := []string{token, timestamp, nonce, encrypt}
	sort.Strings(parts)
	h := sha1.New()
	h.Write([]byte(strings.Join(parts, "")))
	return hex.EncodeToString(h.Sum(nil))
}

func TestVerifySignature_AcceptsMatchingDigest(t *testing.T) {
	p := &Platform{token: "tok"}
	sig := wecomSignature(p.token, "1700000000", "abc", "ciphertext")
	if !p.verifySignature(sig, "1700000000", "abc", "ciphertext") {
		t.Fatalf("verifySignature should accept a matching SHA1 digest")
	}
}

func TestVerifySignature_RejectsMismatchedDigest(t *testing.T) {
	p := &Platform{token: "tok"}
	good := wecomSignature(p.token, "1700000000", "abc", "ciphertext")
	cases := []string{
		"",                                  // empty signature
		"deadbeef",                          // too short
		good[:len(good)-1] + "x",            // last byte wrong
		"y" + good[1:],                      // first byte wrong
		good + "0",                          // length mismatch (longer)
		good[:len(good)-1],                  // length mismatch (shorter)
		"INVALID-NON-HEX-SIGNATURE-VALUE!!", // garbage
	}
	for _, bad := range cases {
		if p.verifySignature(bad, "1700000000", "abc", "ciphertext") {
			t.Errorf("verifySignature should reject %q", bad)
		}
	}
}

func TestVerifySignature_DigestIsLowercaseHex(t *testing.T) {
	p := &Platform{token: "tok"}
	good := wecomSignature(p.token, "1700000000", "abc", "ciphertext")
	if good != strings.ToLower(good) {
		t.Fatalf("local helper produced non-lowercase digest %q", good)
	}
	// Uppercase variant must not be accepted by a constant-time comparator.
	upper := strings.ToUpper(good)
	if upper == good {
		t.Skip("digest has no letters; case check is degenerate")
	}
	if p.verifySignature(upper, "1700000000", "abc", "ciphertext") {
		t.Fatalf("verifySignature should reject uppercase-hex digest (constant-time compare is case sensitive)")
	}
}

