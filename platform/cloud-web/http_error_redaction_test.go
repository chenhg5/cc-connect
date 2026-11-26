package cloudweb

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPollRegisterErrorOmitsResponseSecret(t *testing.T) {
	const marker = "SENSITIVE_SENTINEL"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"message":"temporary failure","secret":"` + marker + `"}`))
	}))
	defer server.Close()
	transport := newPollTransport(server.URL, marker, "test", "project", "", "", 1000)
	_, err := transport.register(context.Background())
	if err == nil || !strings.Contains(err.Error(), "502") {
		t.Fatalf("register error = %v, want HTTP status", err)
	}
	if strings.Contains(err.Error(), marker) {
		t.Fatal("cloud-web register error exposed response secret")
	}
}
