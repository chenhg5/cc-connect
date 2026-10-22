package codex

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSend_FollowupWaitsForCompletedProcess(t *testing.T) {
	for _, cancelWaiting := range []bool{false, true} {
		name := "resumes_after_exit"
		if cancelWaiting {
			name = "cancellation_unblocks_waiter"
		}
		t.Run(name, func(t *testing.T) {
			workDir := t.TempDir()
			binDir := filepath.Join(workDir, "bin")
			if err := os.MkdirAll(binDir, 0o755); err != nil {
				t.Fatal(err)
			}
			startsFile := filepath.Join(workDir, "starts.txt")
			releaseFile := filepath.Join(workDir, "release")
			script := "#!/bin/sh\n" +
				"prompt=$(cat)\n" +
				"printf '%s\\n' \"$prompt\" >> \"$CODEX_STARTS_FILE\"\n" +
				"printf '%s\\n' '{\"type\":\"turn.completed\"}'\n" +
				"if [ \"$prompt\" = first ]; then\n" +
				"  while [ ! -f \"$CODEX_RELEASE_FILE\" ]; do sleep 0.01; done\n" +
				"fi\n"
			powershellScript := `
$prompt = [Console]::In.ReadToEnd()
[IO.File]::AppendAllText($env:CODEX_STARTS_FILE, $prompt + [Environment]::NewLine)
[Console]::Out.WriteLine('{"type":"turn.completed"}')
if ($prompt -eq 'first') {
    while (-not (Test-Path $env:CODEX_RELEASE_FILE)) { Start-Sleep -Milliseconds 10 }
}
`
			writeFakeCodexScript(t, binDir, script, powershellScript)
			t.Setenv("CODEX_STARTS_FILE", startsFile)
			t.Setenv("CODEX_RELEASE_FILE", releaseFile)
			t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
			cs, err := newCodexSession(context.Background(), "codex", nil, workDir, "", "", "", "thread-followup", "", nil, "", "", "")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				// Release the fake child even when an assertion fails on Windows,
				// where killing the .cmd wrapper may leave PowerShell alive.
				_ = os.WriteFile(releaseFile, nil, 0o644)
				_ = cs.Close()
			})
			cs.cmd = filepath.Join(workDir, "missing-codex")
			if err := cs.Send("setup-failure", "", nil, nil); err == nil {
				t.Fatal("Send with a missing executable unexpectedly succeeded")
			}
			cs.cmd = "codex"
			if err := cs.Send("first", "", nil, nil); err != nil {
				t.Fatal(err)
			}
			waitForDoneResult(t, cs.Events())

			followup := make(chan error, 1)
			go func() { followup <- cs.Send("second", "", nil, nil) }()
			select {
			case err := <-followup:
				t.Fatalf("follow-up returned before the completed process exited: %v", err)
			case <-time.After(100 * time.Millisecond):
			}
			data, err := os.ReadFile(startsFile)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(data), "second") {
				t.Fatal("follow-up launched a concurrent writer")
			}

			if cancelWaiting {
				cs.cancel()
			} else if err := os.WriteFile(releaseFile, nil, 0o644); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-followup:
				if cancelWaiting && !errors.Is(err, context.Canceled) {
					t.Fatalf("cancelled follow-up error = %v, want context.Canceled", err)
				}
				if !cancelWaiting && err != nil {
					t.Fatalf("follow-up after process exit: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("follow-up did not unblock")
			}
			if !cancelWaiting {
				waitForDoneResult(t, cs.Events())
				waitForFileLines(t, startsFile, 2)
			}
		})
	}
}
