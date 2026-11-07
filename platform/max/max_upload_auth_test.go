package max

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestUploadAttachmentKeepsAuthOnAPIOrigin(t *testing.T) {
	apiAuth := make(chan string, 1)
	uploadAuth := make(chan string, 1)
	var api *httptest.Server
	api = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/uploads":
			apiAuth <- r.Header.Get("Authorization")
			_ = json.NewEncoder(w).Encode(map[string]string{"url": api.URL + "/upload"})
		case "/upload":
			uploadAuth <- r.Header.Get("Authorization")
			_, _ = w.Write([]byte(`{"token":"uploaded"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()

	p := newTestPlatform(t, api.URL)
	if _, err := p.uploadAttachment(context.Background(), "file", []byte("payload"), "a.txt"); err != nil {
		t.Fatal(err)
	}
	if got := <-apiAuth; got != "test-token" {
		t.Errorf("API auth = %q, want bot token", got)
	}
	if got := <-uploadAuth; got != "test-token" {
		t.Errorf("same-origin upload auth = %q, want bot token", got)
	}
}

func TestSameOriginForBotAuthorization(t *testing.T) {
	api, _ := url.Parse("https://api.example.com")
	for _, tc := range []struct {
		url  string
		want bool
	}{
		{"https://API.example.com:443/upload", true},
		{"http://api.example.com/upload", false},
		{"https://api.example.com:444/upload", false},
		{"https://api.example.com.evil.test/upload", false},
		{"https://cdn.api.example.com/upload", false},
	} {
		t.Run(tc.url, func(t *testing.T) {
			target, err := url.Parse(tc.url)
			if err != nil {
				t.Fatal(err)
			}
			if got := sameOrigin(target, api); got != tc.want {
				t.Errorf("sameOrigin(%q, %q) = %t, want %t", target, api, got, tc.want)
			}
		})
	}
}

func TestUploadAttachmentDoesNotForwardAuthToDifferentOriginCDN(t *testing.T) {
	cdnAuth := make(chan string, 1)
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cdnAuth <- r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"token":"uploaded"}`))
	}))
	defer cdn.Close()
	apiAuth := make(chan string, 1)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiAuth <- r.Header.Get("Authorization")
		_ = json.NewEncoder(w).Encode(map[string]string{"url": cdn.URL + "/upload"})
	}))
	defer api.Close()

	p := newTestPlatform(t, api.URL)
	if _, err := p.uploadAttachment(context.Background(), "file", []byte("payload"), "a.txt"); err != nil {
		t.Fatal(err)
	}
	if got := <-apiAuth; got != "test-token" {
		t.Errorf("API auth = %q, want bot token", got)
	}
	if got := <-cdnAuth; got != "" {
		t.Errorf("different-origin CDN auth = %q, want empty", got)
	}
}

func TestUploadAttachmentDoesNotForwardAuthToArbitraryOrigin(t *testing.T) {
	attackerAuth := make(chan string, 1)
	attacker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attackerAuth <- r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"token":"uploaded"}`))
	}))
	defer attacker.Close()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"url": attacker.URL + "/upload"})
	}))
	defer api.Close()

	p := newTestPlatform(t, api.URL)
	if _, err := p.uploadAttachment(context.Background(), "file", []byte("payload"), "a.txt"); err != nil {
		t.Fatal(err)
	}
	if got := <-attackerAuth; got != "" {
		t.Errorf("arbitrary-origin auth = %q, want empty", got)
	}
}

func TestUploadAttachmentRedirectDropsAuthOnOriginChange(t *testing.T) {
	redirectAuth := make(chan string, 1)
	redirectTarget := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirectAuth <- r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"token":"uploaded"}`))
	}))
	defer redirectTarget.Close()
	var api *httptest.Server
	api = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/uploads":
			_ = json.NewEncoder(w).Encode(map[string]string{"url": api.URL + "/redirect"})
		case "/redirect":
			http.Redirect(w, r, redirectTarget.URL+"/upload", http.StatusTemporaryRedirect)
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()

	p := newTestPlatform(t, api.URL)
	if _, err := p.uploadAttachment(context.Background(), "file", []byte("payload"), "a.txt"); err != nil {
		t.Fatal(err)
	}
	if got := <-redirectAuth; got != "" {
		t.Errorf("redirect target auth = %q, want empty", got)
	}
}

func TestUploadURLRedirectDropsAuthOnOriginChange(t *testing.T) {
	redirectAuth := make(chan string, 1)
	var redirectTarget *httptest.Server
	redirectTarget = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/uploads":
			redirectAuth <- r.Header.Get("Authorization")
			_ = json.NewEncoder(w).Encode(map[string]string{"url": redirectTarget.URL + "/upload"})
		case "/upload":
			_, _ = w.Write([]byte(`{"token":"uploaded"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer redirectTarget.Close()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, redirectTarget.URL+"/uploads", http.StatusTemporaryRedirect)
	}))
	defer api.Close()

	p := newTestPlatform(t, api.URL)
	if _, err := p.uploadAttachment(context.Background(), "file", []byte("payload"), "a.txt"); err != nil {
		t.Fatal(err)
	}
	if got := <-redirectAuth; got != "" {
		t.Errorf("upload URL redirect auth = %q, want empty", got)
	}
}

func TestUploadAttachmentPreservesErrors(t *testing.T) {
	t.Run("API error", func(t *testing.T) {
		api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "no upload URL", http.StatusBadGateway)
		}))
		defer api.Close()
		p := newTestPlatform(t, api.URL)
		_, err := p.uploadAttachment(context.Background(), "file", []byte("payload"), "a.txt")
		if err == nil || !strings.Contains(err.Error(), "upload url: HTTP 502") {
			t.Fatalf("error = %v, want upload URL 502", err)
		}
	})
	t.Run("CDN error", func(t *testing.T) {
		cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "rejected", http.StatusForbidden)
		}))
		defer cdn.Close()
		api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]string{"url": cdn.URL + "/upload"})
		}))
		defer api.Close()
		p := newTestPlatform(t, api.URL)
		_, err := p.uploadAttachment(context.Background(), "file", []byte("payload"), "a.txt")
		if err == nil || !strings.Contains(err.Error(), "cdn upload: HTTP 403") {
			t.Fatalf("error = %v, want CDN 403", err)
		}
	})
}
