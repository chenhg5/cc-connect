package config

import (
	"testing"

	"github.com/chenhg5/cc-connect/agent/pi"
)

func TestPiReasoningRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, target := range []string{"high", ""} {
		t.Run("target="+target, func(t *testing.T) {
			writeTestConfig(t, `[[projects]]
name = "demo"
[projects.agent]
type = "pi"
[projects.agent.options]
cmd = "/bin/true"
model = "test/model"
thinking = "low"
`)
			if err := SaveAgentReasoningEffort("demo", target); err != nil {
				t.Fatal(err)
			}
			cfg := readTestConfig(t)
			reloaded, err := pi.New(cfg.Projects[0].Agent.Options)
			if err != nil {
				t.Fatal(err)
			}
			got := reloaded.(*pi.Agent).GetReasoningEffort()
			if got != target {
				t.Fatalf("restart effort=%q, selected=%q; persisted options=%v", got, target, cfg.Projects[0].Agent.Options)
			}
		})
	}
}
