package claudecode

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/chenhg5/cc-connect/core"
	"github.com/creack/pty"
)

const (
	claudeUsageSessionWindowSeconds = 5 * 60 * 60
	claudeUsageWeekWindowSeconds    = 7 * 24 * 60 * 60
	claudeUsagePollInterval         = 100 * time.Millisecond
	claudeUsageStableFor            = 450 * time.Millisecond
	claudeUsageActionGap            = 250 * time.Millisecond
	// claudeUsageCleanupWait bounds each cleanup step (waiting for the process / reader to exit).
	// The engine runs /usage synchronously inside the platform receive loop, so every second
	// spent here is a second the bot cannot receive messages.
	claudeUsageCleanupWait = 1 * time.Second
	// claudeUsageDialogRetry is how long to wait for the screen to react to a dialog key before retrying.
	claudeUsageDialogRetry = 1 * time.Second
	// claudeUsageMaxDialogResponses caps dialog keystrokes so an unrecognised UI cannot loop forever.
	claudeUsageMaxDialogResponses = 8
	// claudeUsageSettleMax treats the screen as settled after this long even if it keeps changing (spinners).
	claudeUsageSettleMax = 3 * time.Second
)

// claudeUsageProbeDir returns the working directory for the /usage probe.
// A stable cache directory means the folder-trust dialog only appears once, and each probe
// no longer adds a random temp-dir project entry to ~/.claude.json.
// Falls back to a one-off temp dir (removed afterwards) when no cache dir is available.
func claudeUsageProbeDir() (dir string, cleanup func(), err error) {
	if cacheDir, cerr := os.UserCacheDir(); cerr == nil {
		dir = filepath.Join(cacheDir, "cc-connect", "claude-usage-probe")
		if mkErr := os.MkdirAll(dir, 0o700); mkErr == nil {
			return dir, func() {}, nil
		}
	}
	dir, err = os.MkdirTemp("", "cc-connect-claude-usage-*")
	if err != nil {
		return "", nil, err
	}
	return dir, func() { _ = os.RemoveAll(dir) }, nil
}

var (
	claudeUsagePercentRe    = regexp.MustCompile(`(?i)\b(\d{1,3})\s*%\s*used\b`)
	claudeUsageResetLineRe  = regexp.MustCompile(`(?i)^resets\s+(.+?)\s*$`)
	claudeUsageParenTZRe    = regexp.MustCompile(`^(.*?)\s*\(([^()]+)\)\s*$`)
	claudeUsageWhitespaceRe = regexp.MustCompile(`[ \t]+`)
	claudeUsageRuleLineRe   = regexp.MustCompile(`^[\p{Zs}\-─━_=]{4,}$`)
)

type claudeUsageProbeState struct {
	dialogResponses int
	sentUsage       bool // typed "/usage" into the input box (without Enter)
	sentEnter       bool // saw "/usage" echoed in the input box and pressed Enter
	sentEnterRetry  bool
	sentUsageRetry  bool
	lastActionAt    time.Time
	usageSentAt     time.Time
}

func (a *Agent) GetUsage(ctx context.Context) (*core.UsageReport, error) {
	if _, err := exec.LookPath("claude"); err != nil {
		return nil, fmt.Errorf("claudecode: 'claude' CLI not found in PATH")
	}

	screen, err := a.runClaudeUsageProbe(ctx)
	if err != nil {
		return nil, err
	}
	return parseClaudeUsageReport(screen, time.Now())
}

func (a *Agent) runClaudeUsageProbe(ctx context.Context) (string, error) {
	probeCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	workDir, cleanupDir, err := claudeUsageProbeDir()
	if err != nil {
		return "", fmt.Errorf("claudecode: create usage probe dir: %w", err)
	}
	defer cleanupDir()

	args := []string{
		"--tools", "",
		"--permission-mode", "plan",
		"--no-chrome",
		// Only use MCP servers from --mcp-config (none given): the probe does not start the
		// user's configured MCP servers, which is faster and leaves no grandchild processes.
		"--strict-mcp-config",
	}
	cmd := exec.CommandContext(probeCtx, "claude", args...)
	cmd.Dir = workDir
	// CommandContext's default cancel is SIGKILL, which can corrupt ~/.claude.json if claude is
	// writing it. Send SIGTERM to the process group instead (pty.Start uses Setsid, so claude leads
	// its own group); WaitDelay then kills it and closes pipes, so a grandchild holding stderr
	// cannot keep Wait blocked.
	cmd.Cancel = func() error { return signalProcessGroup(cmd, syscall.SIGTERM) }
	cmd.WaitDelay = claudeUsageCleanupWait

	env := filterEnv(os.Environ(), "CLAUDECODE")
	env = append(env, "DISABLE_TELEMETRY=true")
	env = append(env, "DISABLE_COST_WARNINGS=true")
	if extra := a.usageProbeEnv(); len(extra) > 0 {
		env = core.MergeEnv(env, extra)
	}
	cmd.Env = env

	// exec copies stderr from its own goroutine while the probe loop reads it every tick.
	stderr := &lockedBuffer{}
	cmd.Stderr = stderr

	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 40, Cols: 120})
	if err != nil {
		return "", fmt.Errorf("claudecode: start claude usage probe: %w", err)
	}

	var waitErr error
	processDone := make(chan struct{})
	go func() {
		waitErr = cmd.Wait()
		close(processDone)
	}()

	terminal := newClaudeUsageTerminal()
	readDone := make(chan error, 1)
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := ptmx.Read(buf)
			if n > 0 {
				terminal.Write(buf[:n])
			}
			if err != nil {
				if errors.Is(err, io.EOF) || errors.Is(err, os.ErrClosed) {
					readDone <- nil
				} else {
					readDone <- err
				}
				return
			}
		}
	}()

	// readerExited records whether the main loop already received from readDone. readDone is
	// written exactly once; the old deferred `<-readDone` blocked forever after the loop had
	// consumed it, freezing the caller (and with it the whole platform receive loop).
	readerExited := false

	defer func() {
		// Order: let claude exit gracefully, close the pty, then wait (bounded) for the reader.
		// Every step is bounded: leaking a reader goroutine is better than hanging the caller.
		cancel()
		select {
		case <-processDone:
		case <-time.After(2 * claudeUsageCleanupWait):
			slog.Warn("claudeSession: usage probe did not exit after SIGTERM + WaitDelay")
		}
		// When WaitDelay expires exec only SIGKILLs claude itself; grandchildren that ignore
		// SIGTERM would survive. Always kill the whole group (ESRCH is ignored if it is gone).
		if err := forceKillCmd(cmd); err != nil {
			slog.Warn("claudeSession: kill usage probe process group", "error", err)
		}
		select {
		case <-processDone:
		case <-time.After(claudeUsageCleanupWait):
			slog.Warn("claudeSession: usage probe process did not exit after kill")
		}
		if err := ptmx.Close(); err != nil {
			slog.Warn("claudeSession: close ptmx", "error", err)
		}
		// On macOS a child may inherit the pty slave, so Read may not return right after Close.
		if !readerExited {
			select {
			case <-readDone:
			case <-time.After(claudeUsageCleanupWait):
				slog.Warn("claudeSession: usage probe reader still blocked; leaking goroutine")
			}
		}
	}()

	probeStart := time.Now()
	ticker := time.NewTicker(claudeUsagePollInterval)
	defer ticker.Stop()

	var (
		state       claudeUsageProbeState
		lastScreen  string
		lastChange  = time.Now()
		usageScreen string
		readCh      = readDone
	)

	for {
		select {
		case <-probeCtx.Done():
			if usageScreen != "" {
				return usageScreen, nil
			}
			if screenErr := detectClaudeUsageOutputError(lastScreen, stderr.String()); screenErr != nil {
				return "", screenErr
			}
			return "", fmt.Errorf("claudecode: timed out waiting for Claude Code /usage panel: %w", probeCtx.Err())
		case err := <-readCh:
			readerExited = true
			// Disable this case (a nil channel blocks forever) and keep waiting for processDone.
			// readDone itself is left untouched for the reader goroutine and the deferred cleanup.
			readCh = nil
			if err != nil {
				return "", fmt.Errorf("claudecode: read Claude Code /usage output: %w", err)
			}
		case <-processDone:
			slog.Debug("claudecode: usage probe process exited", "screen_tail", claudeUsageScreenTail(normalizeClaudeUsageText(terminal.String())))
			if usageScreen != "" {
				return usageScreen, nil
			}
			if screenErr := detectClaudeUsageOutputError(lastScreen, stderr.String()); screenErr != nil {
				return "", screenErr
			}
			if waitErr != nil {
				return "", fmt.Errorf("claudecode: Claude Code exited before /usage rendered: %w", waitErr)
			}
			return "", fmt.Errorf("claudecode: Claude Code exited before /usage rendered")
		case <-ticker.C:
			screen := normalizeClaudeUsageText(terminal.String())
			if screen != lastScreen {
				lastScreen = screen
				lastChange = time.Now()
			}
			// Refresh the snapshot on every ready tick: the panel renders in chunks.
			if usageReady(lastScreen) {
				usageScreen = lastScreen
			}
			if screenErr := detectClaudeUsageOutputError(lastScreen, stderr.String()); screenErr != nil {
				return "", screenErr
			}
			if usageScreen != "" && time.Since(lastChange) >= claudeUsageStableFor {
				return usageScreen, nil
			}
			// No blind keys before the UI has rendered: an early Enter sits in the input buffer and
			// is consumed by the trust dialog as soon as it appears, selecting the default "No, exit".
			settled := lastScreen != "" &&
				(time.Since(lastChange) >= claudeUsageStableFor || time.Since(probeStart) >= claudeUsageSettleMax)
			screenChanged := lastChange.After(state.lastActionAt)
			if action := nextClaudeUsageProbeAction(lastScreen, &state, time.Now(), settled, screenChanged); action != "" {
				slog.Debug("claudecode: usage probe input", "action", fmt.Sprintf("%q", action), "screen_tail", claudeUsageScreenTail(lastScreen))
				if _, err := io.WriteString(ptmx, action); err != nil {
					return "", fmt.Errorf("claudecode: write Claude Code /usage probe input: %w", err)
				}
			}
		}
	}
}

func (a *Agent) usageProbeEnv() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.runtimeEnvLocked()
}

// nextClaudeUsageProbeAction decides what to type into claude next. Rules:
//   - only type when positively at the Claude Code input box (REPL);
//   - answer the folder-trust dialog by cursor position (it only trusts the fixed probe dir);
//   - press Esc on any other dialog (promos, default-mode nudges, telemetry, ...);
//   - send nothing on unrecognised screens: failing this probe beats choosing for the user.
//
// Background: the old code pressed Enter blindly. On Claude Code 2.1.289 that accepted the
// default "Yes" of "Make auto mode your default permission mode?" and rewrote the user's
// ~/.claude/settings.json.
//
// settled: the screen has rendered and is stable. screenChanged: the screen changed since the
// last keystroke (prevents acting twice on a stale frame).
func nextClaudeUsageProbeAction(screen string, state *claudeUsageProbeState, now time.Time, settled, screenChanged bool) string {
	if now.Sub(state.lastActionAt) < claudeUsageActionGap {
		return ""
	}
	// The panel is up: any key could close it.
	if usageReady(screen) {
		return ""
	}

	lower := strings.ToLower(screen)
	firstKey := state.lastActionAt.IsZero()
	// Esc / arrow keys: allowed once the screen changed, or after claudeUsageDialogRetry without a
	// change. Keep that interval above Claude Code's double-Esc window (~800ms), otherwise two Escs
	// open the rewind menu.
	retryTurn := (firstKey || screenChanged || now.Sub(state.lastActionAt) >= claudeUsageDialogRetry) &&
		state.dialogResponses < claudeUsageMaxDialogResponses
	// Enter: only after the screen actually changed. An unchanged screen may mean the previous Enter
	// was accepted but not yet redrawn; a repeated Enter would land on whatever gets focus next
	// (e.g. a promo dialog that pops up right after).
	enterTurn := (firstKey || screenChanged) && state.dialogResponses < claudeUsageMaxDialogResponses

	if isTrustDialog(lower) {
		action := trustDialogAction(lower)
		if (action == "\r" && enterTurn) || (action != "" && action != "\r" && retryTurn) {
			state.dialogResponses++
			state.lastActionAt = now
			return action
		}
		return ""
	}

	// Until Enter is pressed (including after typing /usage): Esc any dialog.
	if !state.sentEnter && hasDialog(lower) {
		if retryTurn {
			state.dialogResponses++
			state.lastActionAt = now
			return "\x1b"
		}
		return ""
	}

	// Step 1: type "/usage" without Enter. If a dialog pops up right before, the letters are
	// ignored by the selection list and never echo into the input box, so step 2 never fires.
	if !state.sentUsage {
		if !settled || replInput(lower) != "" || !isREPLReady(lower) {
			return ""
		}
		state.sentUsage = true
		state.usageSentAt = now
		state.lastActionAt = now
		return "/usage"
	}

	input := replInput(lower)

	// Step 2: press Enter only once "/usage" is visibly in the input box and no dialog is shown.
	if !state.sentEnter {
		switch {
		case strings.HasPrefix(input, "/usage") && screenChanged:
			state.sentEnter = true
			state.lastActionAt = now
			return "\r"
		case input == "" && isREPLReady(lower) && !state.sentUsageRetry && now.Sub(state.lastActionAt) >= 1500*time.Millisecond:
			// A dialog swallowed the letters and was dismissed with Esc; the input is empty, so
			// retype once. Ctrl+U first, in case the echo was merely slow (avoids "/usage/usage").
			state.sentUsageRetry = true
			state.lastActionAt = now
			return "\x15/usage"
		}
		return ""
	}

	// Enter was pressed. The loading panel also shows an "Esc to cancel" footer, so stay quiet;
	// only recover when the screen is still at the input box (the command did not run).
	if hasDialog(lower) || !replLinePresent(lower) {
		return ""
	}
	switch {
	case strings.HasPrefix(input, "/usage") && screenChanged && !state.sentEnterRetry && now.Sub(state.lastActionAt) >= 900*time.Millisecond:
		// The command is still in the input box (Enter was likely eaten by the completion menu):
		// press Enter again. Requires screenChanged; the menu closing changes the screen anyway.
		state.sentEnterRetry = true
		state.lastActionAt = now
		return "\r"
	case input == "" && isREPLReady(lower) && !state.sentUsageRetry && now.Sub(state.lastActionAt) >= 1500*time.Millisecond:
		// Input is empty but no panel appeared: go through type -> confirm -> Enter again.
		state.sentUsageRetry = true
		state.sentEnter = false
		state.lastActionAt = now
		return "\x15/usage"
	}
	return ""
}

// hasDialog reports whether a selection or confirmation dialog is on screen.
func hasDialog(lowerScreen string) bool {
	if isSelectionDialog(lowerScreen) {
		return true
	}
	// Dialog-specific footers only; phrases like "to navigate" also appear in REPL tips.
	for _, footer := range []string{"enter to confirm", "esc to cancel"} {
		if strings.Contains(lowerScreen, footer) {
			return true
		}
	}
	return false
}

// isREPLReady reports whether the Claude Code input box is shown: a ❯ input line plus the
// status bar below it or the welcome banner.
func isREPLReady(lowerScreen string) bool {
	if !replLinePresent(lowerScreen) {
		return false
	}
	return strings.Contains(lowerScreen, "shift+tab to cycle") ||
		strings.Contains(lowerScreen, "for shortcuts") ||
		strings.Contains(lowerScreen, "claude code v")
}

// replLinePresent / replInput: the input line looks like `❯`, `❯ try "…"` (placeholder) or
// `❯ /usage` (typed).
func replLinePresent(lowerScreen string) bool {
	_, ok := findREPLLine(lowerScreen)
	return ok
}

// replInput returns the typed input; "" for an empty box or a placeholder.
func replInput(lowerScreen string) string {
	input, _ := findREPLLine(lowerScreen)
	return input
}

func findREPLLine(lowerScreen string) (string, bool) {
	for _, line := range strings.Split(lowerScreen, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "❯") {
			continue
		}
		rest := strings.TrimSpace(strings.TrimPrefix(trimmed, "❯"))
		if !isREPLInputText(rest) {
			continue
		}
		if strings.HasPrefix(rest, "/") {
			return rest, true
		}
		return "", true
	}
	return "", false
}

// isREPLInputText reports whether the text after ❯ looks like the input line: empty, a
// placeholder (try "…") or a typed slash command.
func isREPLInputText(rest string) bool {
	return rest == "" || strings.HasPrefix(rest, "try \"") || strings.HasPrefix(rest, "/")
}

// isSelectionDialog reports whether a ❯-highlighted option is on screen ("❯ Yes, …", "❯ 1. …").
// The input line does not count; any other ❯ line is treated as a selection (prefer an extra Esc).
func isSelectionDialog(lowerScreen string) bool {
	for _, line := range strings.Split(lowerScreen, "\n") {
		trimmed := strings.TrimSpace(line)
		idx := strings.Index(trimmed, "❯")
		if idx < 0 {
			continue
		}
		rest := strings.TrimSpace(trimmed[idx+len("❯"):])
		if idx == 0 && isREPLInputText(rest) {
			continue
		}
		return true
	}
	return false
}

// trustDialogAction answers the folder-trust dialog based on which option the ❯ cursor is on.
// Older Claude Code preselected "Yes, proceed"; since 2.1.x the default is "No, exit", so a
// blind Enter makes claude quit. Move down when on no/exit, press Enter once on yes/trust.
func trustDialogAction(lowerScreen string) string {
	for _, line := range strings.Split(lowerScreen, "\n") {
		trimmed := strings.TrimSpace(line)
		idx := strings.Index(trimmed, "❯")
		if idx < 0 {
			continue
		}
		option := strings.TrimSpace(trimmed[idx+len("❯"):])
		// Stale dialog text may sit above the REPL input line; never treat that line as an option.
		if idx == 0 && isREPLInputText(option) {
			continue
		}
		// Match whole words / prefixes so "notification" or "yesterday" do not match.
		words := strings.FieldsFunc(option, func(r rune) bool {
			return r == ' ' || r == ',' || r == '.' || r == '\t'
		})
		for _, w := range words {
			switch {
			case w == "yes" || strings.HasPrefix(w, "trust"):
				return "\r"
			case w == "no" || w == "exit":
				return "\x1b[B"
			}
		}
	}
	// Cursor not found (options not rendered yet): wait for the next frame. A blind Enter would
	// pick "No, exit" on current versions.
	return ""
}

// claudeUsageScreenTail returns the last few screen lines for debug logging.
func claudeUsageScreenTail(screen string) string {
	lines := strings.Split(screen, "\n")
	if len(lines) > 8 {
		lines = lines[len(lines)-8:]
	}
	return strings.Join(lines, " ⏎ ")
}

// isTrustDialog reports whether the folder-trust dialog is on screen.
func isTrustDialog(lowerScreen string) bool {
	return strings.Contains(lowerScreen, "quick safety check") ||
		strings.Contains(lowerScreen, "yes, i trust this folder") ||
		strings.Contains(lowerScreen, "do you trust the files in this folder")
}

// usageReady reports whether the /usage panel has rendered enough to parse. It asks the parser
// instead of counting "% used": the panel renders in chunks and blocks such as Extra usage also
// carry percentages, so counting accepted half-loaded panels ("missing usage percentage").
func usageReady(screen string) bool {
	lower := strings.ToLower(screen)
	if !strings.Contains(lower, "current session") || !strings.Contains(lower, "current week") {
		return false
	}
	_, err := parseClaudeUsageReport(screen, time.Now())
	return err == nil
}

func normalizeClaudeUsageText(raw string) string {
	raw = strings.ReplaceAll(raw, "\r", "\n")
	lines := strings.Split(raw, "\n")

	out := make([]string, 0, len(lines))
	lastBlank := false
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			if !lastBlank {
				out = append(out, "")
				lastBlank = true
			}
			continue
		}
		line = claudeUsageWhitespaceRe.ReplaceAllString(line, " ")
		if claudeUsageRuleLineRe.MatchString(line) {
			continue
		}
		out = append(out, line)
		lastBlank = false
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

func parseClaudeUsageReport(text string, now time.Time) (*core.UsageReport, error) {
	text = normalizeClaudeUsageText(text)
	if text == "" {
		return nil, fmt.Errorf("claudecode: Claude Code /usage produced empty output")
	}
	if err := detectClaudeUsageOutputError(text, ""); err != nil {
		return nil, err
	}

	report := &core.UsageReport{
		Provider: "claudecode",
	}

	lines := strings.Split(text, "\n")
	session, err := parseClaudeUsageWindow(lines, "Current session", claudeUsageSessionWindowSeconds, now)
	if err != nil {
		return nil, err
	}
	week, err := parseClaudeUsageWindow(lines, "Current week", claudeUsageWeekWindowSeconds, now)
	if err != nil {
		return nil, err
	}

	report.Buckets = []core.UsageBucket{{
		Name:    "Usage",
		Allowed: true,
		Windows: []core.UsageWindow{session, week},
	}}
	return report, nil
}

func parseClaudeUsageWindow(lines []string, header string, windowSeconds int, now time.Time) (core.UsageWindow, error) {
	start := -1
	headerLower := strings.ToLower(header)
	for i, line := range lines {
		lower := strings.ToLower(strings.TrimSpace(line))
		if strings.HasPrefix(lower, headerLower) {
			start = i
			break
		}
	}
	if start < 0 {
		return core.UsageWindow{}, fmt.Errorf("claudecode: missing %s block in Claude Code /usage output", header)
	}

	var (
		usedPercent *int
		resetRaw    string
	)
	for i := start + 1; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		lower := strings.ToLower(line)
		if i > start+1 && (strings.HasPrefix(lower, "current ") || strings.HasPrefix(lower, "extra usage")) {
			break
		}
		if usedPercent == nil {
			if m := claudeUsagePercentRe.FindStringSubmatch(line); len(m) == 2 {
				v, _ := strconv.Atoi(m[1])
				usedPercent = &v
				continue
			}
		}
		if resetRaw == "" {
			if m := claudeUsageResetLineRe.FindStringSubmatch(line); len(m) == 2 {
				resetRaw = strings.TrimSpace(m[1])
			}
		}
	}

	if usedPercent == nil {
		return core.UsageWindow{}, fmt.Errorf("claudecode: missing usage percentage in %s block", header)
	}

	window := core.UsageWindow{
		Name:          header,
		UsedPercent:   *usedPercent,
		WindowSeconds: windowSeconds,
	}

	if resetRaw != "" {
		resetAt, err := parseClaudeUsageResetTime(resetRaw, now)
		if err == nil {
			resetAfter := int(resetAt.Sub(now).Round(time.Second).Seconds())
			if resetAfter < 0 {
				resetAfter = 0
			}
			window.ResetAfterSeconds = resetAfter
			window.ResetAtUnix = resetAt.Unix()
		}
	}

	return window, nil
}

func parseClaudeUsageResetTime(raw string, now time.Time) (time.Time, error) {
	label := strings.TrimSpace(raw)
	loc := now.Location()
	if m := claudeUsageParenTZRe.FindStringSubmatch(label); len(m) == 3 {
		label = strings.TrimSpace(m[1])
		tzName := strings.TrimSpace(m[2])
		tzLoc, err := time.LoadLocation(tzName)
		if err != nil {
			return time.Time{}, fmt.Errorf("unknown timezone %q", tzName)
		}
		loc = tzLoc
	}

	nowInLoc := now.In(loc)
	for _, layout := range []string{"3:04pm", "3pm"} {
		parsed, err := time.ParseInLocation(layout, label, loc)
		if err != nil {
			continue
		}
		resetAt := time.Date(nowInLoc.Year(), nowInLoc.Month(), nowInLoc.Day(), parsed.Hour(), parsed.Minute(), 0, 0, loc)
		if !resetAt.After(nowInLoc) {
			resetAt = resetAt.Add(24 * time.Hour)
		}
		return resetAt, nil
	}

	for _, layout := range []string{"Jan 2, 3:04pm", "Jan 2, 3pm"} {
		parsed, err := time.ParseInLocation(layout, label, loc)
		if err != nil {
			continue
		}
		resetAt := time.Date(nowInLoc.Year(), parsed.Month(), parsed.Day(), parsed.Hour(), parsed.Minute(), 0, 0, loc)
		if !resetAt.After(nowInLoc) {
			resetAt = resetAt.AddDate(1, 0, 0)
		}
		return resetAt, nil
	}

	return time.Time{}, fmt.Errorf("unsupported reset time %q", raw)
}

func detectClaudeUsageOutputError(screen, stderr string) error {
	joined := strings.ToLower(strings.TrimSpace(screen + "\n" + stderr))
	switch {
	case joined == "":
		return nil
	case strings.Contains(joined, "unknown command") && strings.Contains(joined, "/usage"):
		return fmt.Errorf("claudecode: this Claude Code version does not support /usage; please upgrade Claude Code")
	case strings.Contains(joined, "auth login") || strings.Contains(joined, "not logged in") || strings.Contains(joined, "sign in"):
		return fmt.Errorf("claudecode: Claude Code is not logged in for /usage; run `claude auth login`")
	case strings.Contains(joined, "/usage") && (strings.Contains(joined, "not available") || strings.Contains(joined, "not supported") || strings.Contains(joined, "subscription")):
		return fmt.Errorf("claudecode: current Claude account does not support /usage")
	default:
		return nil
	}
}

// lockedBuffer is a bytes.Buffer safe for exec's copy goroutine (writer) and the probe loop (reader).
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

type claudeUsageTerminal struct {
	mu    sync.RWMutex
	lines [][]rune
	row   int
	col   int
}

func newClaudeUsageTerminal() *claudeUsageTerminal {
	return &claudeUsageTerminal{
		lines: [][]rune{nil},
	}
}

func (t *claudeUsageTerminal) Write(p []byte) {
	t.mu.Lock()
	defer t.mu.Unlock()

	for i := 0; i < len(p); {
		switch p[i] {
		case 0x1b:
			next := t.consumeEscape(p[i:])
			if next <= 0 {
				i++
			} else {
				i += next
			}
		case '\r':
			t.col = 0
			i++
		case '\n':
			t.row++
			t.ensureRow(t.row)
			i++
		case '\b':
			if t.col > 0 {
				t.col--
			}
			i++
		case '\t':
			advance := 4 - (t.col % 4)
			for j := 0; j < advance; j++ {
				t.writeRune(' ')
			}
			i++
		default:
			if p[i] < 0x20 || p[i] == 0x7f {
				i++
				continue
			}
			r, size := utf8.DecodeRune(p[i:])
			if r == utf8.RuneError && size == 1 {
				i++
				continue
			}
			t.writeRune(r)
			i += size
		}
	}
}

func (t *claudeUsageTerminal) String() string {
	t.mu.RLock()
	defer t.mu.RUnlock()

	lines := make([]string, 0, len(t.lines))
	for _, line := range t.lines {
		lines = append(lines, strings.TrimRight(string(line), " "))
	}
	return strings.Join(lines, "\n")
}

func (t *claudeUsageTerminal) consumeEscape(p []byte) int {
	if len(p) < 2 {
		return len(p)
	}
	switch p[1] {
	case '[':
		for i := 2; i < len(p); i++ {
			if p[i] >= '@' && p[i] <= '~' {
				t.applyCSI(string(p[2:i]), p[i])
				return i + 1
			}
		}
		return len(p)
	case ']':
		for i := 2; i < len(p); i++ {
			if p[i] == '\a' {
				return i + 1
			}
			if p[i] == 0x1b && i+1 < len(p) && p[i+1] == '\\' {
				return i + 2
			}
		}
		return len(p)
	default:
		return 2
	}
}

func (t *claudeUsageTerminal) applyCSI(params string, final byte) {
	switch final {
	case 'A', 'B', 'C', 'D':
		n := parseCSIInt(params, 1)
		switch final {
		case 'A':
			t.row -= n
			if t.row < 0 {
				t.row = 0
			}
		case 'B':
			t.row += n
			t.ensureRow(t.row)
		case 'C':
			t.col += n
		case 'D':
			t.col -= n
			if t.col < 0 {
				t.col = 0
			}
		}
	case 'G':
		// CHA: move to column n (1-based). Claude Code 2.1.x (Ink) uses it instead of spaces between
		// words; without it the screen renders as "Quicksafetycheck" and all matching/parsing fails.
		col := parseCSIInt(params, 1) - 1
		if col < 0 {
			col = 0
		}
		t.col = col
	case 'H', 'f':
		row, col := parseCSICursor(params)
		if row < 0 {
			row = 0
		}
		if col < 0 {
			col = 0
		}
		t.row, t.col = row, col
		t.ensureRow(t.row)
	case 'J':
		switch params {
		case "", "0":
			// ED 0: erase from the cursor to the end of the screen (previously treated as a full clear).
			t.ensureRow(t.row)
			if t.row < len(t.lines) {
				if t.col < len(t.lines[t.row]) {
					t.lines[t.row] = t.lines[t.row][:t.col]
				}
				t.lines = t.lines[:t.row+1]
			}
		case "2":
			t.lines = [][]rune{nil}
			t.row, t.col = 0, 0
		case "3":
			// ED 3 only clears scrollback, which this emulator does not keep.
		}
	case 'K':
		t.ensureRow(t.row)
		switch params {
		case "", "0":
			if t.col < len(t.lines[t.row]) {
				t.lines[t.row] = t.lines[t.row][:t.col]
			}
		case "2":
			t.lines[t.row] = nil
			t.col = 0
		}
	default:
	}
}

func (t *claudeUsageTerminal) writeRune(r rune) {
	if t.row >= maxTerminalRows || t.col >= maxTerminalCols {
		return
	}
	t.ensureCell(t.row, t.col)
	if t.row < len(t.lines) && t.col < len(t.lines[t.row]) {
		t.lines[t.row][t.col] = r
	}
	t.col++
}

const maxTerminalRows = 500
const maxTerminalCols = 500

func (t *claudeUsageTerminal) ensureRow(row int) {
	if row >= maxTerminalRows {
		return
	}
	for len(t.lines) <= row {
		t.lines = append(t.lines, nil)
	}
}

func (t *claudeUsageTerminal) ensureCell(row, col int) {
	if row >= maxTerminalRows || col >= maxTerminalCols {
		return
	}
	t.ensureRow(row)
	for len(t.lines[row]) <= col {
		t.lines[row] = append(t.lines[row], ' ')
	}
}

func parseCSIInt(raw string, fallback int) int {
	raw = strings.TrimPrefix(raw, "?")
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return fallback
	}
	return n
}

func parseCSICursor(raw string) (int, int) {
	raw = strings.TrimPrefix(raw, "?")
	if raw == "" {
		return 0, 0
	}
	parts := strings.Split(raw, ";")
	row := parseCSIInt(parts[0], 1) - 1
	col := 0
	if len(parts) > 1 {
		col = parseCSIInt(parts[1], 1) - 1
	}
	return row, col
}
