package core

import (
	"context"
	"strings"
	"testing"
)

// headlessDoctorAgent is an HTTP/webhook agent with no local executable.
type headlessDoctorAgent struct{ stubAgent }

func (h *headlessDoctorAgent) Name() string           { return "headless" }
func (h *headlessDoctorAgent) CLIBinaryName() string  { return "" }
func (h *headlessDoctorAgent) CLIDisplayName() string { return "Headless" }

func TestCheckAgentBinary_SkipsHeadlessAgent(t *testing.T) {
	got := checkAgentBinary(context.Background(), &headlessDoctorAgent{})
	if len(got) != 1 {
		t.Fatalf("checks = %+v", got)
	}
	if got[0].Status != DoctorPass {
		t.Fatalf("status = %v, detail = %s", got[0].Status, got[0].Detail)
	}
	if strings.Contains(got[0].Detail, "not found") {
		t.Fatalf("looked up a binary: %+v", got[0])
	}
	if !strings.Contains(got[0].Name, "Headless") {
		t.Fatalf("name = %q", got[0].Name)
	}
	if auth := checkAgentAuth(context.Background(), &headlessDoctorAgent{}); len(auth) != 0 {
		t.Fatalf("auth checks = %+v, want none", auth)
	}
}

func TestCheckAgentBinary_StillLooksUpCLIAgents(t *testing.T) {
	got := checkAgentBinary(context.Background(), &stubAgent{})
	if len(got) != 1 || got[0].Status != DoctorFail {
		t.Fatalf("checks = %+v, want a PATH miss for the stub CLI name", got)
	}
	if !strings.Contains(got[0].Detail, "not found") {
		t.Fatalf("detail = %q", got[0].Detail)
	}
}
