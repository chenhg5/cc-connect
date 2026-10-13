//go:build agent_team

package main

import (
	"slices"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

// Capture registrations before tests add their fake factories.
var teamRegisteredAgents = core.ListRegisteredAgents()
var teamRegisteredPlatforms = core.ListRegisteredPlatforms()

func TestAgentTeam_RegisteredPlugins(t *testing.T) {
	agents := append([]string(nil), teamRegisteredAgents...)
	platforms := append([]string(nil), teamRegisteredPlatforms...)
	for _, want := range []string{"acp", "claudecode", "codex", "gemini", "cursor"} {
		if !slices.Contains(agents, want) {
			t.Fatalf("Team build lost native backend %s: %v", want, agents)
		}
	}
	for _, want := range []string{"feishu", "lark", "telegram", "discord", "slack"} {
		if !slices.Contains(platforms, want) {
			t.Fatalf("Team build lost native platform %s: %v", want, platforms)
		}
	}
}

func TestAgentTeam_PreservesOptionalCommands(t *testing.T) {
	handlers := withOptionalPlatformCommands(map[string]func([]string){"web": func([]string) {}, "weixin": func([]string) {}})
	for _, name := range []string{"web", "weixin", "tuitui", "yuanbao"} {
		if handlers[name] == nil {
			t.Fatalf("Team build removed %s", name)
		}
	}
}
