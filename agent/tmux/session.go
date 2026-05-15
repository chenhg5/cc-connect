package tmux

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

type tmuxSession struct {
	target          string // e.g., "mywork:0"
	sessionID       string
	promptPat       *regexp.Regexp
	pollInt         time.Duration
	stripInputBlock bool
	stripPatterns   []*regexp.Regexp
	events          chan core.Event
	ctx             context.Context
	cancel          context.CancelFunc
	alive           atomic.Bool
	closeOnce       sync.Once

	mu          sync.Mutex
	pollCancel  context.CancelFunc
	baselineLen int // scrollback line count at the time of the last Send()
}

func newTmuxSession(ctx context.Context, target, sessionID, promptPattern string, pollInt time.Duration, stripInputBlock bool, stripPatternStrs []string) (*tmuxSession, error) {
	sessCtx, cancel := context.WithCancel(ctx)

	var pat *regexp.Regexp
	if promptPattern != "" {
		var err error
		pat, err = regexp.Compile(promptPattern)
		if err != nil {
			cancel()
			return nil, fmt.Errorf("tmux: invalid prompt_pattern %q: %w", promptPattern, err)
		}
	}

	var stripPats []*regexp.Regexp
	for _, s := range stripPatternStrs {
		re, err := regexp.Compile(s)
		if err != nil {
			cancel()
			return nil, fmt.Errorf("tmux: invalid strip_pattern %q: %w", s, err)
		}
		stripPats = append(stripPats, re)
	}

	s := &tmuxSession{
		target:          target,
		sessionID:       sessionID,
		promptPat:       pat,
		pollInt:         pollInt,
		stripInputBlock: stripInputBlock,
		stripPatterns:   stripPats,
		events:          make(chan core.Event, 128),
		ctx:       sessCtx,
		cancel:    cancel,
	}
	s.alive.Store(true)
	return s, nil
}

func (s *tmuxSession) Send(prompt string, _ []core.ImageAttachment, files []core.FileAttachment) error {
	if !s.alive.Load() {
		return fmt.Errorf("tmux: session closed")
	}

	// Save attached files and append their paths to the prompt
	if len(files) > 0 {
		paths := core.SaveFilesToDisk(".", files)
		if len(paths) > 0 {
			prompt = prompt + "\n# files: " + strings.Join(paths, ", ")
		}
	}

	// Cancel any running poll from a previous Send
	s.mu.Lock()
	if s.pollCancel != nil {
		s.pollCancel()
		s.pollCancel = nil
	}

	// Use the scrollback buffer line count as baseline so that terminal scrolling
	// never causes the previous response to bleed into the next one.
	scrollback, err := captureScrollback(s.target)
	if err != nil {
		s.mu.Unlock()
		return fmt.Errorf("tmux: capture baseline: %w", err)
	}
	visibleBase, _ := capturePane(s.target) // for stability comparison only
	s.baselineLen = len(strings.Split(scrollback, "\n"))

	pollCtx, pollCancel := context.WithCancel(s.ctx)
	s.pollCancel = pollCancel
	s.mu.Unlock()

	if err := sendKeys(s.target, prompt); err != nil {
		pollCancel()
		return fmt.Errorf("tmux: send-keys: %w", err)
	}

	go s.poll(pollCtx, visibleBase)
	return nil
}

func (s *tmuxSession) poll(ctx context.Context, baseline string) {
	ticker := time.NewTicker(s.pollInt)
	defer ticker.Stop()

	prev := baseline
	stable := 0

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			current, err := capturePane(s.target)
			if err != nil {
				slog.Warn("tmux: capture-pane error", "target", s.target, "err", err)
				continue
			}

			if current == prev {
				stable++
			} else {
				stable = 0
				prev = current
			}

			// Done: pane stable AND changed from baseline.
			// fast path — prompt pattern matched; slow path — 5 s idle fallback.
			if stable >= 2 && current != baseline {
				trimmed := strings.TrimRight(current, " \t\n")
				promptOK := s.promptPat == nil || s.promptPat.MatchString(trimmed)
				idleN := max(10, 5000/int(s.pollInt.Milliseconds()))
				if promptOK || stable >= idleN {
					// Guard against the race where Send() cancelled this poll
					// just as we were about to emit — avoids duplicate responses.
					select {
					case <-ctx.Done():
						return
					default:
					}
					if !promptOK {
						lastLine := trimmed
						if nl := strings.LastIndex(trimmed, "\n"); nl >= 0 {
							lastLine = trimmed[nl+1:]
						}
						slog.Info("tmux: idle-done (prompt pattern did not match)", "target", s.target, "last_line", lastLine)
					}
					response := s.extractResponse()
					s.safeSend(core.Event{Type: core.EventResult, Content: response, Done: true})
					return
				}
			}
		}
	}
}

func (s *tmuxSession) safeSend(ev core.Event) {
	defer func() { _ = recover() }() // channel may be closed on session teardown
	select {
	case s.events <- ev:
	case <-s.ctx.Done():
	}
}

func (s *tmuxSession) RespondPermission(_ string, _ core.PermissionResult) error {
	return fmt.Errorf("tmux: permission requests are not supported")
}

func (s *tmuxSession) Events() <-chan core.Event { return s.events }

func (s *tmuxSession) CurrentSessionID() string { return s.sessionID }

func (s *tmuxSession) Alive() bool { return s.alive.Load() }

func (s *tmuxSession) Close() error {
	s.closeOnce.Do(func() {
		s.alive.Store(false)
		s.mu.Lock()
		if s.pollCancel != nil {
			s.pollCancel()
			s.pollCancel = nil
		}
		s.mu.Unlock()
		s.cancel()
		close(s.events)
	})
	return nil
}

// ── tmux helpers ──────────────────────────────────────────────────────────────

func tmuxSessionExists(name string) bool {
	return exec.Command("tmux", "has-session", "-t", name).Run() == nil
}

// tmuxWindowExists checks whether a window or pane target (e.g. "sess:win") exists.
func tmuxWindowExists(target string) bool {
	return exec.Command("tmux", "has-session", "-t", target).Run() == nil
}

// createTmuxSession creates a new detached tmux session with the given window name.
func createTmuxSession(name, windowName, workDir, shell string) error {
	args := []string{"new-session", "-d", "-s", name, "-n", windowName}
	if workDir != "" && workDir != "." {
		args = append(args, "-c", workDir)
	}
	if shell != "" {
		args = append(args, shell)
	}
	out, err := exec.Command("tmux", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	// Enable focus events so Claude Code doesn't warn about them being off.
	_ = exec.Command("tmux", "set-option", "-t", name, "-g", "focus-events", "on").Run()
	return nil
}

// createTmuxWindow adds a new window to an existing session.
// Using "session:" (trailing colon) tells tmux to pick the next free index,
// avoiding index collisions when multiple windows are created concurrently.
func createTmuxWindow(session, windowName, workDir string) error {
	args := []string{"new-window", "-d", "-t", session + ":", "-n", windowName}
	if workDir != "" && workDir != "." {
		args = append(args, "-c", workDir)
	}
	out, err := exec.Command("tmux", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func capturePane(target string) (string, error) {
	out, err := exec.Command("tmux", "capture-pane", "-t", target, "-p").Output()
	if err != nil {
		return "", err
	}
	return normalizeCapture(string(out)), nil
}

func sendKeys(target, keys string) error {
	out, err := exec.Command("tmux", "send-keys", "-t", target, keys, "Enter").CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// extractResponse reads the current scrollback buffer and returns only the lines
// that appeared after the baseline captured in Send(). This is immune to terminal
// scrolling because the scrollback buffer never drops lines.
func (s *tmuxSession) extractResponse() string {
	scrollback, err := captureScrollback(s.target)
	if err != nil {
		slog.Warn("tmux: captureScrollback failed", "err", err)
		current, _ := capturePane(s.target)
		return s.cleanTUIContent(current)
	}

	s.mu.Lock()
	baselineLen := s.baselineLen
	s.mu.Unlock()

	lines := strings.Split(scrollback, "\n")
	if baselineLen < len(lines) {
		newLines := lines[baselineLen:]
		response := strings.TrimRight(strings.Join(newLines, "\n"), "\n")
		response = s.cleanTUIContent(response)
		if response != "" {
			response = "```\n" + response + "\n```"
		}
		return response
	}
	// Baseline is at or beyond current scrollback — nothing new yet.
	return ""
}

// captureScrollback captures up to 1000 lines of scrollback history plus the
// visible pane, giving a stable view that does not lose content to scrolling.
func captureScrollback(target string) (string, error) {
	out, err := exec.Command("tmux", "capture-pane", "-t", target, "-p", "-S", "-1000").Output()
	if err != nil {
		return "", err
	}
	return normalizeCapture(string(out)), nil
}

// shellQuote wraps a path in single quotes and escapes any embedded single quotes.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

// cleanTUIContent removes Claude Code TUI frame lines from captured output:
//   - horizontal separator lines made of ─ (U+2500)
//   - bare prompt lines (❯, >, $, #, %)
// tuiInputBlockRe matches Claude Code's 3-line input area:
//   ────────────────   (U+2500 separator line)
//   ❯ …               (U+276F prompt, any trailing chars)
//   ────────────────
// Uses explicit Unicode codepoints and [^\n]* to be immune to invisible
// trailing characters on the prompt line.
var tuiInputBlockRe = regexp.MustCompile("(?m)^─+\n❯[^\n]*\n─+")

func (s *tmuxSession) cleanTUIContent(text string) string {
	if s.stripInputBlock {
		text = tuiInputBlockRe.ReplaceAllString(text, "")
	}
	if len(s.stripPatterns) == 0 {
		return strings.TrimRight(text, "\n")
	}
	lines := strings.Split(text, "\n")
	out := lines[:0]
	for _, line := range lines {
		drop := false
		for _, re := range s.stripPatterns {
			if re.MatchString(line) {
				drop = true
				break
			}
		}
		if !drop {
			out = append(out, line)
		}
	}
	return strings.TrimRight(strings.Join(out, "\n"), "\n")
}

// normalizeCapture trims trailing whitespace per line and strips ANSI codes.
func normalizeCapture(raw string) string {
	raw = ansiRe.ReplaceAllString(raw, "")
	lines := strings.Split(raw, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " \t\r")
	}
	return strings.TrimRight(strings.Join(lines, "\n"), "\n")
}

// ansiRe matches common ANSI/VT escape sequences.
// OSC must come first so "\x1b]" is consumed fully (not as a generic two-char sequence).
var ansiRe = regexp.MustCompile(
	`\x1b\][^\x07\x1b]*\x07` + // OSC: ESC ] ... BEL
		`|\x1b\[[0-9;]*[a-zA-Z]` + // CSI: ESC [ params letter
		`|\x1b.`, // Other two-char escape sequences
)

// extractNew returns the response text that appeared in current after the baseline.
// It handles three cases:
//  1. Linear shell output — current is baseline + new lines (HasPrefix fast path).
//  2. TUI redraws (e.g. Claude Code) — terminal overwrites lines in place; find the
//     longest common line prefix shared by both snapshots, then return the new lines
//     that follow it in current, stripping the repeated trailing prompt lines.
//  3. Terminal scrolled — baseline has partially scrolled off; use a shrinking anchor.
func extractNew(baseline, current string) string {
	if current == baseline {
		return ""
	}
	if baseline == "" {
		return current
	}

	// Fast path: linear output, content only grew.
	if strings.HasPrefix(current, baseline) {
		return strings.TrimLeft(current[len(baseline):], "\n")
	}

	baseLines := strings.Split(baseline, "\n")
	curLines := strings.Split(current, "\n")

	// TUI path: find how many leading lines the two snapshots share (the static
	// frame/header), then return the new lines that follow in current.
	commonLen := 0
	for i := 0; i < len(baseLines) && i < len(curLines); i++ {
		if baseLines[i] != curLines[i] {
			break
		}
		commonLen = i + 1
	}
	if commonLen > 0 && commonLen < len(curLines) {
		newLines := curLines[commonLen:]
		// Strip trailing lines that duplicate the baseline's suffix (e.g. the prompt ">").
		bl := baseLines
		for len(newLines) > 0 && len(bl) > 0 && newLines[len(newLines)-1] == bl[len(bl)-1] {
			newLines = newLines[:len(newLines)-1]
			bl = bl[:len(bl)-1]
		}
		result := strings.TrimRight(strings.Join(newLines, "\n"), "\n")
		if result != "" {
			return result
		}
	}

	// Scroll path: baseline has partially scrolled off the top; try progressively
	// shorter anchors from the end of baseline to find where new content begins.
	maxAnchor := 5
	if len(baseLines) < maxAnchor {
		maxAnchor = len(baseLines)
	}
	for n := maxAnchor; n >= 1; n-- {
		anchor := strings.Join(baseLines[len(baseLines)-n:], "\n")
		if idx := strings.Index(current, anchor); idx >= 0 {
			rest := strings.TrimLeft(current[idx+len(anchor):], "\n")
			if rest != "" {
				return rest
			}
		}
	}

	return current
}
