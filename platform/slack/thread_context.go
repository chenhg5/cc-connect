package slack

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/slack-go/slack"
)

// Slack delivers only the triggering message in an event payload, so a bot
// pulled into a thread that was already running sees the @-mention and nothing
// else — not the question it is being asked about, not what anyone else in the
// thread already said. This file bootstraps that missing context: the FIRST
// time a thread reaches the agent, the messages posted before it are fetched
// via conversations.replies and prepended as core.Message.ExtraContent, the
// same slot the Feishu adapter fills from its reply chain.
//
// Once per thread, not once per message: after the bootstrap the agent's own
// session carries the conversation, and re-sending the transcript on every
// turn would bury the user's actual text (feishu/#764).

const (
	defaultThreadContextDepth = 20
	maxThreadContextDepth     = 100
	// threadHistoryTimeout bounds the extra API call. The socket-mode loop
	// dispatches events one at a time, so a hung fetch would delay every event
	// behind it — the request is already acked by then, so nothing is
	// redelivered, but the bot would look asleep. conversations.replies
	// normally answers in well under a second; 5s is the give-up point.
	threadHistoryTimeout = 5 * time.Second
)

// normalizeThreadContextDepth resolves the configured thread_context_depth to a
// usable message count. TOML numbers reach the factory as int64, but a value
// injected programmatically may be int or float64, so all three are accepted.
// Out-of-range values are clamped rather than rejected: a too-small depth would
// silently defeat the feature, and a too-large one would page through a whole
// thread on every bootstrap.
func normalizeThreadContextDepth(raw any) int {
	depth := 0
	switch v := raw.(type) {
	case int64:
		depth = int(v)
	case int:
		depth = v
	case float64:
		depth = int(v)
	}
	if depth <= 0 {
		return defaultThreadContextDepth
	}
	if depth > maxThreadContextDepth {
		slog.Warn("slack: thread_context_depth above the cap, clamping",
			"configured", depth, "using", maxThreadContextDepth)
		return maxThreadContextDepth
	}
	return depth
}

// markThreadBootstrapped records that a thread has been handed to the agent and
// reports whether this call was the first one. Mirrors the Feishu adapter's
// markThreadSessionActive: entries are small and bounded by the number of
// distinct threads the bot is pulled into, and a restart simply re-bootstraps
// once per thread.
func (p *Platform) markThreadBootstrapped(channel, threadTS string) bool {
	if channel == "" || threadTS == "" {
		return false
	}
	_, loaded := p.bootstrappedThreads.LoadOrStore(channel+":"+threadTS, time.Now())
	return !loaded
}

// threadHistoryFor returns the context block for an inbound message, or "" when
// there is nothing to inject. Every message marks its thread, so the three cases
// separate cleanly:
//
//   - a top-level message (threadTS == messageTS) claims the thread it is about
//     to become the root of and injects nothing — there is nothing before it,
//     and the agent is about to be told the message itself;
//   - a later message in a thread this bot already handled injects nothing —
//     the agent session already carries the conversation, and repeating the
//     transcript every turn would bury the user's actual text (feishu/#764);
//   - the first message of a thread the bot did NOT start — being @-mentioned
//     into a discussion already in progress — is the one case where the agent
//     is missing everything, and is the case that fetches.
//
// Every failure path returns "" so a missing history scope, a revoked token, or
// a Slack outage degrades to today's behaviour instead of dropping the message.
func (p *Platform) threadHistoryFor(channel, threadTS, messageTS string) string {
	if !p.threadContext || channel == "" || threadTS == "" {
		return ""
	}
	first := p.markThreadBootstrapped(channel, threadTS)
	if !first || threadTS == messageTS {
		return ""
	}
	return formatThreadHistory(p.fetchThreadHistory(channel, threadTS, messageTS), p.resolveUserName)
}

// fetchThreadHistory reads up to threadContextDepth messages posted before
// messageTS in the thread rooted at threadTS, oldest first.
func (p *Platform) fetchThreadHistory(channel, threadTS, messageTS string) []slack.Message {
	if p.client == nil {
		return nil
	}
	// The socket-mode event loop hands events off without a context, and this
	// call must not outlive the reply the user is waiting for either way — so
	// the deadline is the bound that matters, not cancellation.
	ctx, cancel := context.WithTimeout(context.Background(), threadHistoryTimeout)
	defer cancel()

	// Limit is +1 because the trigger message itself is part of the thread and
	// is dropped below; asking for exactly depth would otherwise return
	// depth-1 usable messages.
	msgs, _, _, err := p.client.GetConversationRepliesContext(ctx, &slack.GetConversationRepliesParameters{
		ChannelID: channel,
		Timestamp: threadTS,
		Limit:     p.threadContextDepth + 1,
		Inclusive: true,
	})
	if err != nil {
		// missing_scope is the expected error for an app installed before this
		// feature existed: conversations.replies needs channels:history /
		// groups:history / im:history / mpim:history. Name it so the fix is
		// obvious from one log line.
		slog.Warn("slack: thread history unavailable, continuing without it",
			"error", err, "channel", channel, "thread_ts", threadTS,
			"hint", "conversations.replies needs a *:history scope for this conversation type")
		return nil
	}

	return filterThreadHistory(msgs, messageTS, p.threadContextDepth)
}

// filterThreadHistory keeps the messages that belong in the injected block:
// everything posted strictly before the trigger, in Slack's own order, capped
// at depth by dropping the OLDEST — the messages nearest the question being
// asked are the ones worth the context budget.
func filterThreadHistory(msgs []slack.Message, messageTS string, depth int) []slack.Message {
	history := make([]slack.Message, 0, len(msgs))
	for _, m := range msgs {
		// Slack timestamps are fixed-width "seconds.micros" strings, so a
		// lexicographic compare is a chronological one. Dropping equality also
		// drops the trigger message itself, which Inclusive:true returns
		// alongside the root we do want.
		if m.Timestamp == "" || m.Timestamp >= messageTS {
			continue
		}
		// Attachment-only and join/leave messages carry no text worth quoting.
		if strings.TrimSpace(m.Text) == "" {
			continue
		}
		history = append(history, m)
	}
	if depth > 0 && len(history) > depth {
		history = history[len(history)-depth:]
	}
	return history
}

// formatThreadHistory renders the transcript the agent receives. resolveName
// turns a Slack user ID into a display name; it may be nil.
func formatThreadHistory(history []slack.Message, resolveName func(string) string) string {
	if len(history) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "--- Slack thread history (%d messages before this one) ---\n", len(history))
	for i, m := range history {
		role := "user"
		name := ""
		if m.BotID != "" || m.SubType == "bot_message" {
			role = "assistant"
			name = m.Username
		} else if resolveName != nil {
			name = resolveName(m.User)
		}
		if name == "" {
			name = m.User
		}
		if name == "" {
			name = role
		}
		fmt.Fprintf(&b, "[%d] %s (%s):\n%s\n\n", i+1, name, role, strings.TrimSpace(m.Text))
	}
	b.WriteString("---\n\n")
	return b.String()
}
