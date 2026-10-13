package codex

import (
	"github.com/chenhg5/cc-connect/core"
	"reflect"
	"testing"
)

func TestProjectDirectories_ReplaceValidateAndSnapshot(t *testing.T) {
	a := &Agent{cmd: "true", mode: "suggest", cliExtraArgs: []string{"wrapper"}}
	d, ok := any(a).(core.ProjectDirectoryConfigurer)
	if !ok {
		t.Fatal("Codex does not implement project directories")
	}
	primary, extra, replacement := t.TempDir(), t.TempDir(), t.TempDir()
	if err := d.ConfigureProjectDirectories(primary, []string{extra}); err != nil {
		t.Fatal(err)
	}
	if err := d.ConfigureProjectDirectories(primary, []string{replacement}); err != nil {
		t.Fatal(err)
	}
	opts := a.WorkspaceAgentOptions()
	if !reflect.DeepEqual(opts["additional_dirs"], []string{replacement}) {
		t.Fatalf("snapshot lost/repeated roots: %#v", opts)
	}
	if err := d.ConfigureProjectDirectories(primary, []string{"relative"}); err == nil {
		t.Fatal("accepted relative path")
	}
	if !reflect.DeepEqual(opts, a.WorkspaceAgentOptions()) {
		t.Fatal("invalid update mutated configuration")
	}
	if a.GetMode() != "suggest" || !reflect.DeepEqual(a.cliExtraArgs, []string{"wrapper"}) {
		t.Fatal("changed approval mode or wrapper")
	}
	if err := d.ConfigureProjectDirectories(primary, nil); err != nil {
		t.Fatal(err)
	}
	if roots, _ := a.WorkspaceAgentOptions()["additional_dirs"].([]string); len(roots) != 0 {
		t.Fatal("removed directories retained")
	}
}
