package main

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/chenhg5/cc-connect/config"
	"github.com/creack/pty"
)

func TestStartupConfigErrorDoesNotLogSensitivePathOrValue(t *testing.T) {
	const marker = "STARTUP_PRIVACY_MARKER"
	path := filepath.Join(t.TempDir(), marker+".toml")
	if err := os.WriteFile(path, []byte("secret = [\""+marker+"\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestLogPrivacyStartupHelper$", "--", "--config", path)
	cmd.Env = append(os.Environ(), "CC_LOG_PRIVACY_HELPER=1", "CC_LOG_FILE=")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	out := append(stdout.Bytes(), stderr.Bytes()...)
	if stdout.Len() != 0 {
		t.Fatalf("startup diagnostic polluted stdout: %s", &stdout)
	}
	if err == nil {
		t.Fatal("invalid config unexpectedly started")
	}
	if strings.Contains(string(out), marker) {
		t.Fatalf("startup error exposed config path or value: %s", out)
	}
	if !bytes.Contains(out, []byte("level=ERROR")) || !bytes.Contains(out, []byte("startup_cause=config_parse_error")) {
		t.Fatal("missing bounded configuration parse diagnostic")
	}
	if bytes.Contains(out, []byte("parse config:")) {
		t.Fatal("redirected startup retained raw configuration error")
	}
}

func TestForegroundConfigErrorDetailsAreNotKeptInManagedLog(t *testing.T) {
	for _, managedLog := range []bool{false, true} {
		name := "terminal"
		if managedLog {
			name = "terminal-with-managed-log"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "broken-config.toml")
			// Broken TOML with no credential fixture.
			if err := os.WriteFile(path, []byte("projects = [\n"), 0600); err != nil {
				t.Fatal(err)
			}
			master, slave, err := pty.Open()
			if errors.Is(err, pty.ErrUnsupported) {
				t.Skip("pseudo-terminal is not supported")
			}
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = master.Close() })
			t.Cleanup(func() { _ = slave.Close() })

			cmd := exec.Command(os.Args[0], "-test.run=^TestLogPrivacyStartupHelper$", "--", "--config", path)
			logPath := ""
			if managedLog {
				logPath = filepath.Join(dir, "operational.log")
			}
			cmd.Env = append(os.Environ(), "CC_LOG_PRIVACY_HELPER=1", "CC_LOG_FILE="+logPath)
			var stdout bytes.Buffer
			cmd.Stdout = &stdout
			cmd.Stderr = slave
			err = cmd.Run()
			_ = slave.Close()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 1 || stdout.Len() != 0 {
				t.Fatalf("expected startup failure and empty stdout, got error %v, stdout length %d", err, stdout.Len())
			}
			terminalOutput, err := io.ReadAll(master)
			// Linux reports EIO when the last slave closes; BSDs return EOF.
			if err != nil && !errors.Is(err, syscall.EIO) {
				t.Fatal(err)
			}

			if !managedLog {
				for _, want := range []string{"cannot load configuration", path, "parse config", "toml:"} {
					if !strings.Contains(string(terminalOutput), want) {
						t.Fatalf("missing terminal diagnostic %q", want)
					}
				}
				return
			}
			fileOutput, err := os.ReadFile(logPath)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Contains(fileOutput, []byte("level=ERROR")) ||
				!bytes.Contains(fileOutput, []byte("startup_cause=config_parse_error")) ||
				!bytes.Contains(fileOutput, []byte("[redacted]")) {
				t.Fatal("managed file did not record bounded error")
			}
			for _, out := range [][]byte{terminalOutput, fileOutput} {
				if bytes.Contains(out, []byte(path)) || bytes.Contains(out, []byte("parse config")) {
					t.Fatal("managed startup exposed config path or raw cause")
				}
			}
		})
	}
}

func TestRedirectedStartupClassifiesHeldAndUnavailableLocks(t *testing.T) {
	dir := t.TempDir()
	heldPath := filepath.Join(dir, "held-private-config.toml")
	lock, err := AcquireInstanceLock(heldPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(lock.Release)

	notDirectory := filepath.Join(dir, "private-file")
	if err := os.WriteFile(notDirectory, nil, 0600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, path, code string
		forceHint        bool
	}{
		{name: "held", path: heldPath, code: "lock_held", forceHint: true},
		{name: "unavailable", path: filepath.Join(notDirectory, "config.toml"), code: "lock_unavailable"},
	} {
		t.Run(test.name, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestLogPrivacyStartupHelper$", "--", "--config", test.path)
			cmd.Env = append(os.Environ(), "CC_LOG_PRIVACY_HELPER=1", "CC_LOG_FILE=")
			var stdout, stderr bytes.Buffer
			cmd.Stdout = &stdout
			cmd.Stderr = &stderr
			var exit *exec.ExitError
			if err := cmd.Run(); !errors.As(err, &exit) || exit.ExitCode() != 1 || stdout.Len() != 0 {
				t.Fatalf("expected startup failure and empty stdout, got error %v, stdout length %d", err, stdout.Len())
			}
			if !strings.Contains(stderr.String(), "startup_cause="+test.code) {
				t.Fatal("missing stable lock failure category")
			}
			if strings.Contains(stderr.String(), dir) || strings.Contains(stderr.String(), "already running") {
				t.Fatal("lock failure exposed the raw diagnostic")
			}
			if strings.Contains(stderr.String(), "Use --force") != test.forceHint {
				t.Fatal("force hint should appear only on lock contention")
			}
		})
	}
}

func TestConfigLoadCauseDoesNotRetainPath(t *testing.T) {
	path := "private-config-path"
	for _, test := range []struct {
		err  error
		code string
	}{
		{&os.PathError{Op: "open", Path: path, Err: os.ErrNotExist}, "config_not_found"},
		{&os.PathError{Op: "open", Path: path, Err: os.ErrPermission}, "config_permission_denied"},
		{&os.PathError{Op: "read", Path: path, Err: io.ErrUnexpectedEOF}, "config_read_error"},
		{errors.New("private validation detail"), "config_invalid"},
	} {
		if got := configLoadCause(test.err); got != test.code {
			t.Errorf("got cause %q, want %q", got, test.code)
		}
	}
}

func TestForegroundLoggerKeepsRuntimeMessageOnlyOnTerminalPolicy(t *testing.T) {
	for _, details := range []bool{false, true} {
		var output bytes.Buffer
		logger := newOperationalLogger("debug", &output, details)
		logger.Warn("actionable runtime message", "error", errors.New("runtime cause"))
		for _, want := range []string{"actionable runtime message", "runtime cause"} {
			if strings.Contains(output.String(), want) != details {
				t.Errorf("unexpected presence of %q (terminal policy: %t)", want, details)
			}
		}
		if !details && !strings.Contains(output.String(), "[redacted]") {
			t.Error("non-terminal logger did not retain redaction")
		}
	}
}

func TestLogPrivacyStartupHelper(t *testing.T) {
	if os.Getenv("CC_LOG_PRIVACY_HELPER") != "1" {
		return
	}
	for i, arg := range os.Args {
		if arg == "--" {
			os.Args = append([]string{"cc-connect"}, os.Args[i+1:]...)
			main()
			os.Exit(0)
		}
	}
	t.Fatal("missing helper arguments")
}

func TestDefaultConfigCreationDoesNotExposeItsPath(t *testing.T) {
	const marker = "BOOTSTRAP_PATH_MARKER"
	path := filepath.Join(t.TempDir(), marker+".toml")
	cmd := exec.Command(os.Args[0], "-test.run=^TestLogPrivacyStartupHelper$", "--", "--config", path)
	cmd.Env = append(os.Environ(), "CC_LOG_PRIVACY_HELPER=1", "CC_LOG_FILE=")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bootstrap failed: %v: %s", err, out)
	}
	if strings.Contains(string(out), marker) {
		t.Fatalf("bootstrap exposed path: %s", out)
	}
	generated, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	const prompt = `app_secret = "" # set your Feishu app secret before starting`
	if !bytes.Contains(generated, []byte(prompt)) {
		t.Fatal("generated starter should ask the operator to set a Feishu app secret")
	}

	// Model an operator filling the generated template with runtime-only data;
	// only load/validate the config, never construct a live platform client.
	appID, appSecret := "cli_"+rand.Text(), rand.Text()
	edited := strings.Replace(string(generated), `app_id = "your-feishu-app-id"`, "app_id = "+strconv.Quote(appID), 1)
	edited = strings.Replace(edited, prompt, "app_secret = "+strconv.Quote(appSecret), 1)
	if err := os.WriteFile(path, []byte(edited), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal("user-edited starter did not pass config validation")
	}
	opts := cfg.Projects[0].Platforms[0].Options
	if opts["app_id"] != appID || opts["app_secret"] != appSecret {
		t.Fatal("user-edited starter did not preserve configured Feishu credentials")
	}
}

func TestPermissionHookKeepsJSONOnlyStdout(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestLogPrivacyStartupHelper$", "--", "_agy-permission-hook")
	const marker = "HOOK_CREDENTIAL_MARKER"
	cmd.Env = append(os.Environ(), "CC_LOG_PRIVACY_HELPER=1", "CC_LOG_FILE=", "CC_CONNECT_AGY_PERMISSION_ADDR="+marker, "CC_CONNECT_AGY_PERMISSION_TOKEN="+rand.Text())
	cmd.Stdin = strings.NewReader("{}")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stderr.String(), marker) {
		t.Fatalf("hook diagnostic exposed bridge data: %s", &stderr)
	}
	var decision map[string]any
	if err := json.Unmarshal(out, &decision); err != nil {
		t.Fatalf("hook stdout is not one JSON decision: %s", out)
	}
	if decision["decision"] != "deny" {
		t.Fatalf("expected fail-closed hook: %s", out)
	}
}
