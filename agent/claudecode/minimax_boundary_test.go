package claudecode

import (
	"context"
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

// The agent delegates inference to Claude Code. A local echo executable checks
// that the selected model and provider reach that process without any inference.
func TestMiniMaxModelHandoffPreservesExplicitSelection(t *testing.T) {
	for _, model := range []string{"MiniMax-M3.1-Flash", "MiniMax-M3", "MiniMax-M2.7", "saved-custom-model", "claude-sonnet-4"} {
		t.Run(model, func(t *testing.T) {
			a := &Agent{
				cmd: "/bin/echo", workDir: t.TempDir(), model: "agent-default", mode: "default",
				providers: []core.ProviderConfig{{Name: "minimax", BaseURL: "https://api.minimax.io/anthropic", APIKey: "mock-key", Model: model}},
				activeIdx: 0,
			}
			env := envSliceToMap(a.providerEnvLocked())
			if env["ANTHROPIC_MODEL"] != model || env["ANTHROPIC_BASE_URL"] != "https://api.minimax.io/anthropic" || env["ANTHROPIC_AUTH_TOKEN"] != "mock-key" {
				t.Fatalf("provider handoff changed: %v", env)
			}
			if a.proxyLocalURL != "" {
				t.Fatal("ordinary preset unexpectedly enabled a thinking rewrite proxy")
			}
			session, err := a.StartSession(context.Background(), "")
			if err != nil {
				t.Fatal(err)
			}
			defer session.Close()
			cs := session.(*claudeSession)
			if !strings.Contains(strings.Join(cs.cmd.Args, " "), "--model "+model) {
				t.Fatalf("selected model missing from subprocess: %v", cs.cmd.Args)
			}
		})
	}
}
