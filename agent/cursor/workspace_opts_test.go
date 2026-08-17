package cursor

import (
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func TestWorkspaceAgentOptionsPreservesCmdAndEnv(t *testing.T) {
	a := &Agent{
		cmd:          "/custom/cursor-agent",
		cliExtraArgs: []string{"--force"},
		configEnv:    []string{"FOO=bar"},
		model:        "cursor-grok-4.6-high",
		mode:         "force",
	}

	snapshotter, ok := any(a).(core.WorkspaceAgentOptionSnapshotter)
	if !ok {
		t.Fatal("cursor agent does not implement WorkspaceAgentOptionSnapshotter")
	}
	opts := snapshotter.WorkspaceAgentOptions()
	if got, _ := opts["cmd"].(string); got != "/custom/cursor-agent --force" {
		t.Fatalf("cmd = %#v, want %q", opts["cmd"], "/custom/cursor-agent --force")
	}
	gotEnv, _ := opts["env"].(map[string]string)
	if gotEnv["FOO"] != "bar" {
		t.Fatalf("env = %#v, want FOO=bar", gotEnv)
	}

	// 多 workspace 重建必须带着自定义 cmd，不能回落到 PATH 上的裸 agent。
	rebuilt := &Agent{}
	cmd, extra := core.ParseCmdOpts(opts, "agent")
	rebuilt.cmd = cmd
	rebuilt.cliExtraArgs = extra
	rebuilt.configEnv = core.ParseConfigEnv(opts)
	again := rebuilt.WorkspaceAgentOptions()
	if again["cmd"] != opts["cmd"] {
		t.Fatalf("rebuilt cmd = %#v, want %#v", again["cmd"], opts["cmd"])
	}
}

func TestJoinCursorCommand(t *testing.T) {
	if got := joinCursorCommand("", nil); got != "" {
		t.Fatalf("empty cmd = %q, want empty", got)
	}
	if got := joinCursorCommand("/bin/cursor-agent", nil); got != "/bin/cursor-agent" {
		t.Fatalf("cmd only = %q", got)
	}
	if got := joinCursorCommand("/bin/cursor-agent", []string{"--force", "--yolo"}); got != "/bin/cursor-agent --force --yolo" {
		t.Fatalf("cmd+args = %q", got)
	}
}
