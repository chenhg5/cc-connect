package core

import (
	"bufio"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	rehydrationLetterRe = regexp.MustCompile(`(?i)\bL-?(\d{4,})\b`)
	leadingZeroRe       = regexp.MustCompile(`\b0\d{3,}\b`)
	keywordLetterRe     = regexp.MustCompile(`(?i)\b(?:process|letter|id|thread|query|result)\s+(\d{4,})\b`)
	standaloneDigitsRe  = regexp.MustCompile(`^\s*(\d{4,})\s*$`)
)

// ── RehydrationConfig ────────────────────────────────────────────

// RehydrationBudget keeps the spawn-time digest bounded. Token counts are
// rough estimates; trimming is deliberately conservative and deterministic.
type RehydrationBudget struct {
	Name               string
	MaxTokens          int
	IndexTailLines     int
	ParentChainDepth   int
	OpenSummaryEntries int
}

// RehydrationConfig controls the depth and scope of a spawn-time
// Rehydration Digest (L-0251).
type RehydrationConfig struct {
	// ArchiveDir is the root of the letter archive, e.g. F:\nexus\docs\archive.
	// Derived from the engine's dataDir when empty.
	ArchiveDir string

	// IndexTailLines controls how many lines from INDEX.md to include.
	// 0 or negative = budget default.
	IndexTailLines int

	// ParentChainDepth controls how many ancestor letter digests to include.
	// 0 or negative = budget default.
	ParentChainDepth int

	// OpenSummaryEntries controls how many open/STUCK/BLOCKED rows to include.
	// 0 or negative = budget default.
	OpenSummaryEntries int

	// MaxTokens is the rough upper bound for the entire Markdown digest.
	// 0 or negative = budget default.
	MaxTokens int

	// ActiveLetterID optionally specifies the letter this spawn was
	// triggered from (e.g. "L-0251"). When set, the digest includes the
	// letter's context + parent chain up to ParentChainDepth.
	ActiveLetterID string
}

// RehydrationBudgetForPersonaClass returns the default digest budget for a
// seat class. This is the L-0251 OQ1 policy:
// write-class seats get enough archive context to resume implementation;
// secretary gets broad coordination context; read-only/flash specialists get
// a smaller digest that still includes current letter + recent blockers.
func RehydrationBudgetForPersonaClass(class PersonaClass) RehydrationBudget {
	switch class {
	case PersonaClassWrite:
		return RehydrationBudget{
			Name:               "write-heavy",
			MaxTokens:          6000,
			IndexTailLines:     80,
			ParentChainDepth:   2,
			OpenSummaryEntries: 30,
		}
	case PersonaClassSecretary:
		return RehydrationBudget{
			Name:               "secretary-coordination",
			MaxTokens:          5000,
			IndexTailLines:     80,
			ParentChainDepth:   2,
			OpenSummaryEntries: 40,
		}
	default:
		return RehydrationBudget{
			Name:               "read-flash",
			MaxTokens:          3000,
			IndexTailLines:     40,
			ParentChainDepth:   1,
			OpenSummaryEntries: 20,
		}
	}
}

func (c RehydrationConfig) fillDefaults() RehydrationConfig {
	out := c
	budget := RehydrationBudgetForPersonaClass(PersonaClassRead)
	if out.IndexTailLines <= 0 {
		out.IndexTailLines = budget.IndexTailLines
	}
	if out.ParentChainDepth <= 0 {
		out.ParentChainDepth = budget.ParentChainDepth
	}
	if out.OpenSummaryEntries <= 0 {
		out.OpenSummaryEntries = budget.OpenSummaryEntries
	}
	if out.MaxTokens <= 0 {
		out.MaxTokens = budget.MaxTokens
	}
	out.ActiveLetterID = normalizeLetterID(out.ActiveLetterID)
	return out
}

// ── DeriveArchiveDir ──────────────────────────────────────────────

// DeriveArchiveDir returns the expected letter-archive directory derived
// from the engine's dataDir. Convention:
//
//	dataDir = F:\nexus\data  →  archive = F:\nexus\docs\archive
//	dataDir = /opt/nexus/data → archive = /opt/nexus/docs/archive
//
// Both / and \ are treated as separators so a Windows-style dataDir string
// still derives correctly if the process later runs on Linux (migration/CI).
// Returns "" when dataDir is empty.
func DeriveArchiveDir(dataDir string) string {
	if dataDir == "" {
		return ""
	}
	// filepath.Dir only splits on the host OS separator. Normalize first so
	// "F:\nexus\data" does not collapse to "." on Linux.
	normalized := filepath.FromSlash(strings.ReplaceAll(dataDir, `\`, "/"))
	nexusDir := filepath.Dir(normalized)
	return filepath.Join(nexusDir, "docs", "archive")
}

// ── BuildRehydrationDigest ────────────────────────────────────────

// BuildRehydrationDigest constructs a frozen Markdown digest from the
// letter archive. It is a "spawn-time snapshot": the agent receives
// exactly what the archive contained at the moment of spawn.
//
// Returns "" when the archive is unreachable or empty, so callers can
// safely skip injection without special-casing.
func BuildRehydrationDigest(cfg RehydrationConfig) string {
	cfg = cfg.fillDefaults()

	if cfg.ArchiveDir == "" {
		return ""
	}
	indexPath := filepath.Join(cfg.ArchiveDir, "INDEX.md")

	var b strings.Builder
	b.WriteString("## Rehydration Digest (spawn-time snapshot)\n\n")
	b.WriteString(fmt.Sprintf(
		"_Budget: %d rough tokens; INDEX tail: %d lines; parent depth: %d; open/STUCK/BLOCKED rows: %d._\n\n",
		cfg.MaxTokens, cfg.IndexTailLines, cfg.ParentChainDepth, cfg.OpenSummaryEntries))
	b.WriteString("This digest is frozen at process spawn. Before writing a RESULT or acting on a latest/follow-up request, reread `F:\\nexus\\docs\\archive\\INDEX.md` tail to catch newer letters.\n\n")

	// 1. INDEX tail — recent fleet activity.
	indexTail := readTail(indexPath, cfg.IndexTailLines)
	if indexTail == "" {
		slog.Debug("rehydration: INDEX.md is empty or unreadable",
			"path", indexPath)
		return ""
	}
	b.WriteString("### 当前舰队状态（INDEX 尾部——最近 " +
		fmt.Sprintf("%d 行）\n", cfg.IndexTailLines))
	b.WriteString("```\n")
	b.WriteString(indexTail)
	b.WriteString("\n```\n\n")

	// 2. Open / STUCK / BLOCKED thread summary.
	openRows := extractOpenStuckBlocked(indexTail, cfg.OpenSummaryEntries)
	if len(openRows) > 0 {
		b.WriteString("### 开放/阻塞线程（open / STUCK / BLOCKED）\n")
		for _, s := range openRows {
			b.WriteString("- " + s + "\n")
		}
		b.WriteString("\n")
	}

	// 3. Active-letter context (if known).
	if cfg.ActiveLetterID != "" {
		context := buildLetterContext(cfg.ArchiveDir, cfg.ActiveLetterID,
			indexTail, cfg.ParentChainDepth)
		if context != "" {
			b.WriteString(context)
		}
	}

	digest := strings.TrimSpace(b.String())
	digest = trimDigestToBudget(digest, cfg.MaxTokens)
	slog.Debug("rehydration: built digest",
		"len", len(digest),
		"tokens_est", EstimateTokenCount(digest),
		"letter", cfg.ActiveLetterID,
	)
	return digest
}

// ── Internal helpers ─────────────────────────────────────────────

// readTail returns the last n lines of the file at path. Returns "" if
// the file cannot be read or is empty.
func readTail(path string, n int) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	lines := make([]string, 0, n)
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 256*1024), 512*1024)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
		if len(lines) > n {
			lines = lines[1:]
		}
	}
	if err := scanner.Err(); err != nil {
		slog.Warn("rehydration: scanner error reading INDEX tail",
			"path", path, "error", err)
	}
	if len(lines) == 0 {
		return ""
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// extractStuckBlocked scans the INDEX tail text for lines containing
// "STUCK" or "BLOCKED" markers (case-sensitive per convention). Returns
// matching lines trimmed.
func extractStuckBlocked(indexTail string) []string {
	var out []string
	for _, line := range strings.Split(indexTail, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.Contains(trimmed, "STUCK") ||
			strings.Contains(trimmed, "BLOCKED") {
			out = append(out, trimmed)
		}
	}
	return out
}

// extractOpenStuckBlocked summarizes unresolved rows from the bounded INDEX
// tail. A QUERY is open when the same tail does not contain a RESULT/CLOSED row
// for the same letter; STUCK/BLOCKED rows are always included.
func extractOpenStuckBlocked(indexTail string, maxEntries int) []string {
	if maxEntries <= 0 {
		return nil
	}
	type row struct {
		id      string
		typ     string
		thread  string
		parent  string
		summary string
		raw     string
	}
	var rows []row
	closed := map[string]bool{}
	for _, line := range strings.Split(indexTail, "\n") {
		r, ok := parseIndexRow(line)
		if !ok {
			continue
		}
		rows = append(rows, r)
		if r.typ == "RESULT" || r.typ == "CLOSED" {
			closed[r.id] = true
		}
	}
	var out []string
	for i := len(rows) - 1; i >= 0 && len(out) < maxEntries; i-- {
		r := rows[i]
		switch {
		case strings.Contains(r.raw, "STUCK") || strings.Contains(r.raw, "BLOCKED"):
			out = append(out, r.raw)
		case r.typ == "QUERY" && !closed[r.id]:
			out = append(out, r.raw)
		}
	}
	return out
}

func parseIndexRow(line string) (struct {
	id      string
	typ     string
	thread  string
	parent  string
	summary string
	raw     string
}, bool) {
	var r struct {
		id      string
		typ     string
		thread  string
		parent  string
		summary string
		raw     string
	}
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "|") || strings.Contains(trimmed, "---") {
		return r, false
	}
	parts := strings.Split(trimmed, "|")
	if len(parts) < 7 {
		return r, false
	}
	r.id = strings.TrimSpace(parts[1])
	r.typ = strings.TrimSpace(parts[2])
	r.thread = strings.TrimSpace(parts[3])
	r.parent = strings.TrimSpace(parts[4])
	r.summary = strings.TrimSpace(parts[5])
	r.raw = trimmed
	if !dispatchLetterRe.MatchString(r.id) {
		return r, false
	}
	return r, true
}

// buildLetterContext reads the active letter's QUERY and walks the
// Parent chain up to depth to provide thread continuity. Returns a
// Markdown section or "".
func buildLetterContext(archiveDir, letterID, indexTail string, depth int) string {
	thread := resolveThreadFromIndex(indexTail, letterID)
	if thread == "" {
		slog.Debug("rehydration: cannot resolve thread for letter",
			"letter", letterID)
		return ""
	}

	var b strings.Builder

	// 2. Read current QUERY file.
	letterDigest := readCurrentLetterDigest(archiveDir, thread, letterID)
	if letterDigest != "" {
		b.WriteString("### 当前信上下文\n")
		b.WriteString(letterDigest)
		b.WriteString("\n")
	}

	// 3. Walk parent chain (read parent's QUERY/RESULT digest).
	if depth > 0 {
		parentDigest := walkParentDigest(archiveDir, thread, letterID, depth)
		if parentDigest != "" {
			b.WriteString("### 父链上下文\n")
			b.WriteString(parentDigest)
			b.WriteString("\n")
		}
	}

	return strings.TrimSpace(b.String())
}

// resolveThreadFromIndex scans INDEX text for a row containing the
// given letter ID and returns the thread slug from column 3.
func resolveThreadFromIndex(indexTail, letterID string) string {
	for _, line := range strings.Split(indexTail, "\n") {
		if !strings.Contains(line, "|") {
			continue
		}
		parts := strings.Split(line, "|")
		for i, p := range parts {
			if cell := strings.TrimSpace(p); cell == letterID {
				// Column 3 (0-indexed: col 0 is empty pre-pipe, col 1 = ID,
				// col 2 = Type, col 3 = Thread slug).
				if i+3 < len(parts) {
					return strings.TrimSpace(parts[i+2])
				}
			}
		}
	}
	return ""
}

// readLetterDigest reads the QUERY or RESULT letter file and returns a
// short digest of the context.
func readLetterDigest(archiveDir, thread, letterID string) string {
	for _, suffix := range []string{"query.md", "result.md"} {
		path := filepath.Join(archiveDir, "threads", thread,
			letterID+"."+suffix)
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		body := string(data)
		if body == "" {
			continue
		}
		body = stripFrontmatter(body)

		digest := extractSection(body, "Context Digest")
		if digest != "" {
			if len(digest) > 1200 {
				digest = digest[:1200] + "...(truncated)"
			}
			return fmt.Sprintf("**%s** (%s):\n%s\n", letterID, suffix[:5], digest)
		}
	}
	return ""
}

// readCurrentLetterDigest reads the active QUERY and includes the sections a
// new instance needs to execute the dispatch without seeing the original chat.
func readCurrentLetterDigest(archiveDir, thread, letterID string) string {
	path := filepath.Join(archiveDir, "threads", thread, letterID+".query.md")
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	body := stripFrontmatter(string(data))
	var sections []string
	for _, heading := range []string{"Context Digest", "Query", "Expected Output"} {
		if section := extractSection(body, heading); section != "" {
			sections = append(sections, fmt.Sprintf("#### %s\n%s", heading, truncateRehydrationRunes(section, 1800)))
		}
	}
	if len(sections) == 0 {
		return ""
	}
	return fmt.Sprintf("**%s QUERY**:\n%s\n", letterID, strings.Join(sections, "\n\n"))
}

// walkParentDigest reads the letter's Parent field and reads the
// parent's digest up to depth levels.
func walkParentDigest(archiveDir, thread, letterID string, depth int) string {
	if depth <= 0 {
		return ""
	}

	parentID := readParentField(archiveDir, thread, letterID)
	if parentID == "" || parentID == "ROOT" {
		return ""
	}

	indexPath := filepath.Join(archiveDir, "INDEX.md")
	indexTail := readTail(indexPath, 200)
	parentThread := resolveThreadFromIndex(indexTail, parentID)
	if parentThread == "" {
		parentThread = thread
	}

	var b strings.Builder

	parentDigest := readLetterDigest(archiveDir, parentThread, parentID)
	if parentDigest != "" {
		b.WriteString(fmt.Sprintf("**Parent %s** digest:\n", parentID))
		b.WriteString(parentDigest)
		b.WriteString("\n")
	}

	parentResult := readLetterResultDigest(archiveDir, parentThread, parentID)
	if parentResult != "" {
		b.WriteString(fmt.Sprintf("**%s RESULT** conclusion:\n", parentID))
		b.WriteString(parentResult)
		b.WriteString("\n")
	}

	if depth > 1 {
		upper := walkParentDigest(archiveDir, parentThread, parentID, depth-1)
		if upper != "" {
			b.WriteString(upper)
		}
	}

	return strings.TrimSpace(b.String())
}

// readParentField parses the YAML frontmatter of a letter file and
// returns the Parent value.
func readParentField(archiveDir, thread, letterID string) string {
	for _, suffix := range []string{"query.md", "result.md"} {
		path := filepath.Join(archiveDir, "threads", thread,
			letterID+"."+suffix)
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		body := string(data)
		if !strings.HasPrefix(body, "---\n") {
			continue
		}
		rest := body[4:]
		endIdx := strings.Index(rest, "\n---\n")
		if endIdx < 0 {
			continue
		}
		frontmatter := rest[:endIdx]
		for _, fmLine := range strings.Split(frontmatter, "\n") {
			if strings.HasPrefix(fmLine, "Parent:") {
				return strings.TrimSpace(fmLine[7:])
			}
		}
	}
	return ""
}

// readLetterResultDigest reads the RESULT file and returns a short
// excerpt of the Conclusion section.
func readLetterResultDigest(archiveDir, thread, letterID string) string {
	path := filepath.Join(archiveDir, "threads", thread,
		letterID+".result.md")
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	body := stripFrontmatter(string(data))
	section := extractSection(body, "Conclusion")
	if section != "" {
		if len(section) > 600 {
			section = section[:600] + "...(truncated)"
		}
		return section
	}
	return ""
}

// ── Text helpers ────────────────────────────────────────────────

// stripFrontmatter removes the YAML frontmatter (---\n...\n---\n) from
// a letter file body. Returns the body unchanged if no frontmatter is
// found.
func stripFrontmatter(body string) string {
	if !strings.HasPrefix(body, "---\n") {
		return body
	}
	rest := body[4:]
	endIdx := strings.Index(rest, "\n---\n")
	if endIdx < 0 {
		return body
	}
	return rest[endIdx+5:]
}

// extractSection extracts the body text under a ## heading until the
// next ## or end of content. heading is the heading text after ##
// (e.g. "Context Digest" for "## Context Digest").
func extractSection(body, heading string) string {
	marker := "## " + heading
	idx := strings.Index(body, marker)
	if idx < 0 {
		return ""
	}
	after := body[idx+len(marker):]
	nl := strings.IndexByte(after, '\n')
	if nl >= 0 {
		after = after[nl+1:]
	}
	endIdx := strings.Index(after, "\n## ")
	if endIdx < 0 {
		return strings.TrimSpace(after)
	}
	return strings.TrimSpace(after[:endIdx])
}

// EstimateTokenCount provides a rough upper-bound token count for a
// given digest string. Used for logging and debug visibility.
func EstimateTokenCount(s string) int {
	return len([]rune(s)) / 2
}

func normalizeLetterID(value string) string {
	if value == "" {
		return ""
	}
	if m := rehydrationLetterRe.FindStringSubmatch(value); len(m) > 1 {
		return "L-" + m[1]
	}
	if m := keywordLetterRe.FindStringSubmatch(value); len(m) > 1 {
		return "L-" + m[1]
	}
	if m := leadingZeroRe.FindString(value); m != "" {
		return "L-" + m
	}
	if m := standaloneDigitsRe.FindStringSubmatch(value); len(m) > 1 {
		return "L-" + m[1]
	}
	return ""
}

func ExtractLetterIDFromText(text string) string {
	return normalizeLetterID(text)
}

func trimDigestToBudget(digest string, maxTokens int) string {
	if maxTokens <= 0 || EstimateTokenCount(digest) <= maxTokens {
		return digest
	}
	maxRunes := maxTokens * 2
	runes := []rune(digest)
	if len(runes) <= maxRunes {
		return digest
	}
	notice := fmt.Sprintf("\n\n[Rehydration Digest truncated to budget: ~%d tokens]", maxTokens)
	noticeRunes := []rune(notice)
	if len(noticeRunes) >= maxRunes {
		return string(runes[:maxRunes])
	}
	return string(runes[:maxRunes-len(noticeRunes)]) + notice
}

func truncateRehydrationRunes(s string, maxRunes int) string {
	runes := []rune(strings.TrimSpace(s))
	if maxRunes <= 0 || len(runes) <= maxRunes {
		return string(runes)
	}
	return string(runes[:maxRunes]) + "...(truncated)"
}
