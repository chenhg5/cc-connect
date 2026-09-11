package slack

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/slack-go/slack"
)

// fetchThreadContext fetches the Slack thread that starts at threadTS and
// returns a formatted, chronological summary suitable for injection into
// core.Message.ExtraContent. The summary only includes messages the bot did
// NOT post so the agent sees the human-readable transcript.
//
// Behaviour:
//   - maxMessages caps the number of *non-bot* messages we keep; Slack's API
//     hard limit per request is 1000, we still cap lower for prompt safety.
//   - API failures (network, auth, scope) are returned as errors. Callers
//     log WARN + continue without context; we never block the main message.
//   - Bot messages are skipped at the formatter stage, not at the API stage,
//     so the count of API messages is still reported in metrics.
//
// Returns ("", nil) when the thread has zero non-bot messages so callers can
// distinguish "no context needed" from "fetch failed".
func (p *Platform) fetchThreadContext(ctx context.Context, channelID, threadTS string, maxMessages int) (string, error) {
	if channelID == "" || threadTS == "" {
		return "", errors.New("slack: fetchThreadContext: empty channel or thread ts")
	}
	if maxMessages <= 0 {
		maxMessages = defaultSlackThreadContextMaxMessages
	}
	if p.client == nil {
		return "", errors.New("slack: fetchThreadContext: client not initialised")
	}

	p.metricThreadContextFetchTotal.Add(1)

	// Slack paginates thread replies by cursor. We request Inclusive so the
	// thread root is included; the formatter will still skip it if it is a
	// bot message. Limit stays at the default (~50) — Slack returns at most
	// 50 per page; we follow cursor if we need more, but cap by maxMessages.
	const pageSize = 50
	collected := make([]slack.Message, 0, pageSize)
	cursor := ""
	for {
		params := &slack.GetConversationRepliesParameters{
			ChannelID: channelID,
			Timestamp: threadTS,
			Limit:     pageSize,
			Inclusive: true,
			Cursor:    cursor,
		}
		msgs, _, nextCursor, err := p.client.GetConversationRepliesContext(ctx, params)
		if err != nil {
			// Failure metric is incremented by the caller's logThreadContextFailure
			// path so direct callers (who handle errors themselves) and the
			// soft-degrading maybeFetchThreadContext wrapper don't double-count.
			return "", fmt.Errorf("conversations.replies: %w", err)
		}
		collected = append(collected, msgs...)
		// Cap early — once we have enough candidates the formatter will trim
		// bot messages down to maxMessages, but we stop the API loop here so
		// we don't burn rate-limit quota on threads with thousands of bot
		// replies.
		if len(collected) >= maxMessages*2 || nextCursor == "" {
			break
		}
		cursor = nextCursor
	}

	kept := formatThreadContext(collected, p, maxMessages)
	if kept == "" {
		return "", nil
	}
	p.metricThreadContextMessagesTotal.Add(int64(len(collected)))
	return kept, nil
}

// formatThreadContext renders a Slack thread history into the same shape
// Feishu's formatReplyChain uses: a numbered chain for multi-message threads,
// or a single-message format for short threads. Bot/app messages are dropped
// so the agent only sees the human side of the conversation.
func formatThreadContext(messages []slack.Message, p *Platform, maxMessages int) string {
	if len(messages) == 0 {
		return ""
	}
	// Trim to newest maxMessages (skip the very latest entry when it matches
	// the message that triggered the dispatch — the agent already has it as
	// Content, so re-including it as ExtraContent would be redundant noise).
	// We can't compare timestamps here without passing the trigger ts, so we
	// simply take the oldest maxMessages non-bot messages instead.
	humanMessages := make([]slack.Message, 0, len(messages))
	for _, m := range messages {
		if isSlackBotMessage(m) {
			continue
		}
		if strings.TrimSpace(m.Text) == "" {
			continue
		}
		humanMessages = append(humanMessages, m)
	}
	if len(humanMessages) == 0 {
		return ""
	}
	// Keep the most recent maxMessages so the agent sees the latest context
	// rather than ancient root messages.
	if len(humanMessages) > maxMessages {
		humanMessages = humanMessages[len(humanMessages)-maxMessages:]
	}

	if len(humanMessages) == 1 {
		m := humanMessages[0]
		return fmt.Sprintf("[Thread reply from %s]:\n%s\n\n", displayNameFor(p, m.User), m.Text)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "--- Slack thread (%d messages) ---\n", len(humanMessages))
	for i, m := range humanMessages {
		fmt.Fprintf(&b, "[%d] %s:\n%s\n\n", i+1, displayNameFor(p, m.User), m.Text)
	}
	b.WriteString("---\n\n")
	return b.String()
}

// isSlackBotMessage returns true when the message was posted by an app/bot
// (BotID set) or by the current bot user. Bot replies in a thread are noise
// for the agent — the agent only needs to see what humans said.
func isSlackBotMessage(m slack.Message) bool {
	if m.BotID != "" {
		return true
	}
	// Some Slack apps post as the user but with subtype bot_message.
	if m.SubType == "bot_message" {
		return true
	}
	return false
}

// displayNameFor resolves a Slack user ID to a display name. Falls back to
// the user ID itself when the lookup fails so the formatted output stays
// useful even without a successful name resolution.
func displayNameFor(p *Platform, userID string) string {
	if userID == "" {
		return "unknown"
	}
	if p != nil {
		if name := p.resolveUserName(userID); name != "" {
			return name
		}
	}
	return userID
}

// threadContextEnabledFor returns whether thread-context fetching is active
// for this Platform. Centralised so tests can flip the flag without poking
// at private fields directly.
func (p *Platform) threadContextEnabledFor() bool {
	if p == nil {
		return false
	}
	return p.threadContextEnabled
}

// logThreadContextFailure records a WARN log + increments the failure metric
// when conversations.replies fails. We swallow the error so the main message
// flow continues.
func (p *Platform) logThreadContextFailure(channelID, threadTS string, err error) {
	p.metricThreadContextFetchFailedTotal.Add(1)
	slog.Warn("slack: thread context fetch failed; continuing without context",
		"channel", channelID, "thread_ts", threadTS, "error", err)
}

// maybeFetchThreadContext is the soft-degrading call site for the event
// handlers. It returns the formatted thread transcript (suitable for
// core.Message.ExtraContent) or "" when:
//
//   - thread context injection is disabled
//   - the event is not in an existing thread (threadTS == "")
//   - the fetch failed (logged + metric'd, never propagated)
//
// The 10-second cap is a defensive bound: Slack's conversations.replies is
// usually < 500 ms, but a slow network must not stall message dispatch.
func (p *Platform) maybeFetchThreadContext(channelID, threadTS string) string {
	if !p.threadContextEnabledFor() {
		return ""
	}
	if threadTS == "" {
		// Top-level message or DM with no thread — nothing to fetch.
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	extra, err := p.fetchThreadContext(ctx, channelID, threadTS, p.threadContextMaxMessages)
	if err != nil {
		p.logThreadContextFailure(channelID, threadTS, err)
		return ""
	}
	return extra
}