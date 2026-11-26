package core

import "testing"

func TestSafeURLHostOmitsURLCredentials(t *testing.T) {
	const secret = "SIGNED-URL-SENTINEL"
	if got := SafeURLHost("https://user:" + secret + "@cdn.example/private-" + secret + "?sig=" + secret + "#" + secret); got != "cdn.example" {
		t.Fatalf("host = %q, want cdn.example", got)
	}
	if got := SafeURLHost("://" + secret); got != "unknown" {
		t.Fatalf("malformed URL host = %q, want unknown", got)
	}
}
