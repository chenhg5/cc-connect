package core

import (
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// maomao fork: channel backfill. The agent only ever sees the messages cc-connect
// hands it, so anything that never reached it (sent while cc-connect was down,
// silenced by the gate, dropped as empty, or said by a teammate) is invisible.
// Before each turn the engine re-reads the channel since the last message this
// session saw and prepends what the agent missed, in channel order.

// ChannelHistoryMessage is one message read back from a platform channel.
type ChannelHistoryMessage struct {
	ID      string
	Author  string
	IsBot   bool
	IsSelf  bool // sent by this bot
	Content string
	Time    time.Time
}

// ChannelHistoryReader is implemented by platforms that can read channel history.
// It returns up to limit messages posted before beforeID, oldest first.
type ChannelHistoryReader interface {
	ChannelHistory(channelID, beforeID string, limit int) ([]ChannelHistoryMessage, error)
}

const (
	backfillFetchLimit   = 50
	backfillFirstWindow  = 3 * time.Hour // no last-seen mark yet: how far back to look
	backfillMaxMsgRunes  = 600
	backfillMaxTotalRune = 8000
)

// snowflakeAfter reports whether message ID a was posted after b (numeric IDs as strings).
func snowflakeAfter(a, b string) bool {
	if len(a) != len(b) {
		return len(a) > len(b)
	}
	return a > b
}

// channelBackfill returns the formatted messages the session has not seen yet
// (between its last-seen mark and msg), and ok=false when the platform cannot
// read history or the read failed — callers then fall back to the gate buffer.
func (e *Engine) channelBackfill(p Platform, msg *Message, session *Session) (string, bool) {
	r, ok := p.(ChannelHistoryReader)
	if !ok || msg.ChannelID == "" || msg.MessageID == "" {
		return "", false
	}
	msgs, err := r.ChannelHistory(msg.ChannelID, msg.MessageID, backfillFetchLimit)
	if err != nil {
		slog.Warn("backfill: read channel history failed", "channel", msg.ChannelID, "error", err)
		return "", false
	}
	lastSeen := session.GetLastSeenMsgID()
	var lines []string
	truncated := len(msgs) == backfillFetchLimit
	for _, m := range msgs {
		if lastSeen != "" {
			if !snowflakeAfter(m.ID, lastSeen) {
				truncated = false
				continue
			}
		} else if time.Since(m.Time) > backfillFirstWindow {
			truncated = false
			continue
		}
		if m.IsSelf {
			continue // the agent already has its own replies
		}
		text := strings.TrimSpace(m.Content)
		if text == "" {
			text = "（空消息或只有附件）"
		}
		if r := []rune(text); len(r) > backfillMaxMsgRunes {
			text = string(r[:backfillMaxMsgRunes]) + "…"
		}
		who := m.Author
		if m.IsBot {
			who += "（bot）"
		}
		lines = append(lines, fmt.Sprintf("[%s] %s: %s", m.Time.Local().Format("15:04"), who, text))
	}
	if len(lines) == 0 {
		return "", true
	}
	// Keep the newest messages when over budget.
	total := 0
	start := len(lines)
	for start > 0 {
		n := len([]rune(lines[start-1]))
		if total+n > backfillMaxTotalRune {
			truncated = true
			break
		}
		total += n
		start--
	}
	head := "（频道里你上次看到之后的消息，按时间顺序；这是真实记录，先读它再回答）"
	if truncated {
		head = "（频道里你上次看到之后的消息，按时间顺序；更早的还有，没列出来，需要时去翻）"
	}
	return head + "\n" + strings.Join(lines[start:], "\n"), true
}

// markSeen records msg as the newest message this session has seen.
func markSeen(session *Session, msg *Message) {
	id := msg.MessageID
	if msg.SeenUpToID != "" && snowflakeAfter(msg.SeenUpToID, id) {
		id = msg.SeenUpToID
	}
	if id == "" {
		return
	}
	if cur := session.GetLastSeenMsgID(); cur == "" || snowflakeAfter(id, cur) {
		session.SetLastSeenMsgID(id)
	}
}
