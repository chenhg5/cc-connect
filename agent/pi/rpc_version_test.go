package pi

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// The test binary acts as a wrapper CLI so probes exercise the configured
// executable, arguments, directory, and environment without a provider.
func TestRPCVersionCLIHelper(t *testing.T) {
	if os.Getenv("CC_PI_VERSION_HELPER") != "1" {
		return
	}
	args := os.Args
	if len(args) < 5 || args[3] != "wrapper-option" {
		os.Exit(81)
	}
	if expected := os.Getenv("CC_PI_EXPECT_DIR"); expected != "" {
		cwd, _ := os.Getwd()
		expected, _ = filepath.EvalSymlinks(expected)
		cwd, _ = filepath.EvalSymlinks(cwd)
		if cwd != expected || os.Getenv("CC_PI_MERGED") != "session" {
			os.Exit(82)
		}
	}
	if args[len(args)-1] == "--version" {
		switch os.Getenv("CC_PI_VERSION") {
		case "fail":
			os.Exit(83)
		case "sleep":
			time.Sleep(20 * time.Second)
		default:
			fmt.Println(os.Getenv("CC_PI_VERSION"))
		}
		os.Exit(0)
	}
	if marker := os.Getenv("CC_PI_RPC_MARKER"); marker != "" {
		_ = os.WriteFile(marker, []byte("started"), 0600)
	}
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		if strings.Contains(scanner.Text(), "get_state") {
			fmt.Println(`{"type":"response","id":"cc-connect-state-probe","command":"get_state","success":true,"data":{"sessionId":"version-test-session"}}`)
		}
	}
	os.Exit(0)
}

func versionTestAgent(t *testing.T, version string) *Agent {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return &Agent{
		cmd:          executable,
		cliExtraArgs: []string{"-test.run=^TestRPCVersionCLIHelper$", "--", "wrapper-option"},
		workDir:      t.TempDir(), rpc: true,
		configEnv: []string{"CC_PI_VERSION_HELPER=1", "CC_PI_VERSION=" + version},
		i18n:      core.NewI18n(core.LangEnglish),
	}
}

func TestStartSessionRPCRequiresLifecycleVersion(t *testing.T) {
	for _, tc := range []struct {
		version string
		accept  bool
	}{
		{"0.98.9", false}, {"0.99.0", true}, {"v0.99.0", true},
		{"0.99.1+build.123", true}, {"1.0.0", true},
		{"0.99.0-rc.1", false}, {"0.99.0-01", false},
		{"not-a-version", false}, {"pi 0.99.0", false},
		{"0.99.0\n1.0.0", false}, {"00.99.0", false}, {"", false}, {"fail", false},
	} {
		t.Run(tc.version, func(t *testing.T) {
			a := versionTestAgent(t, tc.version)
			marker := a.workDir + "/rpc-started"
			a.configEnv = append(a.configEnv, "CC_PI_RPC_MARKER="+marker)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			s, err := a.StartSession(ctx, "")
			if tc.accept {
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = s.Close() }()
				if s.CurrentSessionID() != "version-test-session" {
					t.Fatalf("session ID = %q", s.CurrentSessionID())
				}
			} else {
				if err == nil {
					_ = s.Close()
					t.Fatal("unsupported version started RPC")
				}
				if !strings.Contains(err.Error(), minimumRPCVersion) || !strings.Contains(err.Error(), "--version") || !strings.Contains(err.Error(), "npm install") {
					t.Fatalf("missing actionable upgrade/wrapper guidance: %v", err)
				}
				if _, err := os.Stat(marker); !os.IsNotExist(err) {
					t.Fatal("version rejection must occur before RPC process starts")
				}
			}
		})
	}
}

func TestStartSessionRPCVersionUsesConfiguredEnvironmentAndDirectory(t *testing.T) {
	a := versionTestAgent(t, "0.99.0")
	a.configEnv = append(a.configEnv, "CC_PI_EXPECT_DIR="+a.workDir, "CC_PI_MERGED=config")
	a.SetSessionEnv([]string{"CC_PI_MERGED=session"})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s, err := a.StartSession(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
}

func TestStartSessionRPCVersionCancellation(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		t.Run(fmt.Sprint(deadline), func(t *testing.T) {
			a := versionTestAgent(t, "sleep")
			var ctx context.Context
			var cancel context.CancelFunc
			want := context.Canceled
			if deadline {
				ctx, cancel = context.WithTimeout(context.Background(), 100*time.Millisecond)
				want = context.DeadlineExceeded
			} else {
				ctx, cancel = context.WithCancel(context.Background())
				cancel()
			}
			defer cancel()
			_, err := a.StartSession(ctx, "")
			if !errors.Is(err, want) {
				t.Fatalf("error = %v, want %v", err, want)
			}
		})
	}
}

func TestStartSessionRPCVersionProbeTimesOut(t *testing.T) {
	a := versionTestAgent(t, "sleep")
	start := time.Now()
	_, err := a.StartSession(context.Background(), "")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(start); elapsed < 5*time.Second || elapsed > 8*time.Second {
		t.Fatalf("probe elapsed %s, want bounded five-second probe", elapsed)
	}
}

func TestStartSessionJSONSkipsRPCVersionProbe(t *testing.T) {
	a := versionTestAgent(t, "fail")
	a.rpc = false
	s, err := a.StartSession(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
}

func TestRPCVersionGuidancePreservesWorkspaceLanguage(t *testing.T) {
	ag, err := New(map[string]any{"cmd": "echo", "rpc": true, "language": "zh"})
	if err != nil {
		t.Fatal(err)
	}
	if got := ag.(core.WorkspaceAgentOptionSnapshotter).WorkspaceAgentOptions()["language"]; got != "zh" {
		t.Fatalf("language = %v", got)
	}
	_, err = ag.StartSession(context.Background(), "")
	if err == nil || !strings.Contains(err.Error(), "升级") {
		t.Fatalf("expected Chinese version guidance, got %v", err)
	}
}

func TestNewMissingCommandSuggestsCurrentPiPackage(t *testing.T) {
	_, err := New(map[string]any{"cmd": filepath.Join(t.TempDir(), "missing-pi")})
	if err == nil || !strings.Contains(err.Error(), "npm install -g @earendil-works/pi-coding-agent") {
		t.Fatalf("expected current Pi package installation guidance, got %v", err)
	}
}
