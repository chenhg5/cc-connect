package main

import (
	"github.com/chenhg5/cc-connect/config"
	"github.com/chenhg5/cc-connect/core"
	"testing"
)

func TestBotContextPath_IsolatesIdentityAndConfiguration(t *testing.T) {
	p := core.BusinessProject{ID: "a", Name: "A", PrimaryDirectory: "src", Directories: []core.ProjectDirectory{{ID: "src", Path: "/repo"}}}
	b := config.ProjectConfig{ID: "dev", Role: "developer", ContextProjectID: "a", Agent: config.AgentConfig{Type: "codex"}}
	path, err := botContextDataDir("/data", b, &p)
	if err != nil {
		t.Fatal(err)
	}
	p.Name = "renamed"
	b.Name = "renamed"
	renamed, _ := botContextDataDir("/data", b, &p)
	if renamed != path {
		t.Fatal("display rename changed context")
	}
	b.ContextProjectID = "b"
	next, _ := botContextDataDir("/data", b, &p)
	if next == path {
		t.Fatal("two projects share context")
	}
	b.ContextProjectID = "a"
	b.ID = "other"
	next, _ = botContextDataDir("/data", b, &p)
	if next == path {
		t.Fatal("two bots share context")
	}
	b.ID = "dev"
	b.Agent.Type = "claudecode"
	next, _ = botContextDataDir("/data", b, &p)
	if next == path {
		t.Fatal("platform change resumed incompatible session")
	}
}

func TestBotContextPath_RuntimeAccessDoesNotDiscardConversation(t *testing.T) {
	b := config.ProjectConfig{ID: "manager", Role: "manager", Agent: config.AgentConfig{Type: "codex", Options: map[string]any{"mode": "full-auto"}}}
	before, err := botContextDataDir("/data", b, nil)
	if err != nil {
		t.Fatal(err)
	}
	b.Agent.Options["sandbox_writable_roots"] = []string{"/cache/lark"}
	b.Agent.Options["sandbox_network_access"] = true
	after, err := botContextDataDir("/data", b, nil)
	if err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatal("runtime access repair discarded existing conversation")
	}
	if _, ok := b.Agent.Options["sandbox_network_access"]; !ok {
		t.Fatal("fingerprint mutated live config")
	}
}
