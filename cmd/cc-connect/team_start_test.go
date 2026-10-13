package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/config"
)

func TestTeamInit_AutomaticallyStartsConfiguredService(t *testing.T) {
	for _, noStart := range []bool{false, true} {
		t.Run(map[bool]string{false: "automatic", true: "configuration-only"}[noStart], func(t *testing.T) {
			spec := teamInitFixture(t, "codex")
			args := []string{"--config", spec.ConfigPath, "--work-dir", spec.WorkDir, "--agent", "codex"}
			if noStart {
				args = append(args, "--no-start")
			}
			registrations, starts := 0, 0
			var output bytes.Buffer
			register := func(opts registrationFlowOptions) (*registrationFlowResult, error) {
				registrations++
				return fakeTeamRegistration(opts)
			}
			start := func(path string) error {
				starts++
				if path != spec.ConfigPath {
					t.Fatal("wrong config handed to service")
				}
				cfg, err := config.Load(path)
				if err != nil || len(cfg.Projects) != 1 || cfg.Projects[0].BotOwnerClaimHash != "" || cfg.Projects[0].BotOwnerID != "ou_scanning_user" || cfg.Projects[0].AdminFrom != "ou_scanning_user" {
					t.Fatal("service started without saved manager", err)
				}
				lock, err := AcquireInstanceLock(path)
				if err != nil {
					t.Fatal("initializer kept the service lock", err)
				}
				lock.Release()
				if strings.Contains(output.String(), "/new claim ") || !strings.Contains(output.String(), "自动启动") {
					t.Fatal("missing startup/ownership instructions")
				}
				return nil
			}
			if err := teamInitCommandWithDeps(args, register, start, &output); err != nil {
				t.Fatal(err)
			}
			wantStarts := 1
			if noStart {
				wantStarts = 0
			}
			if registrations != 1 || starts != wantStarts {
				t.Fatalf("register=%d start=%d", registrations, starts)
			}
			if noStart && !strings.Contains(output.String(), "--no-start") {
				t.Fatal("configuration-only not explained")
			}
		})
	}
}

func TestTeamInit_StartFailureRetriesWithoutAnotherRegistration(t *testing.T) {
	spec := teamInitFixture(t, "codex")
	args := []string{"--config", spec.ConfigPath, "--work-dir", spec.WorkDir, "--agent", "codex"}
	var out bytes.Buffer
	want := errors.New("service handoff failed")
	if err := teamInitCommandWithDeps(args, fakeTeamRegistration, func(string) error { return want }, &out); !errors.Is(err, want) {
		t.Fatal("start error lost", err)
	}
	calls := 0
	if err := teamInitCommandWithDeps(args, func(registrationFlowOptions) (*registrationFlowResult, error) {
		t.Fatal("duplicate registration")
		return nil, nil
	}, func(string) error { calls++; return nil }, &out); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal("service not retried")
	}
}

func TestTeamInit_RegistrationFailureAndHelpDoNotStartService(t *testing.T) {
	spec := teamInitFixture(t, "codex")
	start := func(string) error { t.Fatal("service must not start"); return nil }
	var out bytes.Buffer
	want := context.Canceled
	if err := teamInitCommandWithDeps([]string{"--config", spec.ConfigPath, "--work-dir", spec.WorkDir}, func(registrationFlowOptions) (*registrationFlowResult, error) { return nil, want }, start, &out); !errors.Is(err, want) {
		t.Fatal(err)
	}
	if err := teamInitCommandWithDeps([]string{"--help"}, func(registrationFlowOptions) (*registrationFlowResult, error) {
		t.Fatal("help must not register")
		return nil, nil
	}, start, &out); err != nil {
		t.Fatal(err)
	}
}

func TestTeamServiceArgs_UsesNormalServiceModeAndLiteralPath(t *testing.T) {
	path := "/tmp/a folder/$(no-shell); config.toml"
	got := teamServiceArgs("/some path/cc-connect-team", path)
	if !reflect.DeepEqual(got, []string{"/some path/cc-connect-team", "--config", path}) {
		t.Fatalf("unexpected arguments %v", got)
	}
	opts, err := parseRootCLIOptions(got[1:])
	if err != nil || opts.configPath != path || len(opts.args) != 0 || opts.force {
		t.Fatal("handoff re-enters init or enables force", err)
	}
}

// Exercise the actual OS handoff in a subprocess, keeping this test process
// alive and avoiding real platform connections. Stdout and environment must
// survive; on Unix the initializer is replaced rather than daemonized.
func TestTeamServiceHandoff_ForegroundProcess(t *testing.T) {
	if os.Getenv("CC_TEAM_HANDOFF_TEST") == "1" {
		marker := ""
		for _, arg := range os.Args {
			if arg == "--handoff-child" {
				marker = arg
			}
		}
		if marker != "" {
			json.NewEncoder(os.Stdout).Encode(map[string]any{"pid": os.Getpid(), "value": os.Getenv("CC_TEAM_HANDOFF_VALUE")})
			os.Exit(0)
		}
		exe, err := os.Executable()
		if err != nil {
			os.Exit(91)
		}
		if err := handoffTeamService(exe, []string{exe, "-test.run=^TestTeamServiceHandoff_ForegroundProcess$", "--", "--handoff-child"}); err != nil {
			os.Exit(92)
		}
		// Windows waits for the foreground child and then returns.
		os.Exit(0)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, exe, "-test.run=^TestTeamServiceHandoff_ForegroundProcess$")
	cmd.Env = append(os.Environ(), "CC_TEAM_HANDOFF_TEST=1", "CC_TEAM_HANDOFF_VALUE=inherited")
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	if err := cmd.Wait(); err != nil {
		t.Fatal(err, output.String())
	}
	var got struct {
		PID   int    `json:"pid"`
		Value string `json:"value"`
	}
	if err := json.Unmarshal(output.Bytes(), &got); err != nil {
		t.Fatal(err, output.String())
	}
	if got.Value != "inherited" {
		t.Fatal("environment lost")
	}
	if runtime.GOOS != "windows" && got.PID != pid {
		t.Fatal("service did not replace the foreground initializer")
	}
}

func TestTeamServiceHandoff_ErrorIsReturned(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing-executable")
	if err := handoffTeamService(missing, teamServiceArgs(missing, "config.toml")); err == nil {
		t.Fatal("expected handoff error")
	}
}
