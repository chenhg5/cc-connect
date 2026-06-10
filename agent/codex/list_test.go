package codex

import (
	"os"
	"path/filepath"
	"testing"
)

// TestIsUserPrompt_DefaultInternalPrefixes verifies that the built-in
// defaults catch the internal/system prompt templates reported in #1271.
func TestIsUserPrompt_DefaultInternalPrefixes(t *testing.T) {
	defaults := defaultInternalSummaryPrefixes

	cases := []string{
		"You are the final diagnosis layer for an internal game issue diagnosis system",
		"You are the decision layer for an internal game issue diagnosis system",
		"You are selecting one best skill for issue diagnosis",
		"You are a senior game backend engineer diagnosing login exceptions",
		"The following is the Codex agent history whose request action you are assessing",
		// Match should be prefix-anchored and case-sensitive: leading whitespace trimmed.
		"   You are the final diagnosis layer for the project",
	}
	for _, c := range cases {
		if isUserPrompt(c, defaults) {
			t.Errorf("isUserPrompt(%q) = true, want false (default internal prefix should reject)", c)
		}
	}
}

// TestIsUserPrompt_RejectsAGENTSAndXML verifies the original filters still work.
func TestIsUserPrompt_RejectsAGENTSAndXML(t *testing.T) {
	cases := []string{
		"<environment_context>...</environment_context>",
		"# AGENTS.md instructions",
		"#AGENTS.md",
		"   <permissions>...</permissions>",
	}
	for _, c := range cases {
		if isUserPrompt(c, nil) {
			t.Errorf("isUserPrompt(%q) = true, want false (system context should be rejected)", c)
		}
	}
}

// TestIsUserPrompt_AcceptsRealUserPrompts verifies real user prompts are not
// filtered out by default or custom prefixes.
func TestIsUserPrompt_AcceptsRealUserPrompts(t *testing.T) {
	real := []string{
		"fix the login bug in user service",
		"add a /list command to the bot",
		"please refactor the session manager",
		"You are a friend",            // not in default list (no "the final diagnosis layer")
		"You are the new project setup", // ambiguous, not on default list
		"",                              // empty is rejected but treated as not-a-user-prompt
	}
	defaults := defaultInternalSummaryPrefixes
	for _, r := range real {
		if r == "" {
			if isUserPrompt(r, defaults) {
				t.Errorf("isUserPrompt(\"\") = true, want false")
			}
			continue
		}
		if !isUserPrompt(r, defaults) {
			t.Errorf("isUserPrompt(%q) = false, want true (real prompt should pass default filter)", r)
		}
	}
}

// TestIsUserPrompt_RespectsUserConfiguredPrefixes verifies user-configured
// prefixes are honored in addition to the built-in defaults.
func TestIsUserPrompt_RespectsUserConfiguredPrefixes(t *testing.T) {
	userPrefixes := []string{
		"[internal-automation]",
		"Bot: scheduled task report",
	}

	cases := []string{
		"[internal-automation] running skill selector",
		"Bot: scheduled task report for project A",
		"  [internal-automation] indented match",
	}
	for _, c := range cases {
		if isUserPrompt(c, userPrefixes) {
			t.Errorf("isUserPrompt(%q) = true, want false (user prefix should reject)", c)
		}
	}

	// Real prompts should still pass.
	real := []string{
		"please ship the release",
		"review my PR",
		// Should still be caught by the default prefix even when user prefixes
		// are also set.
		"You are the final diagnosis layer for X",
	}
	for _, r := range real {
		if !isUserPrompt(r, userPrefixes) {
			t.Errorf("isUserPrompt(%q) = false, want true (real prompt should pass)", r)
		}
	}
}

// TestIsUserPrompt_EmptyAndWhitespace verifies edge cases around empty input
// and prefix entries.
func TestIsUserPrompt_EmptyAndWhitespace(t *testing.T) {
	if isUserPrompt("", nil) {
		t.Error("isUserPrompt(\"\") = true, want false")
	}
	if isUserPrompt("   \n\t  ", nil) {
		t.Error("isUserPrompt(whitespace) = true, want false")
	}
	// Empty prefix in the list should be skipped, not treated as a wildcard.
	if !isUserPrompt("any text", []string{""}) {
		t.Error("isUserPrompt with empty-only prefix list rejected a real prompt")
	}
}

// TestEffectiveIgnorePrefixes_MergesDefaultsAndUser verifies the merge order.
func TestEffectiveIgnorePrefixes_MergesDefaultsAndUser(t *testing.T) {
	got := effectiveIgnorePrefixes([]string{"[custom]"})
	if len(got) < len(defaultInternalSummaryPrefixes)+1 {
		t.Fatalf("effectiveIgnorePrefixes = %v (len %d), want at least %d entries",
			got, len(got), len(defaultInternalSummaryPrefixes)+1)
	}
	// User prefix must be at the end.
	last := got[len(got)-1]
	if last != "[custom]" {
		t.Errorf("last prefix = %q, want %q (user prefix should be appended)", last, "[custom]")
	}
	// First few must be the defaults.
	for i, want := range defaultInternalSummaryPrefixes {
		if got[i] != want {
			t.Errorf("prefix[%d] = %q, want %q (defaults should be first)", i, got[i], want)
		}
	}
}

// TestIsInternalSummary_DefaultsAndCustom verifies the hidden-session
// classification used by parseCodexSessionFile.
func TestIsInternalSummary_DefaultsAndCustom(t *testing.T) {
	// Empty summary is never internal.
	if isInternalSummary("", defaultInternalSummaryPrefixes) {
		t.Error("isInternalSummary(\"\") = true, want false")
	}
	// Default prefix matches.
	if !isInternalSummary("You are the decision layer for X", defaultInternalSummaryPrefixes) {
		t.Error("isInternalSummary(default match) = false, want true")
	}
	// Non-default prefix does not match.
	if isInternalSummary("Please review the PR", defaultInternalSummaryPrefixes) {
		t.Error("isInternalSummary(real prompt) = true, want false")
	}
	// User prefix + defaults work together.
	if !isInternalSummary("[custom-tag] do something", []string{"[custom-tag]"}) {
		t.Error("isInternalSummary(user prefix match) = false, want true")
	}
}

// TestParseCodexSessionFile_FiltersInternalSessions builds a synthetic
// JSONL transcript for an internal-prompt session and verifies the parser
// returns nil (filtered out).
func TestParseCodexSessionFile_FiltersInternalSessions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rollout-internal.jsonl")
	lines := []string{
		`{"type":"session_meta","timestamp":"2026-06-09T00:00:00Z","payload":{"id":"internal-1","cwd":"/tmp/x"}}`,
		`{"type":"response_item","timestamp":"2026-06-09T00:00:01Z","payload":{"role":"user","content":[{"type":"input_text","text":"You are the decision layer for an internal game issue diagnosis system. Decide which skill to use."}]}}`,
	}
	if err := os.WriteFile(path, []byte(lines[0]+"\n"+lines[1]+"\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	got := parseCodexSessionFile(path, "/tmp/x", nil)
	if got != nil {
		t.Errorf("parseCodexSessionFile(internal) = %+v, want nil (default prefixes should hide it)", got)
	}
}

// TestParseCodexSessionFile_KeepsRealUserSession verifies a real user prompt
// is not hidden even when the file also contains the AGENTS.md block.
func TestParseCodexSessionFile_KeepsRealUserSession(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rollout-real.jsonl")
	lines := []string{
		`{"type":"session_meta","timestamp":"2026-06-09T00:00:00Z","payload":{"id":"real-1","cwd":"/tmp/x"}}`,
		`{"type":"response_item","timestamp":"2026-06-09T00:00:01Z","payload":{"role":"user","content":[{"type":"input_text","text":"# AGENTS.md\nfollow these rules..."}]}}`,
		`{"type":"response_item","timestamp":"2026-06-09T00:00:02Z","payload":{"role":"user","content":[{"type":"input_text","text":"fix the login bug in user service"}]}}`,
	}
	body := lines[0] + "\n" + lines[1] + "\n" + lines[2] + "\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	got := parseCodexSessionFile(path, "/tmp/x", nil)
	if got == nil {
		t.Fatal("parseCodexSessionFile(real) = nil, want non-nil (real prompt should pass)")
	}
	if got.ID != "real-1" {
		t.Errorf("ID = %q, want real-1", got.ID)
	}
	// Summary should be the last real user prompt (not the AGENTS.md block).
	if got.Summary != "fix the login bug in user service" {
		t.Errorf("Summary = %q, want %q", got.Summary, "fix the login bug in user service")
	}
	if got.MessageCount != 2 {
		t.Errorf("MessageCount = %d, want 2 (AGENTS.md is filtered, real user msg counts)", got.MessageCount)
	}
}

// TestParseCodexSessionFile_CustomPrefixHidesInternal verifies a user
// config prefix hides an otherwise-valid session.
func TestParseCodexSessionFile_CustomPrefixHidesInternal(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rollout-bot.jsonl")
	lines := []string{
		`{"type":"session_meta","timestamp":"2026-06-09T00:00:00Z","payload":{"id":"bot-1","cwd":"/tmp/x"}}`,
		`{"type":"response_item","timestamp":"2026-06-09T00:00:01Z","payload":{"role":"user","content":[{"type":"input_text","text":"[bot-scheduler] run nightly cleanup"}]}}`,
	}
	if err := os.WriteFile(path, []byte(lines[0]+"\n"+lines[1]+"\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Without the custom prefix, the session is visible.
	if parseCodexSessionFile(path, "/tmp/x", nil) == nil {
		t.Fatal("without user prefix, session should be visible")
	}
	// With the custom prefix, it's hidden.
	if parseCodexSessionFile(path, "/tmp/x", []string{"[bot-scheduler]"}) != nil {
		t.Error("with user prefix [bot-scheduler], session should be hidden")
	}
}

// TestParseCodexSessionFile_CwdMismatchReturnsNil verifies the existing cwd
// filter is unchanged.
func TestParseCodexSessionFile_CwdMismatchReturnsNil(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rollout-other.jsonl")
	lines := []string{
		`{"type":"session_meta","timestamp":"2026-06-09T00:00:00Z","payload":{"id":"x-1","cwd":"/tmp/other"}}`,
		`{"type":"response_item","timestamp":"2026-06-09T00:00:01Z","payload":{"role":"user","content":[{"type":"input_text","text":"hi"}]}}`,
	}
	if err := os.WriteFile(path, []byte(lines[0]+"\n"+lines[1]+"\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	if got := parseCodexSessionFile(path, "/tmp/x", nil); got != nil {
		t.Errorf("parseCodexSessionFile(cwd mismatch) = %+v, want nil", got)
	}
}

// TestGetSessionHistory_AppliesIgnorePrefixes verifies getSessionHistory
// filters individual history entries by the same prefix list.
func TestGetSessionHistory_AppliesIgnorePrefixes(t *testing.T) {
	dir := t.TempDir()
	codexHome := dir
	sessDir := filepath.Join(codexHome, "sessions", "2026", "06", "09")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	rollout := sessDir + "/rollout-2026-06-09T00-00-00-hist-1.jsonl"
	lines := []string{
		`{"type":"session_meta","timestamp":"2026-06-09T00:00:00Z","payload":{"id":"hist-1","cwd":"/tmp/x"}}`,
		`{"type":"response_item","timestamp":"2026-06-09T00:00:01Z","payload":{"role":"user","content":[{"type":"input_text","text":"# AGENTS.md\n..."}]}}`,
		`{"type":"response_item","timestamp":"2026-06-09T00:00:02Z","payload":{"role":"user","content":[{"type":"input_text","text":"You are the final diagnosis layer for X"}]}}`,
		`{"type":"response_item","timestamp":"2026-06-09T00:00:03Z","payload":{"role":"user","content":[{"type":"input_text","text":"please refactor module A"}]}}`,
		`{"type":"response_item","timestamp":"2026-06-09T00:00:04Z","payload":{"role":"assistant","content":[{"type":"output_text","text":"sure"}]}}`,
	}
	body := ""
	for _, l := range lines {
		body += l + "\n"
	}
	if err := os.WriteFile(rollout, []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	// With default prefixes: AGENTS.md + internal "You are..." should be
	// hidden, "please refactor" and assistant reply should be visible.
	got, err := getSessionHistory("hist-1", codexHome, 0, defaultInternalSummaryPrefixes)
	if err != nil {
		t.Fatalf("getSessionHistory: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("history len = %d, want 2 (AGENTS.md + internal hidden); got=%+v", len(got), got)
	}
	if got[0].Content != "please refactor module A" {
		t.Errorf("entry[0] = %q, want %q", got[0].Content, "please refactor module A")
	}
	if got[0].Role != "user" {
		t.Errorf("entry[0].Role = %q, want user", got[0].Role)
	}
	if got[1].Role != "assistant" {
		t.Errorf("entry[1].Role = %q, want assistant", got[1].Role)
	}
}
