//go:build !windows

package main

import (
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func TestDefaultAuditDir_HomeSuffix(t *testing.T) {
	dir, err := defaultAuditDir()
	if err != nil {
		t.Fatalf("defaultAuditDir error: %v", err)
	}
	if !strings.HasSuffix(dir, "/.cc-connect/audits") {
		t.Errorf("audit dir = %q, want suffix /.cc-connect/audits", dir)
	}
}

func TestWriteHumanReport_RendersAllSections(t *testing.T) {
	r := core.IsolationReport{Project: "demo", RunAsUser: "coder"}
	r.Identity.Whoami = "coder"
	r.Identity.ID = "uid=1001(coder)"
	r.Identity.Home = "/home/coder"
	r.WorkDirStatus.Path = "/tmp/wd"
	r.WorkDirStatus.Readable = true
	r.WorkDirStatus.Writable = true
	r.TargetPaths = []core.PathStatus{
		{Path: "/home/coder/.claude/settings.json", Status: "has"},
		{Path: "/home/coder/.pgpass", Status: "missing"},
	}
	r.CrossUser = []core.CrossUserResult{
		{OtherUser: "leigh", Path: "/home/leigh/.pgpass", Status: "leaked"},
	}
	r.Fatal = []string{"cross-user leak"}

	var out strings.Builder
	writeHumanReport(&out, r)
	s := out.String()
	for _, want := range []string{"whoami", "workdir", "target home", "cross-user", "LEAKED", "FATAL"} {
		if !strings.Contains(s, want) {
			t.Errorf("report missing %q:\n%s", want, s)
		}
	}
}
