package acp

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func TestListSessionsIncludesLateProbeStderr(t *testing.T) {
	agent := &Agent{
		cmd:      os.Args[0],
		args:     []string{"-test.run=^TestLateProbeStderrHelper$"},
		workDir:  t.TempDir(),
		extraEnv: []string{"CC_ACP_LATE_STDERR_HELPER=1"},
	}
	_, err := agent.ListSessions(context.Background())
	if err == nil || !strings.Contains(err.Error(), "late probe diagnostic") {
		t.Fatalf("ListSessions() error = %v, want late stderr diagnostic", err)
	}
}

func TestLateProbeStderrHelper(t *testing.T) {
	if os.Getenv("CC_ACP_LATE_STDERR_HELPER") != "1" {
		return
	}
	_ = os.Stdout.Close()
	time.Sleep(100 * time.Millisecond)
	_, _ = fmt.Fprint(os.Stderr, "late probe diagnostic")
	os.Exit(0)
}
