package antigravity

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func TestSlugify(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"cc-connect", "cc-connect"},
		{"Daily", "daily"},
		{"My Project", "my-project"},
		{"hello_world", "hello-world"},
		{"Test.123", "test-123"},
		{"---weird---", "weird"},
		{"", "project"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := slugify(tt.input)
			if got != tt.want {
				t.Errorf("slugify(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestNormalizeMode(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"default", "default"},
		{"yolo", "yolo"},
		{"auto", "yolo"},
		{"force", "yolo"},
		{"plan", "plan"},
		{"sandbox", "plan"},
		{"invalid", "default"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := normalizeMode(tt.input)
			if got != tt.want {
				t.Errorf("normalizeMode(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestSession_ContinueSessionTreatedAsFresh(t *testing.T) {
	s, err := newAntigravitySession(context.Background(), "echo", "/tmp", "", "default", core.ContinueSession, nil, 0)
	if err != nil {
		t.Fatalf("newAntigravitySession: %v", err)
	}
	defer func() { _ = s.Close() }()

	if got := s.CurrentSessionID(); got != "" {
		t.Errorf("ContinueSession should be treated as fresh: chatID = %q, want empty", got)
	}
}

func TestBuildAntigravityArgs_PromptAtEnd(t *testing.T) {
	args := buildAntigravityArgs("sid-1", true, "plan", "What is 1+1?")
	if len(args) < 2 {
		t.Fatalf("args too short: %v", args)
	}
	if args[len(args)-2] != "-p" || args[len(args)-1] != "What is 1+1?" {
		t.Fatalf("expected prompt to be final '-p <prompt>', got: %v", args)
	}
	if !contains(args, "--sandbox") {
		t.Fatalf("expected --sandbox in args, got: %v", args)
	}
	if contains(args, "-m") || contains(args, "--model") {
		t.Fatalf("did not expect model flags in args, got: %v", args)
	}
}

func TestUsesInteractivePermission(t *testing.T) {
	if !usesInteractivePermission("default") {
		t.Fatal("default mode should use interactive permission stdin")
	}
	if usesInteractivePermission("yolo") {
		t.Fatal("yolo mode should not use interactive permission stdin")
	}
	if usesInteractivePermission("plan") {
		t.Fatal("plan mode should not use interactive permission stdin")
	}
}

func TestRespondPermission_WritesTerminalAnswer(t *testing.T) {
	s, err := newAntigravitySession(context.Background(), "echo", "/tmp", "", "default", "", nil, 0)
	if err != nil {
		t.Fatalf("newAntigravitySession: %v", err)
	}
	defer func() { _ = s.Close() }()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	defer func() { _ = r.Close() }()
	defer func() { _ = w.Close() }()
	s.stdin = w

	s.permReqID.Store("req")
	if err := s.RespondPermission("req", core.PermissionResult{Behavior: "allow"}); err != nil {
		t.Fatalf("RespondPermission allow: %v", err)
	}
	buf := make([]byte, 8)
	n, err := r.Read(buf)
	if err != nil && err != io.EOF {
		t.Fatalf("read allow response: %v", err)
	}
	if got := string(buf[:n]); got != "y\n" {
		t.Fatalf("allow response = %q, want %q", got, "y\n")
	}

	s.permReqID.Store("req")
	if err := s.RespondPermission("req", core.PermissionResult{Behavior: "deny"}); err != nil {
		t.Fatalf("RespondPermission deny: %v", err)
	}
	n, err = r.Read(buf)
	if err != nil && err != io.EOF {
		t.Fatalf("read deny response: %v", err)
	}
	if got := string(buf[:n]); got != "n\n" {
		t.Fatalf("deny response = %q, want %q", got, "n\n")
	}
}

func TestExtractPermissionPrompt(t *testing.T) {
	text := "Tool wants to run command. Allow this action? (y/N)"
	got, ok := extractPermissionPrompt(text)
	if !ok {
		t.Fatalf("expected permission prompt to be detected")
	}
	if got == "" {
		t.Fatalf("detected prompt should not be empty")
	}
}

func TestExtractPermissionPrompt_SplitChunksDetectedInWindow(t *testing.T) {
	part1 := "Tool wants to run command. Allow this"
	part2 := " action? (y/N)"
	got, ok := extractPermissionPrompt(part1 + part2)
	if !ok || got == "" {
		t.Fatalf("expected split prompt to be detected, got ok=%v prompt=%q", ok, got)
	}
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if strings.TrimSpace(x) == want {
			return true
		}
	}
	return false
}
