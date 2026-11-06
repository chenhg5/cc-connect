package core

import (
	"testing"
)

func TestSharedDaemonHistoryDoesNotExposeBridgeInvocationInstructions(t *testing.T) {
	env, _ := newSharedCompatEnv(t)
	env.e.dataDir = "/tmp/bridge data"
	prompt := "[cc-connect invocation context]\n{\"project\":\"shared\",\"session_key\":\"test:user\"}\nLegacy invocation instructions\n[/cc-connect invocation context]\n[cc-connect sender_id=user]\nORIGINAL USER INPUT"
	if got := StripSharedBridgePrompt("Configured first-turn preamble\n" + prompt); got != "ORIGINAL USER INPUT" {
		t.Fatalf("internal instructions visible: %s", got)
	}
	plain := "CLI USER INPUT"
	if StripSharedBridgePrompt(plain) != plain {
		t.Fatal("ordinary CLI input changed")
	}
}
