package core

import (
	"fmt"
	"path/filepath"
	"sort"
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

// normalizeIndexDate collapses YYYY-MM-DD → MM-DD for lexicographic compare.
func normalizeIndexDate(d string) string {
	d = strings.TrimSpace(d)
	if len(d) == 10 && d[4] == '-' && d[7] == '-' {
		return d[5:]
	}
	return d
}

func dateOnOrAfter(rowDate, since string) bool {
	if since == "" {
		return true
	}
	rd := normalizeIndexDate(rowDate)
	sd := normalizeIndexDate(since)
	if rd == "" {
		return false
	}
	return rd >= sd
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
func formatMailOverview(letters []mailLetter, opts mailOpts, tailLines int) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("📬 活跃信件（INDEX 尾部 %d 行）\n", tailLines))
	if opts.Thread != "" {
		b.WriteString(fmt.Sprintf("筛选 thread: %s\n", opts.Thread))
	}
	if opts.Since != "" {
		b.WriteString(fmt.Sprintf("筛选 since: %s\n", opts.Since))
	}
	b.WriteString("\n")

	if len(letters) == 0 {
		b.WriteString("（无活跃 QUERY/RESULT）\n")
		b.WriteString("\n用法: /mail [--thread <slug>] [--since MM-DD]")
		return b.String()
	}

	// Preserve first-seen thread order, letters already in INDEX order.
	threads := make([]string, 0)
	byThread := map[string][]mailLetter{}
	for _, l := range letters {
		if _, ok := byThread[l.Thread]; !ok {
			threads = append(threads, l.Thread)
		}
		byThread[l.Thread] = append(byThread[l.Thread], l)
	}
	sort.SliceStable(threads, func(i, j int) bool {
		return threads[i] < threads[j]
	})

	for _, th := range threads {
		b.WriteString("## ")
		b.WriteString(th)
		b.WriteString("\n")
		for _, l := range byThread[th] {
			sum := truncateRunes(l.Summary, mailSummaryMaxRunes)
			b.WriteString(fmt.Sprintf("• %s %s [%s] — %s\n", l.ID, l.Type, l.Status, sum))
		}
		b.WriteString("\n")
	}
	b.WriteString("用法: /mail [--thread <slug>] [--since MM-DD]")
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
	body := formatMailOverview(letters, opts, mailDefaultTailLines)

	for _, chunk := range splitMessage(body, maxPlatformMessageLen) {
		e.reply(p, msg.ReplyCtx, chunk)
	}
}
