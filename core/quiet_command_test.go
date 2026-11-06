package core

import (
	"strings"
	"testing"
	"time"
)

// waitSent waits up to d for p to have sent at least n messages.
func waitSent(p *stubPlatformEngine, n int, d time.Duration) []string {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if sent := p.getSent(); len(sent) >= n {
			return sent
		}
		time.Sleep(20 * time.Millisecond)
	}
	return p.getSent()
}

// A regular exec command that prints nothing still answers "(no output)".
func TestEngine_ExecCommand_EmptyOutputStillAnswers(t *testing.T) {
	e := newTestEngine()
	e.SetAdminFrom("owner")
	e.commands.Add("loud", "", "", "true", t.TempDir(), "config")
	p := &stubPlatformEngine{n: "test"}
	e.handleCommand(p, &Message{SessionKey: "test:owner", UserID: "owner", ReplyCtx: "ctx"}, "/loud")
	sent := waitSent(p, 1, 5*time.Second)
	if len(sent) != 1 || !strings.Contains(sent[0], "(no output)") {
		t.Fatalf("expected the (no output) reply, got %q", sent)
	}
}

// A quiet command sends nothing for an empty stdout, no progress message
// while it runs, its stdout as plain text, and a failure as an error.
func TestEngine_QuietExecCommand(t *testing.T) {
	e := newTestEngine()
	e.SetAdminFrom("owner")
	dir := t.TempDir()
	// The sleeps outlast quickFinishTimeout, past which a regular exec
	// command would post a progress message.
	e.commands.Add("silent", "", "", "sleep 0.8", dir, "config")
	e.commands.Add("answer", "", "", "sleep 0.6; echo opened {{args}}", dir, "config")
	e.commands.Add("broken", "", "", "echo nope >&2; exit 3", dir, "config")
	for _, name := range []string{"silent", "answer", "broken"} {
		if !e.commands.MarkQuiet(name) {
			t.Fatalf("MarkQuiet did not find %s", name)
		}
	}
	if e.commands.MarkQuiet("missing") {
		t.Fatal("MarkQuiet found a command that does not exist")
	}
	msg := func() *Message {
		return &Message{SessionKey: "test:owner", UserID: "owner", ReplyCtx: "ctx"}
	}

	p := &stubPlatformEngine{n: "test"}
	e.handleCommand(p, msg(), "/silent")
	time.Sleep(1500 * time.Millisecond)
	if sent := p.getSent(); len(sent) != 0 {
		t.Fatalf("a quiet command with no output sent %q", sent)
	}

	e.handleCommand(p, msg(), "/answer debug")
	sent := waitSent(p, 1, 5*time.Second)
	if len(sent) != 1 || sent[0] != "opened debug" {
		t.Fatalf("expected only the plain answer, got %q", sent)
	}

	p.clearSent()
	e.handleCommand(p, msg(), "/broken")
	sent = waitSent(p, 1, 5*time.Second)
	if len(sent) != 1 || !strings.Contains(sent[0], "exit code 3") || !strings.Contains(sent[0], "nope") {
		t.Fatalf("expected the failure reported, got %q", sent)
	}
}
