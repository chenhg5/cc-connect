package codex

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestDesktopHelperErrorProcess(t *testing.T) {
	if os.Getenv("CC_CONNECT_DESKTOP_ERROR_TEST") != "1" {
		return
	}
	fmt.Fprintln(os.Stderr, "Desktop owner unavailable; keep Codex open", os.Getenv("TEST_API_TOKEN"), "argument-secret", strings.Repeat("长", 1024))
	os.Exit(3)
}

func TestDesktopHelperErrorPreservesBoundedRedactedDiagnostics(t *testing.T) {
	a := &Agent{desktopHelper: []string{os.Args[0], "-test.run=^TestDesktopHelperErrorProcess$", "--", "--token=argument-secret"}, configEnv: []string{"CC_CONNECT_DESKTOP_ERROR_TEST=1", "TEST_API_TOKEN=environment-secret"}}
	_, err := a.desktopCall(context.Background(), "reply", "thread", "queue", "message", "hello")
	if err == nil || !strings.Contains(err.Error(), "Desktop owner unavailable") {
		t.Fatal("actionable helper stderr was lost", err)
	}
	if strings.Contains(err.Error(), "environment-secret") || strings.Contains(err.Error(), "argument-secret") {
		t.Fatal("helper stderr leaked credentials")
	}
	if utf8.RuneCountInString(err.Error()) > 700 || !utf8.ValidString(err.Error()) {
		t.Fatal("helper stderr was not bounded UTF-8")
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		t.Fatal("underlying helper failure was not wrapped", err)
	}
}
