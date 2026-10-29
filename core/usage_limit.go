package core

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// UsageLimitError is what ExecuteTimerJob returns when a turn of the job's
// run was refused because a usage limit has been reached. It ends up in the
// job's last_error, so whoever scheduled the job can tell that the prompt
// was not answered and when to try again.
type UsageLimitError struct {
	ResetsAt time.Time // zero if the agent did not say
}

func (e *UsageLimitError) Error() string {
	if e.ResetsAt.IsZero() {
		return "usage limit reached"
	}
	return "usage limit reached; resets " + e.ResetsAt.Local().Format(time.RFC3339)
}

// usageLimitResetRe matches the reset time in the CLI's refusal text, e.g.
// "You've hit your session limit · resets 8:30am (Asia/Shanghai)".
var usageLimitResetRe = regexp.MustCompile(`(?i)\bresets\s+(\d{1,2})(?::(\d{2}))?\s*(am|pm)\b`)

// usageLimitResetClock says when a limit resets as local "15:04", or
// "01-02 15:04" when that is not today. It prefers the reset time the agent
// reported and falls back to the time in the refusal text, which is a wall
// clock time in the user's own zone. It returns "" when neither says.
func usageLimitResetClock(limit *UsageLimit, text string, now time.Time) string {
	if limit != nil && !limit.ResetsAt.IsZero() {
		t := limit.ResetsAt.In(now.Location())
		if y, m, d := t.Date(); y == now.Year() && m == now.Month() && d == now.Day() {
			return t.Format("15:04")
		}
		return t.Format("01-02 15:04")
	}
	m := usageLimitResetRe.FindStringSubmatch(text)
	if m == nil {
		return ""
	}
	h, _ := strconv.Atoi(m[1])
	mins := 0
	if m[2] != "" {
		mins, _ = strconv.Atoi(m[2])
	}
	if h < 1 || h > 12 || mins > 59 {
		return ""
	}
	h %= 12
	if strings.EqualFold(m[3], "pm") {
		h += 12
	}
	return fmt.Sprintf("%02d:%02d", h, mins)
}

// usageLimitUnknownReset stands in for {reset_at} when the reset time is unknown.
const usageLimitUnknownReset = "稍后"

// usageLimitReplyFor decides what the chat gets for a turn refused for a
// usage limit. Without usage_limit_reply it is the agent's own text. With it,
// the configured text, {reset_at} filled in, goes out once per limit
// episode; refusals after that send nothing (silent is true) until a turn
// gets an answer or the reset time the reply announced has passed.
func (e *Engine) usageLimitReplyFor(event Event) (reply string, silent bool) {
	if e.usageLimitReply == "" {
		return event.Content, false
	}
	now := time.Now()
	e.usageLimitMu.Lock()
	defer e.usageLimitMu.Unlock()
	if e.usageLimitNotified && (e.usageLimitNotifiedUntil.IsZero() || now.Before(e.usageLimitNotifiedUntil)) {
		return "", true
	}
	e.usageLimitNotified = true
	e.usageLimitNotifiedUntil = time.Time{}
	if event.UsageLimit != nil {
		e.usageLimitNotifiedUntil = event.UsageLimit.ResetsAt
	}
	clock := usageLimitResetClock(event.UsageLimit, event.Content, now)
	if clock == "" {
		clock = usageLimitUnknownReset
	}
	return strings.ReplaceAll(e.usageLimitReply, "{reset_at}", clock), false
}

// usageLimitCleared ends the current limit episode after a turn the
// provider answered, so the next refusal is announced again.
func (e *Engine) usageLimitCleared() {
	e.usageLimitMu.Lock()
	e.usageLimitNotified = false
	e.usageLimitNotifiedUntil = time.Time{}
	e.usageLimitMu.Unlock()
}

// noteUsageLimit records that a turn on this session was refused for a usage
// limit; takeUsageLimit returns and clears it.
func (s *Session) noteUsageLimit(limit *UsageLimit) {
	s.usageLimitMu.Lock()
	s.usageLimit = limit
	s.usageLimitMu.Unlock()
}

func (s *Session) takeUsageLimit() *UsageLimit {
	s.usageLimitMu.Lock()
	defer s.usageLimitMu.Unlock()
	l := s.usageLimit
	s.usageLimit = nil
	return l
}
