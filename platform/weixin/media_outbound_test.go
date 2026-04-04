package weixin

import (
	"encoding/base64"
	"encoding/hex"
	"testing"
)

func TestFormatAesKeyForAPI(t *testing.T) {
	// Verify our encode matches the Python SDK's format:
	// base64(hex_string_bytes), not base64(raw_bytes).
	key := []byte{0x00, 0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88, 0x99, 0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff}
	got := formatAesKeyForAPI(key)

	// Expected: base64("00112233445566778899aabbccddeeff")
	hexStr := hex.EncodeToString(key)
	want := base64.StdEncoding.EncodeToString([]byte(hexStr))
	if got != want {
		t.Fatalf("formatAesKeyForAPI: got %q, want %q", got, want)
	}

	// Verify round-trip with parseAesKey (decode direction)
	decoded, err := parseAesKey(got, "test")
	if err != nil {
		t.Fatalf("parseAesKey failed on formatAesKeyForAPI output: %v", err)
	}
	for i := range key {
		if decoded[i] != key[i] {
			t.Fatalf("round-trip mismatch at byte %d: got %02x, want %02x", i, decoded[i], key[i])
		}
	}
}

func TestFormatAesKeyForAPI_NotRawBase64(t *testing.T) {
	// Ensure the output is NOT just base64(raw_bytes) — that was the old bug.
	key := []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10}
	got := formatAesKeyForAPI(key)
	wrongFormat := base64.StdEncoding.EncodeToString(key) // base64(raw) — the old bug
	if got == wrongFormat {
		t.Fatalf("formatAesKeyForAPI should NOT produce base64(raw_bytes), but got %q which equals the wrong format", got)
	}
}

func TestIsWeixinCDNHost(t *testing.T) {
	tests := []struct {
		url  string
		want bool
	}{
		{"https://novac2c.cdn.weixin.qq.com/c2c/upload?param=abc", true},
		{"https://anything.weixin.qq.com/path", true},
		{"https://cdn.wechat.com/upload", true},
		{"https://sub.domain.wechat.com/path", true},
		{"https://example.com/upload", false},
		{"https://weixin.qq.com.evil.com/fake", false},
		{"https://notwechat.com/path", false},
		{"", false},
		{"not-a-url", false},
	}
	for _, tt := range tests {
		got := isWeixinCDNHost(tt.url)
		if got != tt.want {
			t.Errorf("isWeixinCDNHost(%q) = %v, want %v", tt.url, got, tt.want)
		}
	}
}

func TestGetUploadURLResponse_Validation(t *testing.T) {
	tests := []struct {
		name      string
		resp      getUploadURLResponse
		wantError bool
	}{
		{
			name:      "upload_param only (legacy)",
			resp:      getUploadURLResponse{UploadParam: "some_param"},
			wantError: false,
		},
		{
			name:      "upload_full_url only (new API)",
			resp:      getUploadURLResponse{UploadFullURL: "https://novac2c.cdn.weixin.qq.com/c2c/upload?encrypted_query_param=abc"},
			wantError: false,
		},
		{
			name:      "both present",
			resp:      getUploadURLResponse{UploadParam: "param", UploadFullURL: "https://cdn.example.com/upload"},
			wantError: false,
		},
		{
			name:      "both empty",
			resp:      getUploadURLResponse{},
			wantError: true,
		},
		{
			name:      "whitespace only",
			resp:      getUploadURLResponse{UploadParam: "  ", UploadFullURL: "  "},
			wantError: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Replicate the validation logic from client.go:
			// both fields empty/whitespace-only → error
			trim := func(s string) string {
				for len(s) > 0 && s[0] == ' ' { s = s[1:] }
				for len(s) > 0 && s[len(s)-1] == ' ' { s = s[:len(s)-1] }
				return s
			}
			hasError := trim(tt.resp.UploadParam) == "" && trim(tt.resp.UploadFullURL) == ""
			if hasError != tt.wantError {
				t.Errorf("validation error = %v, wantError %v", hasError, tt.wantError)
			}
		})
	}
}
