package qq

import (
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func TestPlatform_Name(t *testing.T) {
	p := &Platform{}
	if got := p.Name(); got != "qq" {
		t.Errorf("Name() = %q, want %q", got, "qq")
	}
}

func TestNew_DefaultWSURL(t *testing.T) {
	p, err := New(map[string]any{})
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	platform := p.(*Platform)
	if platform.wsURL != "ws://127.0.0.1:3001" {
		t.Errorf("wsURL = %q, want %q", platform.wsURL, "ws://127.0.0.1:3001")
	}
}

func TestNew_CustomWSURL(t *testing.T) {
	p, err := New(map[string]any{
		"ws_url": "ws://example.com:8080",
	})
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	platform := p.(*Platform)
	if platform.wsURL != "ws://example.com:8080" {
		t.Errorf("wsURL = %q, want %q", platform.wsURL, "ws://example.com:8080")
	}
}

func TestNew_WithToken(t *testing.T) {
	p, err := New(map[string]any{
		"token": "my-secret-token",
	})
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	platform := p.(*Platform)
	if platform.token != "my-secret-token" {
		t.Errorf("token = %q, want %q", platform.token, "my-secret-token")
	}
}

func TestNew_WithAllowFrom(t *testing.T) {
	p, err := New(map[string]any{
		"allow_from": "user1,user2,*",
	})
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	platform := p.(*Platform)
	if platform.allowFrom != "user1,user2,*" {
		t.Errorf("allowFrom = %q, want %q", platform.allowFrom, "user1,user2,*")
	}
}

func TestNew_ShareSessionInChannel(t *testing.T) {
	p, err := New(map[string]any{
		"share_session_in_channel": true,
	})
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	platform := p.(*Platform)
	if !platform.shareSessionInChannel {
		t.Error("shareSessionInChannel = false, want true")
	}
}

// verify Platform implements core.Platform
var _ core.Platform = (*Platform)(nil)

func TestReconstructReplyCtx(t *testing.T) {
	p := &Platform{}

	tests := []struct {
		name        string
		sessionKey  string
		wantErr     bool
		wantType    string
		wantUserID  int64
		wantGroupID int64
	}{
		{
			name:       "private session",
			sessionKey: "qq:12345",
			wantType:   "private",
			wantUserID: 12345,
		},
		{
			name:        "group with shared session",
			sessionKey:  "qq:g:67890",
			wantType:    "group",
			wantGroupID: 67890,
		},
		{
			name:        "group with per-user session",
			sessionKey:  "qq:67890:12345",
			wantType:    "group",
			wantGroupID: 67890,
			wantUserID:  12345,
		},
		{
			name:       "missing prefix",
			sessionKey: "telegram:123",
			wantErr:    true,
		},
		{
			name:       "too few parts",
			sessionKey: "qq",
			wantErr:    true,
		},
		{
			name:       "non-numeric private user ID",
			sessionKey: "qq:notanumber",
			wantErr:    true,
		},
		{
			name:       "non-numeric shared-group ID",
			sessionKey: "qq:g:notanumber",
			wantErr:    true,
		},
		{
			name:       "non-numeric per-user group ID",
			sessionKey: "qq:abc:12345",
			wantErr:    true,
		},
		{
			name:       "non-numeric per-user user ID",
			sessionKey: "qq:67890:xyz",
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, err := p.ReconstructReplyCtx(tt.sessionKey)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error for %q, got nil (ctx=%+v)", tt.sessionKey, ctx)
				}
				if ctx != nil {
					t.Errorf("expected nil ctx on error, got %+v", ctx)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error for %q: %v", tt.sessionKey, err)
			}
			rc, ok := ctx.(*replyContext)
			if !ok {
				t.Fatalf("ctx type = %T, want *replyContext", ctx)
			}
			if rc.messageType != tt.wantType {
				t.Errorf("messageType = %q, want %q", rc.messageType, tt.wantType)
			}
			if rc.userID != tt.wantUserID {
				t.Errorf("userID = %d, want %d", rc.userID, tt.wantUserID)
			}
			if rc.groupID != tt.wantGroupID {
				t.Errorf("groupID = %d, want %d", rc.groupID, tt.wantGroupID)
			}
		})
	}
}
