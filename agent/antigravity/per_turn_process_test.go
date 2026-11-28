package antigravity

import (
	"context"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

// /ps queues for print mode (one agy process per turn) and sends into the
// running turn for ACP, whose session embeds the print-mode one.
func TestAgySessionsReportWhetherTheySpawnAProcessPerTurn(t *testing.T) {
	print := newAntigravitySessionState(context.Background(), "agy", nil, t.TempDir(), "", "yolo", "", nil, 0)
	defer print.cancel()
	var session core.AgentSession = print
	perTurn, ok := session.(core.PerTurnProcessSession)
	if !ok || !perTurn.SpawnsProcessPerTurn() {
		t.Fatal("print-mode session must report one process per turn")
	}
	session = &agyACPSession{antigravitySession: print}
	if perTurn, ok := session.(core.PerTurnProcessSession); ok && perTurn.SpawnsProcessPerTurn() {
		t.Fatal("ACP session keeps one process; /ps must reach its running turn")
	}
}
