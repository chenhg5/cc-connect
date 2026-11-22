package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
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
	if !strings.Contains(string(out), "level=ERROR") {
		t.Fatalf("missing bounded startup failure: %s", out)
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
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}

func TestPermissionHookKeepsJSONOnlyStdout(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestLogPrivacyStartupHelper$", "--", "_agy-permission-hook")
	const marker = "HOOK_CREDENTIAL_MARKER"
	cmd.Env = append(os.Environ(), "CC_LOG_PRIVACY_HELPER=1", "CC_LOG_FILE=", "CC_CONNECT_AGY_PERMISSION_ADDR="+marker, "CC_CONNECT_AGY_PERMISSION_TOKEN=synthetic")
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
