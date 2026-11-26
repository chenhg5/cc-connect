package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestWeixinFetchQRCodeSendsClientVersion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ilink/bot/get_bot_qrcode" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		if got := r.Header.Get("iLink-App-ClientVersion"); got != "1" {
			t.Errorf("QR request client version = %q, want 1", got)
		}
		_, _ = fmt.Fprint(w, `{"qrcode":"test-key","qrcode_img_content":"https://example.invalid/qr"}`)
	}))
	defer server.Close()

	if _, err := weixinFetchBotQRCode(context.Background(), server.URL, defaultWeixinBotType, "", false); err != nil {
		t.Fatal(err)
	}
}

func TestWeixinQRLoginRejectsUpgradeWithoutWaiting(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ilink/bot/get_bot_qrcode":
			_, _ = fmt.Fprint(w, `{"qrcode":"test-key","qrcode_img_content":"https://example.invalid/qr"}`)
		case "/ilink/bot/get_qrcode_status":
			_, _ = fmt.Fprint(w, `{"status":"need_upgrade","ret":-1,"errmsg":"please upgrade ilink"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	start := time.Now()
	_, err := runWeixinQRLoginFlow(weixinQRLoginOptions{APIBaseURL: server.URL, Timeout: 3 * time.Second})
	if err == nil || !strings.Contains(err.Error(), "please upgrade ilink") {
		t.Fatalf("expected upstream upgrade reason, got %v", err)
	}
	if elapsed := time.Since(start); elapsed >= 2*time.Second {
		t.Fatalf("upgrade rejection waited %v instead of returning promptly", elapsed)
	}
}
