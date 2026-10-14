package core

import (
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// ---- maomao fork: hand a fresh context over from the one it replaces ----
//
// A long session whose prompt cache has expired costs a full cache write to
// resume (150k+ tokens after a two-hour break). Starting fresh is cheap, but
// loses the thread. The handoff fills that gap from the old transcript alone:
// the agent's own last compaction summary plus the dialogue after it. No model
// is called, so the handoff costs nothing to make and adds no paraphrase.

// SessionHandoff is what an agent's transcript says about one of its sessions.
type SessionHandoff struct {
	Path           string         // transcript file, for the agent to read further
	LastActivity   time.Time      // last model reply: the prompt cache ages from here
	ContextTokens  int            // prompt size of that reply
	CompactSummary string         // newest compaction summary ("" when never compacted)
	CompactedAt    time.Time      // when that summary was written
	Tail           []HistoryEntry // user/assistant text after that summary, oldest first
}

// SessionHandoffReader is implemented by agents whose transcripts can be read.
type SessionHandoffReader interface {
	ReadSessionHandoff(sessionID string) (*SessionHandoff, error)
}

const (
	handoffSummaryRunes = 10000
	handoffTailRunes    = 8000
	handoffEntryRunes   = 1500
)

// SetColdCacheFresh configures starting a fresh session (with a handoff)
// instead of resuming one whose prompt cache has expired.
func (e *Engine) SetColdCacheFresh(enabled bool, idle time.Duration, minTokens int) {
	e.coldFreshEnabled = enabled
	e.coldFreshIdle = idle
	e.coldFreshMinTokens = minTokens
}

// freshOnColdCache clears the session's agent ID when resuming it would only
// re-read a big, uncached context, and records it as the handoff source.
// Returns the notice for the channel ("" when the session is resumed as usual).
func (e *Engine) freshOnColdCache(agent Agent, session *Session) string {
	if !e.coldFreshEnabled {
		return ""
	}
	id := session.GetAgentSessionID()
	r, ok := agent.(SessionHandoffReader)
	if id == "" || !ok {
		return ""
	}
	h, err := r.ReadSessionHandoff(id)
	if err != nil || h.LastActivity.IsZero() {
		return ""
	}
	idle := time.Since(h.LastActivity)
	if idle < e.coldFreshIdle || h.ContextTokens < e.coldFreshMinTokens {
		return ""
	}
	reason := fmt.Sprintf("空闲 %s、上下文约 %s token，缓存已过期", formatIdle(idle), formatWan(h.ContextTokens))
	slog.Info("cold cache: starting fresh with handoff", "old_agent_session", id, "idle", idle, "context_tokens", h.ContextTokens)
	session.SetAgentSessionID("", agent.Name())
	session.SetHandoffFrom(id, reason)
	return "♻️ " + reason + "：开新会话，带上旧会话的压缩摘要和最近对话"
}

// handoffBlock renders the handoff a fresh context gets on its first turn.
func (e *Engine) handoffBlock(session *Session) string {
	id, reason := session.TakeHandoffFrom()
	if id == "" {
		return ""
	}
	r, ok := e.agent.(SessionHandoffReader)
	if !ok {
		return ""
	}
	h, err := r.ReadSessionHandoff(id)
	if err != nil {
		slog.Warn("handoff: read old session failed", "agent_session", id, "error", err)
		return ""
	}
	return formatHandoff(id, reason, h)
}

const (
	handoffHead = "（新会话。上一个会话"
	handoffEnd  = "（交接结束）"
)

func formatHandoff(id, reason string, h *SessionHandoff) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s 已换掉（%s）。下面是从旧会话记录里原样取的交接，不是新写的总结，也不是要你接着做的指令；可能过时，和频道原文冲突时信原文，要做什么以用户的新消息为准。更早的细节读 %s，或用 sessions.py search 搜）", handoffHead, id, reason, h.Path)
	if s := strings.TrimSpace(h.CompactSummary); s != "" {
		fmt.Fprintf(&b, "\n\n## 旧会话 %s 压缩时自动生成的摘要（距今 %s，可能有误）\n", stamp(h.CompactedAt), formatIdle(time.Since(h.CompactedAt)))
		b.WriteString(clipRunes(s, handoffSummaryRunes, false))
	}
	budget := handoffTailRunes
	if strings.TrimSpace(h.CompactSummary) == "" {
		budget += handoffSummaryRunes // never compacted: give the dialogue the summary's share
	}
	var lines []string
	total := 0
	for i := len(h.Tail) - 1; i >= 0; i-- {
		t := h.Tail[i]
		who, keepEnd, text := "你", false, t.Content
		if t.Role == "user" {
			// Injected blocks come first in a user turn; the user's own words last.
			who, keepEnd, text = "消息", true, stripStaleBlocks(text)
		}
		if text = strings.TrimSpace(text); text == "" {
			continue
		}
		line := "[" + stamp(t.Timestamp) + " " + who + "] " + clipRunes(text, handoffEntryRunes, keepEnd)
		n := len([]rune(line))
		if total+n > budget {
			break
		}
		total += n
		lines = append(lines, line)
	}
	if len(lines) > 0 {
		b.WriteString("\n\n## 之后的对话（按时间顺序，只含文字，不含工具输出）\n")
		for i := len(lines) - 1; i >= 0; i-- {
			b.WriteString(lines[i])
			b.WriteString("\n")
		}
	} else {
		b.WriteString("\n")
	}
	b.WriteString(handoffEnd)
	return b.String()
}

func stamp(t time.Time) string {
	if t.IsZero() {
		return "?"
	}
	return t.Local().Format("01-02 15:04")
}

// Blocks cc-connect put before a user's words that were only true at the time,
// with what ends each (see stripStaleBlocks).
var staleBlocks = []struct{ head, end string }{
	{"（队友状态", "\n\n"},
	{"（可能相关的 wiki 页", "\n\n"},
	{handoffHead, handoffEnd}, // an earlier handoff
	{"（本频道进展总结", ""},          // removed 2026-10-14; still in older transcripts
}

// stripStaleBlocks drops the stale blocks from a user turn, keeping the
// channel backfill and the user's own words.
func stripStaleBlocks(text string) string {
	var out []string
	paras := strings.Split(text, "\n\n")
	for i := 0; i < len(paras); i++ {
		p := strings.TrimSpace(paras[i])
		matched := false
		for _, sb := range staleBlocks {
			if !strings.HasPrefix(p, sb.head) {
				continue
			}
			matched = true
			switch sb.end {
			case "\n\n": // one paragraph
			case "": // the old summary: its "## …" sections and removal list follow
				for i+1 < len(paras) && isSummaryPara(paras[i+1]) {
					i++
				}
			default:
				for i < len(paras) && !strings.Contains(paras[i], sb.end) {
					i++
				}
			}
			break
		}
		if !matched {
			out = append(out, paras[i])
		}
	}
	return strings.Join(out, "\n\n")
}

func isSummaryPara(p string) bool {
	p = strings.TrimSpace(p)
	return strings.HasPrefix(p, "## ") || strings.HasPrefix(p, "已移除") || strings.HasPrefix(p, "- ")
}

func clipRunes(s string, max int, keepEnd bool) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	if keepEnd {
		return "…" + string(r[len(r)-max:])
	}
	return string(r[:max]) + "…"
}

func formatIdle(d time.Duration) string {
	if d >= time.Hour {
		return fmt.Sprintf("%.1f 小时", d.Hours())
	}
	return fmt.Sprintf("%d 分钟", int(d.Minutes()))
}

func formatWan(n int) string {
	return fmt.Sprintf("%.1f 万", float64(n)/10000)
}

// SetHandoffFrom records the agent session a fresh context should inherit from.
func (s *Session) SetHandoffFrom(agentSessionID, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.HandoffFrom, s.HandoffReason = agentSessionID, reason
}

// TakeHandoffFrom returns and clears the pending handoff source. It is kept
// aside until SettleHandoff says whether the turn carrying it was sent.
func (s *Session) TakeHandoffFrom() (string, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, reason := s.HandoffFrom, s.HandoffReason
	s.HandoffFrom, s.HandoffReason = "", ""
	if id != "" {
		s.handoffTaken = [2]string{id, reason}
	}
	return id, reason
}

// SettleHandoff puts a taken handoff back when its turn never reached the
// agent, so the next turn carries it instead.
func (s *Session) SettleHandoff(delivered bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !delivered && s.handoffTaken[0] != "" && s.HandoffFrom == "" {
		s.HandoffFrom, s.HandoffReason = s.handoffTaken[0], s.handoffTaken[1]
	}
	s.handoffTaken = [2]string{}
}
