package main

import (
	"reflect"
	"testing"

	"github.com/chenhg5/cc-connect/config"
)

func TestSharedSessionCommandPlatforms(t *testing.T) {
	platforms := []config.PlatformConfig{
		{Type: "feishu", Options: map[string]any{"share_session_in_channel": true}},
		{Type: "discord", Options: map[string]any{"thread_isolation": true}},
		{Type: "slack", Options: map[string]any{"session_scope": "channel"}},
		{Type: "googlechat", Options: map[string]any{}},
		{Type: "telegram", Options: map[string]any{}},
		{Type: "slack", Options: map[string]any{"session_scope": "user"}},
	}
	want := []string{"feishu", "discord", "slack", "googlechat"}
	if got := sharedSessionCommandPlatforms(platforms); !reflect.DeepEqual(got, want) {
		t.Fatalf("shared session platforms = %v, want %v", got, want)
	}
}
