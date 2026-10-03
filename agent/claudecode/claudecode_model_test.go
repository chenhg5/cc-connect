package claudecode

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

// modelsAPIStub serves a /v1/models response and counts how many times it was
// consulted, so precedence tests can assert the API was not reached at all.
func modelsAPIStub(t *testing.T, hits *int32) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(hits, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[{"id":"api-model","display_name":"API Model"}]}`)
	}))
	t.Cleanup(srv.Close)
	return srv
}

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
		model: "sonnet",
		providers: []core.ProviderConfig{
			{Name: "anthropic", Model: "opus"},
		},
		activeIdx: 0,
	}

	if got := a.GetModel(); got != "opus" {
		t.Fatalf("GetModel() = %q, want opus", got)
	}
}

// The hardcoded fallback must use the rolling "fable" alias, not a pinned
// generation id: a pinned "claude-fable-5" goes stale as soon as the next
// Fable model ships (see #1642 follow-up on PR #1755).
func TestAvailableModels_FallbackUsesRollingFableAlias(t *testing.T) {
	// Empty the API credentials so the fallback is reached without a network
	// call, independent of the developer's environment.
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("ANTHROPIC_BASE_URL", "")

	a := &Agent{activeIdx: -1}
	got := a.AvailableModels(context.Background())

	byName := make(map[string]string, len(got))
	for _, m := range got {
		byName[m.Name] = m.Desc
	}

	desc, ok := byName["fable"]
	if !ok {
		t.Fatalf("AvailableModels() fallback = %v, want a rolling %q entry", got, "fable")
	}
	if desc != "Claude Fable (frontier)" {
		t.Fatalf("fable desc = %q, want %q", desc, "Claude Fable (frontier)")
	}
	if _, ok := byName["claude-fable-5"]; ok {
		t.Fatalf("AvailableModels() fallback = %v, still pins the stale %q id", got, "claude-fable-5")
	}
}

func TestAvailableModels_ConfiguredModelsWinWithoutAPICall(t *testing.T) {
	var hits int32
	srv := modelsAPIStub(t, &hits)

	a := &Agent{
		providers: []core.ProviderConfig{{
			APIKey:  "test-key",
			BaseURL: srv.URL,
			Models:  []core.ModelOption{{Name: "configured-model", Desc: "Configured"}},
		}},
		activeIdx: 0,
	}

	got := a.AvailableModels(context.Background())

	if len(got) != 1 || got[0].Name != "configured-model" {
		t.Fatalf("AvailableModels() = %v, want the configured provider models", got)
	}
	if n := atomic.LoadInt32(&hits); n != 0 {
		t.Fatalf("configured models must short-circuit before the API; got %d API call(s)", n)
	}
}

func TestAvailableModels_DynamicAPIBeatsFallback(t *testing.T) {
	var hits int32
	srv := modelsAPIStub(t, &hits)

	a := &Agent{
		providers: []core.ProviderConfig{{APIKey: "test-key", BaseURL: srv.URL}},
		activeIdx: 0,
	}

	got := a.AvailableModels(context.Background())

	if len(got) != 1 || got[0].Name != "api-model" {
		t.Fatalf("AvailableModels() = %v, want the dynamic API models", got)
	}
	if n := atomic.LoadInt32(&hits); n != 1 {
		t.Fatalf("API consulted %d time(s), want exactly 1", n)
	}
}
