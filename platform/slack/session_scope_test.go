package slack

import "testing"

func TestNormalizeSessionScope(t *testing.T) {
	cases := []struct {
		raw   any
		share bool
		want  string
	}{
		{nil, false, "user"},
		{nil, true, "channel"},
		{"", false, "user"},
		{"", true, "channel"},
		{"user", true, "user"},
		{"channel", false, "channel"},
		{"thread", false, "thread"},
		{"Thread", false, "thread"},     // case-insensitive
		{" channel ", false, "channel"}, // trimmed
		{"bogus", false, "user"},        // unknown -> share-derived default
		{"bogus", true, "channel"},
	}
	for _, c := range cases {
		if got := normalizeSessionScope(c.raw, c.share); got != c.want {
			t.Errorf("normalizeSessionScope(%v, share=%v) = %q, want %q", c.raw, c.share, got, c.want)
		}
	}
}

func TestBuildSessionKey(t *testing.T) {
	const (
		ch     = "C123"
		user   = "U456"
		thread = "1717000000.000100"
	)
	cases := []struct {
		scope    string
		threadTS string
		want     string
	}{
		{"user", thread, "slack:C123:U456"}, // thread ignored in user scope
		{"channel", thread, "slack:C123"},   // user/thread ignored in channel scope
		{"thread", thread, "slack:C123:t:1717000000.000100"},
		{"thread", "", "slack:C123:U456"}, // no thread context -> falls back to user
		{"", thread, "slack:C123:U456"},   // empty scope behaves as user
	}
	for _, c := range cases {
		p := &Platform{sessionScope: c.scope}
		if got := p.buildSessionKey(ch, user, c.threadTS); got != c.want {
			t.Errorf("scope=%q threadTS=%q: buildSessionKey = %q, want %q", c.scope, c.threadTS, got, c.want)
		}
	}
}

func TestReconstructReplyCtx(t *testing.T) {
	p := &Platform{}
	cases := []struct {
		key       string
		channel   string
		timestamp string // thread_ts, "" when none
	}{
		{"slack:C123:U456", "C123", ""},                                 // user scope -> no thread
		{"slack:C123", "C123", ""},                                      // channel scope
		{"slack:C123:t:1717000000.000100", "C123", "1717000000.000100"}, // thread scope -> thread ts
	}
	for _, c := range cases {
		got, err := p.ReconstructReplyCtx(c.key)
		if err != nil {
			t.Fatalf("ReconstructReplyCtx(%q) error: %v", c.key, err)
		}
		rc := got.(replyContext)
		if rc.channel != c.channel || rc.timestamp != c.timestamp {
			t.Errorf("ReconstructReplyCtx(%q) = {channel:%q timestamp:%q}, want {channel:%q timestamp:%q}",
				c.key, rc.channel, rc.timestamp, c.channel, c.timestamp)
		}
	}
	if _, err := p.ReconstructReplyCtx("telegram:123"); err == nil {
		t.Error("ReconstructReplyCtx should reject non-slack keys")
	}
}
