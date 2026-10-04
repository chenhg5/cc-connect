package codex

import "testing"

func TestManagedBusyMessageModeConfigAndWorkspaceClone(t *testing.T) {
	for _, mode := range []string{"queue", "steer"} {
		a := fixtureManagedAgent(t, "/socket", t.TempDir(), map[string]any{"daemon_busy_message_mode": mode})
		if a.AutoSteerBusyMessages() != (mode == "steer") {
			t.Fatalf("incorrect policy for %s", mode)
		}
		clone, err := New(a.WorkspaceAgentOptions())
		if err != nil {
			t.Fatal(err)
		}
		if clone.(*managedAgent).daemon.busyMessageMode != mode {
			t.Fatal("workspace clone lost busy message mode")
		}
	}
	defaults, err := parseManagedOptions(nil)
	if err != nil || defaults.busyMessageMode != "queue" {
		t.Fatalf("default mode changed: %+v %v", defaults, err)
	}
	for _, opts := range []map[string]any{
		{"daemon_busy_message_mode": "invalid"},
		{"daemon_busy_message_mode": true},
		{"daemon_busy_message_mode": "steer", "daemon_enable_steer": false},
	} {
		if _, err := parseManagedOptions(opts); err == nil {
			t.Fatalf("invalid configuration accepted: %v", opts)
		}
	}
}
