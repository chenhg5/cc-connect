package codex

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestCodexExecSessionAliveBetweenTurnsWithoutProcess(t *testing.T) {
	binDir := t.TempDir()
	writeFakeCodexScript(t, binDir, `#!/bin/sh
cat >/dev/null
printf '%s\n' '{"type":"thread.started","thread_id":"test-lifecycle"}' '{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":1}}'
`, `[Console]::In.ReadToEnd() | Out-Null
Write-Output '{"type":"thread.started","thread_id":"test-lifecycle"}'
Write-Output '{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":1}}'
`)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	s, err := newCodexSession(context.Background(), "codex", nil, t.TempDir(), "", "", "", "", "", []string{"CODEX_HOME=" + home}, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if !s.Alive() || len(s.activeCmds()) != 0 {
		t.Fatal("new exec AgentSession should be alive before starting any child")
	}
	if err := s.Send("hello", "", nil, nil); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for (s.turnInFlight.Load() || len(s.activeCmds()) != 0) && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if s.turnInFlight.Load() || len(s.activeCmds()) != 0 {
		t.Fatal("fake exec child did not exit")
	}
	if !s.Alive() {
		t.Fatal("AgentSession lifecycle must remain alive between exec turns")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if s.Alive() {
		t.Fatal("closed AgentSession still reports alive")
	}
}
