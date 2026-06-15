package slack

import "testing"

const testBotID = "U0BOT123"

func TestMentionsSlackBot(t *testing.T) {
	cases := []struct {
		name string
		text string
		bot  string
		want bool
	}{
		{"explicit mention", "hey <@U0BOT123> ping", testBotID, true},
		{"mention with leading", "<@U0BOT123> do thing", testBotID, true},
		{"different user mentioned", "<@U0OTHER99> look at this", testBotID, false},
		{"no mention", "just chatting in the channel", testBotID, false},
		{"empty bot id", "<@U0BOT123> hi", "", false},
		{"substring is not a mention", "U0BOT123 without brackets", testBotID, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := mentionsSlackBot(c.text, c.bot); got != c.want {
				t.Fatalf("mentionsSlackBot(%q, %q) = %v, want %v", c.text, c.bot, got, c.want)
			}
		})
	}
}

func TestShouldForwardSlackMessage(t *testing.T) {
	cases := []struct {
		name           string
		channelType    string
		text           string
		botUserID      string
		requireMention bool
		autoFollow     bool
		want           bool
	}{
		// DMs always pass, mention or not, regardless of auto-follow.
		{"dm no mention passes", "im", "hello", testBotID, true, false, true},
		{"dm with mention passes", "im", "<@U0BOT123> hi", testBotID, true, false, true},

		// require_mention off => channels reply to everything.
		{"channel gate off passes", "channel", "random chatter", testBotID, false, false, true},

		// Fail-open when we can't identify ourselves.
		{"channel unknown botid fails open", "channel", "random chatter", "", true, false, true},

		// Explicit mention always passes.
		{"channel mention passes", "channel", "<@U0BOT123> please help", testBotID, true, false, true},
		{"channel other-user mention dropped", "channel", "<@U0OTHER99> wdyt?", testBotID, true, false, false},

		// No mention, not in a participated thread => dropped (the cross-talk fix).
		{"channel no mention dropped", "channel", "team cross-talk", testBotID, true, false, false},
		{"group no mention dropped", "group", "private channel banter", testBotID, true, false, false},
		{"mpim no mention dropped", "mpim", "group dm side chat", testBotID, true, false, false},

		// Thread auto-follow: no mention, but the bot is in this thread => passes.
		{"thread autofollow no mention passes", "channel", "yeah I agree", testBotID, true, true, true},
		// Same message, auto-follow disabled (thread_require_explicit_mention) => dropped.
		{"thread no autofollow dropped", "channel", "yeah I agree", testBotID, true, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := shouldForwardSlackMessage(c.channelType, c.text, c.botUserID, c.requireMention, c.autoFollow)
			if got != c.want {
				t.Fatalf("shouldForwardSlackMessage(%q, %q, %q, rm=%v, af=%v) = %v, want %v",
					c.channelType, c.text, c.botUserID, c.requireMention, c.autoFollow, got, c.want)
			}
		})
	}
}
