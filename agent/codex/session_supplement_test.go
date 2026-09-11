package codex

import "testing"

func TestCodexExecSession_DisablesConcurrentLegacySupplement(t *testing.T) {
	var session any = &codexSession{}
	policy, ok := session.(interface{ SupportsLegacySupplement() bool })
	if !ok || policy.SupportsLegacySupplement() {
		t.Fatal("exec session must reject legacy supplements that start concurrent exec processes")
	}
}
