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
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/chenhg5/cc-connect/core"
)

const (
	claudeUsageSessionWindowSeconds = 5 * 60 * 60
	claudeUsageWeekWindowSeconds    = 7 * 24 * 60 * 60
	claudeUsagePollInterval         = 100 * time.Millisecond
	claudeUsageStableFor            = 450 * time.Millisecond
	claudeUsageActionGap            = 250 * time.Millisecond
	claudeUsageRows                 = 40
	claudeUsageCols                 = 120

	// claudeUsageReaderExitTimeout bounds how long the probe's cleanup waits
	// for the PTY reader after closing the terminal.
	claudeUsageReaderExitTimeout = 2 * time.Second
	// The probe's temp dir is removed in up to claudeUsageWorkDirRemoveAttempts
	// tries. The delay between tries starts at claudeUsageWorkDirRemoveDelay
	// and doubles, about 1.5 seconds in total.
	claudeUsageWorkDirRemoveAttempts = 5
	claudeUsageWorkDirRemoveDelay    = 100 * time.Millisecond
)

var (
	claudeUsagePercentRe    = regexp.MustCompile(`(?i)\b(\d{1,3})\s*%\s*used\b`)
	claudeUsageResetLineRe  = regexp.MustCompile(`(?i)^resets\s+(.+?)\s*$`)
	claudeUsageParenTZRe    = regexp.MustCompile(`^(.*?)\s*\(([^()]+)\)\s*$`)
	claudeUsageWhitespaceRe = regexp.MustCompile(`[ \t]+`)
	claudeUsageRuleLineRe   = regexp.MustCompile(`^[\p{Zs}\-─━_=]{4,}$`)
)

type claudeUsageProbeState struct {
	promptResponses int
	sentWake        bool
	sentUsage       bool
	sentEnterRetry  bool
	sentUsageRetry  bool
	lastActionAt    time.Time
	usageSentAt     time.Time
}

// lockedBuffer is a bytes.Buffer that can be written by os/exec's stderr copier goroutine
// while the probe loop reads it. A plain bytes.Buffer is a data race there, and -race
// reports it in TestRunClaudeUsageProbe_ReturnsWhenReaderFinishesFirst.
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

	workDir, err := os.MkdirTemp("", "cc-connect-claude-usage-*")
	if err != nil {
		return "", fmt.Errorf("claudecode: create usage temp dir: %w", err)
	}
	defer removeClaudeUsageWorkDir(workDir)

	args := []string{
		"--tools", "",
		"--permission-mode", "plan",
		"--no-chrome",
	}
	cmd := exec.CommandContext(probeCtx, "claude", args...)
	cmd.Dir = workDir

	env := filterEnv(os.Environ(), "CLAUDECODE")
	env = append(env, "DISABLE_TELEMETRY=true")
	env = append(env, "DISABLE_COST_WARNINGS=true")
	if extra := a.usageProbeEnv(); len(extra) > 0 {
		env = core.MergeEnv(env, extra)
	}
	cmd.Env = env

	var stderr lockedBuffer
	cmd.Stderr = &stderr

	ptmx, waitProcess, err := startClaudeUsagePTY(cmd, claudeUsageRows, claudeUsageCols)
	if err != nil {
		return "", fmt.Errorf("claudecode: start claude usage probe: %w", err)
	}

	var waitErr error
	processDone := make(chan struct{})
	go func() {
		waitErr = waitProcess()
		close(processDone)
	}()

	terminal := newClaudeUsageProbeTerminal(claudeUsageRows, claudeUsageCols)
	// readDone carries the reader's single result and may be consumed by the
	// loop below; readerExited is closed when the reader returns, so cleanup can
	// wait on it no matter who received from readDone.
	readDone := make(chan error, 1)
	readerExited := make(chan struct{})
	go func() {
		defer close(readerExited)
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

	defer func() {
		if err := ptmx.Close(); err != nil {
			slog.Warn("claudeSession: close ptmx", "error", err)
		}
		cancel()
		// Closing the terminal ends the reader's blocked Read, so wait for the
		// reader instead of leaking it. The wait is bounded because a Read
		// that never returns must not hang the probe: this cleanup also runs
		// when the context times out. A reader that is still blocked exits on
		// its own once its Read returns.
		if !waitClaudeUsageReader(readerExited, claudeUsageReaderExitTimeout) {
			slog.Warn("claudecode: usage probe reader did not exit after the terminal was closed",
				"timeout", claudeUsageReaderExitTimeout)
		}
		select {
		case <-processDone:
		case <-time.After(2 * time.Second):
			if cmd.Process != nil {
				if err := cmd.Process.Kill(); err != nil {
					slog.Warn("claudeSession: kill process", "error", err)
				}
			}
			<-processDone
		}
	}()

	ticker := time.NewTicker(claudeUsagePollInterval)
	defer ticker.Stop()

	var (
		state       claudeUsageProbeState
		lastScreen  string
		lastChange  = time.Now()
		usageScreen string
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
		case err := <-readDone:
			if err != nil {
				return "", fmt.Errorf("claudecode: read Claude Code /usage output: %w", err)
			}
		case <-processDone:
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
			if usageReady(lastScreen) {
				if usageScreen == "" {
					usageScreen = lastScreen
				}
			}
			if screenErr := detectClaudeUsageOutputError(lastScreen, stderr.String()); screenErr != nil {
				return "", screenErr
			}
			if usageScreen != "" && time.Since(lastChange) >= claudeUsageStableFor {
				return usageScreen, nil
			}
			if action := nextClaudeUsageProbeAction(lastScreen, &state, time.Now()); action != "" {
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

func nextClaudeUsageProbeAction(screen string, state *claudeUsageProbeState, now time.Time) string {
	if now.Sub(state.lastActionAt) < claudeUsageActionGap {
		return ""
	}

	if action := promptActionForScreen(screen); action != "" && state.promptResponses < 6 {
		state.promptResponses++
		state.lastActionAt = now
		return action
	}

	if usageReady(screen) {
		return ""
	}

	if !state.sentWake {
		state.sentWake = true
		state.lastActionAt = now
		return "\r"
	}

	if !state.sentUsage {
		state.sentUsage = true
		state.usageSentAt = now
		state.lastActionAt = now
		return "/usage\r"
	}

	if !state.sentEnterRetry && now.Sub(state.usageSentAt) >= 900*time.Millisecond {
		state.sentEnterRetry = true
		state.lastActionAt = now
		return "\r"
	}

	if !state.sentUsageRetry && now.Sub(state.usageSentAt) >= 1500*time.Millisecond {
		state.sentUsageRetry = true
		state.lastActionAt = now
		return "/usage\r"
	}

	return ""
}

func promptActionForScreen(screen string) string {
	lower := strings.ToLower(screen)
	if lower == "" {
		return ""
	}
	if strings.Contains(lower, "quick safety check") || strings.Contains(lower, "yes, i trust this folder") {
		return "\r"
	}
	if (strings.Contains(lower, "telemetry") || strings.Contains(lower, "help improve") || strings.Contains(lower, "usage data")) &&
		(strings.Contains(lower, "2. no") || strings.Contains(lower, "2. disable") || strings.Contains(lower, "2. don't")) {
		return "\x1b[B\r"
	}
	if strings.Contains(lower, "enter to confirm") && !usageReady(lower) {
		return "\r"
	}
	return ""
}

func usageReady(screen string) bool {
	lower := strings.ToLower(screen)
	return strings.Contains(lower, "current session") &&
		strings.Contains(lower, "current week") &&
		strings.Contains(lower, "resets") &&
		claudeUsagePercentRe.MatchString(screen)
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

// waitClaudeUsageReader waits up to timeout for the probe's PTY reader to
// exit and reports whether it did.
func waitClaudeUsageReader(readerExited <-chan struct{}, timeout time.Duration) bool {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-readerExited:
		return true
	case <-timer.C:
		return false
	}
}

// removeClaudeUsageWorkDir removes the probe's temp dir and logs a failure,
// so a leaked directory is visible.
func removeClaudeUsageWorkDir(dir string) {
	err := removeAllWithRetry(dir, os.RemoveAll, claudeUsageWorkDirRemoveAttempts, claudeUsageWorkDirRemoveDelay)
	if err != nil {
		slog.Warn("claudecode: remove usage probe temp dir", "dir", dir, "error", err)
	}
}

// removeAllWithRetry calls remove(path) up to attempts times and returns the
// last error. It sleeps between tries, starting at delay and doubling.
//
// One try is enough on Unix. Windows cannot remove a directory that a process
// still uses as its working directory, and a killed process releases it only
// some time after the kill returns.
func removeAllWithRetry(path string, remove func(string) error, attempts int, delay time.Duration) error {
	var err error
	for i := 0; i < attempts; i++ {
		if i > 0 {
			time.Sleep(delay)
			delay *= 2
		}
		if err = remove(path); err == nil {
			return nil
		}
	}
	return err
}

type claudeUsageTerminal struct {
	mu    sync.RWMutex
	lines [][]rune
	row   int
	col   int

	// width and height are zero unless the terminal emulates a fixed-size
	// screen (see newClaudeUsageScreenTerminal); wrapPending is only used then.
	width       int
	height      int
	wrapPending bool
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
			t.wrapPending = false
			i++
		case '\n':
			t.lineFeed()
			i++
		case '\b':
			if t.col > 0 {
				t.col--
			}
			t.wrapPending = false
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
	if csiCancelsPendingWrap(final) {
		t.wrapPending = false
	}
	defer t.clampCursorToScreen()

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
		if params == "2" || params == "" {
			t.lines = [][]rune{nil}
			t.row, t.col = 0, 0
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
	case 'X':
		t.eraseScreenChars(parseCSIInt(params, 1))
	case 'd':
		t.setScreenRow(parseCSIInt(params, 1) - 1)
	default:
	}
}

func (t *claudeUsageTerminal) writeRune(r rune) {
	if t.width > 0 {
		t.writeScreenRune(r)
		return
	}
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
	if row >= maxTerminalRows || (t.height > 0 && row >= t.height) {
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
