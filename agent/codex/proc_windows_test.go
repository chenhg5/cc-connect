//go:build windows

package codex

import (
	"os/exec"
	"testing"
)

func TestBackgroundCodex_NoConsoleWindow(t *testing.T) {
	cmd := exec.Command("codex", "app-server")
	prepareCmdForKill(cmd)
	if !cmd.SysProcAttr.HideWindow || cmd.SysProcAttr.CreationFlags&0x08000000 == 0 {
		t.Fatal("background Codex can create a visible console window")
	}
}
