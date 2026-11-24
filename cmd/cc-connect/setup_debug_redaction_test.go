package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func captureSetupStderr(t *testing.T, run func()) string {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	previous := os.Stderr
	os.Stderr = writer
	defer func() {
		os.Stderr = previous
		_ = reader.Close()
		_ = writer.Close()
	}()
	run()
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	os.Stderr = previous
	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	return string(output)
}

func TestWeixinSetupDebugLogsOmitResponseCredentials(t *testing.T) {
	const marker = "SENSITIVE_SENTINEL"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ilink/bot/get_bot_qrcode":
			_, _ = w.Write([]byte(`{"qrcode":"QR_SENTINEL","qrcode_img_content":"QR_URL_SENTINEL"}`))
		case "/ilink/bot/get_qrcode_status":
			_, _ = w.Write([]byte(`{"status":"confirmed","bot_token":"` + marker + `"}`))
		case "/ilink/bot/getupdates":
			_, _ = w.Write([]byte(`{"bot_token":"` + marker + `"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	logs := captureSetupStderr(t, func() {
		if _, err := weixinFetchBotQRCode(context.Background(), server.URL, defaultWeixinBotType, "", true); err != nil {
			t.Error(err)
		}
		if _, err := weixinPollQRStatus(context.Background(), server.URL, "QR_SENTINEL", "", true); err != nil {
			t.Error(err)
		}
		if err := verifyWeixinToken(context.Background(), server.URL, "test", "", true); err != nil {
			t.Error(err)
		}
	})
	for _, secret := range []string{marker, "QR_SENTINEL", "QR_URL_SENTINEL"} {
		if strings.Contains(logs, secret) {
			t.Fatal("Weixin CLI debug output exposed setup credentials")
		}
	}
}

func TestFeishuSetupDebugLogsOmitResponseCredentials(t *testing.T) {
	const marker = "SENSITIVE_SENTINEL"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"app_secret":"` + marker + `"}`))
	}))
	defer server.Close()

	client := &registrationClient{baseURL: server.URL, http: server.Client(), debug: true}
	logs := captureSetupStderr(t, func() {
		var result map[string]any
		if err := client.registrationCall("init", nil, &result); err != nil {
			t.Error(err)
		}
	})
	if strings.Contains(logs, marker) {
		t.Fatal("Feishu CLI debug output exposed setup credentials")
	}
}
