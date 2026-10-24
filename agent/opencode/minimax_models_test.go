package opencode

import (
	"context"
	"reflect"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func TestMiniMaxAvailableModelsPreservesSelection(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Any unintended CLI discovery uses a local failing mock command.
	a := &Agent{cmd: "/bin/false", activeIdx: 0, providers: []core.ProviderConfig{{BaseURL: "https://api.minimax.io/anthropic", Model: "MiniMax-M2.7"}}}
	want := []core.ModelOption{{Name: "MiniMax-M3.1-Flash"}, {Name: "MiniMax-M3"}}
	if got := a.AvailableModels(ctx); !reflect.DeepEqual(got, want) {
		t.Fatalf("ordinary MiniMax picker = %v", got)
	}
	if got := a.GetModel(); got != "MiniMax-M2.7" {
		t.Fatalf("saved selection reset to %q", got)
	}
	custom := []core.ModelOption{{Name: "custom-model", Alias: "custom"}, {Name: "MiniMax-M2.7", Alias: "legacy"}}
	a.providers[0].Models = custom
	if got := a.AvailableModels(ctx); !reflect.DeepEqual(got, custom) {
		t.Fatalf("explicit list changed: %v", got)
	}
}
