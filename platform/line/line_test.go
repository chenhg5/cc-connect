package line

import (
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func TestPlatform_Name(t *testing.T) {
	p := &Platform{}
	if got := p.Name(); got != "line" {
		t.Errorf("Name() = %q, want %q", got, "line")
	}
}

func TestNew_MissingCredentials(t *testing.T) {
	// Missing both channel_secret and channel_token
	_, err := New(map[string]any{})
	if err == nil {
		t.Error("expected error for missing credentials, got nil")
	}
}

func TestNew_MissingChannelSecret(t *testing.T) {
	// Only channel_token provided
	_, err := New(map[string]any{"channel_token": "token"})
	if err == nil {
		t.Error("expected error for missing channel_secret, got nil")
	}
}

func TestNew_MissingChannelToken(t *testing.T) {
	// Only channel_secret provided
	_, err := New(map[string]any{"channel_secret": "secret"})
	if err == nil {
		t.Error("expected error for missing channel_token, got nil")
	}
}

func TestNew_WithValidCredentials(t *testing.T) {
	p, err := New(map[string]any{
		"channel_secret": "test-secret",
		"channel_token":  "test-token",
	})
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if p == nil {
		t.Fatal("expected platform, got nil")
	}
	if p.Name() != "line" {
		t.Errorf("Name() = %q, want %q", p.Name(), "line")
	}
}

func TestNew_DefaultPort(t *testing.T) {
	p, err := New(map[string]any{
		"channel_secret": "test-secret",
		"channel_token":  "test-token",
	})
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	platform := p.(*Platform)
	if platform.port != "8080" {
		t.Errorf("port = %q, want %q", platform.port, "8080")
	}
	if platform.callbackPath != "/callback" {
		t.Errorf("callbackPath = %q, want %q", platform.callbackPath, "/callback")
	}
}

func TestNew_CustomPortAndPath(t *testing.T) {
	p, err := New(map[string]any{
		"channel_secret": "test-secret",
		"channel_token":  "test-token",
		"port":           "9090",
		"callback_path":  "/webhook",
	})
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	platform := p.(*Platform)
	if platform.port != "9090" {
		t.Errorf("port = %q, want %q", platform.port, "9090")
	}
	if platform.callbackPath != "/webhook" {
		t.Errorf("callbackPath = %q, want %q", platform.callbackPath, "/webhook")
	}
}

func TestInferTargetType(t *testing.T) {
	tests := map[string]string{
		"U123": "user",
		"C123": "group",
		"R123": "room",
		"X123": "unknown",
	}
	for input, want := range tests {
		if got := inferTargetType(input); got != want {
			t.Fatalf("inferTargetType(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestReconstructReplyCtx_TargetType(t *testing.T) {
	p := &Platform{}
	rctx, err := p.ReconstructReplyCtx("line:C123")
	if err != nil {
		t.Fatalf("ReconstructReplyCtx() error = %v", err)
	}
	rc := rctx.(replyContext)
	if rc.targetID != "C123" || rc.targetType != "group" || rc.sessionKey != "line:C123" {
		t.Fatalf("replyContext = %#v, want reconstructed group context", rc)
	}
}

func TestNew_WithAllowFrom(t *testing.T) {
	p, err := New(map[string]any{
		"channel_secret": "test-secret",
		"channel_token":  "test-token",
		"allow_from":     "user1,user2",
	})
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	platform := p.(*Platform)
	if platform.allowFrom != "user1,user2" {
		t.Errorf("allowFrom = %q, want %q", platform.allowFrom, "user1,user2")
	}
}

// verify Platform implements core.Platform
var _ core.Platform = (*Platform)(nil)
