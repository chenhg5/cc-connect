package core

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestResolveModelAlias_CaseInsensitive(t *testing.T) {
	models := []ModelOption{{Name: "gpt-5.3-codex", Alias: "Codex"}}

	got := resolveModelAlias(models, "codex")
	if got != "gpt-5.3-codex" {
		t.Fatalf("resolveModelAlias() = %q, want %q", got, "gpt-5.3-codex")
	}
}

func TestResolveModelAlias_NoMatchFallsBackToInput(t *testing.T) {
	models := []ModelOption{{Name: "gpt-5.3-codex", Alias: "codex"}}

	got := resolveModelAlias(models, "gpt-5.4")
	if got != "gpt-5.4" {
		t.Fatalf("resolveModelAlias() = %q, want original input", got)
	}
}

func TestParseModelSwitchArgs(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
		ok   bool
	}{
		{name: "legacy syntax", args: []string{"gpt"}, want: "gpt", ok: true},
		{name: "switch syntax", args: []string{"switch", "gpt"}, want: "gpt", ok: true},
		{name: "missing switch target", args: []string{"switch"}, ok: false},
		{name: "unknown subcommand", args: []string{"list", "gpt"}, ok: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseModelSwitchArgs(tt.args)
			if ok != tt.ok {
				t.Fatalf("parseModelSwitchArgs() ok = %v, want %v", ok, tt.ok)
			}
			if got != tt.want {
				t.Fatalf("parseModelSwitchArgs() = %q, want %q", got, tt.want)
			}
		})
	}
}

type validatingModelAgent struct{ stubModelModeAgent }

func (a *validatingModelAgent) ValidateModel(_ context.Context, model string) error {
	if model != "valid" {
		return fmt.Errorf("unknown model")
	}
	return nil
}
func TestModelValidationRunsBeforeSavingAndPreservesSession(t *testing.T) {
	a := &validatingModelAgent{stubModelModeAgent: stubModelModeAgent{model: "original"}}
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", a, []Platform{p}, "", LangEnglish)
	saves := 0
	e.SetModelSaveFunc(func(string) error { saves++; return nil })
	key := "test:invalid-model"
	s := e.sessions.GetOrCreateActive(key)
	s.SetAgentSessionID("long-native-session", "test")
	s.AddHistory("user", "keep")
	e.cmdModel(p, &Message{SessionKey: key, ReplyCtx: "ctx"}, []string{"invalid"})
	if saves != 0 || a.GetModel() != "original" || s.GetAgentSessionID() != "long-native-session" || len(s.GetHistory(10)) != 1 {
		t.Fatal("rejected model changed config or session")
	}
	if !strings.Contains(strings.Join(p.getSent(), "\n"), "unknown model") {
		t.Fatal("missing validation error")
	}
}
