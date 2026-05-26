package core

// Auto session naming.
//
// When cc-connect spawns an agent CLI (e.g. cursor-agent) without an IDE
// renderer, the agent's persisted meta.name is typically empty, so /list
// falls back to a session-id hash prefix. This file fills the gap by
// deriving a short title from the first user message of each session and
// recording it via SessionManager.SetSessionName, so /list shows the
// human-readable title with the existing pinned-name (📌) rendering.
//
// The hook is called from the engine's turn-complete path (see
// processInteractiveEvents). It is idempotent: re-running on later turns
// is a no-op once a name (auto- or user-assigned) exists.

import (
	"log/slog"
	"regexp"
	"strings"
)

// Cleanup regexes used by deriveAutoTitle. Compiled once at package init.
//
// Order of application matters because each pass operates on the output of
// the previous one. The fenced-code regex consumes triple-backtick blocks
// before inline-code spans run, so a stray backtick inside a fenced block
// cannot leak out and confuse the inline pass.
var (
	// Triple-backtick fenced code blocks (greedy across newlines).
	autoTitleReFencedCode = regexp.MustCompile("(?s)```.*?```")
	// Inline code spans, e.g. `like this`.
	autoTitleReInlineCode = regexp.MustCompile("`[^`]+`")
	// http/https URLs.
	autoTitleReURL = regexp.MustCompile(`https?://\S+`)
	// @file or @path/to/file references (cursor-agent prompt convention).
	autoTitleReAtRef = regexp.MustCompile(`@\S+`)
	// Markdown emphasis markers (* / _, 1-3 of them).
	autoTitleReMdEmph = regexp.MustCompile(`(\*{1,3}|_{1,3})`)
	// Whitespace runs (collapsed to single space).
	autoTitleReWS = regexp.MustCompile(`\s+`)
)

// autoTitleSentenceTerminators are characters that terminate the first
// sentence used as the title. Includes both Chinese (。！？) and Western
// (. ! ?) punctuation plus a literal newline so a title-on-its-own-line
// wins over the body that follows.
//
// Trade-off: aggressive English period handling means abbreviations
// ("e.g.", "v1.2.3") will be truncated at the first dot. We accept this
// because (a) titles are short by design, (b) the truncated prefix is
// still human-readable, and (c) the user can always /name override.
const autoTitleSentenceTerminators = ".。！？!?\n"

// autoTitleDefaultMaxRunes is the fallback rune cap when callers pass
// maxRunes <= 0. Chosen to fit short Chinese titles (~14 chars) and
// roughly half a tweet of English (~28 chars) without truncating.
const autoTitleDefaultMaxRunes = 28

// deriveAutoTitle produces a short, human-readable session title from
// the first user message of a conversation. It returns an empty string
// when no usable title can be extracted, in which case callers must
// skip naming and leave the agent's existing Summary fallback in place.
//
// Pipeline:
//  1. Strip noise: fenced code blocks, inline code, URLs, @-references,
//     markdown emphasis markers.
//  2. Take the first sentence (split on .。！？!?\n). This MUST run
//     before whitespace collapsing — otherwise newline terminators are
//     turned into spaces and a multi-line prompt collapses into one
//     long string.
//  3. Collapse remaining whitespace.
//  4. Truncate to maxRunes (rune-safe; never splits a multi-byte CJK char).
//  5. Append a single ellipsis "…" rune when truncation occurred.
func deriveAutoTitle(firstUserMsg string, maxRunes int) string {
	if maxRunes <= 0 {
		maxRunes = autoTitleDefaultMaxRunes
	}
	s := firstUserMsg

	// 1. Strip noise. Replace with a single space rather than empty
	// string so that "看 `code` 文件" becomes "看  文件" (then collapsed)
	// instead of "看code文件".
	s = autoTitleReFencedCode.ReplaceAllString(s, " ")
	s = autoTitleReInlineCode.ReplaceAllString(s, " ")
	s = autoTitleReURL.ReplaceAllString(s, " ")
	s = autoTitleReAtRef.ReplaceAllString(s, " ")
	s = autoTitleReMdEmph.ReplaceAllString(s, "")

	// 2. First sentence (BEFORE whitespace collapse — see doc comment).
	// IndexAny operates on bytes but matches valid UTF-8 multi-byte
	// runes (e.g. 。 → 0xE3 0x80 0x82) correctly because each
	// terminator is a complete UTF-8 sequence. ASCII terminators are
	// single-byte and unambiguous.
	if idx := strings.IndexAny(s, autoTitleSentenceTerminators); idx > 0 {
		s = s[:idx]
	}

	// 3. Collapse whitespace.
	s = autoTitleReWS.ReplaceAllString(s, " ")
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}

	// 4. Rune-safe truncation. Convert to []rune so a CJK character is
	// counted as 1 rune (not 3 bytes) and never gets split mid-byte.
	runes := []rune(s)
	if len(runes) > maxRunes {
		s = string(runes[:maxRunes]) + "…"
	}

	return s
}

// maybeAutoNameSession assigns an auto-generated display name to the
// agent session associated with `session` if all of the following hold:
//
//  1. e.autoSessionName is set to a non-"off" mode
//  2. The session has a bound agent session ID (i.e. an agent has accepted
//     it; sessions are sometimes constructed before the agent attaches)
//  3. The agent session is not yet named (neither auto- nor user-named —
//     a manual /name from the user is therefore preserved)
//  4. deriveAutoTitle on the first user message returns a non-empty title
//
// Idempotent: safe to call after every turn-complete event. The third
// guard ensures we write at most once per session, even though the
// hook fires every turn.
//
// Reads session.GetHistory(0) to find the first user message; this avoids
// any signature change in upstream call sites (the engine's
// processInteractiveEvents does not have direct access to msg.Content).
//
// Concurrency: SessionManager.GetSessionName / SetSessionName already use
// sync.RWMutex; Session.GetHistory copies under sync.Mutex; e.autoSession*
// fields are write-once at startup. No additional locking is required.
func (e *Engine) maybeAutoNameSession(session *Session) {
	if e == nil || session == nil {
		return
	}
	if e.autoSessionName == "" || e.autoSessionName == "off" {
		return
	}
	agentSID := session.GetAgentSessionID()
	if agentSID == "" {
		return
	}
	if e.sessions.GetSessionName(agentSID) != "" {
		// Already named (user /name or earlier auto-name). Don't overwrite.
		return
	}

	// Find the first user message in this session's history.
	history := session.GetHistory(0)
	var firstUserMsg string
	for _, entry := range history {
		if entry.Role == "user" {
			firstUserMsg = entry.Content
			break
		}
	}
	if firstUserMsg == "" {
		return
	}

	title := deriveAutoTitle(firstUserMsg, e.autoSessionNameMaxLen)
	if title == "" {
		return
	}

	e.sessions.SetSessionName(agentSID, title)
	slog.Info("auto-named session",
		"agent_sid", agentSID,
		"session", session.ID,
		"title", title,
	)
}
