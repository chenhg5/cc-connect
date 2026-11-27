package pi

import (
	"os"
	"os/exec"
	"testing"
)

func TestKillRPCJoinsCmdWait(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	s := &piSession{rpcCmd: cmd}
	cmd.Stderr = &s.stderrBuf
	prepareCmdForKill(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}

	s.killRPC()
	if cmd.ProcessState == nil {
		t.Fatal("killRPC returned before exec.Cmd.Wait joined stderr copying")
	}
	s.killRPC() // Concurrent shutdown paths may each try to reap the process.
}
