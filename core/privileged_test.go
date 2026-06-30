package core

import (
	"strings"
	"testing"
)

func TestIsPrivilegedCommandInvocation_StaticListUnchanged(t *testing.T) {
	for cmd := range privilegedCommands {
		if !isPrivilegedCommandInvocation(cmd, nil) {
			t.Errorf("%q should still be privileged even without args", cmd)
		}
		if !isPrivilegedCommandInvocation(cmd, []string{"whatever"}) {
			t.Errorf("%q should still be privileged with arbitrary args", cmd)
		}
	}
}

func TestIsPrivilegedCommandInvocation_CommandsSubcommandGate(t *testing.T) {
	if isPrivilegedCommandInvocation("commands", nil) {
		t.Fatal("/commands without subcommand must not be privileged")
	}
	for _, sub := range []string{"list", "add", "del", "delete", "rm", "remove"} {
		if isPrivilegedCommandInvocation("commands", []string{sub}) {
			t.Errorf("/commands %s must NOT require admin", sub)
		}
	}
	for _, sub := range []string{"addexec", "ADDEXEC", "addEx"} {
		if !isPrivilegedCommandInvocation("commands", []string{sub}) {
			t.Errorf("/commands %s must require admin", sub)
		}
	}
}

func TestIsPrivilegedCommandInvocation_CronSubcommandGate(t *testing.T) {
	if isPrivilegedCommandInvocation("cron", nil) {
		t.Fatal("/cron without subcommand must not be privileged")
	}
	for _, sub := range []string{"add", "list", "del", "enable", "disable", "mute", "unmute", "setup"} {
		if isPrivilegedCommandInvocation("cron", []string{sub}) {
			t.Errorf("/cron %s must NOT require admin", sub)
		}
	}
	for _, sub := range []string{"addexec", "ADDEXEC"} {
		if !isPrivilegedCommandInvocation("cron", []string{sub}) {
			t.Errorf("/cron %s must require admin", sub)
		}
	}
}

func TestIsPrivilegedCommandInvocation_UnknownCmdNotPrivileged(t *testing.T) {
	if isPrivilegedCommandInvocation("help", []string{"addexec"}) {
		t.Error("/help addexec is not a real privileged path; only commands/cron addexec are")
	}
}

func TestHandleCommand_CommandsAddexecBlocksNonAdmin(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	e.SetAdminFrom("") // no admins → addexec must be denied for everyone

	msg := &Message{UserID: "u", Platform: "test", ReplyCtx: "rctx"}
	handled := e.handleCommand(p, msg, "/commands addexec foo bar")
	if !handled {
		t.Fatal("addexec invocation must be intercepted, not forwarded to agent")
	}
	sent := p.getSent()
	if len(sent) == 0 || !strings.Contains(strings.ToLower(sent[0]), "admin") {
		t.Fatalf("expected admin-required reply; got %#v", sent)
	}
}

func TestHandleCommand_CommandsListNoAdmin(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	e.SetAdminFrom("")

	msg := &Message{UserID: "u", Platform: "test", ReplyCtx: "rctx"}
	handled := e.handleCommand(p, msg, "/commands list")
	if !handled {
		t.Fatal("/commands list must be handled by cc-connect")
	}
	for _, s := range p.getSent() {
		if strings.Contains(strings.ToLower(s), "admin") &&
			strings.Contains(strings.ToLower(s), "required") {
			t.Errorf("/commands list must NOT be admin-gated; got %q", s)
		}
	}
}

func TestHandleCommand_CronAddexecBlocksNonAdmin(t *testing.T) {
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	e.SetAdminFrom("")

	msg := &Message{UserID: "u", Platform: "test", ReplyCtx: "rctx"}
	handled := e.handleCommand(p, msg, "/cron addexec daily echo hi")
	if !handled {
		t.Fatal("cron addexec must be intercepted")
	}
	sent := p.getSent()
	if len(sent) == 0 || !strings.Contains(strings.ToLower(sent[0]), "admin") {
		t.Fatalf("expected admin-required reply; got %#v", sent)
	}
}

