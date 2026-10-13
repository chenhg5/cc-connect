package acp

import (
	"github.com/chenhg5/cc-connect/core"
	"testing"
)

func TestProjectDirectories_TraeOnly(t *testing.T) {
	for _, command := range []string{"traecli", "other-acp-agent"} {
		a := &Agent{cmd: command}
		d, ok := any(a).(core.ProjectDirectoryConfigurer)
		if !ok {
			t.Fatal("ACP does not implement project directories")
		}
		err := d.ConfigureProjectDirectories(t.TempDir(), []string{t.TempDir()})
		if (err == nil) != (command == "traecli") {
			t.Fatalf("%s: %v", command, err)
		}
	}
}
