package codex

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ── Issue #1909: codex_home must work out of the box ──────────────────────
//
// A user who sets `codex_home` to isolate the Codex home and logs in with a
// ChatGPT subscription never got the directory created: the provider path
// creates it, but a subscription login configures no provider, so nothing
// did. The Codex CLI then refused to start with "Error finding codex home:
// CODEX_HOME points to ..., but that path does not exist" and the session
// never started.

func TestEnsureCodexHomeDir_CreatesMissingDirectory(t *testing.T) {
	home := filepath.Join(t.TempDir(), "nested", "codex-home")

	if err := ensureCodexHomeDir(home); err != nil {
		t.Fatalf("ensureCodexHomeDir returned error: %v", err)
	}

	info, err := os.Stat(home)
	if err != nil {
		t.Fatalf("codex home was not created: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("codex home %q is not a directory", home)
	}
}

func TestEnsureCodexHomeDir_EmptyIsNoop(t *testing.T) {
	if err := ensureCodexHomeDir("   "); err != nil {
		t.Fatalf("ensureCodexHomeDir(\"   \") = %v, want nil", err)
	}
}

// When the directory cannot be created, the error has to name the option and
// the path — and it must be a cc-connect error, not the raw CLI message.
func TestEnsureCodexHomeDir_ErrorNamesOptionAndPath(t *testing.T) {
	// A regular file cannot become a directory.
	file := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(file, "codex-home")

	err := ensureCodexHomeDir(home)
	if err == nil {
		t.Fatal("ensureCodexHomeDir must fail when the directory cannot be created")
	}
	if !strings.Contains(err.Error(), "codex_home") {
		t.Fatalf("error %q must name the codex_home option", err)
	}
	if !strings.Contains(err.Error(), home) {
		t.Fatalf("error %q must name the path %q", err, home)
	}
	if strings.Contains(err.Error(), "that path does not exist") {
		t.Fatalf("error %q must not be the raw CLI message", err)
	}
}

// TestStartSession_CreatesMissingCodexHome starts a real session (with the
// test binary as the Codex CLI) against a codex_home that does not exist yet,
// for both backends, and checks that the directory is there afterwards.
func TestStartSession_CreatesMissingCodexHome(t *testing.T) {
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	for _, backend := range []string{"exec", "app_server"} {
		t.Run(backend, func(t *testing.T) {
			home := filepath.Join(root, backend, "isolated-home")
			if _, err := os.Stat(home); !os.IsNotExist(err) {
				t.Fatalf("codex home %q must not exist before the session starts", home)
			}

			a, err := New(map[string]any{
				"cmd":        []string{bin, "-test.run=^TestDesktopCLIProcess$", "--", "desktop-fixture"},
				"backend":    backend,
				"work_dir":   root,
				"codex_home": home,
				"env":        map[string]string{"CC_DESKTOP_CLI_HELPER": "1"},
			})
			if err != nil {
				t.Fatal(err)
			}

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			session, err := a.StartSession(ctx, "")
			if err != nil {
				t.Fatalf("StartSession failed: %v", err)
			}
			defer func() {
				if err := session.Close(); err != nil {
					t.Errorf("close session: %v", err)
				}
			}()

			info, err := os.Stat(home)
			if err != nil {
				t.Fatalf("codex home was not created: %v", err)
			}
			if !info.IsDir() {
				t.Fatalf("codex home %q is not a directory", home)
			}
		})
	}
}
