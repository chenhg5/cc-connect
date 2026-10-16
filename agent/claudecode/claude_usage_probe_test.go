package claudecode

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestTrustDialogAction(t *testing.T) {
	cases := []struct {
		name   string
		screen string
		want   string
	}{
		{
			// Claude Code 2.1.x preselects "No, exit": move down, never Enter.
			name:   "cursor on no-exit moves down",
			screen: "quick safety check: is this a project you created or one you trust?\n❯ no, exit\n  yes, i trust this folder\nenter to confirm · esc to cancel",
			want:   "\x1b[B",
		},
		{
			name:   "cursor on trust confirms",
			screen: "quick safety check: is this a project you created or one you trust?\n  no, exit\n❯ yes, i trust this folder\nenter to confirm · esc to cancel",
			want:   "\r",
		},
		{
			// Legacy numbered layout with Yes preselected.
			name:   "legacy numbered layout with yes selected",
			screen: "do you trust the files in this folder?\n❯ 1. yes, proceed\n  2. no, exit",
			want:   "\r",
		},
		{
			// Options not rendered yet: wait instead of a blind Enter.
			name:   "no cursor waits",
			screen: "quick safety check: is this a project you created or one you trust?",
			want:   "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := trustDialogAction(tc.screen); got != tc.want {
				t.Fatalf("trustDialogAction() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestNextClaudeUsageProbeAction_TrustDialogUsesCursor(t *testing.T) {
	screen := "Quick safety check: Is this a project you created or one you trust?\n❯ No, exit\n  Yes, I trust this folder\nEnter to confirm · Esc to cancel"
	if got := nextClaudeUsageProbeAction(screen, &claudeUsageProbeState{}, time.Now(), true, true); got != "\x1b[B" {
		t.Fatalf("action = %q, want down arrow", got)
	}
}

// Regression: when claude exits immediately (e.g. "No, exit" was selected) the old code blocked
// forever on a second <-readDone in the deferred cleanup, freezing the platform receive loop.
// It must now return an error around the caller's deadline.
func TestGetUsage_ReturnsWhenClaudeExitsImmediately(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake claude script requires a POSIX shell")
	}
	binDir := t.TempDir()
	fake := filepath.Join(binDir, "claude")
	script := "#!/bin/sh\necho 'Quick safety check'\nexit 1\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake claude: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+"/usr/bin:/bin")
	t.Setenv("HOME", t.TempDir()) // keep the probe dir out of the real cache

	a := &Agent{}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		_, err := a.GetUsage(ctx)
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("GetUsage succeeded against a claude that exits immediately; want error")
		}
		if !strings.Contains(err.Error(), "claudecode") {
			t.Fatalf("unexpected error: %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("GetUsage hung after claude exited (readDone double-receive deadlock)")
	}
}

func TestClaudeUsageProbeDir_StableUnderCacheDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("UserCacheDir uses %LocalAppData% on Windows, not HOME")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))

	first, cleanup1, err := claudeUsageProbeDir()
	if err != nil {
		t.Fatalf("claudeUsageProbeDir: %v", err)
	}
	cleanup1()
	second, cleanup2, err := claudeUsageProbeDir()
	if err != nil {
		t.Fatalf("claudeUsageProbeDir: %v", err)
	}
	defer cleanup2()

	if first != second {
		t.Fatalf("probe dir not stable: %q vs %q", first, second)
	}
	if !strings.HasPrefix(first, home) {
		t.Fatalf("probe dir %q not under HOME %q", first, home)
	}
	if fi, err := os.Stat(first); err != nil || !fi.IsDir() {
		t.Fatalf("probe dir %q should still exist after cleanup: %v", first, err)
	}
}

// Claude Code 2.1.x uses CSI n G (cursor to column n) instead of spaces.
// Raw bytes captured from the Claude Code 2.1.289 trust dialog.
func TestClaudeUsageTerminal_CursorHorizontalAbsolute(t *testing.T) {
	raw := "\x1b[2GQuick\x1b[8Gsafety\x1b[15Gcheck:\r\r\n\r\r\n" +
		"\x1b[2G\xe2\x9d\xaf\x1b[4GNo,\x1b[8Gexit\r\r\n" +
		"\x1b[4GYes,\x1b[9GI\x1b[11Gtrust\x1b[17Gthis\x1b[22Gfolder\r\r\n"

	terminal := newClaudeUsageTerminal()
	terminal.Write([]byte(raw))
	got := normalizeClaudeUsageText(terminal.String())

	for _, want := range []string{"Quick safety check:", "❯ No, exit", "Yes, I trust this folder"} {
		if !strings.Contains(got, want) {
			t.Fatalf("rendered screen missing %q:\n%s", want, got)
		}
	}
	if action := trustDialogAction(strings.ToLower(got)); action != "\x1b[B" {
		t.Fatalf("trustDialogAction() = %q, want down arrow on rendered dialog", action)
	}
}

// Observed on 2.1.289: "Make auto mode your default permission mode?" pops up during the probe
// with Yes preselected. It must be dismissed with Esc, never Enter (that rewrites user settings).
func TestNextClaudeUsageProbeAction_EscapesUnknownSelectionDialog(t *testing.T) {
	screen := "Make auto mode your default permission mode?\n\n" +
		"Auto mode lets Claude handle permission prompts automatically.\n\n" +
		"❯ Yes, set auto mode as my default permission mode\n" +
		"  No, keep bypass permissions"
	state := &claudeUsageProbeState{}
	got := nextClaudeUsageProbeAction(screen, state, time.Now(), true, true)
	if got != "\x1b" {
		t.Fatalf("action = %q, want Esc for unknown selection dialog", got)
	}
	if state.sentUsage {
		t.Fatal("must not type /usage while a selection dialog is visible")
	}
}

func TestNextClaudeUsageProbeAction_TypesUsageOnceSettledAtPrompt(t *testing.T) {
	screen := "Claude Code v2.1.289\nOpus 5.5 · Claude Max\n\n❯ \n\n⏵⏵ auto mode on (shift+tab to cycle)"
	state := &claudeUsageProbeState{}
	now := time.Now()

	if got := nextClaudeUsageProbeAction(screen, state, now, false, true); got != "" {
		t.Fatalf("unsettled screen: action = %q, want none", got)
	}
	if got := nextClaudeUsageProbeAction(screen, state, now, true, true); got != "/usage" {
		t.Fatalf("settled prompt: action = %q, want /usage typed without Enter", got)
	}
	// Step 2: Enter only after "❯ /usage" is echoed.
	typed := strings.Replace(screen, "❯ ", "❯ /usage", 1)
	if got := nextClaudeUsageProbeAction(typed, state, now.Add(300*time.Millisecond), true, false); got != "" {
		t.Fatalf("screen unchanged since typing: action = %q, want none", got)
	}
	if got := nextClaudeUsageProbeAction(typed, state, now.Add(300*time.Millisecond), true, true); got != "\r" {
		t.Fatalf("echoed /usage: action = %q, want Enter", got)
	}
}

func TestIsSelectionDialog(t *testing.T) {
	cases := map[string]bool{
		"❯ yes, set auto mode as my default permission mode\n  no, keep bypass permissions": true,
		"  yes, i trust this folder\n❯ no, exit":                                            true,
		"❯ 1. dark mode\n  2. light mode":                                                   true,
		"choose the default model\n❯ opus (recommended)\n  sonnet":                          true,
		"claude code v2.1.289\n❯ \n? for shortcuts":                                         false,
		"❯ try \"how does this repo work?\"":                                                false,
		"❯ /usage":                                                                          false,
		"current session\n12% used":                                                         false,
	}
	for screen, want := range cases {
		if got := isSelectionDialog(screen); got != want {
			t.Errorf("isSelectionDialog(%q) = %v, want %v", screen, got, want)
		}
	}
}

// Dialogs whose options are not yes/no (e.g. choosing a default model) must also get Esc.
func TestNextClaudeUsageProbeAction_EscapesNonYesNoDialog(t *testing.T) {
	screen := "Choose the default model\n\n❯ Opus (recommended)\n  Sonnet\n\nEnter to confirm · Esc to cancel"
	state := &claudeUsageProbeState{}
	if got := nextClaudeUsageProbeAction(screen, state, time.Now(), true, true); got != "\x1b" {
		t.Fatalf("action = %q, want Esc", got)
	}
	if state.sentUsage {
		t.Fatal("must not type /usage while a dialog is visible")
	}
}

// Telemetry prompts get Esc too (no blind down+Enter).
func TestNextClaudeUsageProbeAction_EscapesTelemetryPrompt(t *testing.T) {
	screen := "Help improve Claude Code by sharing usage data?\n❯ 1. Yes\n  2. No"
	if got := nextClaudeUsageProbeAction(screen, &claudeUsageProbeState{}, time.Now(), true, true); got != "\x1b" {
		t.Fatalf("action = %q, want Esc", got)
	}
}

// No second key on a stale frame (double arrow / double Esc opens rewind); retry after the interval.
func TestNextClaudeUsageProbeAction_WaitsForScreenChangeBetweenDialogKeys(t *testing.T) {
	screen := "Quick safety check\n❯ No, exit\n  Yes, I trust this folder"
	state := &claudeUsageProbeState{}
	t0 := time.Now()
	if got := nextClaudeUsageProbeAction(screen, state, t0, true, true); got != "\x1b[B" {
		t.Fatalf("first action = %q, want down arrow", got)
	}
	if got := nextClaudeUsageProbeAction(screen, state, t0.Add(400*time.Millisecond), true, false); got != "" {
		t.Fatalf("stale screen: action = %q, want none", got)
	}
	if got := nextClaudeUsageProbeAction(screen, state, t0.Add(claudeUsageDialogRetry+time.Millisecond), true, false); got != "\x1b[B" {
		t.Fatalf("after retry window: action = %q, want down arrow", got)
	}
}

// Dialog keystrokes are capped on unrecognised screens.
func TestNextClaudeUsageProbeAction_DialogResponsesCapped(t *testing.T) {
	screen := "Something new\n❯ Option A\n  Option B"
	state := &claudeUsageProbeState{}
	now := time.Now()
	sent := 0
	for i := 0; i < 20; i++ {
		now = now.Add(claudeUsageDialogRetry + time.Millisecond)
		if nextClaudeUsageProbeAction(screen, state, now, true, true) != "" {
			sent++
		}
	}
	if sent != claudeUsageMaxDialogResponses {
		t.Fatalf("sent %d keys, want cap %d", sent, claudeUsageMaxDialogResponses)
	}
}

// After /usage, while the panel loads ("Esc to cancel" footer): send nothing.
func TestNextClaudeUsageProbeAction_QuietWhileUsagePanelLoads(t *testing.T) {
	screen := "Status Config Usage\n\nLoading usage data…\n\nEsc to cancel"
	state := &claudeUsageProbeState{sentUsage: true, sentEnter: true, usageSentAt: time.Now().Add(-5 * time.Second)}
	if got := nextClaudeUsageProbeAction(screen, state, time.Now(), true, true); got != "" {
		t.Fatalf("action = %q, want none while panel loads", got)
	}
}

// /usage still in the input box (Enter eaten by completion): retry Enter after 900ms.
func TestNextClaudeUsageProbeAction_RetriesEnterWhenCommandStuckInInput(t *testing.T) {
	screen := "Claude Code v2.1.289\n\n❯ /usage\n\n⏸ plan mode on (shift+tab to cycle)"
	sentAt := time.Now()
	state := &claudeUsageProbeState{sentUsage: true, sentEnter: true, usageSentAt: sentAt, lastActionAt: sentAt}
	if got := nextClaudeUsageProbeAction(screen, state, sentAt.Add(500*time.Millisecond), true, true); got != "" {
		t.Fatalf("too early: action = %q, want none", got)
	}
	// Unchanged screen: no Enter retry (claude may have accepted it and not redrawn yet).
	if got := nextClaudeUsageProbeAction(screen, state, sentAt.Add(time.Second), true, false); got != "" {
		t.Fatalf("unchanged screen: action = %q, want no Enter retry", got)
	}
	if got := nextClaudeUsageProbeAction(screen, state, sentAt.Add(time.Second), true, true); got != "\r" {
		t.Fatalf("action = %q, want Enter retry", got)
	}
}

// Unknown screen (neither REPL nor dialog): send nothing.
func TestNextClaudeUsageProbeAction_UnknownScreenSendsNothing(t *testing.T) {
	screen := "Some splash screen we have never seen"
	if got := nextClaudeUsageProbeAction(screen, &claudeUsageProbeState{}, time.Now(), true, true); got != "" {
		t.Fatalf("action = %q, want none on unknown screen", got)
	}
}

func TestUsageReady_RequiresPercentForEveryBlock(t *testing.T) {
	partial := "Current session\n9% used\nResets 6:20am (Asia/Shanghai)\nCurrent week (all models)\nResets Oct 11 at 12am"
	if usageReady(partial) {
		t.Fatal("usageReady() = true while Current week percentage is still loading")
	}
	full := "Current session\n9% used\nResets 6:20am (Asia/Shanghai)\n" +
		"Current week (all models)\n2% used\nResets Oct 11 at 12am (Asia/Shanghai)\n" +
		"Current week (Fable)\n0% used\nResets Oct 11 at 12am (Asia/Shanghai)"
	if !usageReady(full) {
		t.Fatal("usageReady() = false on a fully rendered panel")
	}
	if _, err := parseClaudeUsageReport(full, time.Now()); err != nil {
		t.Fatalf("parse fully rendered panel: %v", err)
	}
	// Current week still loading while Extra usage shows a percentage must not count as ready.
	extra := "Current session\n9% used\nResets 6:20am (Asia/Shanghai)\n" +
		"Current week (all models)\nLoading…\n" +
		"Extra usage\n50% used\nResets Nov 1"
	if usageReady(extra) {
		t.Fatal("usageReady() = true while Current week is loading (Extra usage percentage must not count)")
	}
}

// CSI J without params erases below the cursor only.
func TestClaudeUsageTerminal_EraseBelowKeepsAbove(t *testing.T) {
	terminal := newClaudeUsageTerminal()
	terminal.Write([]byte("Claude Code v2.1.289\r\nold dialog line\r\n"))
	terminal.Write([]byte("\x1b[1A\r\x1b[J"))
	got := terminal.String()
	if !strings.Contains(got, "Claude Code v2.1.289") {
		t.Fatalf("content above cursor was erased: %q", got)
	}
	if strings.Contains(got, "old dialog line") {
		t.Fatalf("content below cursor was not erased: %q", got)
	}
}

// A dialog appearing between typing /usage and Enter gets Esc, never Enter.
func TestNextClaudeUsageProbeAction_DialogBetweenTypingAndEnter(t *testing.T) {
	state := &claudeUsageProbeState{sentUsage: true, lastActionAt: time.Now().Add(-time.Second)}
	screen := "Make auto mode your default permission mode?\n❯ Yes, set auto mode as my default permission mode\n  No, keep bypass permissions"
	if got := nextClaudeUsageProbeAction(screen, state, time.Now(), true, true); got != "\x1b" {
		t.Fatalf("action = %q, want Esc before any Enter", got)
	}
	if state.sentEnter {
		t.Fatal("Enter must not be sent while a dialog is visible")
	}
	// After Esc the input is empty (the dialog ate the letters): retype after 1.5s.
	repl := "Claude Code v2.1.289\n\n❯ \n\n? for shortcuts"
	escAt := state.lastActionAt
	if got := nextClaudeUsageProbeAction(repl, state, escAt.Add(time.Second), true, true); got != "" {
		t.Fatalf("too early: action = %q, want none", got)
	}
	if got := nextClaudeUsageProbeAction(repl, state, escAt.Add(1600*time.Millisecond), true, true); got != "\x15/usage" {
		t.Fatalf("after dialog dismissed: action = %q, want /usage retyped", got)
	}
}

// No repeated Enter on the trust dialog without a screen change.
func TestNextClaudeUsageProbeAction_TrustEnterNotRepeatedWithoutChange(t *testing.T) {
	screen := "Quick safety check\n  No, exit\n❯ Yes, I trust this folder"
	state := &claudeUsageProbeState{}
	t0 := time.Now()
	if got := nextClaudeUsageProbeAction(screen, state, t0, true, true); got != "\r" {
		t.Fatalf("first action = %q, want Enter on trust", got)
	}
	if got := nextClaudeUsageProbeAction(screen, state, t0.Add(3*time.Second), true, false); got != "" {
		t.Fatalf("unchanged screen: action = %q, want no repeated Enter", got)
	}
}

// Stale trust-dialog text above a REPL placeholder containing "notification" is not an option.
func TestTrustDialogAction_IgnoresREPLPlaceholder(t *testing.T) {
	screen := "quick safety check\n\n❯ try \"fix a bug in the notification module\""
	if got := trustDialogAction(screen); got != "" {
		t.Fatalf("trustDialogAction() = %q, want none for REPL placeholder", got)
	}
}

func TestHasDialog_IgnoresNavigateTip(t *testing.T) {
	screen := "claude code v2.1.289\n❯ \ntip: use ↑/↓ to navigate history\n? for shortcuts"
	if hasDialog(screen) {
		t.Fatal("hasDialog() = true for a REPL tip mentioning navigate")
	}
}

func TestClaudeUsageTerminal_ED3KeepsVisibleScreen(t *testing.T) {
	terminal := newClaudeUsageTerminal()
	terminal.Write([]byte("Current session\r\n9% used\r\n\x1b[3J"))
	if got := terminal.String(); !strings.Contains(got, "9% used") {
		t.Fatalf("ED 3 cleared the visible screen: %q", got)
	}
}

// End to end: a fake claude shows a REPL, prints the usage panel after /usage and ignores SIGTERM.
// The happy path must return a report and cleanup (SIGTERM -> WaitDelay -> group kill) stays bounded.
func TestGetUsage_FakeClaudeHappyPathAndStubbornExit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake claude script requires a POSIX shell and pty")
	}
	binDir := t.TempDir()
	script := `#!/bin/sh
trap '' TERM
printf 'Claude Code v9.9.9\r\n? for shortcuts\r\n\r\n'
printf '\342\235\257 '
IFS= read -r line
printf '\033[2J\033[H'
printf 'Current session\r\n9%% used\r\nResets 6:20am (Asia/Shanghai)\r\n'
printf 'Current week (all models)\r\n2%% used\r\nResets Oct 11 at 12am (Asia/Shanghai)\r\n'
printf 'Esc to cancel\r\n'
while :; do sleep 1; done
`
	if err := os.WriteFile(filepath.Join(binDir, "claude"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake claude: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+"/usr/bin:/bin")
	t.Setenv("HOME", t.TempDir())

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	start := time.Now()
	report, err := (&Agent{}).GetUsage(ctx)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("GetUsage: %v (after %s)", err, elapsed)
	}
	if report == nil || len(report.Buckets) == 0 || len(report.Buckets[0].Windows) != 2 {
		t.Fatalf("unexpected report: %+v", report)
	}
	if elapsed > 8*time.Second {
		t.Fatalf("GetUsage took %s; cleanup of a SIGTERM-ignoring probe should be bounded", elapsed)
	}
	t.Logf("GetUsage returned in %s", elapsed.Round(time.Millisecond))
}
