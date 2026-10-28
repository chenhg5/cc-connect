package pi

import (
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

// Only RPC mode steers into the running process; JSON mode spawns a new
// one-shot process per Send and must keep using the queue.
func TestPiSession_AcceptsMidTurnInputOnlyInRPCMode(t *testing.T) {
	for _, tt := range []struct {
		rpc  bool
		want bool
	}{{rpc: true, want: true}, {rpc: false, want: false}} {
		var s core.AgentSession = &piSession{rpc: tt.rpc}
		sender, ok := s.(core.MidTurnInputSender)
		if !ok {
			t.Fatal("piSession must implement core.MidTurnInputSender")
		}
		if got := sender.AcceptsMidTurnInput(); got != tt.want {
			t.Fatalf("rpc=%v: AcceptsMidTurnInput() = %v, want %v", tt.rpc, got, tt.want)
		}
	}
}
