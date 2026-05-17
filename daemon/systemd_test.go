//go:build linux

package daemon

import (
	"os"
	"strings"
	"testing"
)

func TestEscapeSystemdEnvValue(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{`plain`, `plain`},
		{`quote " here`, `quote \" here`},
		{`back\slash`, `back\\slash`},
		{"new\nline", `new\nline`},
		{"tab\there", `tab\there`},
		{"return\rback", `return\rback`},
	}
	for _, c := range cases {
		if got := escapeSystemdEnvValue(c.in); got != c.want {
			t.Errorf("escapeSystemdEnvValue(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestBuildUnit_EscapesEnvValue(t *testing.T) {
	mgr := &systemdManager{system: false}
	cfg := Config{
		BinaryPath: "/bin/true",
		WorkDir:    "/tmp",
		LogFile:    "/tmp/log",
		LogMaxSize: 1024,
		EnvPATH:    "/usr/bin",
		EnvExtra:   map[string]string{"TRICKY": `a"b\c`},
	}
	out := mgr.buildUnit(cfg)
	if !strings.Contains(out, `Environment="TRICKY=a\"b\\c"`) {
		t.Errorf("expected escaped Environment line; got:\n%s", out)
	}
}

func TestBuildUnit_DropsInvalidEnvName(t *testing.T) {
	mgr := &systemdManager{system: false}
	cfg := Config{
		BinaryPath: "/x", WorkDir: "/y", LogFile: "/l", LogMaxSize: 1, EnvPATH: "/p",
		EnvExtra: map[string]string{"FOO BAR": "v", "OK": "fine"},
	}
	out := mgr.buildUnit(cfg)
	if strings.Contains(out, "FOO BAR") {
		t.Errorf("invalid env name leaked: %s", out)
	}
	if !strings.Contains(out, `Environment="OK=fine"`) {
		t.Errorf("OK missing: %s", out)
	}
}

func TestBuildUnit_DropsEmptyValue(t *testing.T) {
	mgr := &systemdManager{system: false}
	cfg := Config{
		BinaryPath: "/x", WorkDir: "/y", LogFile: "/l", LogMaxSize: 1, EnvPATH: "/p",
		EnvExtra: map[string]string{"EMPTY": "", "OK": "ok"},
	}
	out := mgr.buildUnit(cfg)
	if strings.Contains(out, `Environment="EMPTY=`) {
		t.Errorf("empty value should be skipped: %s", out)
	}
	if !strings.Contains(out, `Environment="OK=ok"`) {
		t.Errorf("OK missing: %s", out)
	}
}

// TestSystemdInstall_TightensExistingUnitFrom0644 covers the upgrade
// path: os.WriteFile would truncate-in-place and KEEP the old 0644
// permissions of a unit file left over from earlier cc-connect
// versions, leaving captured token values world-readable.
func TestSystemdInstall_TightensExistingUnitFrom0644(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	origSys := runSystemctl
	t.Cleanup(func() { runSystemctl = origSys })
	runSystemctl = func(args ...string) (string, error) { return "", nil }

	mgr := &systemdManager{system: false}
	unitPath := mgr.unitPath()
	if err := os.MkdirAll(unitPath[:strings.LastIndex(unitPath, "/")], 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(unitPath, []byte("[Service]\nExecStart=/bin/true\n"), 0o644); err != nil {
		t.Fatalf("seed legacy unit: %v", err)
	}
	if info, _ := os.Stat(unitPath); info.Mode().Perm() != 0o644 {
		t.Fatalf("precondition: seeded file mode = %o, want 0644", info.Mode().Perm())
	}

	cfg := Config{
		BinaryPath: "/bin/true",
		WorkDir:    t.TempDir(),
		LogFile:    "/tmp/cc.log",
		LogMaxSize: 1024,
		EnvPATH:    "/usr/bin",
		EnvExtra:   map[string]string{"CUSTOM_TOKEN": "captured"},
	}
	if err := mgr.Install(cfg); err != nil {
		t.Fatalf("Install: %v", err)
	}
	info, err := os.Stat(unitPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("unit mode after reinstall = %o, want 0600", info.Mode().Perm())
	}
}

func TestUnitFileMode_Is0600(t *testing.T) {
	mgr := &systemdManager{system: false}
	t.Setenv("HOME", t.TempDir())
	unitPath := mgr.unitPath()
	if err := os.MkdirAll(unitPath[:strings.LastIndex(unitPath, "/")], 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	content := mgr.buildUnit(Config{
		BinaryPath: "/bin/true", WorkDir: "/tmp", LogFile: "/tmp/l", LogMaxSize: 1, EnvPATH: "/p",
	})
	if err := os.WriteFile(unitPath, []byte(content), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	info, err := os.Stat(unitPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %o, want 0600", info.Mode().Perm())
	}
}
