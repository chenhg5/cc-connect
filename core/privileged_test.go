package core

import (
	"strings"
	"testing"
)

func TestIsPrivilegedCommandInvocation_StaticListUnchanged(t *testing.T) {
	for cmd := range privilegedCommands {
		if !isPrivilegedCommandInvocation(cmd, nil) {
			t.Errorf("%q should still be privileged without arguments", cmd)
		}
		if !isPrivilegedCommandInvocation(cmd, []string{"anything"}) {
			t.Errorf("%q should still be privileged with arguments", cmd)
		}
	}
}

func TestIsPrivilegedCommandInvocation_ExecRegistration(t *testing.T) {
	tests := []struct {
		command    string
		arguments  []string
		privileged bool
	}{
		{command: "commands", arguments: nil, privileged: false},
		{command: "commands", arguments: []string{"list"}, privileged: false},
		{command: "commands", arguments: []string{"add"}, privileged: false},
		{command: "commands", arguments: []string{"addexec"}, privileged: true},
		{command: "commands", arguments: []string{"ADDEXEC"}, privileged: true},
		{command: "commands", arguments: []string{"addEx"}, privileged: true},
		{command: "cron", arguments: nil, privileged: false},
		{command: "cron", arguments: []string{"add"}, privileged: false},
		{command: "cron", arguments: []string{"list"}, privileged: false},
		{command: "cron", arguments: []string{"addexec"}, privileged: true},
		{command: "cron", arguments: []string{"ADDEXEC"}, privileged: true},
		{command: "help", arguments: []string{"addexec"}, privileged: false},
	}

	for _, test := range tests {
		if got := isPrivilegedCommandInvocation(test.command, test.arguments); got != test.privileged {
			t.Errorf("isPrivilegedCommandInvocation(%q, %q) = %t, want %t", test.command, test.arguments, got, test.privileged)
		}
	}
}

func TestHandleCommand_ExecRegistrationRequiresAdmin(t *testing.T) {
	for _, command := range []string{
		"/commands addexec deploy echo deploy",
		"/cron addexec daily echo daily",
	} {
		t.Run(strings.Fields(command)[0], func(t *testing.T) {
			platform := &stubPlatformEngine{n: "test"}
			engine := NewEngine("test", &stubAgent{}, []Platform{platform}, "", LangEnglish)
			engine.SetAdminFrom("")

			message := &Message{UserID: "ordinary-user", Platform: "test", ReplyCtx: "reply"}
			if !engine.handleCommand(platform, message, command) {
				t.Fatal("exec registration was not intercepted")
			}
			sent := platform.getSent()
			if len(sent) == 0 || !strings.Contains(strings.ToLower(sent[0]), "admin") {
				t.Fatalf("expected an admin-required reply, got %#v", sent)
			}
		})
	}
}

func TestHandleCommand_NonExecListingRemainsAvailable(t *testing.T) {
	platform := &stubPlatformEngine{n: "test"}
	engine := NewEngine("test", &stubAgent{}, []Platform{platform}, "", LangEnglish)
	engine.SetAdminFrom("")

	message := &Message{UserID: "ordinary-user", Platform: "test", ReplyCtx: "reply"}
	if !engine.handleCommand(platform, message, "/commands list") {
		t.Fatal("/commands list was not handled")
	}
	for _, sent := range platform.getSent() {
		lower := strings.ToLower(sent)
		if strings.Contains(lower, "admin") && strings.Contains(lower, "required") {
			t.Fatalf("/commands list was unexpectedly admin-gated: %q", sent)
		}
	}
}
