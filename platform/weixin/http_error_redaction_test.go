package weixin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestIlinkHTTPErrorOmitsResponseSecret(t *testing.T) {
	const marker = "SENSITIVE_SENTINEL"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"errmsg":"temporary failure","secret":"` + marker + `"}`))
	}))
	defer server.Close()
	client := newAPIClient(server.URL, marker, "", server.Client())
	_, err := client.post(context.Background(), "test", []byte(`{}`), 0, "test")
	if err == nil || !strings.Contains(err.Error(), "502") {
		t.Fatalf("post error = %v, want HTTP status", err)
	}
	if strings.Contains(err.Error(), marker) {
		t.Fatal("Weixin HTTP error exposed response secret")
	}
}
