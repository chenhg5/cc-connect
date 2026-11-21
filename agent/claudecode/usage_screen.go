package claudecode

import "strings"

// newClaudeUsageScreenTerminal returns a terminal that emulates a fixed
// rows x cols screen: text auto-wraps at the right margin (with the usual
// deferred wrap), a line feed on the bottom row scrolls the screen up, cursor
// moves are clamped to the screen, and ECH (CSI n X) erases characters.
//
// The default terminal from newClaudeUsageTerminal grows without bound, which
// works for output that is written as a stream. Windows ConPTY sends a
// re-encoded copy of its own screen buffer instead. That output assumes the
// real screen size: a full-width line is followed directly by the next line's
// text, and absolute cursor positions count from the top of the visible
// screen after it has scrolled.
func newClaudeUsageScreenTerminal(rows, cols int) *claudeUsageTerminal {
	t := newClaudeUsageTerminal()
	if rows > 0 && cols > 0 {
		t.height = min(rows, maxTerminalRows)
		t.width = min(cols, maxTerminalCols)
	}
	return t
}

// lineFeed moves the cursor down one row. On a fixed-size screen a line feed
// on the bottom row scrolls the screen up by one row instead.
func (t *claudeUsageTerminal) lineFeed() {
	t.wrapPending = false
	if t.height > 0 && t.row >= t.height-1 {
		t.ensureRow(t.height - 1)
		t.lines = append(t.lines[1:], nil)
		t.row = t.height - 1
		return
	}
	t.row++
	t.ensureRow(t.row)
}

// writeScreenRune writes r on a fixed-size screen. Writing in the last column
// leaves the cursor there and marks a pending wrap. The next printable rune
// then starts on the following row, as on a VT100-compatible terminal.
func (t *claudeUsageTerminal) writeScreenRune(r rune) {
	if t.wrapPending {
		t.wrapPending = false
		t.col = 0
		t.lineFeed()
	}
	t.ensureCell(t.row, t.col)
	if t.row < len(t.lines) && t.col < len(t.lines[t.row]) {
		t.lines[t.row][t.col] = r
	}
	if t.col >= t.width-1 {
		t.wrapPending = true
		return
	}
	t.col++
}

// eraseScreenChars implements ECH (CSI n X) on a fixed-size screen: it blanks
// n characters from the cursor without moving it.
func (t *claudeUsageTerminal) eraseScreenChars(n int) {
	if t.width <= 0 {
		return
	}
	t.ensureRow(t.row)
	if t.row >= len(t.lines) {
		return
	}
	line := t.lines[t.row]
	for c := t.col; c < t.col+n && c < len(line); c++ {
		line[c] = ' '
	}
}

// clampCursorToScreen keeps the cursor inside a fixed-size screen.
func (t *claudeUsageTerminal) clampCursorToScreen() {
	if t.width <= 0 {
		return
	}
	if t.row >= t.height {
		t.row = t.height - 1
	}
	if t.col >= t.width {
		t.col = t.width - 1
	}
	t.ensureRow(t.row)
}

// csiCancelsPendingWrap reports whether a CSI sequence moves the cursor or
// edits the line, which cancels a pending wrap. SGR (colors) and mode changes
// do not, so a colored character after a full-width line still wraps.
func csiCancelsPendingWrap(final byte) bool {
	return strings.IndexByte("ABCDGHfJKX", final) >= 0
}
