package claudecode

import (
	"reflect"
	"testing"
)

func TestProjectDirectories_PreserveWrapperAndWorkspaceSnapshot(t *testing.T) {
	primary, extra := t.TempDir(), t.TempDir()
	a := &Agent{cmd: "true", cliExtraArgs: []string{"wrapped"}}
	if err := a.ConfigureProjectDirectories(primary, []string{extra}); err != nil {
		t.Fatal(err)
	}
	opts := a.WorkspaceAgentOptions()
	opts["work_dir"] = primary // workspace clones choose their own primary root
	child, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	got := child.(*Agent)
	if got.workDir != primary || !reflect.DeepEqual(got.cliExtraArgs, []string{"wrapped", "--add-dir", extra}) {
		t.Fatal("project directories lost")
	}
	if err := a.ConfigureProjectDirectories(primary, []string{"relative"}); err == nil {
		t.Fatal("relative root accepted")
	}
}
