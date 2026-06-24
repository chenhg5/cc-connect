package wpsagentspace

import (
	"testing"
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
