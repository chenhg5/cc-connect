package claudecode

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// TestStartSessionWithModel_PassesOverrideToCLI spawns a fake claude binary
// and inspects the --model argument it actually receives: the per-job
// override must win over both the configured and the active provider model,
// while plain StartSession and the agent's stored model stay unchanged.
func TestStartSessionWithModel_PassesOverrideToCLI(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell script as a fake claude binary")
	}
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args")
	bin := filepath.Join(dir, "claude")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + argsFile + ".tmp && mv " + argsFile + ".tmp " + argsFile + "\nexec cat >/dev/null\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name     string
		provider string
		override string
		want     string
	}{
		{name: "no override uses configured model", want: "configured-model"},
		{name: "override beats configured model", override: "haiku", want: "haiku"},
		{name: "no override uses provider model", provider: "provider-model", want: "provider-model"},
		{name: "override beats provider model", provider: "provider-model", override: "custom/model-id", want: "custom/model-id"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_ = os.Remove(argsFile)
			ag, err := New(map[string]any{"work_dir": dir, "cmd": bin, "model": "configured-model"})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			a := ag.(*Agent)
			if tt.provider != "" {
				a.providers = []core.ProviderConfig{{Name: "p", Model: tt.provider}}
				a.activeIdx = 0
			}
			var sess core.AgentSession
			if tt.override != "" {
				sess, err = a.StartSessionWithModel(context.Background(), "", tt.override)
			} else {
				sess, err = a.StartSession(context.Background(), "")
			}
			if err != nil {
				t.Fatalf("start session: %v", err)
			}
			defer func() { _ = sess.Close() }()

			got := waitForModelArg(t, argsFile)
			if got != tt.want {
				t.Fatalf("--model = %q, want %q", got, tt.want)
			}
			if a.model != "configured-model" {
				t.Fatalf("agent configured model mutated to %q", a.model)
			}
			if tt.provider != "" && a.providers[0].Model != tt.provider {
				t.Fatalf("provider model mutated to %q", a.providers[0].Model)
			}
		})
	}
}

func waitForModelArg(t *testing.T, argsFile string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(argsFile); err == nil {
			args := strings.Split(strings.TrimSpace(string(b)), "\n")
			for i, a := range args {
				if a == "--model" && i+1 < len(args) {
					return args[i+1]
				}
			}
			return ""
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("fake claude was never spawned")
	return ""
}
