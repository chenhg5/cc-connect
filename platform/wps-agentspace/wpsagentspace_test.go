package wpsagentspace

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/config"
	"github.com/chenhg5/cc-connect/core"
)

func TestDecryptWpsSid(t *testing.T) {
	tests := []struct {
		name      string
		encrypted string
		appId     string
		wantErr   bool
	}{
		{
			name:      "not encrypted",
			encrypted: "plain-text-sid",
			appId:     "",
			wantErr:   false,
		},
		{
			name:      "invalid format",
			encrypted: "a:b:c",
			appId:     "",
			wantErr:   false,
		},
		{
			name:      "invalid hex",
			encrypted: "not-hex:iv:tag:data",
			appId:     "",
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := decryptWpsSid(tt.encrypted, tt.appId)
			if (err != nil) != tt.wantErr {
				t.Errorf("decryptWpsSid() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && got == "" {
				t.Errorf("decryptWpsSid() returned empty string")
			}
		})
	}
}

func TestGenerateUUID(t *testing.T) {
	uuid := generateUUID()
	if len(uuid) != 36 {
		t.Errorf("generateUUID() length = %d, want 36", len(uuid))
	}
	if uuid[8] != '-' || uuid[13] != '-' || uuid[18] != '-' || uuid[23] != '-' {
		t.Errorf("generateUUID() invalid format: %s", uuid)
	}
}

func TestExpandPath(t *testing.T) {
	home := os.Getenv("HOME")
	if home == "" {
		home = os.Getenv("USERPROFILE")
	}
	tests := []struct {
		in, want string
	}{
		{"/abs/path", "/abs/path"},
		{"~", home},
		{"~/foo", home + "/foo"},
	}
	for _, tt := range tests {
		got := expandPath(tt.in)
		if got != tt.want {
			t.Errorf("expandPath(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		s      string
		maxLen int
		want   string
	}{
		{"hello", 10, "hello"},
		{"hello world", 5, "hello..."},
		{"", 5, ""},
	}

	for _, tt := range tests {
		got := truncate(tt.s, tt.maxLen)
		if got != tt.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", tt.s, tt.maxLen, got, tt.want)
		}
	}
}

func TestGetWSURL(t *testing.T) {
	tests := []struct {
		appID string
		want  string
	}{
		{"AK20260316", "wss://agentspace.wps.cn/v7/devhub/ws/AK20260316/chat"},
		{"", "wss://agentspace.wps.cn/v7/devhub/ws/openClaw/chat"},
	}

	for _, tt := range tests {
		p := &Platform{appID: tt.appID, baseURL: defaultWSURL}
		got := p.getWSURL()
		if got != tt.want {
			t.Errorf("getWSURL() with appID=%q = %q, want %q", tt.appID, got, tt.want)
		}
	}
}

func TestEncryptDecryptWpsSid(t *testing.T) {
	originalSid := "test-wps-sid-example-value-for-testing"
	appId := "test-app-id-12345"

	// Encrypt
	encrypted, err := encryptWpsSid(originalSid, appId)
	if err != nil {
		t.Fatalf("encryptWpsSid() error: %v", err)
	}

	t.Logf("Original: %s", originalSid)
	t.Logf("Encrypted: %s", encrypted)

	// Verify format (salt:iv:authTag:ciphertext)
	parts := strings.Split(encrypted, ":")
	if len(parts) != 4 {
		t.Fatalf("encryptWpsSid() invalid format, got %d parts", len(parts))
	}

	// Decrypt
	decrypted, err := decryptWpsSid(encrypted, appId)
	if err != nil {
		t.Fatalf("decryptWpsSid() error: %v", err)
	}

	if decrypted != originalSid {
		t.Errorf("decryptWpsSid() = %q, want %q", decrypted, originalSid)
	}

	t.Logf("Decrypted: %s", decrypted)
	t.Log("Encrypt/Decrypt round-trip successful")
}

func TestDecryptWpsSid_FallbackToDefaultKeySource(t *testing.T) {
	// Token encrypted when appID was empty (uses defaultKeySource).
	legacy := "legacy-token-no-appid"
	encrypted, err := encryptWpsSid(legacy, "")
	if err != nil {
		t.Fatalf("encryptWpsSid() error: %v", err)
	}

	// Decrypted later after appID was configured — should fall back to
	// defaultKeySource and recover the token.
	got, err := decryptWpsSid(encrypted, "new-app-id")
	if err != nil {
		t.Fatalf("decryptWpsSid() error: %v", err)
	}
	if got != legacy {
		t.Errorf("decryptWpsSid() = %q, want %q (fallback did not fire)", got, legacy)
	}
}

func TestStart_DecryptsEncryptedToken(t *testing.T) {
	raw := "test-wps-sid-for-start"
	appID := "test-app-id-start"
	encrypted, err := encryptWpsSid(raw, appID)
	if err != nil {
		t.Fatalf("encryptWpsSid() error: %v", err)
	}

	p := &Platform{
		appID:      appID,
		wpsSid:     encrypted,
		deviceUuid: "dev-start",
		deviceName: "test",
	}
	if err := p.Start(func(core.Platform, *core.Message) {}); err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	defer func() { _ = p.Stop() }()

	if p.wpsSid != raw {
		t.Errorf("Start() did not decrypt wps_sid: got %q, want %q", p.wpsSid, raw)
	}
}

func TestHandleFrame_DispatchesUserMessage(t *testing.T) {
	var got *core.Message
	handlerDone := make(chan struct{})
	p := &Platform{
		appID:      "AK",
		deviceUuid: "dev",
		deviceName: "test",
	}
	p.handler = func(_ core.Platform, msg *core.Message) {
		got = msg
		close(handlerDone)
	}

	data := messageData{
		Role:      "user",
		Type:      "text",
		Content:   "hello",
		SessionID: "s1",
		ChatID:    "c1",
		MessageID: "m1",
		MediaURL:  "http://example.com/file.html",
	}
	raw, _ := json.Marshal(data)
	if err := p.handleFrame(wsFrame{Event: "message", Data: raw}); err != nil {
		t.Fatalf("handleFrame() error: %v", err)
	}

	select {
	case <-handlerDone:
	case <-time.After(time.Second):
		t.Fatal("handler was not called")
	}

	if got == nil {
		t.Fatal("expected message, got nil")
	}
	if got.SessionKey != "wps-agentspace:s1" {
		t.Errorf("SessionKey = %q, want %q", got.SessionKey, "wps-agentspace:s1")
	}
	if !strings.Contains(got.Content, "hello") {
		t.Errorf("Content missing original text: %q", got.Content)
	}
	if !strings.Contains(got.Content, "http://example.com/file.html") {
		t.Errorf("Content missing media URL: %q", got.Content)
	}
}

func TestHandleFrame_FatalErrorReturnsErr(t *testing.T) {
	p := &Platform{}
	fatalCodes := []string{
		"USER_NO_APP_PERMISSION",
		"USER_NO_OPENCLAW_PERMISSION",
		"OPENCLAW_NOT_CONFIGURED",
		"NOT_OPENCLAW_APP",
		"NOT_LOGIN",
	}

	for _, code := range fatalCodes {
		raw, _ := json.Marshal(errorData{Code: code})
		err := p.handleFrame(wsFrame{Event: "error", Data: raw})
		if err == nil {
			t.Errorf("handleFrame(%q) returned nil error", code)
		}
	}

	raw, _ := json.Marshal(errorData{Code: "UNKNOWN_ERROR"})
	if err := p.handleFrame(wsFrame{Event: "error", Data: raw}); err != nil {
		t.Errorf("handleFrame(UNKNOWN_ERROR) returned unexpected error: %v", err)
	}
}

func TestSaveToConfig_SurgicalUpdate(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	initial := `
language = "zh"

[[projects]]
name = "wps-test"

[projects.agent]
type = "claudecode"

[[projects.platforms]]
type = "wps-agentspace"

[projects.platforms.options]
app_id = "AK123"
`
	if err := os.WriteFile(configPath, []byte(initial), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	// Exercise the config.ConfigPath branch (the preferred discovery path):
	// point it at our temp file and clear the env override so it's not used.
	prevConfigPath := config.ConfigPath
	config.ConfigPath = configPath
	t.Cleanup(func() { config.ConfigPath = prevConfigPath })

	if !saveToConfig("AK123", "encrypted-token-value") {
		t.Fatal("saveToConfig returned false")
	}

	updated, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	updatedStr := string(updated)

	if !strings.Contains(updatedStr, `wps_sid = "encrypted-token-value"`) {
		t.Errorf("config missing updated wps_sid:\n%s", updatedStr)
	}
	if !strings.Contains(updatedStr, "# auto-saved") {
		t.Errorf("config missing auto-saved marker:\n%s", updatedStr)
	}
	// Preserve other fields/comments.
	if !strings.Contains(updatedStr, `language = "zh"`) {
		t.Errorf("language setting was lost:\n%s", updatedStr)
	}
}

func TestAutoLogin_Success(t *testing.T) {
	appID := "AK-auto-login"
	expectedToken := "raw-token-from-wps"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login_url":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]string{
					"code":   "code-123",
					"url":    "http://example.com/login",
					"app_id": appID,
				},
			})
		case "/user_token":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]string{
					"token": expectedToken,
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	originalLogin := loginURLAPI
	originalToken := userTokenAPI
	originalPoll := pollInterval
	originalBrowser := openBrowser
	loginURLAPI = server.URL + "/login_url"
	userTokenAPI = server.URL + "/user_token"
	pollInterval = 10 * time.Millisecond
	openBrowser = func(string) {}

	t.Cleanup(func() {
		loginURLAPI = originalLogin
		userTokenAPI = originalToken
		pollInterval = originalPoll
		openBrowser = originalBrowser
	})

	raw, encrypted, err := autoLogin(appID)
	if err != nil {
		t.Fatalf("autoLogin() error: %v", err)
	}
	if raw != expectedToken {
		t.Errorf("autoLogin() raw = %q, want %q", raw, expectedToken)
	}
	if encrypted == "" {
		t.Error("autoLogin() returned empty encrypted token")
	}
}
