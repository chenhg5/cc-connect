package weibo

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRefreshTokenErrorOmitsResponseSecret(t *testing.T) {
	const marker = "SENSITIVE_SENTINEL"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"message":"temporary failure","secret":"` + marker + `"}`))
	}))
	defer server.Close()
	p := &Platform{tokenEndpoint: server.URL, appSecret: marker}
	_, err := p.refreshToken()
	if err == nil || !strings.Contains(err.Error(), "502") {
		t.Fatalf("token error = %v, want HTTP status", err)
	}
	if strings.Contains(err.Error(), marker) {
		t.Fatal("Weibo token error exposed response secret")
	}
}
