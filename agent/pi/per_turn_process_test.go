package pi

import (
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

// JSON mode starts one `pi --mode json` per Send, so /ps must queue; RPC mode
// steers the running turn of its one process, so /ps goes straight to it.
func TestPiSessionSpawnsProcessPerTurnOnlyInJSONMode(t *testing.T) {
	for _, tc := range []struct {
		rpc  bool
		want bool
	}{{rpc: false, want: true}, {rpc: true, want: false}} {
		var session core.AgentSession = &piSession{rpc: tc.rpc}
		perTurn, ok := session.(core.PerTurnProcessSession)
		if !ok || perTurn.SpawnsProcessPerTurn() != tc.want {
			t.Fatalf("rpc=%v: SpawnsProcessPerTurn = %v, want %v", tc.rpc, ok && perTurn.SpawnsProcessPerTurn(), tc.want)
		}
	}
}
