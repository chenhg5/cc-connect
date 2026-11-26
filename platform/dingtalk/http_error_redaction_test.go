package dingtalk

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestUploadMediaErrorOmitsResponseSecret(t *testing.T) {
	const marker = "SENSITIVE_SENTINEL"
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusBadGateway, Body: io.NopCloser(strings.NewReader(`{"errmsg":"temporary failure","secret":"` + marker + `"}`)), Header: make(http.Header)}, nil
	})}
	p := &Platform{httpClient: client, accessToken: marker, tokenExpiry: time.Now().Add(time.Hour)}
	_, err := p.uploadMedia(context.Background(), []byte("image"), "image.png", "image")
	if err == nil || !strings.Contains(err.Error(), "502") {
		t.Fatalf("upload error = %v, want HTTP status", err)
	}
	if strings.Contains(err.Error(), marker) {
		t.Fatal("DingTalk upload error exposed response secret")
	}
}
