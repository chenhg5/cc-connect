package codex

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

type fastModeFixtureRecord struct {
	Args   []string       `json:"args,omitempty"`
	Method string         `json:"method,omitempty"`
	Params map[string]any `json:"params,omitempty"`
}

// Optional tracing for the desktop CLI fixture; the real child process records
// its arguments and resume requests, without a real Codex account or server.
func recordFastModeFixture(record fastModeFixtureRecord) {
	path := os.Getenv("CC_FAST_MODE_TRACE")
	if path == "" {
		return
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		os.Exit(24)
	}
	if err := json.NewEncoder(f).Encode(record); err != nil {
		os.Exit(25)
	}
	if err := f.Close(); err != nil {
		os.Exit(26)
	}
}

func TestStartSession_FastModeReachesConfiguredCLIAndResumesBothBackends(t *testing.T) {
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	cliDir := filepath.Join(root, "desktop app")
	if err := os.MkdirAll(cliDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cli := filepath.Join(cliDir, filepath.Base(bin))
	if err := os.WriteFile(cli, data, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir())
	for _, backend := range []string{"exec", "app_server"} {
		t.Run(backend, func(t *testing.T) {
			trace := filepath.Join(t.TempDir(), "trace.jsonl")
			raw, err := New(map[string]any{
				"cmd":     []string{cli, "-test.run=^TestDesktopCLIProcess$", "--", "desktop-fixture"},
				"backend": backend, "work_dir": root,
				"env": map[string]string{"CC_DESKTOP_CLI_HELPER": "1", "CC_FAST_MODE_TRACE": trace},
			})
			if err != nil {
				t.Fatal(err)
			}
			a := raw.(*Agent)
			resumeID := ""
			for _, tier := range []string{"", "fast", "default"} {
				if tier != "" {
					a.SetFastMode(tier == "fast")
				}
				if err := os.WriteFile(trace, nil, 0o600); err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				session, err := a.StartSession(ctx, resumeID)
				if err != nil {
					cancel()
					t.Fatal(err)
				}
				t.Cleanup(func() {
					cancel()
					if err := session.Close(); err != nil {
						t.Errorf("close session: %v", err)
					}
				})
				if backend == "exec" {
					if err := session.Send("continue", "fast-mode", nil, nil); err != nil {
						t.Fatal(err)
					}
					for done := false; !done; {
						select {
						case event, ok := <-session.Events():
							if !ok || event.Type == core.EventError {
								t.Fatalf("exec failed: %+v", event)
							}
							done = event.Type == core.EventResult
						case <-ctx.Done():
							t.Fatal(ctx.Err())
						}
					}
				}
				if got := session.CurrentSessionID(); got != "fixture-thread" {
					t.Fatalf("session ID = %q, want fixture-thread", got)
				}
				if err := session.Close(); err != nil {
					t.Fatal(err)
				}
				cancel()
				assertFastModeFixture(t, trace, backend, tier, resumeID)
				resumeID = "fixture-thread"
			}
		})
	}
}

func assertFastModeFixture(t *testing.T, path, backend, tier, resumeID string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	command := "exec"
	if backend == "app_server" {
		command = "app-server"
	}
	foundCommand, foundResume := false, resumeID == ""
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var record fastModeFixtureRecord
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatal(err)
		}
		if len(record.Args) >= 2 && record.Args[1] == command {
			foundCommand = true
			if tier == "" {
				for _, arg := range record.Args {
					if strings.Contains(arg, "service_tier=") {
						t.Fatalf("unset tier must inherit native config: %v", record.Args)
					}
				}
			} else if !containsSequence(record.Args, []string{"-c", `service_tier="` + tier + `"`}) {
				t.Fatalf("child process did not receive tier %q: %v", tier, record.Args)
			}
			if backend == "app_server" {
				for _, arg := range record.Args {
					if arg == "--listen" {
						t.Fatalf("stdio process must not open a listener: %v", record.Args)
					}
				}
			} else if resumeID != "" && len(record.Args) > 3 {
				// Global options sit between "resume" and the positional ID.
				for _, arg := range record.Args[3:] {
					if arg == resumeID && record.Args[2] == "resume" {
						foundResume = true
					}
				}
			}
		}
		if record.Method == "thread/resume" && record.Params["threadId"] == resumeID {
			foundResume = true
		}
	}
	if !foundCommand || !foundResume {
		t.Fatalf("missing %s command or resume %q in trace: %s", command, resumeID, data)
	}
}
