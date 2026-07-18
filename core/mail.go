package core

import (
	"fmt"
	"path/filepath"
	"strings"
)

const (
	mailDefaultTailLines = 200
	mailSummaryMaxRunes  = 80
)

// mailOpts are optional filters for /mail.
type mailOpts struct {
	Thread string // --thread <slug>
	Since  string // --since MM-DD or YYYY-MM-DD
}

// parseMailArgs parses /mail arguments.
// Supported:
//
//	/mail
//	/mail --thread <slug>
//	/mail --since <date>
//	/mail -t <slug> -s <date>
func parseMailArgs(args []string) (mailOpts, error) {
	var opts mailOpts
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch a {
		case "--thread", "-t":
			if i+1 >= len(args) {
				return opts, fmt.Errorf("missing value for %s", a)
			}
			i++
			opts.Thread = strings.TrimSpace(args[i])
		case "--since", "-s":
			if i+1 >= len(args) {
				return opts, fmt.Errorf("missing value for %s", a)
			}
			i++
			opts.Since = strings.TrimSpace(args[i])
		default:
			if strings.HasPrefix(a, "--thread=") {
				opts.Thread = strings.TrimSpace(strings.TrimPrefix(a, "--thread="))
				continue
			}
			if strings.HasPrefix(a, "--since=") {
				opts.Since = strings.TrimSpace(strings.TrimPrefix(a, "--since="))
				continue
			}
			return opts, fmt.Errorf("unknown argument: %s", a)
		}
	}
	return opts, nil
}

func isFullIndexDate(d string) bool {
	return len(d) == 10 && d[4] == '-' && d[7] == '-'
}

// normalizeIndexDate returns the MM-DD portion of an INDEX date cell.
func normalizeIndexDate(d string) string {
	d = strings.TrimSpace(d)
	if isFullIndexDate(d) {
		return d[5:]
	}
	return d
}

// dateOnOrAfter reports whether rowDate is on or after since.
// When both sides are YYYY-MM-DD, compare full dates (year-aware).
// Otherwise fall back to MM-DD lexicographic compare (INDEX often omits the year).
func dateOnOrAfter(rowDate, since string) bool {
	if since == "" {
		return true
	}
	rowDate = strings.TrimSpace(rowDate)
	since = strings.TrimSpace(since)
	if rowDate == "" {
		return false
	}
	if isFullIndexDate(rowDate) && isFullIndexDate(since) {
		return rowDate >= since
	}
	return normalizeIndexDate(rowDate) >= normalizeIndexDate(since)
}

// mailLetter is the latest non-CLOSED state for one letter ID.
type mailLetter struct {
	ID      string
	Type    string // QUERY / RESULT (may include STUCK/BLOCKED in summary)
	Thread  string
	Summary string
	Date    string
	Status  string // derived: OPEN / STUCK / BLOCKED / DONE(awaiting close)
}

func deriveMailStatus(r indexRow) string {
	raw := r.raw + " " + r.summary
	switch {
	case strings.Contains(raw, "STUCK"):
		return "STUCK"
	case strings.Contains(raw, "BLOCKED"):
		return "BLOCKED"
	case r.typ == "QUERY":
		return "OPEN"
	case r.typ == "RESULT":
		return "DONE"
	default:
		return r.typ
	}
}

// collectActiveMailLetters scans INDEX tail text and returns one entry per
// letter that has no CLOSED row in the window. Latest QUERY/RESULT wins.
// When opts.Thread is set, all matching non-CLOSED rows for that thread are
// returned (deep dive), still one row per letter ID (latest).
func collectActiveMailLetters(indexTail string, opts mailOpts) []mailLetter {
	closed := map[string]bool{}
	latest := map[string]indexRow{}
	var order []string

	for _, line := range strings.Split(indexTail, "\n") {
		r, ok := parseIndexRow(line)
		if !ok {
			continue
		}
		if r.typ == "CLOSED" {
			closed[r.id] = true
			delete(latest, r.id)
			continue
		}
		if r.typ != "QUERY" && r.typ != "RESULT" {
			continue
		}
		if opts.Thread != "" && !strings.EqualFold(r.thread, opts.Thread) {
			continue
		}
		if !dateOnOrAfter(r.date, opts.Since) {
			continue
		}
		if _, seen := latest[r.id]; !seen {
			order = append(order, r.id)
		}
		latest[r.id] = r
	}

	var out []mailLetter
	for _, id := range order {
		if closed[id] {
			continue
		}
		r, ok := latest[id]
		if !ok {
			continue
		}
		out = append(out, mailLetter{
			ID:      r.id,
			Type:    r.typ,
			Thread:  r.thread,
			Summary: r.summary,
			Date:    r.date,
			Status:  deriveMailStatus(r),
		})
	}
	return out
}

// formatMailOverview groups active letters by thread for Telegram.
// Thread sections follow first-seen order in the INDEX tail (not alphabetical).
func formatMailOverview(i18n *I18n, letters []mailLetter, opts mailOpts, tailLines int) string {
	var b strings.Builder
	fmt.Fprintf(&b, i18n.T(MsgMailTitle), tailLines)
	if opts.Thread != "" {
		fmt.Fprintf(&b, i18n.T(MsgMailFilterThread), opts.Thread)
	}
	if opts.Since != "" {
		fmt.Fprintf(&b, i18n.T(MsgMailFilterSince), opts.Since)
	}
	b.WriteString("\n")

	if len(letters) == 0 {
		b.WriteString(i18n.T(MsgMailEmpty))
		b.WriteString("\n")
		b.WriteString(i18n.T(MsgMailUsageHint))
		return b.String()
	}

	threads := make([]string, 0)
	byThread := map[string][]mailLetter{}
	for _, l := range letters {
		if _, ok := byThread[l.Thread]; !ok {
			threads = append(threads, l.Thread)
		}
		byThread[l.Thread] = append(byThread[l.Thread], l)
	}

	for _, th := range threads {
		b.WriteString("## ")
		b.WriteString(th)
		b.WriteString("\n")
		for _, l := range byThread[th] {
			sum := truncateRunes(l.Summary, mailSummaryMaxRunes)
			fmt.Fprintf(&b, i18n.T(MsgMailItem), l.ID, l.Type, l.Status, sum)
		}
		b.WriteString("\n")
	}
	b.WriteString(i18n.T(MsgMailUsageHint))
	return strings.TrimSpace(b.String())
}

// mailIndexPath resolves INDEX.md: prefer notify_index_path, else dataDir-derived archive.
func (e *Engine) mailIndexPath() string {
	if p := strings.TrimSpace(e.notifyConfig.IndexPath); p != "" {
		return p
	}
	if e.dataDir == "" {
		return ""
	}
	archiveDir := DeriveArchiveDir(e.dataDir)
	if archiveDir == "" {
		return ""
	}
	return filepath.Join(archiveDir, "INDEX.md")
}

func (e *Engine) cmdMail(p Platform, msg *Message, args []string) {
	opts, err := parseMailArgs(args)
	if err != nil {
		e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgMailUsageError, err.Error()))
		return
	}

	indexPath := e.mailIndexPath()
	if indexPath == "" {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgMailNoArchive))
		return
	}

	tail := readTail(indexPath, mailDefaultTailLines)
	if tail == "" {
		e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgMailUnreadable, indexPath))
		return
	}

	letters := collectActiveMailLetters(tail, opts)
	body := formatMailOverview(e.i18n, letters, opts, mailDefaultTailLines)

	for _, chunk := range splitMessage(body, maxPlatformMessageLen) {
		e.reply(p, msg.ReplyCtx, chunk)
	}
}
