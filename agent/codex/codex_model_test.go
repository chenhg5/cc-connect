package codex

import (
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func TestConfiguredModels_BoundaryConditions(t *testing.T) {
	a := &Agent{
		providers: []core.ProviderConfig{
			{Models: []core.ModelOption{{Name: "first"}}},
			{Models: []core.ModelOption{{Name: "second"}}},
		},
	}

	tests := []struct {
		name      string
		activeIdx int
		wantNil   bool
		wantName  string
	}{
		{name: "negative index", activeIdx: -1, wantNil: true},
		{name: "out of range", activeIdx: 2, wantNil: true},
		{name: "valid index", activeIdx: 1, wantName: "second"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a.activeIdx = tt.activeIdx
			got := a.configuredModels()
			if tt.wantNil {
				if got != nil {
					t.Fatalf("configuredModels() = %v, want nil", got)
				}
				return
			}
			if len(got) != 1 || got[0].Name != tt.wantName {
				t.Fatalf("configuredModels() = %v, want %q", got, tt.wantName)
			}
		})
	}
}

func TestGetModel_PrefersActiveProviderModel(t *testing.T) {
	a := &Agent{
		model: "gpt-4.1-mini",
		providers: []core.ProviderConfig{
			{Name: "openai", Model: "gpt-5.4"},
		},
		activeIdx: 0,
	}

	if got := a.GetModel(); got != "gpt-5.4" {
		t.Fatalf("GetModel() = %q, want gpt-5.4", got)
	}
}

func TestNormalizeAppServerURL_StdIOIsExplicit(t *testing.T) {
	for _, raw := range []string{"stdio", " stdio "} {
		if got := normalizeAppServerURL(raw); got != "stdio://" {
			t.Fatalf("normalizeAppServerURL(%q) = %q, want stdio://", raw, got)
		}
	}
}

func TestNormalizeAppServerURL_EmptyKeepsWebSocketDefault(t *testing.T) {
	if got := normalizeAppServerURL(""); got != "ws://127.0.0.1:3845" {
		t.Fatalf("normalizeAppServerURL(empty) = %q, want ws://127.0.0.1:3845", got)
	}
}

func TestWorkspaceAgentOptions_PreservesStdIOAppServerURL(t *testing.T) {
	a := &Agent{
		backend:      "app_server",
		appServerURL: normalizeAppServerURL("stdio"),
	}

	opts := a.WorkspaceAgentOptions()
	if got := opts["app_server_url"]; got != "stdio://" {
		t.Fatalf("WorkspaceAgentOptions()[app_server_url] = %#v, want stdio://", got)
	}
}

func TestIsCodexChatModel(t *testing.T) {
	tests := []struct {
		id   string
		want bool
	}{
		{"", false},

		// Legacy / current chat families (must keep working).
		{"gpt-4o", true},
		{"gpt-4o-mini", true},
		{"gpt-4.1", true},
		{"gpt-4.1-mini", true},
		{"gpt-4.1-nano", true},
		{"gpt-3.5-turbo", true},
		{"chatgpt-4o-latest", true},
		{"o1", true},
		{"o1-mini", true},
		{"o1-preview", true},
		{"o3", true},
		{"o3-mini", true},
		{"o4", true},
		{"o4-mini", true},
		{"codex-mini-latest", true},

		// GPT-5 series — the regression that motivated this change.
		{"gpt-5", true},
		{"gpt-5-mini", true},
		{"gpt-5.3", true},
		{"gpt-5.3-codex", true},
		{"gpt-5.4", true},
		{"gpt-5.5", true},
		{"gpt-5.6", true},
		{"gpt-5.6-sol", true},
		{"gpt-5.6-terra", true},
		{"gpt-5.6-luna", true},

		// Case insensitivity (defensive; ids from /v1/models are usually lower).
		{"GPT-5.6", true},
		{"Codex-Mini-Latest", true},

		// Non-chat modalities that /v1/models returns — must be rejected so
		// they never show up in the /model chooser.
		{"text-embedding-ada-002", false},
		{"text-embedding-3-small", false},
		{"text-embedding-3-large", false},
		{"whisper-1", false},
		{"tts-1", false},
		{"tts-1-hd", false},
		{"gpt-4o-realtime-preview", false},
		{"gpt-4o-audio-preview", false},
		{"gpt-4o-transcribe", false},
		{"gpt-4o-search-preview", false},
		{"dall-e-2", false},
		{"dall-e-3", false},
		{"gpt-image-1", false},
		{"text-moderation-latest", false},
		{"omni-moderation-latest", false},

		// Unrelated model families that should not be surfaced.
		{"babbage-002", false},
		{"davinci-002", false},
		{"claude-3-5-sonnet", false},
		{"gemini-1.5-pro", false},
	}

	for _, tc := range tests {
		t.Run(tc.id, func(t *testing.T) {
			if got := isCodexChatModel(tc.id); got != tc.want {
				t.Fatalf("isCodexChatModel(%q) = %v, want %v", tc.id, got, tc.want)
			}
		})
	}
}
