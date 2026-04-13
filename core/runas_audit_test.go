//go:build !windows

package core

import (
	"strings"
	"testing"
)

// Golden-style test: feed a canned probe output through the parser and
// assert the resulting IsolationReport looks right. No real sudo.
func TestParseProbeOutput_Clean(t *testing.T) {
	out := `BEGIN probe-version=1
ID uid=1001(coder) gid=1001(coder) groups=1001(coder)
WHOAMI coder
GROUPS coder
UMASK 0022
PWD /home/coder
HOME /home/coder
SHELL /bin/bash
WORKDIR_PATH /tmp/wd
WORKDIR_EXISTS yes
WORKDIR_READABLE yes
WORKDIR_WRITABLE yes
TARGET_HAS /home/coder/.claude/settings.json
TARGET_MISSING /home/coder/.pgpass
CROSS_DENIED leigh /home/leigh/.claude/settings.json
CROSS_MISSING leigh /home/leigh/keys
SUPERVISOR_DENIED /home/supervisor/.claude/settings.json
END probe-version=1
`
	report := IsolationReport{Project: "demo", RunAsUser: "coder", WorkDir: "/tmp/wd"}
	parseProbeOutput(&report, out)
	report.Fatal = computeAuditFatal(report)

	if report.ProbeVersion != "1" {
		t.Errorf("probe version = %q, want 1", report.ProbeVersion)
	}
	if report.Identity.Whoami != "coder" {
		t.Errorf("whoami = %q, want coder", report.Identity.Whoami)
	}
	if !report.WorkDirStatus.Writable {
		t.Error("expected workdir writable")
	}
	if len(report.TargetPaths) != 2 {
		t.Errorf("target_paths count = %d, want 2", len(report.TargetPaths))
	}
	if len(report.CrossUser) != 2 {
		t.Errorf("cross_user count = %d, want 2", len(report.CrossUser))
	}
	if report.CrossUser[0].OtherUser != "leigh" || report.CrossUser[0].Status != "denied" {
		t.Errorf("first cross_user entry wrong: %+v", report.CrossUser[0])
	}
	if len(report.Supervisor) != 1 {
		t.Errorf("supervisor count = %d, want 1", len(report.Supervisor))
	}
	if report.HasFatal() {
		t.Errorf("want no fatal, got: %v", report.Fatal)
	}
}

func TestParseProbeOutput_CrossLeakIsFatal(t *testing.T) {
	out := `BEGIN probe-version=1
WORKDIR_PATH /tmp/wd
WORKDIR_WRITABLE yes
CROSS_LEAKED leigh /home/leigh/.claude/settings.json
END probe-version=1
`
	report := IsolationReport{Project: "demo", RunAsUser: "coder", WorkDir: "/tmp/wd"}
	parseProbeOutput(&report, out)
	report.Fatal = computeAuditFatal(report)
	if !report.HasFatal() {
		t.Fatal("want fatal for CROSS_LEAKED")
	}
	if !strings.Contains(report.Fatal[0], "CROSS_LEAKED") {
		t.Errorf("fatal message missing CROSS_LEAKED: %v", report.Fatal)
	}
}

func TestParseProbeOutput_SupervisorLeakIsFatal(t *testing.T) {
	out := `BEGIN probe-version=1
WORKDIR_PATH /tmp/wd
WORKDIR_WRITABLE yes
SUPERVISOR_LEAKED /home/supervisor/.pgpass
END probe-version=1
`
	report := IsolationReport{Project: "demo", RunAsUser: "coder", WorkDir: "/tmp/wd"}
	parseProbeOutput(&report, out)
	report.Fatal = computeAuditFatal(report)
	if !report.HasFatal() {
		t.Fatal("want fatal for SUPERVISOR_LEAKED")
	}
	if !strings.Contains(report.Fatal[0], "SUPERVISOR_LEAKED") {
		t.Errorf("fatal message missing SUPERVISOR_LEAKED: %v", report.Fatal)
	}
}

func TestParseProbeOutput_WorkdirNotWritableIsFatal(t *testing.T) {
	out := `BEGIN probe-version=1
WORKDIR_PATH /tmp/wd
WORKDIR_WRITABLE no
END probe-version=1
`
	report := IsolationReport{Project: "demo", RunAsUser: "coder", WorkDir: "/tmp/wd"}
	parseProbeOutput(&report, out)
	report.Fatal = computeAuditFatal(report)
	if !report.HasFatal() {
		t.Fatal("want fatal for non-writable workdir")
	}
}

func TestShellQuote(t *testing.T) {
	cases := map[string]string{
		"":             "''",
		"simple":       "'simple'",
		"has space":    "'has space'",
		"it's":         `'it'\''s'`,
		"/tmp/w&d":     "'/tmp/w&d'",
		"$HOME/secret": "'$HOME/secret'",
	}
	for in, want := range cases {
		if got := shellQuote(in); got != want {
			t.Errorf("shellQuote(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFilterOtherUsers(t *testing.T) {
	got := filterOtherUsers([]string{"alice", "", "coder", "bob"}, "coder")
	if len(got) != 2 || got[0] != "alice" || got[1] != "bob" {
		t.Errorf("filterOtherUsers = %v, want [alice bob]", got)
	}
}

func TestEmbeddedProbeScriptBeginsWithShebang(t *testing.T) {
	if !strings.HasPrefix(string(runasProbeScript), "#!/bin/sh\n") {
		t.Fatal("embedded probe script missing /bin/sh shebang")
	}
	// Make sure the script has at least the BEGIN and END markers.
	if !strings.Contains(string(runasProbeScript), "BEGIN probe-version=1") {
		t.Error("probe script missing BEGIN marker")
	}
	if !strings.Contains(string(runasProbeScript), "END probe-version=1") {
		t.Error("probe script missing END marker")
	}
}
