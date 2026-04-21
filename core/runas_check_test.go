//go:build !windows

package core

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestPreflightRunAsUser_AllPass(t *testing.T) {
	runner := &stubSudoRunner{
		script: map[string]stubResponse{
			key("-n", "-iu", "target", "--", "/bin/true"):                          {nil, nil},
			key("-n", "-iu", "target", "--", "sudo", "-n", "/bin/true"):            {nil, &exec.ExitError{}}, // escalation fails as required
			key("-n", "-iu", "target", "--", "test", "-r", "/tmp/wd", "-a", "-w", "/tmp/wd"): {nil, nil},
		},
	}
	// Build the find args the real scanner will use. For this test we
	// just make the find call return empty output so no warnings are
	// generated.
	cfg := PreflightConfig{
		Project:   "demo",
		RunAsUser: "target",
		WorkDir:   "/tmp/wd",
		Runner:    runner,
		ScanConfig: DescendantScanConfig{
			PrunePaths: []string{".git"},
			MaxReport:  50,
			Timeout:    time.Second,
		},
	}
	// Script the find call to return empty.
	findArgs := []string{
		"-n", "-iu", "target", "--",
		"find", "/tmp/wd",
		"(", "-name", ".git", ")", "-prune", "-o",
		"(",
		"-not", "-readable", "-printf", `noread\t%p\n`,
		",",
		"-type", "f", "-not", "-writable", "-printf", `nowrite\t%p\n`,
		",",
		"-type", "d", "-not", "-executable", "-printf", `nosearch\t%p\n`,
		")",
	}
	runner.script[key(findArgs...)] = stubResponse{out: []byte(""), err: nil}

	result := PreflightRunAsUser(context.Background(), cfg)
	if result.HasFatal() {
		t.Fatalf("want no fatal, got: %v", result.Fatal)
	}
	if len(result.Warnings) != 0 {
		t.Fatalf("want no warnings, got: %v", result.Warnings)
	}
}

func TestPreflightRunAsUser_NoSudoToTargetIsFatal(t *testing.T) {
	runner := &stubSudoRunner{
		script: map[string]stubResponse{
			key("-n", "-iu", "target", "--", "/bin/true"): {[]byte("a password is required"), &exec.ExitError{}},
		},
	}
	cfg := PreflightConfig{Project: "demo", RunAsUser: "target", WorkDir: "/tmp/wd", Runner: runner}
	result := PreflightRunAsUser(context.Background(), cfg)
	if !result.HasFatal() {
		t.Fatal("want fatal error")
	}
	if !strings.Contains(result.Fatal[0].Error(), "passwordless sudo to user") {
		t.Errorf("want 'passwordless sudo to user' in error, got %v", result.Fatal[0])
	}
}

func TestPreflightRunAsUser_TargetCanEscalateIsFatal(t *testing.T) {
	runner := &stubSudoRunner{
		script: map[string]stubResponse{
			key("-n", "-iu", "target", "--", "/bin/true"):               {nil, nil},
			key("-n", "-iu", "target", "--", "sudo", "-n", "/bin/true"): {nil, nil}, // BAD
			key("-n", "-iu", "target", "--", "sudo", "-n", "-l"):        {[]byte("(ALL) NOPASSWD: ALL"), nil},
			key("-n", "-iu", "target", "--", "test", "-r", "/tmp/wd", "-a", "-w", "/tmp/wd"): {nil, nil},
		},
	}
	findArgs := []string{
		"-n", "-iu", "target", "--",
		"find", "/tmp/wd",
		"(",
		"-not", "-readable", "-printf", `noread\t%p\n`,
		",",
		"-type", "f", "-not", "-writable", "-printf", `nowrite\t%p\n`,
		",",
		"-type", "d", "-not", "-executable", "-printf", `nosearch\t%p\n`,
		")",
	}
	runner.script[key(findArgs...)] = stubResponse{out: nil, err: nil}

	cfg := PreflightConfig{
		Project:    "demo",
		RunAsUser:  "target",
		WorkDir:    "/tmp/wd",
		Runner:     runner,
		ScanConfig: DescendantScanConfig{MaxReport: 50, Timeout: time.Second},
	}
	result := PreflightRunAsUser(context.Background(), cfg)
	if !result.HasFatal() {
		t.Fatal("want fatal error")
	}
	if !strings.Contains(result.Fatal[0].Error(), "can run passwordless sudo") {
		t.Errorf("want 'can run passwordless sudo' in error, got %v", result.Fatal[0])
	}
	if !strings.Contains(result.Fatal[0].Error(), "NOPASSWD: ALL") {
		t.Errorf("want sudo -l output included in error, got %v", result.Fatal[0])
	}
}

func TestPreflightRunAsUser_WorkDirInaccessibleIsFatal(t *testing.T) {
	runner := &stubSudoRunner{
		script: map[string]stubResponse{
			key("-n", "-iu", "target", "--", "/bin/true"):                          {nil, nil},
			key("-n", "-iu", "target", "--", "sudo", "-n", "/bin/true"):            {nil, &exec.ExitError{}},
			key("-n", "-iu", "target", "--", "test", "-r", "/tmp/wd", "-a", "-w", "/tmp/wd"): {[]byte(""), &exec.ExitError{}},
		},
	}
	cfg := PreflightConfig{
		Project:    "demo",
		RunAsUser:  "target",
		WorkDir:    "/tmp/wd",
		Runner:     runner,
		ScanConfig: DescendantScanConfig{MaxReport: 50, Timeout: time.Second},
	}
	result := PreflightRunAsUser(context.Background(), cfg)
	if !result.HasFatal() {
		t.Fatal("want fatal error")
	}
	if !strings.Contains(result.Fatal[0].Error(), "cannot read AND write work_dir") {
		t.Errorf("want 'cannot read AND write work_dir' in error, got %v", result.Fatal[0])
	}
}

func TestPreflightRunAsUser_DescendantWarnings(t *testing.T) {
	runner := &stubSudoRunner{
		script: map[string]stubResponse{
			key("-n", "-iu", "target", "--", "/bin/true"):                          {nil, nil},
			key("-n", "-iu", "target", "--", "sudo", "-n", "/bin/true"):            {nil, &exec.ExitError{}},
			key("-n", "-iu", "target", "--", "test", "-r", "/tmp/wd", "-a", "-w", "/tmp/wd"): {nil, nil},
		},
	}
	findArgs := []string{
		"-n", "-iu", "target", "--",
		"find", "/tmp/wd",
		"(",
		"-not", "-readable", "-printf", `noread\t%p\n`,
		",",
		"-type", "f", "-not", "-writable", "-printf", `nowrite\t%p\n`,
		",",
		"-type", "d", "-not", "-executable", "-printf", `nosearch\t%p\n`,
		")",
	}
	runner.script[key(findArgs...)] = stubResponse{
		out: []byte("noread\t/tmp/wd/secret.env\nnowrite\t/tmp/wd/readonly.log\n"),
		err: nil,
	}
	cfg := PreflightConfig{
		Project:    "demo",
		RunAsUser:  "target",
		WorkDir:    "/tmp/wd",
		Runner:     runner,
		ScanConfig: DescendantScanConfig{MaxReport: 50, Timeout: time.Second},
	}
	result := PreflightRunAsUser(context.Background(), cfg)
	if result.HasFatal() {
		t.Fatalf("want no fatal, got: %v", result.Fatal)
	}
	if len(result.Warnings) != 1 {
		t.Fatalf("want 1 warning, got %d: %v", len(result.Warnings), result.Warnings)
	}
	if !strings.Contains(result.Warnings[0], "secret.env") || !strings.Contains(result.Warnings[0], "readonly.log") {
		t.Errorf("warning missing expected paths: %s", result.Warnings[0])
	}
}

func TestPreflightRunAsUser_DescendantWarningsCapped(t *testing.T) {
	runner := &stubSudoRunner{
		script: map[string]stubResponse{
			key("-n", "-iu", "target", "--", "/bin/true"):                          {nil, nil},
			key("-n", "-iu", "target", "--", "sudo", "-n", "/bin/true"):            {nil, &exec.ExitError{}},
			key("-n", "-iu", "target", "--", "test", "-r", "/tmp/wd", "-a", "-w", "/tmp/wd"): {nil, nil},
		},
	}
	// Generate 75 lines; cap is 3.
	var sb strings.Builder
	for i := 0; i < 75; i++ {
		sb.WriteString("noread\t/tmp/wd/file")
		sb.WriteString(strconv.Itoa(i))
		sb.WriteByte('\n')
	}
	findArgs := []string{
		"-n", "-iu", "target", "--",
		"find", "/tmp/wd",
		"(",
		"-not", "-readable", "-printf", `noread\t%p\n`,
		",",
		"-type", "f", "-not", "-writable", "-printf", `nowrite\t%p\n`,
		",",
		"-type", "d", "-not", "-executable", "-printf", `nosearch\t%p\n`,
		")",
	}
	runner.script[key(findArgs...)] = stubResponse{out: []byte(sb.String()), err: nil}
	cfg := PreflightConfig{
		Project:    "demo",
		RunAsUser:  "target",
		WorkDir:    "/tmp/wd",
		Runner:     runner,
		ScanConfig: DescendantScanConfig{MaxReport: 3, Timeout: time.Second},
	}
	result := PreflightRunAsUser(context.Background(), cfg)
	if result.HasFatal() {
		t.Fatalf("want no fatal, got: %v", result.Fatal)
	}
	if len(result.Warnings) != 1 {
		t.Fatalf("want 1 warning, got %d", len(result.Warnings))
	}
	w := result.Warnings[0]
	if !strings.Contains(w, "... and 72 more") {
		t.Errorf("want '... and 72 more' in warning, got: %s", w)
	}
}

