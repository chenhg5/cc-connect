package slack

import (
	"strings"
	"testing"

	"github.com/slack-go/slack"
)

func TestNormalizeThreadContextDepth(t *testing.T) {
	cases := []struct {
		name string
		raw  any
		want int
	}{
		{"unset", nil, defaultThreadContextDepth},
		{"toml int64", int64(5), 5},
		{"plain int", 7, 7},
		{"float", float64(12), 12},
		{"zero falls back", int64(0), defaultThreadContextDepth},
		{"negative falls back", int64(-3), defaultThreadContextDepth},
		{"above cap is clamped", int64(5000), maxThreadContextDepth},
		{"wrong type falls back", "twenty", defaultThreadContextDepth},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := normalizeThreadContextDepth(c.raw); got != c.want {
				t.Errorf("normalizeThreadContextDepth(%#v) = %d, want %d", c.raw, got, c.want)
			}
		})
	}
}

// TestThreadHistoryFor_OnlyBootstrapsAForeignThread pins which of the three
// inbound shapes actually costs an API call. The bot must read a thread it was
// pulled into, and must not re-read a thread it is already part of.
func TestThreadHistoryFor_OnlyBootstrapsAForeignThread(t *testing.T) {
	const (
		channel  = "C123"
		rootTS   = "1717000000.000100"
		replyTS  = "1717000100.000200"
		secondTS = "1717000200.000300"
	)

	t.Run("top-level message claims its thread without fetching", func(t *testing.T) {
		p := &Platform{threadContext: true, threadContextDepth: defaultThreadContextDepth}
		// threadTS == messageTS: this message IS the future thread root.
		if got := p.threadHistoryFor(channel, rootTS, rootTS); got != "" {
			t.Errorf("top-level message injected context: %q", got)
		}
		// Having claimed it, a later reply in the same thread must not fetch —
		// the agent already received the root message as an ordinary turn.
		if got := p.threadHistoryFor(channel, rootTS, replyTS); got != "" {
			t.Errorf("reply to a bot-handled thread injected context: %q", got)
		}
	})

	t.Run("second message of a foreign thread does not re-fetch", func(t *testing.T) {
		p := &Platform{threadContext: true, threadContextDepth: defaultThreadContextDepth}
		// First contact with a thread the bot did not start. p.client is nil,
		// so fetchThreadHistory returns nil and the block is empty — what is
		// under test is that the thread is now marked, not the transcript.
		_ = p.threadHistoryFor(channel, rootTS, replyTS)
		if _, seen := p.bootstrappedThreads.Load(channel + ":" + rootTS); !seen {
			t.Fatal("first contact did not mark the thread as bootstrapped")
		}
		if got := p.threadHistoryFor(channel, rootTS, secondTS); got != "" {
			t.Errorf("second message re-injected context: %q", got)
		}
	})

	t.Run("disabled and malformed inputs stay inert", func(t *testing.T) {
		off := &Platform{threadContext: false, threadContextDepth: defaultThreadContextDepth}
		if got := off.threadHistoryFor(channel, rootTS, replyTS); got != "" {
			t.Errorf("thread_context=false injected context: %q", got)
		}
		if _, seen := off.bootstrappedThreads.Load(channel + ":" + rootTS); seen {
			t.Error("thread_context=false must not track threads")
		}
		on := &Platform{threadContext: true, threadContextDepth: defaultThreadContextDepth}
		if got := on.threadHistoryFor("", rootTS, replyTS); got != "" {
			t.Errorf("empty channel injected context: %q", got)
		}
		// A slash command has no thread context at all.
		if got := on.threadHistoryFor(channel, "", replyTS); got != "" {
			t.Errorf("empty threadTS injected context: %q", got)
		}
	})
}

func TestFormatThreadHistory(t *testing.T) {
	resolve := func(id string) string {
		if id == "U1" {
			return "Ada"
		}
		return ""
	}

	t.Run("empty history yields no block", func(t *testing.T) {
		if got := formatThreadHistory(nil, resolve); got != "" {
			t.Errorf("formatThreadHistory(nil) = %q, want empty", got)
		}
	})

	t.Run("labels humans and bots so the agent can tell them apart", func(t *testing.T) {
		history := []slack.Message{
			newMessage("U1", "", "", "what broke the deploy?"),
			newMessage("U2", "B9", "other-bot", "the migration timed out"),
			newMessage("U3", "", "", "  padded  "),
		}
		got := formatThreadHistory(history, resolve)

		if !strings.HasPrefix(got, "--- Slack thread history (3 messages before this one) ---\n") {
			t.Errorf("missing or wrong header: %q", got)
		}
		if !strings.HasSuffix(got, "---\n\n") {
			t.Errorf("missing trailer: %q", got)
		}
		for _, want := range []string{
			"[1] Ada (user):\nwhat broke the deploy?",
			// A bot reply is labelled assistant, which is what makes
			// "evaluate what the other bot said" answerable.
			"[2] other-bot (assistant):\nthe migration timed out",
			// Unresolvable display name falls back to the raw user ID.
			"[3] U3 (user):\npadded",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("formatted history missing %q:\n%s", want, got)
			}
		}
	})

	t.Run("tolerates a nil name resolver", func(t *testing.T) {
		got := formatThreadHistory([]slack.Message{newMessage("U7", "", "", "hi")}, nil)
		if !strings.Contains(got, "[1] U7 (user):\nhi") {
			t.Errorf("nil resolver output = %q", got)
		}
	})
}

// TestFilterThreadHistory covers what the injected block must exclude: the
// trigger message, anything after it, textless events, and — once the depth
// budget binds — the oldest messages rather than the newest.
func TestFilterThreadHistory(t *testing.T) {
	at := func(ts, text string) slack.Message {
		m := newMessage("U1", "", "", text)
		m.Timestamp = ts
		return m
	}
	msgs := []slack.Message{
		at("1717000000.000100", "root question"),
		at("1717000100.000100", "   "), // a file share with no text
		at("1717000200.000100", "second"),
		at("1717000300.000100", "third"),
		at("1717000400.000100", "the @-mention itself"),
		at("1717000500.000100", "posted after the trigger"),
		at("", "no timestamp"),
	}
	const triggerTS = "1717000400.000100"

	t.Run("keeps only textful messages before the trigger", func(t *testing.T) {
		got := filterThreadHistory(msgs, triggerTS, 20)
		want := []string{"root question", "second", "third"}
		if len(got) != len(want) {
			t.Fatalf("kept %d messages, want %d: %+v", len(got), len(want), got)
		}
		for i, w := range want {
			if got[i].Text != w {
				t.Errorf("message %d = %q, want %q", i, got[i].Text, w)
			}
		}
	})

	t.Run("depth drops the oldest, keeping what is nearest the question", func(t *testing.T) {
		got := filterThreadHistory(msgs, triggerTS, 2)
		if len(got) != 2 || got[0].Text != "second" || got[1].Text != "third" {
			t.Errorf("depth=2 kept %+v, want the last two before the trigger", got)
		}
	})
}

func newMessage(user, botID, username, text string) slack.Message {
	m := slack.Message{}
	m.User = user
	m.BotID = botID
	m.Username = username
	m.Text = text
	m.Timestamp = "1717000000.000100"
	return m
}
