package core

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func captureSetupLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return &logs
}

func TestSetupWeixinPollLogsOmitCredential(t *testing.T) {
	logs := captureSetupLogs(t)
	const marker = "SENSITIVE_SENTINEL"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"confirmed","bot_token":"` + marker + `","ilink_bot_id":"bot"}`))
	}))
	defer server.Close()

	mgmt := NewManagementServer(0, "test", nil)
	result, code := mgmtPostHandler(t, mgmt.handleSetupWeixinPoll, "/api/v1/setup/weixin/poll", map[string]any{
		"api_url": server.URL,
		"qr_key":  "QR_SENTINEL",
	})
	if code != http.StatusOK || !result.OK {
		t.Fatalf("poll failed: status=%d error=%q", code, result.Error)
	}
	for _, secret := range []string{marker, "QR_SENTINEL"} {
		if strings.Contains(logs.String(), secret) {
			t.Fatal("Weixin setup log exposed a QR credential")
		}
	}
}

func TestSetupWeixinBeginLogsOmitQRCode(t *testing.T) {
	logs := captureSetupLogs(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"qrcode":"QR_SENTINEL","qrcode_img_content":"QR_URL_SENTINEL"}`))
	}))
	defer server.Close()

	mgmt := NewManagementServer(0, "test", nil)
	result, code := mgmtPostHandler(t, mgmt.handleSetupWeixinBegin, "/api/v1/setup/weixin/begin", map[string]any{"api_url": server.URL})
	if code != http.StatusOK || !result.OK {
		t.Fatalf("begin failed: status=%d error=%q", code, result.Error)
	}
	for _, secret := range []string{"QR_SENTINEL", "QR_URL_SENTINEL"} {
		if strings.Contains(logs.String(), secret) {
			t.Fatal("Weixin setup log exposed QR data")
		}
	}
}

func TestSetupWeixinPollNetworkErrorOmitsQRKey(t *testing.T) {
	logs := captureSetupLogs(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		_ = conn.Close()
	}))
	defer server.Close()

	mgmt := NewManagementServer(0, "test", nil)
	result, code := mgmtPostHandler(t, mgmt.handleSetupWeixinPoll, "/api/v1/setup/weixin/poll", map[string]any{
		"api_url": server.URL,
		"qr_key":  "QR_SENTINEL",
	})
	if code != http.StatusOK || !result.OK {
		t.Fatalf("poll fallback failed: status=%d error=%q", code, result.Error)
	}
	if strings.Contains(logs.String(), "QR_SENTINEL") {
		t.Fatal("Weixin network error log exposed the QR key")
	}
}
