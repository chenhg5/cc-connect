package claudecode

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

const (
	fakeClaudeUsageEnv = "CC_CONNECT_FAKE_CLAUDE_USAGE"
	// fakeClaudeUsageArgsEnv names the file the fake claude writes its
	// arguments to, as a JSON array.
	fakeClaudeUsageArgsEnv = "CC_CONNECT_FAKE_CLAUDE_USAGE_ARGS"
)

// TestRunClaudeUsageProbe_StartsOnWindows runs the whole /usage probe against
// a fake claude on a real pseudo-terminal. On Windows the probe used to fail
// before starting claude with "start claude usage probe: unsupported", because
// github.com/creack/pty cannot start processes there (#2299). Windows now uses
// ConPTY. The test runs on every OS so the Unix PTY path stays covered too.
//
// On Windows the fake is a .cmd file that forwards its arguments with %*, like
// the shim npm installs for claude. cmd.exe parses a command line differently
// from the CommandLineToArgvW rules Go composes it for, so the fake records the
// arguments it receives and the test checks that they arrive intact, the empty
// --tools value included. The fake lives in a directory with a space in its
// name, as an npm prefix inside a user profile can have.
func TestRunClaudeUsageProbe_StartsOnWindows(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	binDir := filepath.Join(t.TempDir(), "fake bin")
	if err := os.Mkdir(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// The test binary's flag parsing stops at "--", so the probe's arguments
	// after it reach the helper untouched.
	runHelper := "-test.run=^TestFakeClaudeUsageHelperProcess$"
	if runtime.GOOS == "windows" {
		script := fmt.Sprintf("@\"%s\" \"%s\" -- %%*\r\n", exe, runHelper)
		if err := os.WriteFile(filepath.Join(binDir, "claude.cmd"), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	} else {
		script := fmt.Sprintf("#!/bin/sh\nexec '%s' '%s' -- \"$@\"\n", exe, runHelper)
		if err := os.WriteFile(filepath.Join(binDir, "claude"), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	argsFile := filepath.Join(t.TempDir(), "args.json")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv(fakeClaudeUsageEnv, "1")
	t.Setenv(fakeClaudeUsageArgsEnv, argsFile)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	screen, err := (&Agent{}).runClaudeUsageProbe(ctx)
	if err != nil {
		t.Fatalf("runClaudeUsageProbe: %v", err)
	}
	report, err := parseClaudeUsageReport(screen, time.Now())
	if err != nil {
		t.Fatalf("parseClaudeUsageReport: %v\nscreen:\n%s", err, screen)
	}
	windows := report.Buckets[0].Windows
	if windows[0].UsedPercent != 12 || windows[1].UsedPercent != 34 {
		t.Fatalf("used percent = %d/%d, want 12/34\nscreen:\n%s", windows[0].UsedPercent, windows[1].UsedPercent, screen)
	}

	rawArgs, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("read the fake claude's arguments: %v", err)
	}
	var gotArgs []string
	if err := json.Unmarshal(rawArgs, &gotArgs); err != nil {
		t.Fatalf("decode the fake claude's arguments %q: %v", rawArgs, err)
	}
	wantArgs := []string{"--tools", "", "--permission-mode", "plan", "--no-chrome"}
	if !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Fatalf("claude received arguments %q, want %q", gotArgs, wantArgs)
	}
}

// TestFakeClaudeUsageHelperProcess is not a real test. It acts as the fake
// claude for TestRunClaudeUsageProbe_StartsOnWindows: it records its
// arguments, waits for /usage on stdin, then draws a usage panel below enough
// lines to scroll a 40-row screen, and stays alive until the terminal goes
// away.
func TestFakeClaudeUsageHelperProcess(t *testing.T) {
	if os.Getenv(fakeClaudeUsageEnv) != "1" {
		t.Skip("helper process for TestRunClaudeUsageProbe_StartsOnWindows")
	}
	go func() {
		time.Sleep(30 * time.Second)
		os.Exit(3)
	}()

	if argsFile := os.Getenv(fakeClaudeUsageArgsEnv); argsFile != "" {
		args := []string{}
		for i, arg := range os.Args {
			if arg == "--" {
				args = append(args, os.Args[i+1:]...)
				break
			}
		}
		encoded, err := json.Marshal(args)
		if err == nil {
			err = os.WriteFile(argsFile, encoded, 0o600)
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "fake claude: record arguments: %v\n", err)
			os.Exit(4)
		}
	}

	fmt.Print("\x1b[2J\x1b[HFake Claude Code\r\n> ")
	in := bufio.NewReader(os.Stdin)
	for {
		line, err := in.ReadString('\n')
		if strings.Contains(line, "/usage") {
			break
		}
		if err != nil {
			os.Exit(2)
		}
	}

	// Captured ConPTY output from Claude Code on Windows shows the row after a
	// full-width rule written directly after it, relying on the console's
	// auto-wrap; ConPTY keeps line breaks that the program writes itself. Do
	// the same on Windows so the probe's terminal must handle auto-wrap.
	afterFullRow := "\r\n"
	if runtime.GOOS == "windows" {
		afterFullRow = ""
	}

	var panel strings.Builder
	for i := 0; i < 50; i++ {
		fmt.Fprintf(&panel, "history line %d\r\n", i)
	}
	panel.WriteString("Settings  Status  Config  \x1b[7m Usage \x1b[m\r\n")
	panel.WriteString(strings.Repeat("─", claudeUsageCols) + afterFullRow)
	panel.WriteString("Current session\r\n")
	panel.WriteString("\x1b[32m██████\x1b[m                12% used\r\n")
	panel.WriteString("Resets 3pm (UTC)\r\n\r\n")
	panel.WriteString("Current week (all models)\r\n")
	panel.WriteString("\x1b[32m█████████████████\x1b[m     34% used\r\n")
	panel.WriteString("Resets Dec 23, 4:59pm (UTC)\r\n\r\n")
	panel.WriteString("Esc to cancel")
	fmt.Print(panel.String())

	// Stay alive until the probe closes the terminal.
	for {
		if _, err := in.ReadString('\n'); err != nil {
			os.Exit(0)
		}
	}
}

func TestClaudeUsageScreenTerminal_WrapsAtRightMargin(t *testing.T) {
	term := newClaudeUsageScreenTerminal(5, 10)
	// ConPTY relies on auto-wrap: a full-width rule is followed directly by
	// the next row's text, even with an SGR sequence in between.
	term.Write([]byte(strings.Repeat("─", 10) + "\x1b[m❯ /usage"))
	// A CR LF right after a full-width line must not add a blank row.
	term.Write([]byte("\r\n" + strings.Repeat("=", 10) + "\r\nnext"))

	want := strings.Repeat("─", 10) + "\n❯ /usage\n" + strings.Repeat("=", 10) + "\nnext"
	if got := term.String(); got != want {
		t.Fatalf("screen = %q, want %q", got, want)
	}
}

func TestClaudeUsageScreenTerminal_ScrollsAtBottomRow(t *testing.T) {
	term := newClaudeUsageScreenTerminal(3, 20)
	term.Write([]byte("one\r\ntwo\r\nthree\r\nfour"))
	if got, want := term.String(), "two\nthree\nfour"; got != want {
		t.Fatalf("after scroll = %q, want %q", got, want)
	}
	// Absolute positions count from the top of the visible screen.
	term.Write([]byte("\x1b[1;1HTWO\x1b[3;1H\x1b[KFOUR"))
	if got, want := term.String(), "TWO\nthree\nFOUR"; got != want {
		t.Fatalf("after CUP = %q, want %q", got, want)
	}
}

func TestClaudeUsageScreenTerminal_ClampsCursorAndErasesChars(t *testing.T) {
	term := newClaudeUsageScreenTerminal(3, 10)
	term.Write([]byte("abcdefghij\x1b[1;3H\x1b[4X"))
	term.Write([]byte("\x1b[99;99HZ\x1b[2;1H\x1b[50CY"))
	want := "ab    ghij\n         Y\n         Z"
	if got := term.String(); got != want {
		t.Fatalf("screen = %q, want %q", got, want)
	}
}

// VPA (CSI n d) moves to a row and keeps the column. Like every cursor move it
// cancels a pending wrap, and it is clamped to the screen.
func TestClaudeUsageScreenTerminal_MovesToRow(t *testing.T) {
	term := newClaudeUsageScreenTerminal(3, 10)
	term.Write([]byte("abc\x1b[3dX\x1b[1dY\x1b[99dZ"))
	if got, want := term.String(), "abc Y\n\n   X Z"; got != want {
		t.Fatalf("screen = %q, want %q", got, want)
	}

	term = newClaudeUsageScreenTerminal(3, 10)
	// The cursor stays in the last column after a full row. Moving to row 2
	// must drop the pending wrap, so Z lands in that column, not on row 3.
	term.Write([]byte("abcdefghij\x1b[2dZ"))
	if got, want := term.String(), "abcdefghij\n         Z"; got != want {
		t.Fatalf("after a full row = %q, want %q", got, want)
	}
}

// The cleanup waits for the PTY reader, but only for a bounded time: a Read
// that never returns must not hang the probe.
func TestWaitClaudeUsageReader_IsBounded(t *testing.T) {
	exited := make(chan struct{})
	close(exited)
	if !waitClaudeUsageReader(exited, time.Minute) {
		t.Fatal("reader that already exited was reported as still running")
	}

	start := time.Now()
	if waitClaudeUsageReader(make(chan struct{}), 20*time.Millisecond) {
		t.Fatal("reader that never exits was reported as exited")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("wait took %v, want it to stop at the timeout", elapsed)
	}
}

// Windows cannot remove the probe's temp dir while a killed process still
// holds it as its working directory, so the removal is retried.
func TestRemoveAllWithRetry(t *testing.T) {
	busy := errors.New("directory is in use")

	calls := 0
	err := removeAllWithRetry("dir", func(string) error {
		calls++
		if calls < 3 {
			return busy
		}
		return nil
	}, 5, time.Millisecond)
	if err != nil || calls != 3 {
		t.Fatalf("err = %v after %d calls, want success on the 3rd call", err, calls)
	}

	calls = 0
	err = removeAllWithRetry("dir", func(string) error {
		calls++
		return busy
	}, 4, time.Millisecond)
	if !errors.Is(err, busy) || calls != 4 {
		t.Fatalf("err = %v after %d calls, want the last error after 4 calls", err, calls)
	}
}

// A /usage panel as ConPTY encodes it: the screen has scrolled, the tab row
// follows a full-width rule through auto-wrap, rows are placed with absolute
// cursor positions, spaces become CSI C, and stale text is cleared with ECH.
func TestClaudeUsageScreenTerminal_ParsesConPTYUsagePanel(t *testing.T) {
	var raw strings.Builder
	raw.WriteString("\x1b[?9001h\x1b[?1004h\x1b[?25l\x1b[2J\x1b[m\x1b[H\x1b]0;claude\a\x1b[?25h\x1b[?1049h\x1b[2J")
	for i := 0; i < 45; i++ {
		raw.WriteString("\x1b[Kold output\r\n")
	}
	raw.WriteString(strings.Repeat("▔", 120) + "\x1b[m   Settings  Status  Config  \x1b[7m Usage \x1b[m  Stats\x1b[K")
	raw.WriteString("\x1b[5;1H\x1b[KCurrent\x1b[1Csession\x1b[50X")
	raw.WriteString("\x1b[6;1H\x1b[K\x1b[32m██████\x1b[m\x1b[16C12%\x1b[1Cused")
	raw.WriteString("\x1b[7;1H\x1b[KResets\x1b[1C3pm\x1b[1C(UTC)")
	raw.WriteString("\x1b[8;1H\x1b[K\x1b[9;1H\x1b[KCurrent\x1b[1Cweek\x1b[1C(all\x1b[1Cmodels)")
	raw.WriteString("\x1b[10;1H\x1b[K\x1b[32m█████████████████\x1b[m\x1b[5C34%\x1b[1Cused")
	raw.WriteString("\x1b[11;1H\x1b[KResets\x1b[1CDec\x1b[1C23,\x1b[1C4:59pm\x1b[1C(UTC)")
	for row := 12; row <= 38; row++ {
		fmt.Fprintf(&raw, "\x1b[%d;1H\x1b[K", row)
	}

	term := newClaudeUsageScreenTerminal(40, 120)
	term.Write([]byte(raw.String()))
	screen := normalizeClaudeUsageText(term.String())
	report, err := parseClaudeUsageReport(screen, time.Now())
	if err != nil {
		t.Fatalf("parseClaudeUsageReport: %v\nscreen:\n%s", err, screen)
	}
	windows := report.Buckets[0].Windows
	if windows[0].UsedPercent != 12 || windows[1].UsedPercent != 34 {
		t.Fatalf("used percent = %d/%d, want 12/34", windows[0].UsedPercent, windows[1].UsedPercent)
	}
	if !strings.Contains("\n"+screen+"\n", "\nSettings Status Config Usage Stats\n") {
		t.Fatalf("tab row not wrapped below the rule:\n%s", screen)
	}
}

// Without a screen size the terminal must keep its original unbounded
// behavior, which the Unix probe still uses.
func TestClaudeUsageTerminal_UnsizedIgnoresScreenFeatures(t *testing.T) {
	term := newClaudeUsageTerminal()
	term.Write([]byte(strings.Repeat("x", 130) + "\x1b[1;3H\x1b[2X\r\n\x1b[300;1Hz"))
	lines := strings.Split(term.String(), "\n")
	if len(lines[0]) != 130 || !strings.HasPrefix(lines[0], "xxxxx") {
		t.Fatalf("first row = %q, want 130 x's without wrap or erase", lines[0])
	}
	if len(lines) != 300 || lines[299] != "z" {
		t.Fatalf("rows = %d, last = %q; want 300 rows ending in z", len(lines), lines[len(lines)-1])
	}
}
