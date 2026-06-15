package kiro

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func TestNormalizeMode(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"", "default"},
		{"default", "default"},
		{"yolo", "yolo"},
		{"AUTO", "yolo"},
		{"trust-all", "yolo"},
		{"unknown", "default"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := normalizeMode(tt.input); got != tt.want {
				t.Fatalf("normalizeMode(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestAgentBasics(t *testing.T) {
	a := &Agent{cmd: "kiro-cli"}
	if got := a.Name(); got != "kiro" {
		t.Fatalf("Name() = %q, want kiro", got)
	}
	if got := a.CLIBinaryName(); got != "kiro-cli" {
		t.Fatalf("CLIBinaryName() = %q, want kiro-cli", got)
	}
	if got := a.CLIDisplayName(); got != "Kiro CLI" {
		t.Fatalf("CLIDisplayName() = %q, want Kiro CLI", got)
	}
	a.SetWorkDir("/tmp/project")
	if got := a.GetWorkDir(); got != "/tmp/project" {
		t.Fatalf("GetWorkDir() = %q", got)
	}
	a.SetModel("claude-sonnet-4.5")
	if got := a.GetModel(); got != "claude-sonnet-4.5" {
		t.Fatalf("GetModel() = %q", got)
	}
}

func TestResolveKiroCmdExplicitPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "kiro-cli")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := resolveKiroCmd(path)
	if err != nil {
		t.Fatalf("resolveKiroCmd: %v", err)
	}
	if got != path {
		t.Fatalf("resolveKiroCmd() = %q, want %q", got, path)
	}
}

func TestAgentStartSessionWorkDirRace(t *testing.T) {
	a := &Agent{cmd: "kiro-cli", workDir: "/initial"}

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			a.SetWorkDir(fmt.Sprintf("/path-%d", i))
		}(i)
		go func() {
			defer wg.Done()
			sess, err := a.StartSession(context.Background(), "")
			if err != nil {
				t.Errorf("StartSession: %v", err)
				return
			}
			_ = sess.Close()
		}()
	}
	wg.Wait()
}

func TestBuildArgs(t *testing.T) {
	ks, err := newKiroSession(context.Background(), "kiro-cli", "/tmp", "model-a", "yolo", "reviewer", "kas", "spec", "", "session-123", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ks.Close()

	got := ks.buildArgs("hello")
	want := []string{
		"chat", "--no-interactive",
		"--resume-id", "session-123",
		"--trust-all-tools",
		"--model", "model-a",
		"--agent", "reviewer",
		"--agent-engine", "kas",
		"--mode", "spec",
		"hello",
	}
	if len(got) != len(want) {
		t.Fatalf("args len = %d, want %d: %#v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("arg[%d] = %q, want %q; args=%#v", i, got[i], want[i], got)
		}
	}
}

func TestBuildArgsTrustTools(t *testing.T) {
	ks, err := newKiroSession(context.Background(), "kiro-cli", "/tmp", "auto", "default", "", "", "", "fs_read,fs_write", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ks.Close()

	got := ks.buildArgs("hello")
	joined := fmt.Sprintf("%q", got)
	if !containsArgPair(got, "--trust-tools", "fs_read,fs_write") {
		t.Fatalf("args missing trust-tools: %s", joined)
	}
	if containsArg(got, "--model") {
		t.Fatalf("auto model should not be passed: %s", joined)
	}
}

func TestExtractSessionID(t *testing.T) {
	tests := map[string]string{
		"Session: abc-123":     "abc-123",
		"Session ID: sess_001": "sess_001",
		"no session here":      "",
	}
	for input, want := range tests {
		if got := extractSessionID(input); got != want {
			t.Fatalf("extractSessionID(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestCleanKiroOutput(t *testing.T) {
	input := "\x1b[?25l\r▰▱▱ Opening browser... | Press (^) + C to cancel\nhello\n"
	if got := cleanKiroOutput(input); got != "hello" {
		t.Fatalf("cleanKiroOutput() = %q, want hello", got)
	}
}

func TestParseModelList(t *testing.T) {
	raw := []byte(`{"models":[{"id":"m1","displayName":"Model One"},{"name":"m2","description":"Model Two"}]}`)
	got := parseModelList(raw)
	if len(got) != 2 {
		t.Fatalf("len(models) = %d, want 2: %#v", len(got), got)
	}
	if got[0].Name != "m1" || got[0].Desc != "Model One" {
		t.Fatalf("first model = %#v", got[0])
	}
}

func TestParseSessionList(t *testing.T) {
	raw := []byte(`{"sessions":[{"id":"s1","title":"First","updated_at":"2026-06-10T12:00:00Z"}]}`)
	got := parseSessionList(raw)
	if len(got) != 1 {
		t.Fatalf("len(sessions) = %d, want 1: %#v", len(got), got)
	}
	if got[0].ID != "s1" || got[0].Summary != "First" {
		t.Fatalf("session = %#v", got[0])
	}
	if got[0].ModifiedAt.IsZero() || !got[0].ModifiedAt.Equal(time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)) {
		t.Fatalf("ModifiedAt = %v", got[0].ModifiedAt)
	}
}

func containsArg(args []string, needle string) bool {
	for _, arg := range args {
		if arg == needle {
			return true
		}
	}
	return false
}

func containsArgPair(args []string, key, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == key && args[i+1] == value {
			return true
		}
	}
	return false
}

var (
	_ core.Agent          = (*Agent)(nil)
	_ core.AgentSession   = (*kiroSession)(nil)
	_ core.SessionDeleter = (*Agent)(nil)
)
